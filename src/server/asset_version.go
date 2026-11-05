package server

import (
	"net/http"

	"github.com/apimgr/cairnbin/src/common/buildinfo"
)

// buildCookieName carries the running build stamp to the browser so the next
// HTML request can be detected as stale (AI.md PART 9, line 13332:
// `{project_name}_build`). Essential cookie — no consent required.
const buildCookieName = "cairnbin_build"

// assetURL appends the running build stamp to a static asset path
// (`/static/css/common.css?v={version}-{short_commit}`). Registered as the
// `asset` template helper so no template ever hand-writes a bare `/static/...`
// URL, which AI.md line 13285 calls a bug.
func assetURL(prefix, path string) string {
	return buildinfo.AssetURL(prefix, path)
}

// setStaticCacheHeaders applies the PART 9 static-asset cache policy (AI.md
// 13267-13268, 13286): year-long immutable caching ONLY when the request's
// `?v=` matches the running stamp, otherwise always-revalidated `no-cache`
// plus a build-stamp ETag. The bytes still serve either way, so HTML cached by
// an older build never breaks — it just revalidates.
func setStaticCacheHeaders(w http.ResponseWriter, r *http.Request) {
	stamp := buildinfo.AssetStamp()
	if r.URL.Query().Get("v") == stamp {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+stamp+`"`)
}

// staticCacheHeaders wraps a static file handler with the stamp-aware cache
// policy above.
func staticCacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setStaticCacheHeaders(w, r)
		next.ServeHTTP(w, r)
	})
}

// setHTMLCacheHeaders marks an HTML document uncacheable and attaches a
// build-stamp ETag so an intermediary that ignores `no-store` still revalidates
// after an update (AI.md 13269, 13287).
func setHTMLCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+buildinfo.AssetStamp()+`"`)
}

// versionPurge is the forced recovery path for a browser already holding
// caches from an older build: a mismatched `cairnbin_build` cookie triggers
// `Clear-Site-Data: "cache", "storage"`, evicting the HTTP cache, the Cache API
// caches, and any registered service worker in one response (AI.md
// 13323-13351). `"cookies"` is deliberately absent — it would destroy the
// owner_token cookie and every cookie-stored preference (AI.md 13334).
//
// The same response re-sets the cookie to the current stamp, so the purge is
// naturally one-shot and a first-ever visit (no cookie) never purges. Called
// only from the HTML render path — never for static, API, or /sw.js responses.
func (s *Server) versionPurge(w http.ResponseWriter, r *http.Request) {
	stamp := buildinfo.AssetStamp()
	if c, err := r.Cookie(buildCookieName); err == nil && c.Value != stamp {
		w.Header().Set("Clear-Site-Data", `"cache", "storage"`)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     buildCookieName,
		Value:    stamp,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}
