package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/config"
)

// TestParseAppPref verifies the pastebin_pref_{key} name/value allowlist
// (AI.md 23485/23501) accepts well-formed pairs and rejects everything else.
func TestParseAppPref(t *testing.T) {
	cases := []struct {
		name    string
		pname   string
		pvalue  string
		wantKey string
		wantOK  bool
	}{
		{"valid", "pastebin_pref_view_mode", "compact", "view_mode", true},
		{"wrong prefix", "theme", "dark", "", false},
		{"unrelated cookie", "pastebin_build", "1.0.0-abc123", "", false},
		{"empty key", "pastebin_pref_", "x", "", false},
		{"uppercase key rejected", "pastebin_pref_ViewMode", "x", "", false},
		{"key starting with digit rejected", "pastebin_pref_1abc", "x", "", false},
		{"empty value rejected", "pastebin_pref_view_mode", "", "", false},
		{"value with space rejected", "pastebin_pref_view_mode", "a b", "", false},
		{"value with semicolon rejected", "pastebin_pref_view_mode", "a;b", "", false},
		{"value too long rejected", "pastebin_pref_view_mode", string(make([]byte, 65)), "", false},
		// Concrete keys carry a per-key enum, so a well-formed but
		// unsupported value is rejected here rather than reaching a handler
		// as a cookie the UI has no <option> to render back.
		{"sort enum accepts a real column", "pastebin_pref_sort", "views", "sort", true},
		{"sort enum rejects unknown column", "pastebin_pref_sort", "bogus", "", false},
		{"sort enum rejects SQL fragment", "pastebin_pref_sort", "date;drop", "", false},
		{"order enum accepts desc", "pastebin_pref_order", "desc", "order", true},
		{"order enum rejects sideways", "pastebin_pref_order", "sideways", "", false},
		{"per_page enum accepts a listed size", "pastebin_pref_per_page", "50", "per_page", true},
		{"per_page enum rejects unlisted size", "pastebin_pref_per_page", "37", "", false},
		{"per_page enum rejects non-numeric", "pastebin_pref_per_page", "lots", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, value, ok := parseAppPref(tc.pname, tc.pvalue)
			if ok != tc.wantOK {
				t.Fatalf("parseAppPref(%q, %q) ok = %v, want %v", tc.pname, tc.pvalue, ok, tc.wantOK)
			}
			if ok && (key != tc.wantKey || value != tc.pvalue) {
				t.Fatalf("parseAppPref(%q, %q) = (%q, %q), want (%q, %q)", tc.pname, tc.pvalue, key, value, tc.wantKey, tc.pvalue)
			}
		})
	}
}

// TestAppPreferencesFromRequest verifies only pastebin_pref_* cookies are
// surfaced, non-preference cookies (theme, cookie_consent, pastebin_build)
// are excluded, and malformed values are dropped rather than applied.
func TestAppPreferencesFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/server/preferences", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	req.AddCookie(&http.Cookie{Name: "cookie_consent", Value: `{"essential":true}`})
	req.AddCookie(&http.Cookie{Name: "ccpa_opt_out", Value: "true"})
	req.AddCookie(&http.Cookie{Name: "pastebin_build", Value: "1.0.0-abc123"})
	req.AddCookie(&http.Cookie{Name: "pastebin_pref_view_mode", Value: "compact"})
	req.AddCookie(&http.Cookie{Name: "pastebin_pref_bad value", Value: "x"})

	got := appPreferencesFromRequest(req)
	if len(got) != 1 {
		t.Fatalf("appPreferencesFromRequest() = %v, want exactly 1 entry", got)
	}
	if got["view_mode"] != "compact" {
		t.Fatalf("appPreferencesFromRequest()[\"view_mode\"] = %q, want %q", got["view_mode"], "compact")
	}
}

