package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
)

// prefCookiePrefix names every app-specific guest preference cookie
// (AI.md 23478-23501: "{project_name}_pref_{key}"). AI.md's example category
// list ("default view mode, results-per-page, sort order, ...") maps
// directly onto this app's existing /pastes recents controls, so the
// concrete preferences are: sort, order, per_page (see handleRecent in
// server.go). The export/import mechanism below stays generic so any future
// `cairnbin_pref_*` cookie round-trips automatically without a second
// storage mechanism ever being invented (AI.md 23498-23501).
const prefCookiePrefix = "cairnbin_pref_"

// Concrete cairnbin_pref_* keys (AI.md 23485 "Client-Side Preferences"):
// the /pastes recents list's sort column, sort direction, and page size.
const (
	prefKeySort    = "sort"
	prefKeyOrder   = "order"
	prefKeyPerPage = "per_page"
)

// prefKeyPattern constrains the `{key}` portion of a `cairnbin_pref_{key}`
// cookie/param name — lowercase alnum and underscores only, matching the
// project's own naming conventions for config/cookie keys.
var prefKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// prefValuePattern is a conservative allowlist for app-specific preference
// values in the absence of any concrete preference (and therefore no
// per-key enum) to validate against yet — printable ASCII, no control
// characters, no cookie/URL metacharacters, bounded length. A concrete
// preference listed in prefAllowedValues is checked against its own enum
// first; anything else still has to satisfy this generic pattern.
var prefValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// prefAllowedValues is the per-key enum for app-specific preferences that
// have a fixed set of legal values, with prefDefaultValue holding the value
// each falls back to. Every value is one the preferences page can render
// back as a selected <option>, and every value is one the query layer can
// honor — see the enum/UI parity note on the sort entry below.
var prefAllowedValues = map[string][]string{
	// A deliberate subset of database.publicPasteSortColumns, holding
	// exactly the columns the recents list actually offers. That table also
	// accepts "title" (an alias of "name" — same SQL column) and "id", which
	// have no <option> to render them from; accepting them here would let a
	// cookie hold a value the preferences page shows as "nothing selected",
	// and the page would then display one ordering while the recents list
	// used another. Ordering stays a one-way door: /pastes still accepts any
	// allow-listed column as a query param, but only these become a saved
	// preference.
	prefKeySort:    {"date", "name", "views"},
	prefKeyOrder:   {"desc", "asc"},
	prefKeyPerPage: {"20", "50", "100", "250"},
}

var prefDefaultValue = map[string]string{
	prefKeySort:    "date",
	prefKeyOrder:   "desc",
	prefKeyPerPage: "20",
}

// validAppPrefValue reports whether value is acceptable for a concrete
// app-specific preference key, using that key's enum when it has one. A key
// with no enum is unconstrained here; the generic prefKeyPattern/
// prefValuePattern check still applies to it via parseAppPref.
func validAppPrefValue(key, value string) bool {
	allowed, ok := prefAllowedValues[key]
	if !ok {
		return true
	}
	for _, v := range allowed {
		if v == value {
			return true
		}
	}
	return false
}

// resolvedPref returns the effective value of a single app-specific
// preference for this request: the visitor's cookie when it is present and
// valid, otherwise the key's hard default. This mirrors the fallback chain in
// handleRecent exactly, so the preferences page shows the value the recents
// list would actually use. The cookie is still left unset until the visitor
// saves — resolving a value here does not write anything.
func resolvedPref(r *http.Request, key string) string {
	if v, ok := appPreferencesFromRequest(r)[key]; ok {
		return v
	}
	return prefDefaultValue[key]
}

// appPreferencesFromRequest returns every `cairnbin_pref_*` cookie present
// on the request, keyed by the bare `{key}` suffix (AI.md 23498-23501: "cookie-only,
// read per request, never persisted server-side").
func appPreferencesFromRequest(r *http.Request) map[string]string {
	prefs := make(map[string]string)
	for _, c := range r.Cookies() {
		if key, value, ok := parseAppPref(c.Name, c.Value); ok {
			prefs[key] = value
		}
	}
	return prefs
}

