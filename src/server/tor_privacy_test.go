package server

// Tor privacy tests (PART 12, AI.md 15794-15850): Tor request detection,
// the Tor security.txt variant, Tor CORS origin, and the tor.contact_email
// disclosure rules (clearnet email NEVER shown on Tor responses).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

const testOnion = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvwx.onion"

func newTorTestServer(contactEmail string) *Server {
	cfg := &config.Config{}
	cfg.Server.Tor.OnionAddress = testOnion
	cfg.Server.Tor.ContactEmail = contactEmail
	return newMinimalServer(cfg)
}

func torRequest(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = testOnion
	r.RemoteAddr = "127.0.0.1:9999"
	return r
}

// ─── torRequestOnion ─────────────────────────────────────────────────────────

func TestTorRequestOnion(t *testing.T) {
	s := newTorTestServer("")
	cases := []struct {
		name string
		host string
		want string
	}{
		{"exact match", testOnion, testOnion},
		{"match with port stripped", testOnion + ":80", testOnion},
		{"case-insensitive match", strings.ToUpper(testOnion), testOnion},
		{"clearnet host", "paste.example.com", ""},
		{"empty host", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = tc.host
			if got := s.torRequestOnion(r); got != tc.want {
				t.Errorf("torRequestOnion(host=%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
	t.Run("unset onion address", func(t *testing.T) {
		s2 := newMinimalServer(&config.Config{})
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = testOnion
		if got := s2.torRequestOnion(r); got != "" {
			t.Errorf("torRequestOnion with no configured onion = %q, want empty", got)
		}
	})
}

// ─── baseURL priority 0 ──────────────────────────────────────────────────────

func TestBaseURLTorPriorityZero(t *testing.T) {
	s := newTorTestServer("")
	// server.base_url set: Tor detection must still win (priority 0).
	s.cfg.Server.BaseURL = "https://paste.example.com"
	r := torRequest("/x")
	r.Host = testOnion + ":80"
	if got, want := s.baseURL(r), "http://"+testOnion; got != want {
		t.Errorf("baseURL(tor) = %q, want %q", got, want)
	}
}

// ─── resolveCORSOrigin ───────────────────────────────────────────────────────

func TestResolveCORSOriginTor(t *testing.T) {
	s := newTorTestServer("")
	// Operator-configured clearnet origin must NOT leak into Tor responses.
	s.cfg.Server.Cors.AllowedOrigins = []string{"https://paste.example.com"}
	if got, _, _ := s.resolveCORSOrigin(torRequest("/")); got != "http://"+testOnion {
		t.Errorf("resolveCORSOrigin(tor) = %q, want %q", got, "http://"+testOnion)
	}
	// Clearnet requests keep the operator-configured value.
	clearnet := httptest.NewRequest(http.MethodGet, "/", nil)
	clearnet.Host = "paste.example.com"
	if got, _, _ := s.resolveCORSOrigin(clearnet); got != "https://paste.example.com" {
		t.Errorf("resolveCORSOrigin(clearnet) = %q, want configured origin", got)
	}
}

// ─── security.txt Tor variant ────────────────────────────────────────────────

func TestHandleSecurityTorVariant_WithContactEmail(t *testing.T) {
	s := newTorTestServer("tor-contact@example.onion")
	s.cfg.Server.Contact.Security.Email = "security@clearnet.example.com"
	w := httptest.NewRecorder()
	s.handleSecurity(w, torRequest("/.well-known/security.txt"))
	body := w.Body.String()

	if !strings.Contains(body, "Contact: mailto:tor-contact@example.onion\n") {
		t.Errorf("Tor security.txt missing tor.contact_email mailto line:\n%s", body)
	}
	if strings.Contains(body, "clearnet.example.com") {
		t.Errorf("Tor security.txt leaks clearnet email:\n%s", body)
	}
	if !strings.Contains(body, "Policy: http://"+testOnion+"/server/security\n") {
		t.Errorf("Tor security.txt missing onion Policy line:\n%s", body)
	}
	if strings.Contains(body, "Preferred-Languages:") {
		t.Errorf("Tor security.txt must omit Preferred-Languages (fingerprinting):\n%s", body)
	}
	if !strings.Contains(body, "Expires: ") {
		t.Errorf("Tor security.txt missing Expires line:\n%s", body)
	}
}

func TestHandleSecurityTorVariant_NoContactEmail(t *testing.T) {
	s := newTorTestServer("")
	s.cfg.Server.Contact.Security.Email = "security@clearnet.example.com"
	w := httptest.NewRecorder()
	s.handleSecurity(w, torRequest("/.well-known/security.txt"))
	body := w.Body.String()

	// tor.contact_email unset → no mailto line at all, never clearnet fallback.
	if strings.Contains(body, "mailto:") {
		t.Errorf("Tor security.txt must omit mailto when tor.contact_email unset:\n%s", body)
	}
	if strings.Contains(body, "clearnet.example.com") {
		t.Errorf("Tor security.txt leaks clearnet email:\n%s", body)
	}
}

func TestHandleSecurityClearnetUnchanged(t *testing.T) {
	s := newTorTestServer("tor-contact@example.onion")
	s.cfg.Server.Contact.Security.Email = "security@clearnet.example.com"
	r := httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
	r.Host = "paste.example.com"
	r.RemoteAddr = "8.8.8.8:1234"
	w := httptest.NewRecorder()
	s.handleSecurity(w, r)
	body := w.Body.String()

	if !strings.Contains(body, "Contact: mailto:security@clearnet.example.com\n") {
		t.Errorf("clearnet security.txt missing configured mailto line:\n%s", body)
	}
	if !strings.Contains(body, "Preferred-Languages:") {
		t.Errorf("clearnet security.txt must keep Preferred-Languages:\n%s", body)
	}
}

// ─── public email helpers ────────────────────────────────────────────────────

func TestPublicEmailsOnTor(t *testing.T) {
	s := newTorTestServer("")
	s.cfg.Server.Contact.Security.Email = "security@clearnet.example.com"
	s.cfg.Server.Contact.General.Email = "hello@clearnet.example.com"

	tor := torRequest("/server/contact")
	if got := s.publicSecurityEmail(tor); got != "" {
		t.Errorf("publicSecurityEmail(tor, unset tor.contact_email) = %q, want empty", got)
	}
	if got := s.publicContactEmail(tor); got != "" {
		t.Errorf("publicContactEmail(tor, unset tor.contact_email) = %q, want empty", got)
	}
	if got := s.publicAbuseEmail(tor); got != "" {
		t.Errorf("publicAbuseEmail(tor, unset tor.contact_email) = %q, want empty", got)
	}

	s.cfg.Server.Tor.ContactEmail = " tor@example.onion "
	if got := s.publicSecurityEmail(tor); got != "tor@example.onion" {
		t.Errorf("publicSecurityEmail(tor) = %q, want trimmed tor.contact_email", got)
	}
}