// TestExtractAppPrefs mirrors TestAppPreferencesFromRequest but for the
// import path's url.Values source (query params and decoded `code`).
func TestExtractAppPrefs(t *testing.T) {
	values := url.Values{
		"theme":                   {"dark"},
		"lang":                    {"fr"},
		"code":                    {"abc"},
		"pastebin_pref_view_mode": {"compact"},
		"pastebin_pref_invalid!":  {"x"},
	}

	got := extractAppPrefs(values)
	if len(got) != 1 {
		t.Fatalf("extractAppPrefs() = %v, want exactly 1 entry", got)
	}
	if got["view_mode"] != "compact" {
		t.Fatalf("extractAppPrefs()[\"view_mode\"] = %q, want %q", got["view_mode"], "compact")
	}
}

// TestPreferencesExportQuery verifies theme/lang always round-trip and every
// app-specific pastebin_pref_* entry is appended in stable (sorted) order
// (AI.md 23507/23503).
func TestPreferencesExportQuery(t *testing.T) {
	got := preferencesExportQuery("dark", "fr", nil)
	want := "theme=dark&lang=fr"
	if got != want {
		t.Fatalf("preferencesExportQuery(nil) = %q, want %q", got, want)
	}

	got = preferencesExportQuery("dark", "fr", map[string]string{
		"view_mode": "compact",
		"per_page":  "50",
	})
	want = "theme=dark&lang=fr&pastebin_pref_per_page=50&pastebin_pref_view_mode=compact"
	if got != want {
		t.Fatalf("preferencesExportQuery(extra) = %q, want %q", got, want)
	}
}

