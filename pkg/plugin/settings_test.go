package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/mac-lucky/pushward-integrations/shared/pushward"
)

const testE2EKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func loadSettings(t *testing.T, jsonData string, secure map[string]string) *Settings {
	t.Helper()
	s, err := LoadSettings(backend.AppInstanceSettings{JSONData: json.RawMessage(jsonData), DecryptedSecureJSONData: secure})
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	return s
}

func TestAckSettings(t *testing.T) {
	cases := []struct {
		name           string
		jsonData       string
		ack            bool
		repeat, expire int
	}{
		{"defaults", `{}`, false, 300, 3600},
		{"on", `{"ackEnabled":true}`, true, 300, 3600},
		{"custom", `{"ackEnabled":true,"ackRepeatSeconds":45,"ackExpireSeconds":600}`, true, 45, 600},
		{"clamped low", `{"ackEnabled":true,"ackRepeatSeconds":5,"ackExpireSeconds":10}`, true, 30, 60},
		{"emptied fields keep the defaults", `{"ackEnabled":true,"ackRepeatSeconds":0,"ackExpireSeconds":0}`, true, 300, 3600},
		{"clamped high", `{"ackEnabled":true,"ackRepeatSeconds":99999,"ackExpireSeconds":99999}`, true, 3600, 10800},
		{"off at silent", `{"ackEnabled":true,"notifyLevel":"passive"}`, false, 300, 3600},
		{"on at critical", `{"ackEnabled":true,"notifyLevel":"critical"}`, true, 300, 3600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := loadSettings(t, tc.jsonData, nil)
			if s.Ack != tc.ack || s.AckRepeat != tc.repeat || s.AckExpire != tc.expire {
				t.Errorf("ack/repeat/expire = %v/%d/%d, want %v/%d/%d", s.Ack, s.AckRepeat, s.AckExpire, tc.ack, tc.repeat, tc.expire)
			}
		})
	}
}

func TestE2EKeySettings(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		// Upper case and a line break still parse, as the apps copy it.
		s := loadSettings(t, `{}`, map[string]string{"e2eKey": strings.ToUpper(testE2EKey[:32]) + "\n" + testE2EKey[32:]})
		if s.E2EKey == nil || s.E2EError != "" {
			t.Fatalf("key = %v, error = %q, want a parsed key", s.E2EKey, s.E2EError)
		}
		if len(s.E2EKey.KID()) != 8 {
			t.Errorf("KID = %q, want 8 hex characters", s.E2EKey.KID())
		}
	})
	t.Run("not set", func(t *testing.T) {
		for _, v := range []string{"", "  \n"} {
			s := loadSettings(t, `{}`, map[string]string{"e2eKey": v})
			if s.E2EKey != nil || s.E2EError != "" {
				t.Errorf("e2eKey %q: key = %v, error = %q, want neither", v, s.E2EKey, s.E2EError)
			}
		}
	})
	t.Run("invalid", func(t *testing.T) {
		for _, v := range []string{"deadbeef", strings.Repeat("zz", 32), "hlk_abcdef0123456789"} {
			s := loadSettings(t, `{}`, map[string]string{"e2eKey": v})
			if s.E2EKey != nil || s.E2EError == "" {
				t.Errorf("e2eKey %q: key = %v, error = %q, want an error", v, s.E2EKey, s.E2EError)
			}
			if strings.Contains(s.E2EError, v) {
				t.Errorf("E2EError %q repeats the key", s.E2EError)
			}
		}
	})
	t.Run("integration key", func(t *testing.T) {
		s := loadSettings(t, `{}`, map[string]string{"e2eKey": "hlk_abcdef0123456789"})
		if !strings.Contains(s.E2EError, "integration key") {
			t.Errorf("E2EError = %q, want it to say an integration key was pasted", s.E2EError)
		}
	})
}

func TestAckLevelDefaultIsNotPassive(t *testing.T) {
	// An unknown level falls back to active, so it keeps acknowledge.
	s := loadSettings(t, `{"ackEnabled":true,"notifyLevel":"loud"}`, nil)
	if s.NotifyLevel != pushward.LevelActive || !s.Ack {
		t.Errorf("level/ack = %q/%v, want active/true", s.NotifyLevel, s.Ack)
	}
}
