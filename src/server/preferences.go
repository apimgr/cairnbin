package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/apimgr/pastebin/src/common/httputil"
	"github.com/apimgr/pastebin/src/common/i18n"
)

// prefCookiePrefix names every app-specific guest preference cookie
// (AI.md 23478-23501: "{project_name}_pref_{key}"). AI.md's example category
// list ("default view mode, results-per-page, sort order, ...") maps
// directly onto this app's existing /pastes recents controls, so the
// concrete preferences are: sort, order, per_page (see handleRecent in
// server.go). The export/import mechanism below stays generic so any future
// `pastebin_pref_*` cookie round-trips automatically without a second
// storage mechanism ever being invented (AI.md 23498-23501).
const prefCookiePrefix = "pastebin_pref_"

// Concrete pastebin_pref_* keys (AI.md 23485 "Client-Side Preferences"):
// the /pastes recents list's sort column, sort direction, and page size.
const (
	prefKeySort    = "sort"
	prefKeyOrder   = "order"
	prefKeyPerPage = "per_page"
)

// prefKeyPattern constrains the `{key}` portion of a `pastebin_pref_{key}`
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

// appPreferencesFromRequest returns every `pastebin_pref_*` cookie present
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

// extractAppPrefs scans a set of query values (either the live request query
// or a decoded import `code`) for `pastebin_pref_*` params, applying the same
// key/value allowlist as appPreferencesFromRequest — an imported value is
// still untrusted input (AI.md 23512), so it is revalidated the same way.
func extractAppPrefs(values url.Values) map[string]string {
	prefs := make(map[string]string)
	for name, vals := range values {
		if len(vals) == 0 {
			continue
		}
		if key, value, ok := parseAppPref(name, strings.TrimSpace(vals[0])); ok {
			prefs[key] = value
		}
	}
	return prefs
}

// parseAppPref validates a single `pastebin_pref_{key}` name/value pair
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

// setPreferenceCookie writes a client-side preference cookie using the same
// Secure/SameSite/MaxAge shape as handlePreferencesSet and handleConsentSet
// (AI.md 23489: "the server sets the same cookies on its POST endpoints").
func (s *Server) setPreferenceCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	maxAge := 365 * 24 * 60 * 60
	if name == "lang" {
		name = i18n.CookieName()
		maxAge = i18n.CookieMaxAge()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
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
// the server sets: the connection's own TLS state, overridden by an explicit
// server.csrf.secure config value. Marking a cookie Secure on a plain-HTTP
// deployment would make the browser drop it entirely.
func (s *Server) cookieSecure(r *http.Request) bool {
	switch s.liveCfg().Server.CSRF.Secure {
	case "true":
		return true
	case "false":
		return false
	}
	return r.TLS != nil
}

// handlePreferences serves the guest preferences hub — GET /server/preferences,
// API-mirrored at GET /api/{api_version}/server/preferences (AI.md 23508). It
// reports the two exportable preferences (theme, lang) resolved from the
// request's cookies; nothing is read from or written to the database — there
// is no preferences table (AI.md 23513).
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	theme := s.themeFromRequest(r)
	lang := i18n.LangFromRequest(r)

	// App-specific guest preferences travel with theme/lang everywhere they are
	// reported, so the hub and the export endpoint describe the same state.
	appPrefs := appPreferencesFromRequest(r)

	switch detectClientType(r) {
	case "json":
		payload := map[string]interface{}{
			"theme": theme,
			"lang":  lang,
		}
		if len(appPrefs) > 0 {
			payload["prefs"] = appPrefs
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": payload,
		})
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "preferences.html", s.preferencesPageData(r, theme, lang))
		if err != nil {
			fmt.Fprintf(w, "Preferences — theme=%s lang=%s\n", theme, lang)
			for _, k := range sortedPrefKeys(appPrefs) {
				fmt.Fprintf(w, "%s%s: %s\n", prefCookiePrefix, k, appPrefs[k])
			}
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
	default:
		s.renderTemplate(w, r, "preferences.html", s.preferencesPageData(r, theme, lang))
	}
}

