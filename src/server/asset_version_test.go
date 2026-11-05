package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apimgr/cairnbin/src/common/buildinfo"
	"github.com/apimgr/cairnbin/src/config"
)

func TestSetStaticCacheHeadersMatchingStamp(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/css/common.css?v="+buildinfo.AssetStamp(), nil)

	setStaticCacheHeaders(rec, req)

	if got, want := rec.Header().Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got := rec.Header().Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want empty on an immutable response", got)
	}
}

func TestSetStaticCacheHeadersMismatchedStamp(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/css/common.css?v=0.9.0-0000000", nil)

	setStaticCacheHeaders(rec, req)

	if got, want := rec.Header().Get("Cache-Control"), "no-cache"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("ETag"), `"`+buildinfo.AssetStamp()+`"`; got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

func TestSetHTMLCacheHeaders(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	rec := httptest.NewRecorder()

	setHTMLCacheHeaders(rec)

	if got, want := rec.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("ETag"), `"`+buildinfo.AssetStamp()+`"`; got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

// buildCookie extracts the build-stamp cookie from a recorded response.
func buildCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == buildCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie was set", buildCookieName)
	return nil
}

func TestVersionPurgeFirstVisitDoesNotPurge(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	s := &Server{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	s.versionPurge(rec, req)

	if got := rec.Header().Get("Clear-Site-Data"); got != "" {
		t.Fatalf("Clear-Site-Data = %q, want empty on a first visit", got)
	}
	if got, want := buildCookie(t, rec).Value, buildinfo.AssetStamp(); got != want {
		t.Fatalf("build cookie = %q, want %q", got, want)
	}
}

func TestVersionPurgeMatchingCookieDoesNotPurge(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	s := &Server{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: buildCookieName, Value: buildinfo.AssetStamp()})

	s.versionPurge(rec, req)

	if got := rec.Header().Get("Clear-Site-Data"); got != "" {
		t.Fatalf("Clear-Site-Data = %q, want empty when the stamp matches", got)
	}
}

func TestVersionPurgeStaleCookiePurgesCacheAndStorageOnly(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	s := &Server{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: buildCookieName, Value: "0.9.0-0000000"})

	s.versionPurge(rec, req)

	// "cookies" must never appear here — it would destroy owner_token and every
	// cookie-stored preference (AI.md PART 9, line 13334).
	if got, want := rec.Header().Get("Clear-Site-Data"), `"cache", "storage"`; got != want {
		t.Fatalf("Clear-Site-Data = %q, want %q", got, want)
	}
	// The same response re-stamps the cookie, making the purge one-shot.
	if got, want := buildCookie(t, rec).Value, buildinfo.AssetStamp(); got != want {
		t.Fatalf("build cookie = %q, want %q", got, want)
	}
}

func TestAssetURLHelperMatchesBuildinfo(t *testing.T) {
	buildinfo.Set("1.0.0", "abcdef0")
	got := assetURL("/base", "/static/js/app.js")
	want := buildinfo.AssetURL("/base", "/static/js/app.js")
	if got != want {
		t.Fatalf("assetURL = %q, want %q", got, want)
	}
}
