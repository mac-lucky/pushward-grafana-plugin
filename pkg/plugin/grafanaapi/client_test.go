package grafanaapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ruleJSON is a ruler API answer in the shape Grafana 13 sends: expression
// nodes first (one carrying a stray expr, to prove it is skipped by its
// datasource and not by luck), then the datasource query.
const ruleJSON = `{"grafana_alert": {"condition": "C", "data": [
	{"refId": "B", "datasourceUid": "__expr__", "model": {"expr": "not-a-query", "type": "reduce"}},
	{"refId": "Z", "datasourceUid": "-100", "model": {"expr": "legacy-expression"}},
	{"refId": "A", "datasourceUid": "ds-metrics", "model": {"expr": "up == 0"}},
	{"refId": "C", "datasourceUid": "__expr__", "model": {"type": "threshold"}}
]}}`

func newRuleClient(t *testing.T, ruleTok string, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(
		func() (string, string) { return srv.URL, "iam-token" },
		func() (string, string) { return srv.URL, ruleTok },
	)
	t.Cleanup(c.Close)
	return c, &hits
}

func TestGetRuleQueryReadsRulerAPIWithRuleToken(t *testing.T) {
	c, hits := newRuleClient(t, "viewer-token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ruler/grafana/api/v1/rule/rule-1" {
			t.Errorf("path = %s, want the ruler API", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer viewer-token" {
			t.Errorf("Authorization = %q, want the rule token", got)
		}
		_, _ = w.Write([]byte(ruleJSON))
	})

	for range 2 {
		expr, refID, ds, err := c.GetRuleQuery(context.Background(), "rule-1")
		if err != nil {
			t.Fatal(err)
		}
		if expr != "up == 0" || refID != "A" || ds != "ds-metrics" {
			t.Errorf("GetRuleQuery = (%q, %q, %q), want (up == 0, A, ds-metrics)", expr, refID, ds)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1 (second answer from the cache)", n)
	}
}

func TestGetRuleQueryRefusalIsNotCached(t *testing.T) {
	c, hits := newRuleClient(t, "viewer-token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	for range 2 {
		if _, _, _, err := c.GetRuleQuery(context.Background(), "rule-1"); err == nil {
			t.Fatal("GetRuleQuery succeeded on a 403")
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (a refusal must not be cached)", n)
	}
}

// The backstop decides that an alert resolved, so it must keep the IAM token
// even though rule lookups take the Viewer's: swapping the two conns would
// let a folder-scoped Viewer's empty alert list end a firing activity.
func TestIsAlertFiringUsesIAMToken(t *testing.T) {
	c, _ := newRuleClient(t, "viewer-token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer iam-token" {
			t.Errorf("Authorization = %q, want the IAM token", got)
		}
		_, _ = w.Write([]byte(`[{}]`))
	})
	if firing, err := c.IsAlertFiring(context.Background(), "HighCPU"); err != nil || !firing {
		t.Errorf("IsAlertFiring = (%v, %v), want (true, nil)", firing, err)
	}
}

func TestGetRuleQueryWithoutTokenSendsNothing(t *testing.T) {
	c, hits := newRuleClient(t, " ", func(http.ResponseWriter, *http.Request) {})

	if _, _, _, err := c.GetRuleQuery(context.Background(), "rule-1"); err == nil {
		t.Error("GetRuleQuery succeeded without a token")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests without a token, want 0", n)
	}
}
