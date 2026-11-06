package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/apimgr/cairnbin/src/common/httputil"
	"github.com/apimgr/cairnbin/src/common/i18n"
)

// prefMaxCodeLen bounds the length of an import `code` so an oversized value
// is ignored outright instead of being decoded.
const prefMaxCodeLen = 2048

// prefControlFields are request fields that steer a preferences request
// rather than carry a preference, so they are never reported as ignored.
var prefControlFields = map[string]bool{"code": true, "redirect": true, "csrf_token": true, "token": true, "action": true, "consent_form": true, "consent_preferences": true, "consent_analytics": true, "ccpa_opt_out": true}

// prefBase64Pattern matches a base64url code, padded or not; a plain query
// string always has characters after its first `=`, so it never matches.
var prefBase64Pattern = regexp.MustCompile(`^[A-Za-z0-9_-]+={0,2}$`)

// decodePrefCode turns an import `code` into preference values. A code may be
// a full export URL, a plain query string, or a base64url-encoded query
// (padded or not). Anything undecodable or oversized yields no values.
func decodePrefCode(code string) url.Values {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > prefMaxCodeLen {
		return url.Values{}
	}
	query := code
	if i := strings.Index(code, "?"); i >= 0 {
		query = code[i+1:]
	} else if prefBase64Pattern.MatchString(code) {
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(code, "="))
		if err != nil {
			return url.Values{}
		}
		query = string(decoded)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return url.Values{}
	}
	return values
}

// normalizeGuestLang maps a requested language (case-insensitive, regional
// tags reduced to the base tag) onto a supported language code.
func normalizeGuestLang(raw string) (string, bool) {
	lang := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	if lang == "" || !i18n.IsAvailable(lang) {
		return "", false
	}
	return lang, true
}

// validateGuestPrefs filters submitted values down to the guest preferences
// that pass the same allowlists applied on read. It returns the valid
// name->value pairs (cookie names: theme, lang, cairnbin_pref_{key}) plus the
// sorted names that were valid and the sorted names that were invalid.
func validateGuestPrefs(values url.Values) (valid map[string]string, accepted, ignored []string) {
	valid = make(map[string]string)
	accepted = []string{}
	ignored = []string{}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	considered := 0
	for _, name := range names {
		if prefControlFields[name] {
			continue
		}
		if considered >= 32 {
			ignored = append(ignored, name)
			continue
		}
		considered++
		value := ""
		vals := values[name]
		if len(vals) > 0 {
			value = strings.TrimSpace(vals[0])
		}
		if len(value) > 64 {
			ignored = append(ignored, name)
			continue
		}
		ok := false
		switch {
		case name == "theme":
			if validThemes[value] {
				valid[name], ok = value, true
			}
		case name == "lang":
			if lang, good := normalizeGuestLang(value); good {
				valid[name], ok = lang, true
			}
		case strings.HasPrefix(name, prefCookiePrefix):
			if _, v, good := parseAppPref(name, value); good {
				valid[name], ok = v, true
			}
		}
		if ok {
			accepted = append(accepted, name)
		} else {
			ignored = append(ignored, name)
		}
	}
	sort.Strings(accepted)
	sort.Strings(ignored)
	return valid, accepted, ignored
}

// applyGuestPrefs writes each validated preference as its cookie. Writes go
// through setPreferenceCookie, so no value is reported as applied unless its
// cookie was actually allowed by consent and privacy signals.
func (s *Server) applyGuestPrefs(w http.ResponseWriter, r *http.Request, valid map[string]string) (applied, blocked []string) {
	names := make([]string, 0, len(valid))
	for name := range valid {
		names = append(names, name)
	}
	sort.Strings(names)
	if !preferenceCookiesAllowed(r) {
		return nil, names
	}
	for _, name := range names {
		s.setPreferenceCookie(w, r, name, valid[name])
	}
	return names, nil
}