// parseAppPref validates a single `cairnbin_pref_{key}` name/value pair
// against prefKeyPattern/prefValuePattern and, for a known key, that key's
// enum — returning the bare key on success. Anything rejected is treated as
// absent, so a hand-edited or imported cookie simply falls back to the
// default rather than reaching a handler.
func parseAppPref(name, value string) (key string, val string, ok bool) {
	if !strings.HasPrefix(name, prefCookiePrefix) {
		return "", "", false
	}
	key = strings.TrimPrefix(name, prefCookiePrefix)
	if !prefKeyPattern.MatchString(key) || !prefValuePattern.MatchString(value) {
		return "", "", false
	}
	if !validAppPrefValue(key, value) {
		return "", "", false
	}
	return key, value, true
}

// prefCookieMaxAge is the fixed lifetime of every guest preference cookie
// (AI.md sync contract rule 8: Max-Age=31536000).
const prefCookieMaxAge = 365 * 24 * 60 * 60

// setPreferenceCookie writes a guest preference cookie with the attributes
// the sync contract fixes: Path=/, Max-Age=31536000, SameSite=Lax, not
// HttpOnly, no Domain, Secure whenever the resolved request scheme is https.
func (s *Server) setPreferenceCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	if !preferenceCookiesAllowed(r) {
		return
	}
	if name == "lang" {
		name = i18n.CookieName()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   prefCookieMaxAge,
		HttpOnly: false,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// preferenceCookiesAllowed implements AI.md PART 16 "Decline Behavior": once a
// visitor has recorded a cookie_consent choice that declines the preferences
// category, theme, lang and cairnbin_pref_* cookies are never written. On a
// first visit, these cookies wait until the visitor records a choice.
func preferenceCookiesAllowed(r *http.Request) bool {
	st, ok := priorConsentState(r)
	return ok && st.Preferences && !privacyOptOut(r)
}

// rememberPreference persists an explicit query-param choice as the visitor's
// saved preference, but only when the value passes the exact same allowlist
// parseAppPref applies on read. A /pastes query legitimately accepts a wider
// set than the recents list's own <select> offers — ?limit=37 is a valid page
// size, and "title"/"id" are valid ORDER BY columns (see
// database.publicPasteSortColumns) — but persisting one of those would write a
// cookie that is silently dropped on the next request, leaving the preferences
// page showing a default the visitor never picked. Those values stay a
// one-request override and are simply not remembered; the allowlist is what
// keeps the list's actual ordering and the preferences page's displayed state
// in agreement.
func (s *Server) rememberPreference(w http.ResponseWriter, r *http.Request, key, value string) {
	if _, _, ok := parseAppPref(prefCookiePrefix+key, value); !ok {
		return
	}
	s.setPreferenceCookie(w, r, prefCookiePrefix+key, value)
}

// cookieSecure resolves the Secure attribute for every client-readable cookie
// the server sets: true whenever the request scheme, resolved behind trusted
// reverse proxies (X-Forwarded-Proto and friends) and falling back to the
// connection's TLS state, is https; otherwise only an explicit
// server.csrf.secure=true forces it. Marking a cookie Secure on a plain-HTTP
// deployment would make the browser drop it entirely.
func (s *Server) cookieSecure(r *http.Request) bool {
	if s.torRequestOnion(r) != "" {
		return true
	}
	if forwardedScheme(r, s.isTrustedPeer(r)) == "https" {
		return true
	}
	if forwardedScheme(r, s.isTrustedPeer(r)) == "" && r.TLS != nil {
		return true
	}
	return s.liveCfg().Server.CSRF.Secure == "true"
}

// preferencesPageData assembles the template data shared by the preferences
// hub, export, and import-error views.
func (s *Server) preferencesPageData(r *http.Request, theme, lang string) map[string]interface{} {
	data := s.pageData()
	query := preferencesExportQuery(theme, lang, appPreferencesFromRequest(r))
	data["ExportURL"] = s.baseURL(r) + "/server/preferences/import?" + query
	data["ExportCode"] = base64.RawURLEncoding.EncodeToString([]byte(query))
	data["CancelURL"] = prefRedirectDest(r, r.URL.Query().Get("redirect"))
	data["PrefTheme"] = theme
	data["GPCReceived"] = gpcReceived(r)
	data["PrefLang"] = lang
	// App-specific guest preferences (AI.md 23485: "a control for every
	// app-specific `{project_name}_pref_*` setting"). Gaps are filled with the
	// hard defaults so every control renders with a definite selected option
	// rather than a blank one — the cookies themselves stay unset until the
	// visitor actually saves, matching handleRecent's fallback chain.
	data["PrefSort"] = resolvedPref(r, prefKeySort)
	data["PrefOrder"] = resolvedPref(r, prefKeyOrder)
	data["PrefPerPage"] = resolvedPref(r, prefKeyPerPage)
	// Seed the cookie-consent toggles from the visitor's existing choice (if
	// any), falling back to the configured defaults — the preferences page is
	// the one place a visitor can revisit and change consent after the
	// first-visit banner is gone (no-JS parity: a working control must remain
	// reachable without JavaScript, AI.md PART 16 "Frontend Consumes Backend").
	cfg := s.liveCfg()
	def := buildConsentClientConfig(cfg)
	consentPreferences := def.DefaultPreferences
	consentAnalytics := def.DefaultAnalytics
	if prior, hadPrior := priorConsentState(r); hadPrior {
		consentPreferences = prior.Preferences
		consentAnalytics = prior.Analytics
	}
	data["ConsentPreferences"] = consentPreferences
	data["ConsentAnalytics"] = consentAnalytics
	// CCPA opt-out toggle (AI.md 23485: every cookie in the table needs a
	// control here, including CCPA) — same Privacy/CCPAOptedOut shape
	// privacyPageData exposes to privacy.tmpl, so the same template partial
	// pattern works unchanged on this page.
	privacy := cfg.Server.Privacy
	data["Privacy"] = &privacy
	ccpaOptedOut := false
	if cookie, err := r.Cookie("ccpa_opt_out"); err == nil && cookie.Value == "true" {
		ccpaOptedOut = true
	}
	data["CCPAOptedOut"] = ccpaOptedOut
	data["ConsentAnalyticsConfigured"] = cfg.Server.Tracking.Enabled()
	if primary, db, ok := s.requireOwnerForPage(r); ok {
		data["HasOwnerPrimary"] = true
		tokens, err := db.ListOwnerTokens(primary.OwnerID)
		if err == nil {
			items := make([]map[string]any, 0, len(tokens))
			for _, token := range tokens {
				items = append(items, tokenSummary(token))
			}
			data["OwnerTokens"] = items
		}
	}
	return data
}

// preferencesExportQuery builds the canonical query string for the current
// preferences (rule 2): theme and lang always present, each set app preference
// as `cairnbin_pref_{key}`, keys sorted ascending bytewise and form-encoded, so
// the same preferences always produce the same string. The query string IS the
// portable preference state; consent, CCPA and build-stamp cookies are never
// included (they do not carry the `cairnbin_pref_` prefix extra is built from).
func preferencesExportQuery(theme, lang string, extra map[string]string) string {
	q := url.Values{}
	q.Set("theme", theme)
	q.Set("lang", lang)
	for k, v := range extra {
		q.Set(prefCookiePrefix+k, v)
	}
	return q.Encode()
}

// handlePreferencesExport serves GET /server/preferences/export, API-mirrored
// at GET /api/{api_version}/server/preferences/export (rule 13). It returns the
// guest preferences as a full importable URL and as a padding-free base64url
// short code.
func (s *Server) handlePreferencesExport(w http.ResponseWriter, r *http.Request) {
	theme := s.themeFromRequest(r)
	lang := i18n.LangFromRequest(r)
	appPrefs := appPreferencesFromRequest(r)
	query := preferencesExportQuery(theme, lang, appPrefs)
	exportURL := s.baseURL(r) + "/server/preferences/import?" + query
	code := base64.RawURLEncoding.EncodeToString([]byte(query))

	switch prefsClientType(r) {
	case "json":
		data := prefsJSON(theme, lang, appPrefs)
		data["url"] = exportURL
		data["code"] = code
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": data,
		})
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "URL: %s\nCode: %s\n", exportURL, code)
	default:
		data := s.preferencesPageData(r, theme, lang)
		s.renderTemplate(w, r, "preferences.html", data)
	}
}

