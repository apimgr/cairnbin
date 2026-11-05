package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// ─── isEmbedPath ─────────────────────────────────────────────────────────────

func TestIsEmbedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/emb/abc123", true},
		{"/emb/a", true},
		{"/emb/", false},
		{"/emb", false},
		{"/", false},
		{"/paste/abc", false},
		{"/embed/abc", false},
		{"/api/v1/pastes", false},
	}
	for _, tc := range cases {
		if got := isEmbedPath(tc.path); got != tc.want {
			t.Errorf("isEmbedPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// ─── Embed frame policy: X-Frame-Options + frame-ancestors ──────────────────

func TestSecurityHeadersEmbedFramePolicy(t *testing.T) {
	cases := []struct {
		name                string
		path                string
		embedFrameAncestors string
		wantXFO             string
		wantFrameAncestors  string
	}{
		{"non-embed route keeps SAMEORIGIN and self", "/paste/abc", "", "SAMEORIGIN", "frame-ancestors 'self'"},
		{"embed route drops XFO and widens to *", "/emb/abc123", "", "", "frame-ancestors *"},
		{"embed route honors configured allow-list", "/emb/abc123", "'self' https://example.com", "", "frame-ancestors 'self' https://example.com"},
		{"bare /emb/ is not embeddable", "/emb/", "", "SAMEORIGIN", "frame-ancestors 'self'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Web.CSP.Enabled = true
			cfg.Web.CSP.Mode = "enforce"
			cfg.Web.CSP.EmbedFrameAncestors = tc.embedFrameAncestors
			s := newMinimalServer(cfg)

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			h := s.securityHeadersMiddleware(inner)

			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if got := w.Header().Get("X-Frame-Options"); got != tc.wantXFO {
				t.Errorf("X-Frame-Options = %q, want %q", got, tc.wantXFO)
			}
			csp := w.Header().Get("Content-Security-Policy")
			if csp == "" {
				csp = w.Header().Get("Content-Security-Policy-Report-Only")
			}
			if !strings.Contains(csp, tc.wantFrameAncestors) {
				t.Errorf("CSP %q missing directive %q", csp, tc.wantFrameAncestors)
			}
		})
	}
}

// ─── Sec-Fetch-Dest cross-site framing rejection ─────────────────────────────

func TestSecFetchCrossSiteFraming(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		secFetchSite string
		secFetchDest string
		wantStatus   int
	}{
		{"cross-site iframe of non-embed blocked", "/paste/abc", "cross-site", "iframe", http.StatusForbidden},
		{"cross-site frame of non-embed blocked", "/paste/abc", "cross-site", "frame", http.StatusForbidden},
		{"cross-site embed of non-embed blocked", "/paste/abc", "cross-site", "embed", http.StatusForbidden},
		{"cross-site object of non-embed blocked", "/paste/abc", "cross-site", "object", http.StatusForbidden},
		{"cross-site iframe of /emb allowed", "/emb/abc123", "cross-site", "iframe", http.StatusOK},
		{"same-origin iframe allowed", "/paste/abc", "same-origin", "iframe", http.StatusOK},
		{"cross-site document allowed", "/paste/abc", "cross-site", "document", http.StatusOK},
		{"no sec-fetch headers allowed", "/paste/abc", "", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Web.Headers.SecFetchValidation = true
			s := newMinimalServer(cfg)

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			h := s.secFetchMiddleware(inner)

			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.secFetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.secFetchSite)
			}
			if tc.secFetchDest != "" {
				r.Header.Set("Sec-Fetch-Dest", tc.secFetchDest)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

// ─── buildEmbedSnippets ──────────────────────────────────────────────────────

func TestBuildEmbedSnippets(t *testing.T) {
	base := "https://paste.example.com"

	t.Run("text paste", func(t *testing.T) {
		html, md := buildEmbedSnippets(base, "abc123", "My Paste", false)
		wantHTML := `<iframe src="https://paste.example.com/emb/abc123" width="100%" height="400" loading="lazy" title="My Paste"></iframe>`
		if html != wantHTML {
			t.Errorf("html = %q, want %q", html, wantHTML)
		}
		wantMD := "[My Paste](https://paste.example.com/abc123)"
		if md != wantMD {
			t.Errorf("markdown = %q, want %q", md, wantMD)
		}
	})

	t.Run("image paste links raw", func(t *testing.T) {
		_, md := buildEmbedSnippets(base, "img42", "Logo", true)
		wantMD := "![Logo](https://paste.example.com/raw/img42)"
		if md != wantMD {
			t.Errorf("markdown = %q, want %q", md, wantMD)
		}
	})

	t.Run("title escaping", func(t *testing.T) {
		html, md := buildEmbedSnippets(base, "x1", `a"b <c> [d](e)`, false)
		if strings.Contains(html, `"b`) && !strings.Contains(html, "&#34;") && !strings.Contains(html, "&quot;") {
			t.Errorf("html title not escaped: %q", html)
		}
		if strings.Contains(html, "<c>") {
			t.Errorf("html title angle brackets not escaped: %q", html)
		}
		if !strings.Contains(md, `\[d\]\(e\)`) {
			t.Errorf("markdown link syntax not escaped: %q", md)
		}
	})
}