// TestResolvedPref verifies the preferences page reports the value the
// recents list would actually use: the visitor's cookie when valid, the
// hard default when absent or invalid (AI.md 23485 — every app-specific
// pref needs a control with a definite current value).
func TestResolvedPref(t *testing.T) {
	cases := []struct {
		name    string
		cookies []*http.Cookie
		key     string
		want    string
	}{
		{"no cookie falls back to default", nil, prefKeySort, "date"},
		{"no cookie per_page default", nil, prefKeyPerPage, "20"},
		{"valid cookie wins", []*http.Cookie{
			{Name: prefCookiePrefix + prefKeySort, Value: "views"},
		}, prefKeySort, "views"},
		{"invalid value falls back to default", []*http.Cookie{
			{Name: prefCookiePrefix + prefKeyOrder, Value: "sideways"},
		}, prefKeyOrder, "desc"},
		{"one invalid key does not affect the others", []*http.Cookie{
			{Name: prefCookiePrefix + prefKeySort, Value: "bogus"},
			{Name: prefCookiePrefix + prefKeyPerPage, Value: "100"},
		}, prefKeyPerPage, "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/server/preferences", nil)
			for _, c := range tc.cookies {
				r.AddCookie(c)
			}
			if got := resolvedPref(r, tc.key); got != tc.want {
				t.Errorf("resolvedPref(%s) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestRememberPreference verifies a query-param choice is persisted only when
// the read-side allowlist would accept it back. /pastes accepts a wider set of
// sort columns and page sizes than the recents list's <select> offers; a value
// outside the pref enums (e.g. "title", an alias of "name") is a valid one-shot
// query but must not become a saved preference, or the preferences page would
// show a default the visitor never chose (AI.md 23485 — the page's controls must
// agree with what the list actually does).
func TestRememberPreference(t *testing.T) {
	// A "" want means no cookie is expected at all.
	cases := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"enum-legal sort is remembered", prefKeySort, "views", "views"},
		{"enum-legal per_page is remembered", prefKeyPerPage, "50", "50"},
		// "title" is a real publicPasteSortColumns entry, so /pastes honors it,
		// but no <option> on the preferences page renders it.
		{"sort alias with no option is not remembered", prefKeySort, "title", ""},
		{"id column has no option", prefKeySort, "id", ""},
		{"unlisted page size is not remembered", prefKeyPerPage, "37", ""},
		{"non-numeric page size is not remembered", prefKeyPerPage, "lots", ""},
		{"malformed value is not remembered", prefKeyOrder, "desc;drop", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMinimalServer(config.DefaultConfig())
			r := httptest.NewRequest(http.MethodGet, "/pastes", nil)
			w := httptest.NewRecorder()
			s.rememberPreference(w, r, tc.key, tc.value)

			name := prefCookiePrefix + tc.key
			got, ok := setCookiesOf(w)[name]
			if tc.want == "" {
				if ok {
					t.Errorf("cookie %s = %q, want no cookie set", name, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("cookie %s = %q (set=%v), want %q", name, got, ok, tc.want)
			}
			// Whatever was written must survive the read path unchanged.
			if _, _, valid := parseAppPref(name, got); !valid {
				t.Errorf("cookie %s = %q fails parseAppPref on read", name, got)
			}
		})
	}
}

// TestPreferencesPageRenders guards the page against render-time template
// errors. A missing FuncMap helper is NOT a parse-time error in text/template
// — it only fails when the node executes — so a broken template still passes
// every other test in this package and only shows up as a 500 in production.
// (This is exactly how `(slice "dark" "light" "auto")` shipped: `slice` is a
// text/template builtin for range-slicing, not a list-maker.)
func TestPreferencesPageRenders(t *testing.T) {
	s := &Server{cfg: config.DefaultConfig()}
	pages, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("buildTemplates: %v", err)
	}
	s.templates = pages

	r := httptest.NewRequest(http.MethodGet, "/server/preferences", nil)
	w := httptest.NewRecorder()
	s.renderTemplate(w, r, "preferences.html", s.preferencesPageData(r, "dark", "en"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// AI.md 23485: a control for every app-specific pastebin_pref_* setting.
	// Each must post to the endpoint and render its current value as selected.
	body := w.Body.String()
	for _, want := range []string{
		`name="pastebin_pref_sort"`,
		`name="pastebin_pref_order"`,
		`name="pastebin_pref_per_page"`,
		// No cookie was sent, so each control must render the hard default as
		// its selected option rather than leaving the first <option> selected
		// by accident.
		`<option value="date" selected>`,
		`<option value="desc" selected>`,
		`<option value="20" selected>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preferences.html missing %q", want)
		}
	}
}

func postPrefsAPI(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := newCSRFServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/server/preferences", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handlePreferencesSetAPI(rec, req)
	return rec
}

// TestPreferencesSetAPI_AppliesValidFields verifies a partial update sets the
// matching cookies and reports what was applied and ignored.
func TestPreferencesSetAPI_AppliesValidFields(t *testing.T) {
	rec := postPrefsAPI(t, `{"theme":"light","lang":"es","prefs":{"sort":"name","order":"bogus"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cookies := map[string]string{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c.Value
	}
	if cookies["theme"] != "light" || cookies["lang"] != "es" || cookies[prefCookiePrefix+"sort"] != "name" {
		t.Errorf("cookies = %v, want theme=light lang=es sort=name", cookies)
	}
	if _, set := cookies[prefCookiePrefix+"order"]; set {
		t.Error("invalid pref value must not be stored")
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			Applied map[string]any `json:"applied"`
			Ignored []string       `json:"ignored"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Data.Applied["theme"] != "light" {
		t.Errorf("applied = %v", out.Data.Applied)
	}
	if len(out.Data.Ignored) != 1 || out.Data.Ignored[0] != "prefs.order" {
		t.Errorf("ignored = %v, want [prefs.order]", out.Data.Ignored)
	}
}

// TestPreferencesSetAPI_OnlyWritesSubmittedFields verifies absent fields are
// left alone and bad input is reported without failing the request.
func TestPreferencesSetAPI_OnlyWritesSubmittedFields(t *testing.T) {
	rec := postPrefsAPI(t, `{"theme":"neon","lang":"xx"}`)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 0 {
		t.Errorf("status=%d cookies=%d, want 200 with no cookies for invalid values", rec.Code, len(rec.Result().Cookies()))
	}
	if rec := postPrefsAPI(t, `{bad`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON status = %d, want 400", rec.Code)
	}
}