// sortedPrefKeys returns the app-preference keys in a stable order so plain-text
// output is deterministic across requests (Go map iteration is randomized).
func sortedPrefKeys(prefs map[string]string) []string {
	keys := make([]string, 0, len(prefs))
	for k := range prefs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// handlePreferencesImport serves GET /server/preferences/import, API-mirrored
// at GET /api/{api_version}/server/preferences/import (rules 5-11). The
// request's own query parameters and an optional `code` are merged (direct
// parameters win key by key), validated, and applied additively as cookies.
// The web route answers 303 even when nothing validated; the API route answers
// 200 with the resulting state plus applied/ignored. Nothing is stored
// server-side.
func (s *Server) handlePreferencesImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	merged := url.Values{}
	for name, vals := range decodePrefCode(q.Get("code")) {
		merged[name] = vals
	}
	for name, vals := range q {
		merged[name] = vals
	}

	valid, _, ignored := validateGuestPrefs(merged)
	applied, blocked := s.applyGuestPrefs(w, r, valid)
	ignored = append(ignored, blocked...)
	sort.Strings(ignored)

	if strings.HasPrefix(r.URL.Path, "/api/") {
		theme, lang, prefs := s.currentGuestPrefs(r, valid)
		data := prefsJSON(theme, lang, prefs)
		data["applied"] = applied
		data["ignored"] = ignored
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": data})
		return
	}
	prefSeeOther(w, prefRedirectDest(r, q.Get("redirect")))
}

