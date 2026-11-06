package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSRFMiddlewareSkipsBackendAndCompat verifies out-of-scope routes neither
// reject tokenless requests nor issue CSRF cookies.
func TestCSRFMiddlewareSkipsBackendAndCompat(t *testing.T) {
	s := newCSRFServer()
	for _, path := range []string{"/api/v1/pastes", "/api/new", "/documents", "/upload", "/view/abc"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
			rec := httptest.NewRecorder()
			called := false
			s.csrfMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
			if !called {
				t.Fatal("out-of-scope request did not reach handler")
			}
			if cookies := rec.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("out-of-scope route received CSRF cookie: %#v", cookies)
			}
		})
	}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/", strings.NewReader("sprunge=compat+body"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	called := false
	s.csrfMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
	if !called || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("compat root request reached handler=%v cookies=%v, want pass-through without token", called, rec.Result().Cookies())
	}
}

func TestCSRFMiddlewareProtectsFrontendAliases(t *testing.T) {
	s := newCSRFServer()
	for _, path := range []string{"/", "/pastes", "/create", "/remove/abc", "/server/preferences"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
			rec := httptest.NewRecorder()
			called := false
			s.csrfMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
			if called || rec.Code != http.StatusForbidden {
				t.Fatalf("request reached handler=%v status=%d, want rejection", called, rec.Code)
			}
		})
	}
}

func TestCSRFMiddlewareOnlyIssuesCookiesForHTMLNavigation(t *testing.T) {
	s := newCSRFServer()
	for _, tc := range []struct {
		name       string
		path       string
		accept     string
		wantCookie bool
	}{
		{"HTML page", "/", "text/html", true},
		{"static asset", "/static/app.js", "*/*", false},
		{"raw response", "/raw/abc", "text/plain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com"+tc.path, nil)
			req.Header.Set("Accept", tc.accept)
			rec := httptest.NewRecorder()
			s.csrfMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
			found := false
			for _, cookie := range rec.Result().Cookies() {
				found = found || cookie.Name == "csrf_token"
			}
			if found != tc.wantCookie {
				t.Fatalf("CSRF cookie present = %v, want %v", found, tc.wantCookie)
			}
		})
	}
}

func TestCSRFMiddlewareRootCompatibilityUsesActualDispatcherHosts(t *testing.T) {
	s := newCSRFServer()
	for _, tc := range []struct {
		host   string
		wantOK bool
	}{
		{"termbin.pste.us", true},
		{"sprunge.pste.us", true},
		{"microbin.pste.us", false},
		{"example.com", false},
	} {
		t.Run(tc.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/", nil)
			rec := httptest.NewRecorder()
			called := false
			s.csrfMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
			if called != tc.wantOK {
				t.Fatalf("handler reached = %v, want %v (status %d)", called, tc.wantOK, rec.Code)
			}
		})
	}
}

func TestSecFetchMiddlewareSkipsBackendAndCompat(t *testing.T) {
	s := newCSRFServer()
	s.cfg.Web.Headers.SecFetchValidation = true
	for _, path := range []string{"/api/v1/pastes", "/documents"} {
		req := httptest.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		rec := httptest.NewRecorder()
		called := false
		s.secFetchMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(rec, req)
		if !called || rec.Code != http.StatusOK {
			t.Errorf("%s reached handler=%v status=%d, want pass-through", path, called, rec.Code)
		}
	}
}
