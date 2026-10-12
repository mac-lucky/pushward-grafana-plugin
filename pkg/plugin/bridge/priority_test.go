package bridge

import (
	"context"
	"net/http"
	"testing"
)

func TestPriorityFollowsSeverity(t *testing.T) {
	cases := []struct {
		name       string
		severity   string // "" leaves the label off
		configured int
		want       int
	}{
		{"critical", "critical", 5, priorityCritical},
		{"warning", "warning", 5, 5},
		{"info", "info", 5, priorityLow},
		{"none", "none", 5, priorityLow},
		{"unknown value", "page", 5, 5},
		{"no label", "", 5, 5},
		{"info never above the configured priority", "info", 0, 0},
		{"critical above a high configured priority", "critical", 9, priorityCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubServer(t)
			b := newTestBridge(t, s.URL, nil, Config{Priority: tc.configured, SeverityLabel: "severity", DefaultSeverity: "warning"})
			a := firingAlert()
			delete(a.Labels, "severity")
			if tc.severity != "" {
				a.Labels["severity"] = tc.severity
			}

			b.handleFiring(context.Background(), a)

			create := s.find(http.MethodPost, "/activities")
			if create == nil {
				t.Fatal("expected a POST /activities")
			}
			if got, _ := create.body["priority"].(float64); int(got) != tc.want {
				t.Errorf("priority = %v, want %d", create.body["priority"], tc.want)
			}
		})
	}
}
