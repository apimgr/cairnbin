package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func TestBuildPermissionsPolicy(t *testing.T) {
	got := buildPermissionsPolicy(config.DefaultPermissionsPolicy())
	for _, want := range []string{"camera=()", "fullscreen=(self)", "interest-cohort=()", "web-share=(self)"} {
		if !strings.Contains(got, want) {
			t.Errorf("default policy missing %q: %s", want, got)
		}
	}
	custom := buildPermissionsPolicy(map[string]string{"geolocation": "(self)", "camera": ""})
	if custom != "geolocation=(self)" {
		t.Errorf("custom policy = %q, want only geolocation=(self)", custom)
	}
}

func TestCSPDirectiveOverride(t *testing.T) {
	if got := cspDirective("'self'", "https://a", "", ""); got != "'self' https://a" {
		t.Errorf("append = %q", got)
	}
	if got := cspDirective("'self'", "https://a", "'none'", ""); got != "'none'" {
		t.Errorf("override = %q", got)
	}
	if got := cspDirective("'self'", "", "", ""); got != "'self'" {
		t.Errorf("base = %q", got)
	}
}

func TestSecurityHeadersFromConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Headers.COOP = "same-origin"
	cfg.Web.Headers.OriginAgentCluster = false
	cfg.Web.Headers.DNSPrefetchControl = "off"
	s := newMinimalServer(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Sec-GPC", "1")
	var sawGPC, sawPrivacyOptOut bool
	s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawGPC = gpcReceived(r)
		sawPrivacyOptOut = privacyOptOut(r)
	})).ServeHTTP(rec, req)
	h := rec.Header()
	if h.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Errorf("COOP = %q", h.Get("Cross-Origin-Opener-Policy"))
	}
	if h.Get("Origin-Agent-Cluster") != "" {
		t.Errorf("Origin-Agent-Cluster should be omitted")
	}
	if h.Get("X-DNS-Prefetch-Control") != "off" {
		t.Errorf("X-DNS-Prefetch-Control = %q", h.Get("X-DNS-Prefetch-Control"))
	}
	if h.Get("Server-Timing") != "" {
		t.Errorf("Server-Timing must never be emitted by this middleware")
	}
	if !sawGPC {
		t.Errorf("Sec-GPC: 1 was not flagged as opt-out")
	}
	if !sawPrivacyOptOut {
		t.Errorf("Sec-GPC: 1 did not mark the request as a privacy opt-out")
	}
}

func TestPrivacySignalOptOut(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("DNT", "1")
	if privacySignalOptOut(r, config.HeadersConfig{HonorSecGPC: true}) {
		t.Errorf("DNT must not be honored by default")
	}
	if !privacySignalOptOut(r, config.HeadersConfig{HonorDNT: true}) {
		t.Errorf("DNT must be honored when honor_dnt is set")
	}
}

func TestDNTOptOutIsNotReportedAsGPC(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Headers.HonorDNT = true
	s := newMinimalServer(cfg)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("DNT", "1")
	var gotGPC, gotPrivacy bool
	h := s.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotGPC = gpcReceived(r)
		gotPrivacy = privacyOptOut(r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotGPC {
		t.Error("DNT-only request must not be reported as Sec-GPC")
	}
	if !gotPrivacy {
		t.Error("honored DNT must mark the request as a privacy opt-out")
	}
}

func TestClearSiteDataValue(t *testing.T) {
	if got := clearSiteDataValue(config.ClearSiteDataConfig{}); got != `"cache", "cookies", "storage"` {
		t.Errorf("default = %q", got)
	}
	if got := clearSiteDataValue(config.ClearSiteDataConfig{ExecutionContexts: true}); !strings.HasSuffix(got, `"executionContexts"`) {
		t.Errorf("exec contexts = %q", got)
	}
}

func TestSecurityHeadersSkipTLSSemanticsOverTor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.TLS.Enabled = true
	cfg.Server.FQDN = "example.com"
	cfg.Server.Tor.OnionAddress = "abcdefghij.onion"
	s := newMinimalServer(cfg)

	serve := func(host string) http.Header {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		s.securityHeadersMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
		return rec.Header()
	}

	clear := serve("example.com")
	if clear.Get("Strict-Transport-Security") == "" {
		t.Errorf("clearnet response must carry HSTS")
	}
	onion := serve("abcdefghij.onion")
	if onion.Get("Strict-Transport-Security") != "" {
		t.Errorf("onion response must not carry HSTS")
	}
	if onion.Get("Reporting-Endpoints") != "" {
		t.Errorf("onion response must not leak clearnet reporting endpoints")
	}
	for _, h := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
		if strings.Contains(onion.Get(h), "upgrade-insecure-requests") {
			t.Errorf("onion %s must omit upgrade-insecure-requests", h)
		}
	}
}
