package widgets

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mac-lucky/pushward-integrations/shared/pushward"
	sharedwidgets "github.com/mac-lucky/pushward-integrations/shared/widgets"

	"github.com/mac-lucky/pushward-grafana-plugin/pkg/plugin/bridge"
)

// The engine's create is a POST that replaces the whole content and runs on
// every start, so a configured look has to ride on it and on every PATCH
// after it. An unset one must never go out: the app's pick would lose to it.
func TestLooksRideOnEveryRequest(t *testing.T) {
	const statList = `"template":"stat_list","stat_rows":[{"label":"x","query":"up","value_template":"{{.Value}}"}]`
	cases := []struct {
		name, field, widget, content, want string
	}{
		{"style", "Style", `"query":"up"`, `"style":"Bold"`, `"style":"bold"`},
		{"style_by_size", "StyleBySize", `"query":"up"`, `"style_by_size":{"Large":"classic"}`, `"style_by_size":{"large":"classic"}`},
		{"density", "Density", statList, `"density":"compact"`, `"density":"compact"`},
		{"density_by_size", "DensityBySize", statList, `"density_by_size":{"lock_screen":"comfortable"}`, `"density_by_size":{"lock_screen":"comfortable"}`},
		{"none", "", `"query":"up"`, ``, ``},
		{"none on stat_list", "", statList, ``, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// shortcut: shared/pushward.WidgetContent has none of these fields at
			// the pinned version, so a set look cannot be sent yet. Once shared
			// is bumped these cases run and fail until ToWidgetContent maps
			// them; drop the skip in that change.
			if _, ok := reflect.TypeFor[pushward.WidgetContent]().FieldByName(tc.field); tc.field != "" && !ok {
				t.Skipf("shared/pushward.WidgetContent has no %s field at the pinned version", tc.field)
			}
			reqs := make(chan [2]string, 16)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				select {
				case reqs <- [2]string{r.Method, string(b)}:
				default:
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			raw := `[{"slug":"cpu","update_mode":"always",` + tc.widget + `,"content":{"icon":"cpu"`
			if tc.content != "" {
				raw += "," + tc.content
			}
			cfgs, err := ParseWidgets(json.RawMessage(raw + `}}]`))
			if err != nil {
				t.Fatalf("ParseWidgets: %v", err)
			}
			specs, err := BuildSpecs(cfgs, &stubQuerier{byExpr: map[string][]bridge.LabeledPoint{"up": {point(1, nil)}}})
			if err != nil {
				t.Fatalf("BuildSpecs: %v", err)
			}
			specs[0].Interval = 10 * time.Millisecond
			mgr, err := sharedwidgets.New(pushward.NewClient(srv.URL, "hlk_x"), specs, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); mgr.Wait() }()
			if err := mgr.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}

			var methods []string
			for len(methods) < 3 {
				select {
				case r := <-reqs:
					methods = append(methods, r[0])
					if tc.want != "" && !strings.Contains(r[1], tc.want) {
						t.Errorf("%s body %s: want %s", r[0], r[1], tc.want)
					}
					for _, key := range []string{`"style"`, `"style_by_size"`, `"density"`, `"density_by_size"`} {
						if strings.Contains(r[1], key) && !strings.HasPrefix(tc.want, key) {
							t.Errorf("%s body %s: %s was not configured", r[0], r[1], key)
						}
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("requests = %v, want a POST and two PATCHes", methods)
				}
			}
			if methods[0] != http.MethodPost || methods[1] != http.MethodPatch || methods[2] != http.MethodPatch {
				t.Errorf("requests = %v, want POST then PATCHes", methods)
			}
		})
	}
}
