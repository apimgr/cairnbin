package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

func addConsentCookie(r *http.Request, value string) {
	r.AddCookie(&http.Cookie{Name: "cookie_consent", Value: url.PathEscape(value)})
}

// TestParseAppPref verifies the cairnbin_pref_{key} name/value allowlist
// (AI.md 23485/23501) accepts well-formed pairs and rejects everything else.
func TestParseAppPref(t *testing.T) {
	cases := []struct {
		name    string
		pname   string
		pvalue  string
		wantKey string
		wantOK  bool
	}{
		{"valid", "cairnbin_pref_view_mode", "compact", "view_mode", true},
		{"wrong prefix", "theme", "dark", "", false},
		{"unrelated cookie", "cairnbin_build", "1.0.0-abc123", "", false},
		{"empty key", "cairnbin_pref_", "x", "", false},
		{"uppercase key rejected", "cairnbin_pref_ViewMode", "x", "", false},
		{"key starting with digit rejected", "cairnbin_pref_1abc", "x", "", false},
		{"empty value rejected", "cairnbin_pref_view_mode", "", "", false},
		{"value with space rejected", "cairnbin_pref_view_mode", "a b", "", false},
		{"value with semicolon rejected", "cairnbin_pref_view_mode", "a;b", "", false},
		{"value too long rejected", "cairnbin_pref_view_mode", string(make([]byte, 65)), "", false},
		// Concrete keys carry a per-key enum, so a well-formed but
		// unsupported value is rejected here rather than reaching a handler
		// as a cookie the UI has no <option> to render back.
		{"sort enum accepts a real column", "cairnbin_pref_sort", "views", "sort", true},
		{"sort enum rejects unknown column", "cairnbin_pref_sort", "bogus", "", false},
		{"sort enum rejects SQL fragment", "cairnbin_pref_sort", "date;drop", "", false},
		{"order enum accepts desc", "cairnbin_pref_order", "desc", "order", true},
		{"order enum rejects sideways", "cairnbin_pref_order", "sideways", "", false},
		{"per_page enum accepts a listed size", "cairnbin_pref_per_page", "50", "per_page", true},
		{"per_page enum rejects unlisted size", "cairnbin_pref_per_page", "37", "", false},
		{"per_page enum rejects non-numeric", "cairnbin_pref_per_page", "lots", "", false},
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

// TestAppPreferencesFromRequest verifies only cairnbin_pref_* cookies are
// surfaced, non-preference cookies (theme, cookie_consent, cairnbin_build)
// are excluded, and malformed values are dropped rather than applied.
func TestAppPreferencesFromRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/server/preferences", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	addConsentCookie(req, `{"essential":true}`)
	req.AddCookie(&http.Cookie{Name: "ccpa_opt_out", Value: "true"})
	req.AddCookie(&http.Cookie{Name: "cairnbin_build", Value: "1.0.0-abc123"})
	req.AddCookie(&http.Cookie{Name: "cairnbin_pref_view_mode", Value: "compact"})
	req.AddCookie(&http.Cookie{Name: "cairnbin_pref_bad value", Value: "x"})

	got := appPreferencesFromRequest(req)
	if len(got) != 1 {
		t.Fatalf("appPreferencesFromRequest() = %v, want exactly 1 entry", got)
	}
	if got["view_mode"] != "compact" {
		t.Fatalf("appPreferencesFromRequest()[\"view_mode\"] = %q, want %q", got["view_mode"], "compact")
	}
}

// TestPreferencesExportQuery verifies theme/lang always round-trip and every
// app-specific cairnbin_pref_* entry is appended in stable (sorted) order
// (AI.md 23507/23503).
func TestPreferencesExportQuery(t *testing.T) {
	got := preferencesExportQuery("dark", "fr", nil)
	want := "lang=fr&theme=dark"
	if got != want {
		t.Fatalf("preferencesExportQuery(nil) = %q, want %q", got, want)
	}

	got = preferencesExportQuery("dark", "fr", map[string]string{
		"view_mode": "compact",
		"per_page":  "50",
	})
	want = "cairnbin_pref_per_page=50&cairnbin_pref_view_mode=compact&lang=fr&theme=dark"
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
			addConsentCookie(r, `{"essential":true,"preferences":true}`)
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

	// AI.md 23485: a control for every app-specific cairnbin_pref_* setting.
	// Each must post to the endpoint and render its current value as selected.
	body := w.Body.String()
	if got := strings.Count(body, `method="POST"`) + strings.Count(body, `method="post"`); got != 1 {
		t.Errorf("preferences page has %d POST forms, want exactly one", got)
	}
	if !strings.Contains(body, `action="/server/preferences"`) || strings.Contains(body, `/server/consent`) || strings.Contains(body, `/announcements/dismiss`) {
		t.Error("preferences page must keep all changes in its single preferences form")
	}
	for _, want := range []string{
		`name="cairnbin_pref_sort"`,
		`name="cairnbin_pref_order"`,
		`name="cairnbin_pref_per_page"`,
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
	addConsentCookie(req, `{"essential":true,"preferences":true}`)
	rec := httptest.NewRecorder()
	s.handlePreferencesSetAPI(rec, req)
	return rec
}

type prefsAPIOut struct {
	OK   bool `json:"ok"`
	Data struct {
		Theme   string            `json:"theme"`
		Lang    string            `json:"lang"`
		Prefs   map[string]string `json:"prefs"`
		Applied []string          `json:"applied"`
		Ignored []string          `json:"ignored"`
	} `json:"data"`
}

// TestPreferencesSetAPI_AppliesValidFields verifies a partial update sets the
// matching cookies and reports applied/ignored as name arrays.
func TestPreferencesSetAPI_AppliesValidFields(t *testing.T) {
	rec := postPrefsAPI(t, `{"theme":"light","lang":"es","cairnbin_pref_sort":"name","cairnbin_pref_order":"bogus","cookie_consent":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cookies := setCookiesOf(rec)
	if cookies["theme"] != "light" || cookies["lang"] != "es" || cookies[prefCookiePrefix+"sort"] != "name" {
		t.Errorf("cookies = %v, want theme=light lang=es sort=name", cookies)
	}
	if _, set := cookies[prefCookiePrefix+"order"]; set {
		t.Error("invalid pref value must not be stored")
	}
	var out prefsAPIOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Data.Theme != "light" || out.Data.Lang != "es" || out.Data.Prefs["sort"] != "name" {
		t.Errorf("state = %+v", out.Data)
	}
	if strings.Join(out.Data.Applied, ",") != "cairnbin_pref_sort,lang,theme" {
		t.Errorf("applied = %v", out.Data.Applied)
	}
	if strings.Join(out.Data.Ignored, ",") != "cairnbin_pref_order,cookie_consent" {
		t.Errorf("ignored = %v", out.Data.Ignored)
	}
}

// TestPreferencesSetAPI_ValidationFailed verifies 400 VALIDATION_FAILED only
// when nothing supplied was valid, and 400 BAD_REQUEST for malformed JSON.
func TestPreferencesSetAPI_ValidationFailed(t *testing.T) {
	rec := postPrefsAPI(t, `{"theme":"neon","lang":"xx"}`)
	if rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 {
		t.Errorf("status=%d cookies=%d, want 400 with no cookies", rec.Code, len(rec.Result().Cookies()))
	}
	if !strings.Contains(rec.Body.String(), "VALIDATION_FAILED") {
		t.Errorf("body = %s, want VALIDATION_FAILED", rec.Body.String())
	}
	if rec := postPrefsAPI(t, `{bad`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON status = %d, want 400", rec.Code)
	}
}

func importPrefs(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	s := newMinimalServer(config.DefaultConfig())
	req := httptest.NewRequest(http.MethodGet, target, nil)
	addConsentCookie(req, `{"essential":true,"preferences":true}`)
	rec := httptest.NewRecorder()
	s.handlePreferencesImport(rec, req)
	return rec
}

// TestPreferencesImport_CodeForms verifies a pasted full URL, plain query
// string, padded and unpadded base64url all import, and that direct
// parameters override code values key by key.
func TestPreferencesImport_CodeForms(t *testing.T) {
	query := "cairnbin_pref_sort=name&lang=fr&theme=light"
	unpadded := base64.RawURLEncoding.EncodeToString([]byte(query))
	padded := base64.URLEncoding.EncodeToString([]byte(query))
	for name, code := range map[string]string{
		"unpadded": unpadded,
		"padded":   padded,
		"plain":    query,
		"fullurl":  "https://h.example/server/preferences/import?" + query,
	} {
		rec := importPrefs(t, "/server/preferences/import?code="+url.QueryEscape(code))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want 303", name, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Body.Len() != 0 {
			t.Errorf("%s: want no-store and empty body", name)
		}
		c := setCookiesOf(rec)
		if c["theme"] != "light" || c["lang"] != "fr" || c[prefCookiePrefix+"sort"] != "name" {
			t.Errorf("%s: cookies = %v", name, c)
		}
	}
	rec := importPrefs(t, "/server/preferences/import?theme=dark&code="+unpadded)
	if c := setCookiesOf(rec); c["theme"] != "dark" || c["lang"] != "fr" {
		t.Errorf("direct param must win over code: %v", c)
	}
}

// TestPreferencesImport_InvalidAndExcluded verifies invalid values and
// excluded names are dropped (still 303), never removing existing cookies.
func TestPreferencesImport_InvalidAndExcluded(t *testing.T) {
	rec := importPrefs(t, "/server/preferences/import?theme=neon&lang=xx&cookie_consent=x&cairnbin_build=1&cairnbin_pref_sort=bogus")
	if rec.Code != http.StatusSeeOther || len(rec.Result().Cookies()) != 0 {
		t.Errorf("status=%d cookies=%v, want 303 and no cookies", rec.Code, rec.Result().Cookies())
	}
	rec = importPrefs(t, "/server/preferences/import?lang=FR-CA")
	if c := setCookiesOf(rec); c["lang"] != "fr" {
		t.Errorf("regional tag must fall back to base: %v", c)
	}
	long := strings.Repeat("a", prefMaxCodeLen+1)
	rec = importPrefs(t, "/server/preferences/import?code="+long)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("oversized code must be ignored")
	}
}

// TestPreferencesImport_APIResponse verifies the API mirror answers 200 JSON
// with applied/ignored arrays and no redirect.
func TestPreferencesImport_APIResponse(t *testing.T) {
	rec := importPrefs(t, "/api/v1/server/preferences/import?lang=fr&cookie_consent=x")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out prefsAPIOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Lang != "fr" || len(out.Data.Applied) != 1 || out.Data.Applied[0] != "lang" ||
		len(out.Data.Ignored) != 1 || out.Data.Ignored[0] != "cookie_consent" || out.Data.Prefs == nil {
		t.Errorf("data = %+v", out.Data)
	}
}

// TestPrefRedirectDest verifies the redirect target rules.
func TestPrefRedirectDest(t *testing.T) {
	cases := []struct {
		name, redirect, referer, want string
	}{
		{"relative redirect", "/pastes?x=1", "", "/pastes?x=1"},
		{"protocol-relative rejected", "//evil.example", "", "/"},
		{"backslash rejected", "/\\evil", "", "/"},
		{"absolute rejected", "https://evil.example/", "", "/"},
		{"crlf rejected", "/a\r\nSet-Cookie: x=1", "", "/"},
		{"import path falls through", "/server/preferences/import?x=1", "http://h.example/about", "/about"},
		{"same-origin referer", "", "http://h.example/about?a=1", "/about?a=1"},
		{"foreign referer", "", "http://evil.example/about", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://h.example/server/preferences/import", nil)
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			if got := prefRedirectDest(r, tc.redirect); got != tc.want {
				t.Errorf("prefRedirectDest = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPreferencesCookieAttributes verifies rule 8 behind a TLS-terminating
// proxy: Secure follows the forwarded scheme and Max-Age is fixed.
func TestPreferencesCookieAttributes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/server/preferences/import?theme=dark", nil)
	addConsentCookie(req, `{"essential":true,"preferences":true}`)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	newMinimalServer(config.DefaultConfig()).handlePreferencesImport(rec, req)
	cs := rec.Result().Cookies()
	if len(cs) != 1 || !cs[0].Secure || cs[0].MaxAge != 31536000 || cs[0].HttpOnly || cs[0].Path != "/" || cs[0].Domain != "" {
		t.Errorf("cookie attributes = %+v", cs)
	}
}

// TestPreferencesExportRoundTrip verifies export -> import yields identical
// cookies and the code decodes to the canonical query.
func TestPreferencesExportRoundTrip(t *testing.T) {
	q := preferencesExportQuery("light", "fr", map[string]string{"sort": "name"})
	code := base64.RawURLEncoding.EncodeToString([]byte(q))
	rec := importPrefs(t, "/server/preferences/import?code="+code)
	c := setCookiesOf(rec)
	if c["theme"] != "light" || c["lang"] != "fr" || c[prefCookiePrefix+"sort"] != "name" || len(c) != 3 {
		t.Errorf("round trip cookies = %v", c)
	}
}

func TestPreferencesWebSaveAlwaysRedirectsAndCodeOverrides(t *testing.T) {
	s := newMinimalServer(config.DefaultConfig())
	form := url.Values{"theme": {"light"}, "lang": {"en"}, "consent_form": {"1"}, "consent_preferences": {"1"}}
	form.Set("code", base64.RawURLEncoding.EncodeToString([]byte("theme=dark&lang=fr")))
	req := httptest.NewRequest(http.MethodPost, "/server/preferences", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "test"})
	rec := httptest.NewRecorder()
	s.handlePreferencesSet(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	cookies := setCookiesOf(rec)
	if cookies["theme"] != "dark" || cookies[i18n.CookieName()] != "fr" {
		t.Errorf("imported code did not override form values: %v", cookies)
	}

	empty := httptest.NewRecorder()
	s.handlePreferencesSet(empty, httptest.NewRequest(http.MethodPost, "/server/preferences", strings.NewReader("")))
	if empty.Code != http.StatusSeeOther {
		t.Errorf("empty save status = %d, want 303", empty.Code)
	}
}

func TestPreferencesResetExpiresAppCookiesOnly(t *testing.T) {
	s := newMinimalServer(config.DefaultConfig())
	req := httptest.NewRequest(http.MethodPost, "/server/preferences", strings.NewReader("action=reset"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range []string{"theme=dark", "lang=fr", "cairnbin_pref_sort=name", "cookie_consent=x", "ccpa_opt_out=true", "cairnbin_build=old", "dismissed_announcements=x", "flash=x", "csrf_token=keep", "session=keep", "owner_token=keep"} {
		req.Header.Add("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	s.handlePreferencesSet(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Clear-Site-Data") != `"storage"` {
		t.Fatalf("reset response status=%d Clear-Site-Data=%q", rec.Code, rec.Header().Get("Clear-Site-Data"))
	}
	deleted := map[string]bool{}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge < 0 {
			deleted[cookie.Name] = true
		}
	}
	for _, name := range []string{"theme", "lang", "cairnbin_pref_sort", "cookie_consent", "ccpa_opt_out", "cairnbin_build", "dismissed_announcements", "flash"} {
		if !deleted[name] {
			t.Errorf("reset did not expire %s", name)
		}
	}
	for _, name := range []string{"csrf_token", "session", "owner_token"} {
		if deleted[name] {
			t.Errorf("reset expired credential cookie %s", name)
		}
	}
}
