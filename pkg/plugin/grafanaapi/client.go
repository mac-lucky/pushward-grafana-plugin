// Package grafanaapi calls Grafana's own HTTP API from inside the plugin
// backend. It resolves an alert rule's PromQL expression (ruler API, any token
// with alert-rule read), checks whether an alert is still firing (alertmanager
// API, the auto-provisioned IAM service-account token only), and builds the
// datasource-proxy base URL the metrics querier uses for history queries.
package grafanaapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/syncx"
)

// ruleQuery holds the extracted query details from a Grafana alert rule.
type ruleQuery struct {
	expr          string
	datasourceUID string
	refID         string
	fetchedAt     time.Time
}

// Client queries the Grafana ruler and alertmanager APIs. The connections
// (Grafana app URL + token) are resolved per-request via the conn providers, so
// a token rotated by Grafana between requests is always honoured, including by
// long-lived background pollers. conn yields the IAM service-account token
// (cfg.PluginAppClientSecret(), permissions in the plugin.json iam block) and
// serves the alertmanager check; ruleConn serves the rule lookups and may yield
// any token that can read alert rules.
type Client struct {
	httpClient *http.Client
	conn       func() (baseURL, token string)
	ruleConn   func() (baseURL, token string)

	mu    sync.RWMutex
	cache map[string]*ruleQuery

	cleanup syncx.Periodic
}

const (
	cacheTTL        = 1 * time.Hour
	cacheMaxEntries = 500
	cleanupInterval = 5 * time.Minute
)

// NewClient creates a new Grafana API client. conn returns the current Grafana
// app URL and IAM service-account token, used for the alertmanager check;
// ruleConn returns the URL and token for the rule lookups. Starts a background
// goroutine that prunes expired cache entries; call Close to stop it.
func NewClient(conn, ruleConn func() (baseURL, token string)) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		conn:       conn,
		ruleConn:   ruleConn,
		cache:      make(map[string]*ruleQuery),
	}
	c.cleanup.Start(context.Background(), cleanupInterval, func(context.Context) {
		c.pruneCache()
	})
	return c
}

// Close stops the background cache-cleanup goroutine. Safe to call multiple times.
func (c *Client) Close() {
	c.cleanup.Stop()
}

// DatasourceProxyURL returns the Grafana datasource-proxy base URL for a
// datasource UID. The metrics querier appends /api/v1/query[_range] to it, so
// the Grafana proxy forwards the query to the underlying datasource and returns
// native Prometheus JSON.
func DatasourceProxyURL(appURL, datasourceUID string) string {
	return fmt.Sprintf("%s/api/datasources/proxy/uid/%s",
		strings.TrimRight(appURL, "/"), url.PathEscape(datasourceUID))
}

// pruneCache removes expired entries using a two-pass scan: an RLock'd pass
// collects stale UIDs, then a Lock'd pass deletes them. Each entry is re-checked
// under the write lock to avoid evicting an entry that was concurrently refreshed.
func (c *Client) pruneCache() {
	cutoff := time.Now().Add(-cacheTTL)

	c.mu.RLock()
	stale := make([]string, 0)
	for uid, cached := range c.cache {
		if cached.fetchedAt.Before(cutoff) {
			stale = append(stale, uid)
		}
	}
	c.mu.RUnlock()

	if len(stale) == 0 {
		return
	}

	c.mu.Lock()
	for _, uid := range stale {
		if cached, ok := c.cache[uid]; ok && cached.fetchedAt.Before(cutoff) {
			delete(c.cache, uid)
		}
	}
	c.mu.Unlock()
}

// ruleUIDPattern extracts the rule UID from a generatorURL like
// "https://grafana.example.com/alerting/<uid>/edit"           (Grafana <11)
// "https://grafana.example.com/alerting/grafana/<uid>/view"   (Grafana 11+)
var ruleUIDPattern = regexp.MustCompile(`/alerting/(?:grafana/)?([^/]+)/(?:edit|view)`)

// ExtractRuleUID parses the rule UID from a Grafana generatorURL. Returns empty
// string if the URL doesn't match the expected pattern or contains path
// traversal characters. It is a method (not a free function) so the bridge can
// depend on a single resolver interface; the logic is pure.
func (c *Client) ExtractRuleUID(generatorURL string) string {
	return ExtractRuleUID(generatorURL)
}

