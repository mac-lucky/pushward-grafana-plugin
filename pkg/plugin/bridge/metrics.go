package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

// LabeledSeries is a single time-series result with its metric labels.
type LabeledSeries struct {
	Labels map[string]string
	Points []pushward.HistoryPoint
}

// LabeledPoint is a single instant-query result with its metric labels.
type LabeledPoint struct {
	Labels map[string]string
	Point  pushward.HistoryPoint
}

// SeriesKey builds a display name from metric labels.
// If preferLabel is set and present, use its value.
// If only one label exists, use its value.
// Otherwise join all as "k=v, k=v" sorted by key.
// The result is truncated to 32 runes to satisfy the server's key length limit.
func SeriesKey(labels map[string]string, preferLabel string) string {
	var key string
	switch {
	case len(labels) == 0:
		return "value"
	case preferLabel != "" && labels[preferLabel] != "":
		key = labels[preferLabel]
	case len(labels) == 1:
		for _, v := range labels {
			key = v
		}
	default:
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+labels[k])
		}
		key = strings.Join(parts, ", ")
	}

	if utf8.RuneCountInString(key) > maxSeriesKeyRunes {
		key = string([]rune(key)[:maxSeriesKeyRunes-1]) + "…"
	}
	return key
}

// Mirrored PushWard server Content limits. The server (model.Content.Validate)
// is the source of truth and the shared contract package does not export them,
// so they are mirrored here to keep the emitted timeline payload valid rather
// than 422-ing. Keep in sync with pushward-server model.Content.Validate.
const (
	maxTimelineSeries = 10  // model.MaxTimelineSeries: timeline values/history series
	maxStateRunes     = 256 // content.state
	maxUnitRunes      = 32  // content.unit
	maxSeriesKeyRunes = 32  // timeline value/series key + primary_series
)

// capSeries bounds values to at most maxTimelineSeries entries. Selection is
// deterministic (sorted keys) so the kept set is stable across poller ticks - an
// unstable set would churn the server-accumulated sparkline history. primary,
// when set and present, is always retained so the headline series survives the
// cap. Returns the same map (identity) and false when already within the limit.
func capSeries(values map[string]float64, primary string) (map[string]float64, bool) {
	if len(values) <= maxTimelineSeries {
		return values, false
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	kept := make(map[string]float64, maxTimelineSeries)
	if primary != "" {
		if v, ok := values[primary]; ok {
			kept[primary] = v
		}
	}
	for _, k := range keys {
		if len(kept) >= maxTimelineSeries {
			break
		}
		if _, exists := kept[k]; exists {
			continue
		}
		kept[k] = values[k]
	}
	return kept, true
}

// capHistory restricts history to the given kept value keys so the value and
// history series stay aligned after capSeries trims the value map. The fast path
// returns the same map (identity) when every history key already survives.
func capHistory(history map[string][]pushward.HistoryPoint, keep map[string]float64) map[string][]pushward.HistoryPoint {
	if len(history) == 0 {
		return history
	}
	extra := false
	for k := range history {
		if _, ok := keep[k]; !ok {
			extra = true
			break
		}
	}
	if !extra {
		return history
	}

	out := make(map[string][]pushward.HistoryPoint, len(keep))
	for k, pts := range history {
		if _, ok := keep[k]; ok {
			out[k] = pts
		}
	}
	return out
}

// MetricsClient queries Prometheus or VictoriaMetrics for time-series data.
// In the plugin it is pointed at the Grafana datasource proxy
// ({grafanaAppURL}/api/datasources/proxy/uid/{datasourceUID}), which forwards
// /api/v1/query[_range] to the configured datasource and returns native
// Prometheus JSON, so the parsing below is unchanged from the standalone bridge.
type MetricsClient struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string
	bearer     string
}

// defaultTimeout is used when WithTimeout is not supplied.
const defaultTimeout = 30 * time.Second

// maxResponseBytes bounds how much of a metrics response is decoded. A
// wide-window range query over many series can return tens of MB; this caps
// memory while staying far above any response that feeds a Live-Activity widget
// (a handful of points/series).
const maxResponseBytes = 16 << 20

