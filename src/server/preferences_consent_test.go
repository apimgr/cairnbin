package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/config"
)

// TestPreferenceCookiesRespectConsent verifies PART 16's first-visit wait,
// consent grant, and decline states, plus PART 11 privacy-signal overrides.
func TestPreferenceCookiesRespectConsent(t *testing.T) {
	cases := []struct {
		name          string
		consent       string
		privacyOptOut bool
		want          bool
	}{
		{"first visit waits for consent", "", false, false},
		{"preferences granted", `{"essential":true,"preferences":true,"analytics":false,"timestamp":1}`, false, true},
		{"preferences declined", `{"essential":true,"preferences":false,"analytics":false,"timestamp":1}`, false, false},
		{"privacy signal overrides consent", `{"essential":true,"preferences":true,"analytics":true,"timestamp":1}`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMinimalServer(config.DefaultConfig())
			r := httptest.NewRequest(http.MethodGet, "/pastes", nil)
			if tc.consent != "" {
				addConsentCookie(r, tc.consent)
			}
			if tc.privacyOptOut {
				r = r.WithContext(context.WithValue(r.Context(), privacyOptOutKey{}, true))
			}
			w := httptest.NewRecorder()
			s.setPreferenceCookie(w, r, "theme", "light")
			s.setPreferenceCookie(w, r, "lang", "fr")
			s.rememberPreference(w, r, prefKeySort, "views")
			got := len(w.Result().Cookies()) > 0
			if got != tc.want {
				t.Errorf("cookies written = %v (%v), want %v", got, w.Result().Cookies(), tc.want)
			}
			if tc.want && len(w.Result().Cookies()) != 3 {
				t.Errorf("cookies = %v, want theme, lang and sort", w.Result().Cookies())
			}
		})
	}
}

func TestPreferencesSetAPI_ConsentBlockedWritesAreIgnored(t *testing.T) {
	s := newCSRFServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/preferences", strings.NewReader(`{"theme":"light"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handlePreferencesSetAPI(rec, req)

	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("status=%d cookies=%v, want 200 with no preference cookies before consent", rec.Code, rec.Result().Cookies())
	}
	var out prefsAPIOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Theme != "dark" || len(out.Data.Applied) != 0 || len(out.Data.Ignored) != 1 || out.Data.Ignored[0] != "theme" {
		t.Errorf("response state=%+v, want default theme, no applied values, theme ignored", out.Data)
	}
}

func TestPreferencesImport_ConsentBlockedValuesAreIgnored(t *testing.T) {
	s := newMinimalServer(config.DefaultConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/preferences/sync/import?theme=light", nil)
	rec := httptest.NewRecorder()
	s.handlePreferencesImport(rec, req)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("status=%d cookies=%v, want 200 with no preference cookies before consent", rec.Code, rec.Result().Cookies())
	}
	var out prefsAPIOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Theme != "dark" || len(out.Data.Applied) != 0 || len(out.Data.Ignored) != 1 || out.Data.Ignored[0] != "theme" {
		t.Errorf("response state=%+v, want default theme, no applied values, theme ignored", out.Data)
	}
}

func TestPreferencesConsentChangesAreAudited(t *testing.T) {
	tests := []struct {
		name string
		form string
		want map[string]any
	}{
		{
			name: "consent form",
			form: "consent_form=1&consent_preferences=on",
			want: map[string]any{"essential": true, "preferences": true, "analytics": false},
		},
		{
			name: "reset",
			form: "action=reset",
			want: map[string]any{"essential": true, "preferences": false, "analytics": false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auditDir := t.TempDir()
			s := newMinimalServer(config.DefaultConfig())
			s.SetAuditWriter(audit.New(audit.Config{
				Enabled: true,
				Dir:     auditDir,
				Events:  audit.EventCategories{Security: true},
			}))

			req := httptest.NewRequest(http.MethodPost, "/server/preferences", strings.NewReader(tc.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			s.handlePreferencesSet(rec, req)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
			}

			data, err := os.ReadFile(filepath.Join(auditDir, "audit.log"))
			if err != nil {
				t.Fatalf("read audit log: %v", err)
			}
			var event struct {
				Event   string         `json:"event"`
				Details map[string]any `json:"details"`
			}
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatalf("decode audit event: %v", err)
			}
			if event.Event != "compliance.consent_updated" {
				t.Fatalf("event = %q, want compliance.consent_updated", event.Event)
			}
			for key, want := range tc.want {
				if got := event.Details[key]; got != want {
					t.Errorf("details[%q] = %v, want %v", key, got, want)
				}
			}
			if timestamp, ok := event.Details["timestamp"].(float64); !ok || timestamp <= 0 {
				t.Errorf("details[timestamp] = %v, want positive Unix timestamp", event.Details["timestamp"])
			}
		})
	}
}