// handlePreferencesSetAPI is the JSON mirror of the preferences form
// (`POST /api/{api_version}/server/preferences`): a partial update of the guest
// UI preferences (rule 11). The body is application/json or form fields
// (`theme`, `lang`, `cairnbin_pref_{key}`); only fields present are written,
// each validated like an import. It answers 200 with the resulting state plus
// applied/ignored, or 400 VALIDATION_FAILED when no supplied value was valid.
// It sets only UI cookies and accepts no credential, so there is no ambient
// authority to forge.
func (s *Server) handlePreferencesSetAPI(w http.ResponseWriter, r *http.Request) {
	values := url.Values{}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var body map[string]interface{}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"ok": false, "error": "BAD_REQUEST", "message": "invalid JSON",
			})
			return
		}
		for name, raw := range body {
			switch v := raw.(type) {
			case string:
				values.Set(name, v)
			case float64:
				values.Set(name, strconv.FormatFloat(v, 'f', -1, 64))
			default:
				// A non-scalar value can never be valid; an empty value is
				// rejected by validation and reported in ignored.
				values.Set(name, "")
			}
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"ok": false, "error": "BAD_REQUEST", "message": "invalid form",
			})
			return
		}
		values = r.PostForm
	}

	valid, _, ignored := validateGuestPrefs(values)
	if len(valid) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "VALIDATION_FAILED", "message": "Validation failed: no valid preference supplied",
		})
		return
	}
	applied, blocked := s.applyGuestPrefs(w, r, valid)
	ignored = append(ignored, blocked...)
	sort.Strings(ignored)
	theme, lang, prefs := s.currentGuestPrefs(r, valid)
	data := prefsJSON(theme, lang, prefs)
	data["applied"] = applied
	data["ignored"] = ignored
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": data})
}