// ExtractRuleUID is the package-level pure implementation, exported for tests
// and callers that don't hold a Client.
func ExtractRuleUID(generatorURL string) string {
	m := ruleUIDPattern.FindStringSubmatch(generatorURL)
	if len(m) < 2 {
		return ""
	}
	uid := m[1]
	if strings.ContainsAny(uid, ".%/\\") {
		return ""
	}
	return uid
}

// GetRuleQuery fetches the expression, ref ID and datasource UID of a Grafana
// alert rule's first datasource query via the ruler API, which needs only
// alert-rule read (a Viewer has it; the provisioning API would need more).
// Results are cached for 1 hour.
func (c *Client) GetRuleQuery(ctx context.Context, ruleUID string) (expr, refID, datasourceUID string, err error) {
	c.mu.RLock()
	if cached, ok := c.cache[ruleUID]; ok && time.Since(cached.fetchedAt) < cacheTTL {
		c.mu.RUnlock()
		return cached.expr, cached.refID, cached.datasourceUID, nil
	}
	c.mu.RUnlock()

	baseURL, token := c.ruleConn()
	if strings.TrimSpace(token) == "" {
		return "", "", "", errors.New("no token that can read alert rules")
	}
	reqURL := fmt.Sprintf("%s/api/ruler/grafana/api/v1/rule/%s", strings.TrimRight(baseURL, "/"), url.PathEscape(ruleUID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("fetching alert rule: %w", err)
	}
	// Drain to EOF before close so the keep-alive connection is reusable.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("alert rule API returned %d (alert.rules:read required)", resp.StatusCode)
	}

	var rule alertRuleResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rule); err != nil {
		return "", "", "", fmt.Errorf("decoding alert rule: %w", err)
	}

	rq := extractQuery(rule.GrafanaAlert.Data)
	if rq == nil {
		return "", "", "", fmt.Errorf("no datasource query found in alert rule %s", ruleUID)
	}
	rq.fetchedAt = time.Now()

	c.mu.Lock()
	c.cache[ruleUID] = rq
	if len(c.cache) > cacheMaxEntries {
		// Evict the single oldest entry to keep the map bounded between
		// periodic sweeps.
		var oldestUID string
		var oldestAt time.Time
		for uid, cached := range c.cache {
			if oldestUID == "" || cached.fetchedAt.Before(oldestAt) {
				oldestUID = uid
				oldestAt = cached.fetchedAt
			}
		}
		if oldestUID != "" {
			delete(c.cache, oldestUID)
		}
	}
	c.mu.Unlock()

	return rq.expr, rq.refID, rq.datasourceUID, nil
}

// IsAlertFiring checks the Grafana alertmanager API to determine if any
// instances of the given alertname are currently active (firing).
func (c *Client) IsAlertFiring(ctx context.Context, alertname string) (bool, error) {
	filter := fmt.Sprintf(`alertname="%s"`, alertname)
	baseURL, token := c.conn()
	reqURL := fmt.Sprintf("%s/api/alertmanager/grafana/api/v2/alerts?filter=%s",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(filter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("querying alertmanager: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("alertmanager API returned %d", resp.StatusCode)
	}

	var alerts []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&alerts); err != nil {
		return false, fmt.Errorf("decoding alerts: %w", err)
	}

	return len(alerts) > 0, nil
}

// alertRuleResponse is the Grafana ruler API response for one alert rule.
type alertRuleResponse struct {
	GrafanaAlert struct {
		Data []alertQueryModel `json:"data"`
	} `json:"grafana_alert"`
}

type alertQueryModel struct {
	RefID         string          `json:"refId"`
	DatasourceUID string          `json:"datasourceUid"`
	Model         json.RawMessage `json:"model"`
}

type queryModel struct {
	Expr string `json:"expr"`
}

// extractQuery finds the first real datasource query (not an expression node)
// and returns its PromQL expression.
func extractQuery(queries []alertQueryModel) *ruleQuery {
	for _, q := range queries {
		// Skip expression nodes: datasourceUid "__expr__", or "-100" on rules
		// saved by older Grafana versions.
		if q.DatasourceUID == "__expr__" || q.DatasourceUID == "-100" {
			continue
		}

		var m queryModel
		if err := json.Unmarshal(q.Model, &m); err != nil || m.Expr == "" {
			continue
		}

		return &ruleQuery{
			expr:          m.Expr,
			datasourceUID: q.DatasourceUID,
			refID:         q.RefID,
		}
	}
	return nil
}
