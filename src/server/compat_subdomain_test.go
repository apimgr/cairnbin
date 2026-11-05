package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/handler/compat"
)

func newCompatTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		cfg:       &config.Config{Web: config.WebConfig{SiteTitle: "Cairnbin", Theme: "dark"}},
		version:   "test",
		buildDate: "2026-01-01",
	}
	tmpl, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = tmpl
	return s
}

func TestCompatModeFromLabel(t *testing.T) {
	cases := map[string]string{
		"mb":       "microbin",
		"microbin": "microbin",
		"lp":       "lenpaste",
		"lenpaste": "lenpaste",
		"pb":       "cairnbin",
		"cairnbin": "cairnbin",
		"sk":       "stikked",
		"stikked":  "stikked",
		"hb":       "hastebin",
		"hastebin": "hastebin",
		"dp":       "dpaste",
		"dpaste":   "dpaste",
		"tb":       "termbin",
		"termbin":  "termbin",
		"ix":       "ixio",
		"sprunge":  "sprunge",
		"0x0":      "zerox0",
		"st":       "zerox0",
		"unknown":  "",
		"":         "",
		"paste":    "",
	}
	for label, want := range cases {
		if got := compatModeFromLabel(label); got != want {
			t.Errorf("compatModeFromLabel(%q): got %q, want %q", label, got, want)
		}
	}
}

func TestRequestCompatHostLabelUntrustedPeerIgnoresHeader(t *testing.T) {
	s := newCompatTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "paste.example.com"
	// TEST-NET-1, not private, not trusted
	req.RemoteAddr = "192.0.2.1:12345"
	req.Header.Set("X-Forwarded-Host", "mb.example.com")

	if got := s.requestCompatHostLabel(req); got != "paste" {
		t.Errorf("got %q, want %q (untrusted peer must not honor X-Forwarded-Host)", got, "paste")
	}
}

func TestRequestCompatHostLabelTrustedPeerHonorsHeader(t *testing.T) {
	s := newCompatTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "paste.example.com"
	// loopback, always trusted
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-Host", "mb.example.com")

	if got := s.requestCompatHostLabel(req); got != "mb" {
		t.Errorf("got %q, want %q (trusted peer must honor X-Forwarded-Host)", got, "mb")
	}
}

func TestRequestCompatHostLabelTrustedPeerHeaderPriority(t *testing.T) {
	s := newCompatTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "paste.example.com"
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Real-Host", "lp.example.com")
	req.Header.Set("X-Original-Host", "sk.example.com")

	if got := s.requestCompatHostLabel(req); got != "lp" {
		t.Errorf("got %q, want %q (X-Real-Host must win over X-Original-Host)", got, "lp")
	}
}

func TestRequestCompatHostLabelStripsPort(t *testing.T) {
	s := newCompatTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "mb.example.com:8443"
	req.RemoteAddr = "192.0.2.1:12345"

	if got := s.requestCompatHostLabel(req); got != "mb" {
		t.Errorf("got %q, want %q", got, "mb")
	}
}

func TestCompatRuleMatches(t *testing.T) {
	exact := compatRouteRule{method: http.MethodPost, path: "/documents", prefix: false}
	if !compatRuleMatches(exact, http.MethodPost, "/documents") {
		t.Error("exact match should match")
	}
	if compatRuleMatches(exact, http.MethodPost, "/documents/abc") {
		t.Error("exact rule should not match a longer path")
	}
	if !compatRuleMatches(exact, "post", "/documents") {
		t.Error("method match should be case-insensitive")
	}
	if compatRuleMatches(exact, http.MethodGet, "/documents") {
		t.Error("wrong method should not match")
	}

	prefix := compatRouteRule{method: http.MethodGet, path: "/documents/", prefix: true}
	if !compatRuleMatches(prefix, http.MethodGet, "/documents/abc") {
		t.Error("prefix rule should match a longer path")
	}
	if compatRuleMatches(prefix, http.MethodGet, "/documents") {
		t.Error("prefix rule should not match the bare prefix without trailing content")
	}
}

func TestCompatModeGateUnmatchedHostPassesThrough(t *testing.T) {
	s := newCompatTestServer(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/documents", nil)
	req.Host = "paste.example.com"
	rec := httptest.NewRecorder()

	s.compatModeGate(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should be called for a non-matching Host")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestCompatModeGateMatchedSubdomainBlocksOtherTargets(t *testing.T) {
	s := newCompatTestServer(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/documents", nil)
	// microbin subdomain; /documents belongs to hastebin
	req.Host = "mb.example.com"
	rec := httptest.NewRecorder()

	s.compatModeGate(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler should not be called for a foreign compat-owned route")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}

func TestCompatModeGateMatchedSubdomainAllowsOwnRoute(t *testing.T) {
	s := newCompatTestServer(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/documents", nil)
	// hastebin subdomain; /documents is hastebin's own route
	req.Host = "hb.example.com"
	rec := httptest.NewRecorder()

	s.compatModeGate(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should be called for the matched target's own route")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}

func TestCompatModeGateCurlUploadSubdomainsNeverBlockRootPost(t *testing.T) {
	// The curl-upload family (termbin/ixio/sprunge/zerox0) intentionally has
	// no entry in compatOwnership() — POST / is a shared route across every
	// subdomain, never exclusively hidden. This asserts that invariant holds
	// for each of the new aliases.
	cases := map[string]string{
		"tb.example.com":      "termbin",
		"ix.example.com":      "ixio",
		"sprunge.example.com": "sprunge",
		"0x0.example.com":     "zerox0",
		"st.example.com":      "zerox0",
	}
	for host, want := range cases {
		t.Run(host, func(t *testing.T) {
			s := newCompatTestServer(t)
			var gotTarget string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTarget = compat.TargetFromContextForTest(r)
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Host = host
			rec := httptest.NewRecorder()

			s.compatModeGate(next).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status: got %d, want 200 (POST / must never be blocked)", rec.Code)
			}
			if gotTarget != want {
				t.Errorf("target in context: got %q, want %q", gotTarget, want)
			}
		})
	}
}

func TestCompatModeGateMatchedSubdomainAllowsNativeRoute(t *testing.T) {
	s := newCompatTestServer(t)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/p/abc123", nil)
	// microbin subdomain; /p/{id} is a native-handler route
	req.Host = "mb.example.com"
	rec := httptest.NewRecorder()

	s.compatModeGate(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should be called for a native (non-compat-owned) route")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}