// NewMetricsClient creates a new metrics client. Default HTTP timeout is 30s;
// override with WithTimeout.
func NewMetricsClient(baseURL string, opts ...MetricsOption) *MetricsClient {
	c := &MetricsClient{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    baseURL,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// MetricsOption configures the metrics client.
type MetricsOption func(*MetricsClient)

// WithTimeout sets the HTTP client timeout. Non-positive values are ignored.
func WithTimeout(d time.Duration) MetricsOption {
	return func(c *MetricsClient) {
		if d > 0 {
			c.httpClient.Timeout = d
		}
	}
}

// WithBasicAuth sets basic authentication credentials.
func WithBasicAuth(username, password string) MetricsOption {
	return func(c *MetricsClient) {
		c.username = username
		c.password = password
	}
}

// WithBearerToken sets a bearer token for authentication. In the plugin this is
// the IAM service-account token so the datasource proxy authorizes the query.
func WithBearerToken(token string) MetricsOption {
	return func(c *MetricsClient) { c.bearer = token }
}

// QueryRange fetches time-series data for the first result series only.
func (c *MetricsClient) QueryRange(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]pushward.HistoryPoint, error) {
	series, err := c.QueryRangeAll(ctx, expr, from, to, step)
	if err != nil || len(series) == 0 {
		return nil, err
	}
	return series[0].Points, nil
}

// QueryInstant fetches a single data point for the first result series only.
func (c *MetricsClient) QueryInstant(ctx context.Context, expr string, ts time.Time) (*pushward.HistoryPoint, error) {
	points, err := c.QueryInstantAll(ctx, expr, ts)
	if err != nil || len(points) == 0 {
		return nil, err
	}
	return &points[0].Point, nil
}

// QueryRangeAll fetches time-series data for all result series.
func (c *MetricsClient) QueryRangeAll(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]LabeledSeries, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/query_range"
	q := u.Query()
	q.Set("query", expr)
	q.Set("start", strconv.FormatInt(from.Unix(), 10))
	q.Set("end", strconv.FormatInt(to.Unix(), 10))
	q.Set("step", strconv.Itoa(max(1, int(step.Seconds()))))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying metrics: %w", err)
	}
	// Drain before close so the body is read to EOF (the JSON decoder leaves the
	// trailing newline) and the keep-alive connection is reused across polls.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics query returned %d", resp.StatusCode)
	}

	var result queryRangeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed: %s: %s", result.ErrorType, result.Error)
	}

	if len(result.Data.Result) == 0 {
		return nil, nil
	}

	series := make([]LabeledSeries, 0, len(result.Data.Result))
	for _, r := range result.Data.Result {
		points := parseValues(r.Values)
		if len(points) == 0 {
			continue
		}
		series = append(series, LabeledSeries{
			Labels: filterMetricLabels(r.Metric),
			Points: points,
		})
	}
	return series, nil
}

// QueryInstantAll fetches a single data point for all result series.
func (c *MetricsClient) QueryInstantAll(ctx context.Context, expr string, ts time.Time) ([]LabeledPoint, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/query"
	q := u.Query()
	q.Set("query", expr)
	q.Set("time", strconv.FormatInt(ts.Unix(), 10))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying metrics: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics query returned %d", resp.StatusCode)
	}

	var result instantQueryResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if result.Status != "success" {
		return nil, fmt.Errorf("query failed: %s: %s", result.ErrorType, result.Error)
	}

	if len(result.Data.Result) == 0 {
		return nil, nil
	}

	points := make([]LabeledPoint, 0, len(result.Data.Result))
	for _, r := range result.Data.Result {
		pt, err := parseInstantValue(r.Value)
		if err != nil || pt == nil {
			continue
		}
		points = append(points, LabeledPoint{
			Labels: filterMetricLabels(r.Metric),
			Point:  *pt,
		})
	}
	return points, nil
}

// filterMetricLabels returns labels without the __name__ meta-label.
func filterMetricLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k == "__name__" {
			continue
		}
		out[k] = v
	}
	return out
}

// instantQueryResponse is the Prometheus/VictoriaMetrics /api/v1/query response.
type instantQueryResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     []instantResult `json:"result"`
	} `json:"data"`
}

type instantResult struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"` // [timestamp, "value"]
}

func parseInstantValue(pair []json.RawMessage) (*pushward.HistoryPoint, error) {
	if len(pair) != 2 {
		return nil, fmt.Errorf("unexpected value format: %d elements", len(pair))
	}

	var ts float64
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return nil, fmt.Errorf("parsing timestamp: %w", err)
	}

	var valStr string
	if err := json.Unmarshal(pair[1], &valStr); err != nil {
		return nil, fmt.Errorf("parsing value: %w", err)
	}

	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing float value %q: %w", valStr, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, nil
	}

	return &pushward.HistoryPoint{Timestamp: int64(ts), Value: v}, nil
}

func (c *MetricsClient) setAuth(req *http.Request) {
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	} else if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
}

// queryRangeResponse is the Prometheus/VictoriaMetrics /api/v1/query_range response.
type queryRangeResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string         `json:"resultType"`
		Result     []matrixResult `json:"result"`
	} `json:"data"`
}

type matrixResult struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"`
}

// parseValues converts the Prometheus [timestamp, "value"] pairs to HistoryPoints.
// Values are JSON strings (e.g. "87.3", "NaN", "+Inf") — NaN and Inf are skipped.
func parseValues(values [][]json.RawMessage) []pushward.HistoryPoint {
	points := make([]pushward.HistoryPoint, 0, len(values))
	for _, pair := range values {
		if len(pair) != 2 {
			continue
		}

		// Timestamp is a JSON number
		var ts float64
		if err := json.Unmarshal(pair[0], &ts); err != nil {
			continue
		}

		// Value is a JSON string (e.g. "87.3", "NaN", "+Inf")
		var valStr string
		if err := json.Unmarshal(pair[1], &valStr); err != nil {
			continue
		}

		v, err := strconv.ParseFloat(valStr, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}

		points = append(points, pushward.HistoryPoint{
			Timestamp: int64(ts),
			Value:     v,
		})
	}
	return points
}