// preferencesPageData assembles the template data shared by the preferences
// hub, export, and import-error views.
func (s *Server) preferencesPageData(r *http.Request, theme, lang string) map[string]interface{} {
	data := s.pageData()
	data["PrefTheme"] = theme
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

// preferencesExportQuery builds the canonical `theme=...&lang=...&pastebin_pref_{key}=...`
// query string for the current preferences — the query string IS the portable
// preference state (AI.md 23505: "the code/URL is the preference values, not
// a lookup key"). theme, lang, and every app-specific `pastebin_pref_*`
// cookie round-trip (AI.md 23507); `cookie_consent`/`ccpa_opt_out`/
// `pastebin_build` are never included since they aren't in extra (they don't
// carry the `pastebin_pref_` prefix appPreferencesFromRequest filters on).
func preferencesExportQuery(theme, lang string, extra map[string]string) string {
	q := fmt.Sprintf("theme=%s&lang=%s", url.QueryEscape(theme), url.QueryEscape(lang))
	if len(extra) == 0 {
		return q
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(q)
	for _, k := range keys {
		b.WriteString("&")
		b.WriteString(url.QueryEscape(prefCookiePrefix + k))
		b.WriteString("=")
		b.WriteString(url.QueryEscape(extra[k]))
	}
	return b.String()
}

// handlePreferencesExport serves GET /server/preferences/export, API-mirrored
// at GET /api/{api_version}/server/preferences/export (AI.md 23509). It
// returns the current theme/lang preferences as a full importable URL and as
// a base64url short code for manual retyping on a device without copy/paste.
func (s *Server) handlePreferencesExport(w http.ResponseWriter, r *http.Request) {
	theme := s.themeFromRequest(r)
	lang := i18n.LangFromRequest(r)
	appPrefs := appPreferencesFromRequest(r)
	query := preferencesExportQuery(theme, lang, appPrefs)
	exportURL := s.baseURL(r) + "/server/preferences/import?" + query
	code := base64.RawURLEncoding.EncodeToString([]byte(query))

	switch detectClientType(r) {
	case "json":
		data := map[string]interface{}{
			"theme": theme,
			"lang":  lang,
			"url":   exportURL,
			"code":  code,
		}
		if len(appPrefs) > 0 {
			data["prefs"] = appPrefs
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": data,
		})
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Preferences export\nURL:  %s\nCode: %s\n", exportURL, code)
		for _, k := range sortedPrefKeys(appPrefs) {
			fmt.Fprintf(w, "%s%s: %s\n", prefCookiePrefix, k, appPrefs[k])
		}
	default:
		data := s.preferencesPageData(r, theme, lang)
		data["ExportURL"] = exportURL
		data["ExportCode"] = code
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
// at GET /api/{api_version}/server/preferences/import (AI.md 23512). It
// accepts either explicit `theme`/`lang` query params (from a shared full
// URL) or a `code` param (a pasted base64url short code, with an optional
// leading full-URL prefix already stripped client-side per AI.md 23511;
// stripped again here defensively for the no-JS path). Every value is
// revalidated against its normal allowlist — an imported value is still
// untrusted input (AI.md 23512) — anything unknown or malformed is silently
// dropped rather than applied. Nothing is persisted server-side: decode →
// validate → set cookie → redirect happens in this one request (AI.md 23513).
func (s *Server) handlePreferencesImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	theme := strings.TrimSpace(q.Get("theme"))
	lang := strings.TrimSpace(q.Get("lang"))
	// Values sourced from the query string take precedence; the decoded code
	// only fills in whatever wasn't given directly (mirrors theme/lang below).
	appPrefs := extractAppPrefs(q)

	// The code is decoded whenever one is present: it can carry app preferences
	// the query string never mentions, so gating the decode on a missing
	// theme/lang would silently drop them. Query values still win key by key.
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		if idx := strings.LastIndex(code, "/server/preferences/import?"); idx != -1 {
			code = code[idx+len("/server/preferences/import?"):]
		}
		if decoded, err := base64.RawURLEncoding.DecodeString(code); err == nil {
			if values, err := url.ParseQuery(string(decoded)); err == nil {
				if theme == "" {
					theme = strings.TrimSpace(values.Get("theme"))
				}
				if lang == "" {
					lang = strings.TrimSpace(values.Get("lang"))
				}
				for k, v := range extractAppPrefs(values) {
					if _, exists := appPrefs[k]; !exists {
						appPrefs[k] = v
					}
				}
			}
		}
	}

	if validThemes[theme] {
		s.setPreferenceCookie(w, r, "theme", theme)
	}
	if lang != "" && i18n.IsSupported(lang) {
		s.setPreferenceCookie(w, r, "lang", strings.ToLower(lang))
	}
	for key, value := range appPrefs {
		s.setPreferenceCookie(w, r, prefCookiePrefix+key, value)
	}

	// Never linger on the visible URL/browser history (AI.md 23512).
	dest := "/"
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			if p, ok := safeRedirectPath(u); ok {
				dest = p
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handlePreferencesSetAPI is the JSON mirror of the preferences form
// (`POST /api/{api_version}/server/preferences`, PART 14): a partial update of
// the guest UI preferences. The body may carry `theme`, `lang`, and a `prefs`
// object of app-specific keys. Only the fields present are written, each with
// the same validation as the web form; invalid values are reported in
// `ignored` rather than failing the whole request. It sets the same UI
// cookies as the form and accepts no credential, so there is no ambient
// authority to forge.
func (s *Server) handlePreferencesSetAPI(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Theme *string           `json:"theme"`
		Lang  *string           `json:"lang"`
		Prefs map[string]string `json:"prefs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "invalid JSON",
		})
		return
	}

	applied := map[string]interface{}{}
	ignored := []string{}

	if body.Theme != nil {
		theme := strings.TrimSpace(*body.Theme)
		if validThemes[theme] {
			s.setPreferenceCookie(w, r, "theme", theme)
			applied["theme"] = theme
		} else {
			ignored = append(ignored, "theme")
		}
	}
	if body.Lang != nil {
		lang := strings.ToLower(strings.TrimSpace(*body.Lang))
		if i18n.IsSupported(lang) {
			s.setPreferenceCookie(w, r, "lang", lang)
			applied["lang"] = lang
		} else {
			ignored = append(ignored, "lang")
		}
	}
	appliedPrefs := map[string]string{}
	for key, value := range body.Prefs {
		value = strings.TrimSpace(value)
		if !prefKeyPattern.MatchString(key) || !prefValuePattern.MatchString(value) || !validAppPrefValue(key, value) {
			ignored = append(ignored, "prefs."+sanitizeReportText(key))
			continue
		}
		s.setPreferenceCookie(w, r, prefCookiePrefix+key, value)
		appliedPrefs[key] = value
	}
	if len(appliedPrefs) > 0 {
		applied["prefs"] = appliedPrefs
	}
	sort.Strings(ignored)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": map[string]interface{}{"applied": applied, "ignored": ignored},
	})
}