// currentGuestPrefs reports the effective preference state: what the request
// already carries, overlaid with the values just applied. The overlay is
// skipped when consent or an honored privacy signal blocked the writes.
func (s *Server) currentGuestPrefs(r *http.Request, valid map[string]string) (theme, lang string, prefs map[string]string) {
	theme = s.themeFromRequest(r)
	lang = i18n.LangFromRequest(r)
	prefs = appPreferencesFromRequest(r)
	if !preferenceCookiesAllowed(r) {
		return theme, lang, prefs
	}
	if v, ok := valid["theme"]; ok {
		theme = v
	}
	if v, ok := valid["lang"]; ok {
		lang = v
	}
	for name, v := range valid {
		if strings.HasPrefix(name, prefCookiePrefix) {
			prefs[strings.TrimPrefix(name, prefCookiePrefix)] = v
		}
	}
	return theme, lang, prefs
}

// prefsJSON builds the shared JSON payload describing the guest preferences.
// prefs is always an object, never null.
func prefsJSON(theme, lang string, prefs map[string]string) map[string]interface{} {
	out := make(map[string]string, len(prefs))
	for k, v := range prefs {
		out[k] = v
	}
	return map[string]interface{}{"theme": theme, "lang": lang, "prefs": out}
}

// prefsClientType picks the response format for the preference endpoints:
// API paths answer JSON unless plain text is requested; web paths use the
// standard content negotiation.
func prefsClientType(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if strings.Contains(r.Header.Get("Accept"), "text/plain") {
			return "text"
		}
		return "json"
	}
	return detectClientType(r)
}

// prefSafeLocalPath reports whether p is a same-site absolute path that is
// safe to redirect to: leading single slash, no backslash or control
// characters, and not the import endpoint itself.
func prefSafeLocalPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return false
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	path := strings.SplitN(p, "?", 2)[0]
	return !strings.HasPrefix(path, "/server/preferences/import") &&
		!strings.HasPrefix(path, "/server/preferences/export") &&
		path != "/server/preferences"
}

// prefRedirectDest resolves where a preference change returns to: the
// supplied redirect when it is a safe local path, else the same-origin
// Referer's path and query, else the site root.
func prefRedirectDest(r *http.Request, redirect string) string {
	if prefSafeLocalPath(redirect) {
		return redirect
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			dest := u.EscapedPath()
			if u.RawQuery != "" {
				dest += "?" + u.RawQuery
			}
			if prefSafeLocalPath(dest) {
				return dest
			}
		}
	}
	return "/"
}

// prefSeeOther answers 303 with an empty body and no-store caching, so a
// preference change is never cached or replayed.
func prefSeeOther(w http.ResponseWriter, dest string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", dest)
	w.WriteHeader(http.StatusSeeOther)
}

// handlePreferences serves the guest preferences hub — GET /server/preferences,
// API-mirrored at GET /api/{api_version}/server/preferences. It reports the
// preferences resolved from the request's cookies; nothing is read from or
// written to the database.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	theme := s.themeFromRequest(r)
	lang := i18n.LangFromRequest(r)
	appPrefs := appPreferencesFromRequest(r)

	switch prefsClientType(r) {
	case "json":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": prefsJSON(theme, lang, appPrefs),
		})
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "preferences.html", s.preferencesPageData(r, theme, lang))
		if err != nil {
			_, _ = fmt.Fprintf(w, "Preferences — theme=%s lang=%s\n", theme, lang)
			for _, k := range sortedPrefKeys(appPrefs) {
				_, _ = fmt.Fprintf(w, "%s%s: %s\n", prefCookiePrefix, k, appPrefs[k])
			}
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
	default:
		s.renderTemplate(w, r, "preferences.html", s.preferencesPageData(r, theme, lang))
	}
}

func (s *Server) handleOwnerTokensPage(w http.ResponseWriter, r *http.Request) {
	data := s.preferencesPageData(r, s.themeFromRequest(r), i18n.LangFromRequest(r))
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "preferences_tokens.html", data)
		if err != nil {
			_, _ = fmt.Fprintf(w, "%s\n/server/preferences/tokens\n", s.liveCfg().Web.SiteTitle)
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "preferences_tokens.html", data)
}
