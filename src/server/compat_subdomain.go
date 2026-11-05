package server

import (
	"net/http"
	"strings"

	"github.com/apimgr/cairnbin/src/handler/compat"
)

// compatSubdomainAliases maps a recognized Host label (short and long form,
// lowercase) to the canonical compat-target name. No server.yml config is
// involved — recognition is automatic and hardcoded, per the operator's
// requested "no config, subdomain-only" behavior.
var compatSubdomainAliases = map[string]string{
	"pb":       "cairnbin",
	"cairnbin": "cairnbin",
	"lp":       "lenpaste",
	"lenpaste": "lenpaste",
	"sk":       "stikked",
	"stikked":  "stikked",
	"hb":       "hastebin",
	"hastebin": "hastebin",
	"dp":       "dpaste",
	"dpaste":   "dpaste",
	"mb":       "microbin",
	"microbin": "microbin",
	// Curl-upload family: these four share one route (POST /, dispatched by
	// compat.RootUpload via form-field presence), so unlike the targets above
	// they are NOT listed in compatOwnership() — compatModeGate must never
	// exclusively hide POST / for one target. Instead, the resolved mode is
	// threaded into the request context via compat.WithTarget below, and
	// RootUpload consults it to interpret a plain, field-less body as that
	// target's content.
	"tb":      "termbin",
	"termbin": "termbin",
	"ix":      "ixio",
	"sprunge": "sprunge",
	"0x0":     "zerox0",
	"st":      "zerox0",
}

// compatRouteRule identifies one compat-handler-owned route (method plus an
// exact path or a path prefix). Only routes actually dispatched to a
// compatHandler.* method are listed here — native handlers that are simply
// reachable under an extra alias path (e.g. /list, /file/{id}, /p/{id}) stay
// available under every Host, since they don't emulate a foreign wire format.
type compatRouteRule struct {
	method string
	path   string
	prefix bool
}

// compatOwnership returns, per compat target, the exact set of
// compat-handler-owned routes that belong to it. Built per-request because
// the versioned-API segment depends on the configured API version.
func (s *Server) compatOwnership() map[string][]compatRouteRule {
	av := "/api/" + s.apiVersion()
	return map[string][]compatRouteRule{
		"cairnbin": {
			{http.MethodPost, "/api/api_post.php", false},
			{http.MethodGet, "/api/api_raw.php", false},
			{http.MethodPost, "/api/api_login.php", false},
		},
		"lenpaste": {
			{http.MethodPost, "/api/new", false},
			{http.MethodGet, "/api/get", false},
			{http.MethodDelete, "/api/remove", false},
			{http.MethodGet, "/api/remove", false},
			{http.MethodGet, "/api/list", false},
			{http.MethodPost, av + "/new", false},
			{http.MethodGet, av + "/get", false},
			{http.MethodGet, av + "/getServerInfo", false},
		},
		"stikked": {
			{http.MethodPost, "/api/create", false},
			{http.MethodGet, "/api/paste/", true},
		},
		"hastebin": {
			{http.MethodPost, "/documents", false},
			{http.MethodGet, "/documents/", true},
		},
		"dpaste": {
			{http.MethodPost, "/api/", false},
			{http.MethodPost, "/api/v2/", false},
		},
		"microbin": {
			{http.MethodGet, av + "/pasta", false},
			{http.MethodPost, av + "/pasta", false},
			{http.MethodGet, av + "/pasta/", true},
			{http.MethodDelete, av + "/pasta/", true},
		},
	}
}

// compatRuleMatches reports whether the request method/path matches rule.
func compatRuleMatches(rule compatRouteRule, method, path string) bool {
	if !strings.EqualFold(rule.method, method) {
		return false
	}
	if rule.prefix {
		return strings.HasPrefix(path, rule.path)
	}
	return path == rule.path
}

// requestCompatHostLabel resolves the leftmost DNS label of the effective
// Host for this request, honoring reverse-proxy headers only when the peer
// is trusted (AI.md PART 12 FQDN resolution — mirrors baseURL()'s pattern;
// never trusts a raw Host/X-Forwarded-Host from an untrusted peer).
func (s *Server) requestCompatHostLabel(r *http.Request) string {
	host := r.Host
	if s.isTrustedPeer(r) {
		for _, hdr := range []string{"X-Forwarded-Host", "X-Real-Host", "X-Original-Host"} {
			if fh := strings.TrimSpace(r.Header.Get(hdr)); fh != "" {
				host = fh
				break
			}
		}
	}
	hostname, _ := splitHostPort(host)
	label, _, _ := strings.Cut(hostname, ".")
	return strings.ToLower(strings.TrimSpace(label))
}

// compatModeFromLabel resolves a Host label to a canonical compat-target
// name, or "" when unrecognized (default behavior — every compat target
// remains reachable, exactly like today).
func compatModeFromLabel(label string) string {
	return compatSubdomainAliases[label]
}

// compatModeGate is middleware that, when the request's Host resolves to a
// recognized compat-target subdomain (e.g. mb./microbin. or lp./lenpaste.),
// hides every other target's compat-handler-owned routes with a themed 404
// so routing under that subdomain "mirrors the app routes exactly" for the
// matched target. Native app routes (including compat-style aliases served
// by native handlers) and the matched target's own routes are unaffected.
// Any non-matching Host (including the base domain) falls through unchanged.
func (s *Server) compatModeGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := compatModeFromLabel(s.requestCompatHostLabel(r))
		if mode == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Thread the resolved mode into the request context so
		// compat.RootUpload can force curl-upload-family interpretation of a
		// field-less POST / body on tb./termbin./ix./sprunge./0x0./st.
		// subdomains. A no-op for targets RootUpload doesn't recognise.
		r = compat.WithTarget(r, mode)
		for target, rules := range s.compatOwnership() {
			if target == mode {
				continue
			}
			for _, rule := range rules {
				if compatRuleMatches(rule, r.Method, r.URL.Path) {
					s.renderErrorPage(w, r, http.StatusNotFound, "The requested resource was not found.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
