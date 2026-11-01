package server

// Tests for the internal, loopback-only Tor control channel (AI.md PART 31.1,
// "CLI-to-running-server control channel" and "Vanity Onion Address Search"):
// the loopback gate, the status/validate handlers, the "tor not configured"
// conflict path, and the vanity start/stop/apply plus import-keys handlers.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/tor"
)

// newTorControlServer builds a minimal Server whose config/data directories
// point at a per-test temp tree, optionally with a real (never started) Tor
// manager attached so the vanity handlers exercise their real code paths.
func newTorControlServer(t *testing.T, withManager bool) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.Tor.VirtualPort = 80
	s := newMinimalServer(cfg)
	s.configDir = filepath.Join(root, "config")
	s.dataDir = filepath.Join(root, "data")
	if withManager {
		torBinary := filepath.Join(root, "fake-tor")
		if err := os.WriteFile(torBinary, []byte("test placeholder"), 0o700); err != nil {
			t.Fatal(err)
		}
		torCfg := tor.Config{
			VirtualPort: 80,
			Binary:      torBinary,
			ConfigDir:   s.configDir,
			DataDir:     s.dataDir,
		}
		s.torManager = tor.NewManager(context.Background(), 8080, torCfg, http.NewServeMux())
	}
	return s
}

// decodeEnvelope decodes the canonical {"ok": ..., "data"/"error"} envelope.
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return out
}

// ─── loopback gate ───────────────────────────────────────────────────────────

func TestTorControlLoopbackMiddleware(t *testing.T) {
	cases := []struct {
		name     string
		remote   string
		wantCode int
	}{
		{"ipv4 loopback", "127.0.0.1:44444", http.StatusOK},
		{"ipv4 loopback alias", "127.0.0.53:44444", http.StatusOK},
		{"ipv6 loopback", "[::1]:44444", http.StatusOK},
		{"private lan peer", "192.168.1.10:44444", http.StatusNotFound},
		{"public peer", "203.0.113.5:44444", http.StatusNotFound},
		{"unparseable remote", "not-an-address", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := torControlLoopbackMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequest(http.MethodGet, "/server/tor/status", nil)
			r.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if called != (tc.wantCode == http.StatusOK) {
				t.Errorf("next handler called = %v, want %v", called, tc.wantCode == http.StatusOK)
			}
		})
	}
}

// ─── status ──────────────────────────────────────────────────────────────────

func TestHandleTorControlStatus_NoManager(t *testing.T) {
	s := newTorControlServer(t, false)
	rec := httptest.NewRecorder()
	s.handleTorControlStatus(rec, httptest.NewRequest(http.MethodGet, "/server/tor/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env["ok"] != true {
		t.Fatalf("ok = %v, want true", env["ok"])
	}
	data, _ := env["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("data missing")
	}
	if data["enabled"] != false {
		t.Errorf("enabled = %v, want false", data["enabled"])
	}
	vanity, _ := data["vanity"].(map[string]interface{})
	if vanity == nil || vanity["state"] != "idle" {
		t.Errorf("vanity = %v, want state idle", data["vanity"])
	}
}

func TestHandleTorControlStatus_WithManager(t *testing.T) {
	s := newTorControlServer(t, true)
	rec := httptest.NewRecorder()
	s.handleTorControlStatus(rec, httptest.NewRequest(http.MethodGet, "/server/tor/status", nil))

	env := decodeEnvelope(t, rec)
	data, _ := env["data"].(map[string]interface{})
	if data == nil {
		t.Fatal("data missing")
	}
	if data["enabled"] != true {
		t.Errorf("enabled = %v, want true", data["enabled"])
	}
	vanity, _ := data["vanity"].(map[string]interface{})
	if vanity == nil || vanity["state"] != "idle" {
		t.Errorf("vanity = %v, want state idle", data["vanity"])
	}
}

// ─── validate ────────────────────────────────────────────────────────────────

func TestHandleTorControlValidate(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		s := newTorControlServer(t, false)
		rec := httptest.NewRecorder()
		s.handleTorControlValidate(rec, httptest.NewRequest(http.MethodPost, "/server/tor/validate", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		data, _ := decodeEnvelope(t, rec)["data"].(map[string]interface{})
		if data == nil || data["valid"] != true {
			t.Fatalf("valid = %v, want true (body %s)", data, rec.Body.String())
		}
		checks, _ := data["checks"].([]interface{})
		if len(checks) < 3 {
			t.Errorf("checks = %d, want at least 3 (binary, virtual_port, two dirs)", len(checks))
		}
	})

	t.Run("virtual_port out of range", func(t *testing.T) {
		s := newTorControlServer(t, false)
		s.cfg.Server.Tor.VirtualPort = 0
		rec := httptest.NewRecorder()
		s.handleTorControlValidate(rec, httptest.NewRequest(http.MethodPost, "/server/tor/validate", nil))

		data, _ := decodeEnvelope(t, rec)["data"].(map[string]interface{})
		if data == nil || data["valid"] != false {
			t.Fatalf("valid = %v, want false", data)
		}
		if !strings.Contains(rec.Body.String(), "virtual_port 0 out of range") {
			t.Errorf("missing virtual_port failure detail: %s", rec.Body.String())
		}
	})
}

// ─── tor not configured ──────────────────────────────────────────────────────

func TestTorControlHandlers_TorNotConfigured(t *testing.T) {
	s := newTorControlServer(t, false)
	cases := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"restart", "/server/tor/restart", s.handleTorControlRestart},
		{"regenerate", "/server/tor/regenerate", s.handleTorControlRegenerate},
		{"vanity start", "/server/tor/vanity/start", s.handleTorControlVanityStart},
		{"vanity stop", "/server/tor/vanity/stop", s.handleTorControlVanityStop},
		{"vanity apply", "/server/tor/vanity/apply", s.handleTorControlVanityApply},
		{"import-keys", "/server/tor/import-keys", s.handleTorControlImportKeys},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("{}")))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			env := decodeEnvelope(t, rec)
			if env["ok"] != false || env["error"] != "CONFLICT" {
				t.Errorf("envelope = %v, want ok=false error=CONFLICT", env)
			}
		})
	}
}

// ─── vanity start / stop ─────────────────────────────────────────────────────

func TestHandleTorControlVanityStart_InvalidPrefix(t *testing.T) {
	s := newTorControlServer(t, true)
	cases := []struct {
		name string
		body string
	}{
		{"missing prefix", `{}`},
		{"invalid charset", `{"prefix":"a0b"}`},
		{"too long", `{"prefix":"abcdefg"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/server/tor/vanity/start", strings.NewReader(tc.body))
			s.handleTorControlVanityStart(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if env := decodeEnvelope(t, rec); env["error"] != "BAD_REQUEST" {
				t.Errorf("error = %v, want BAD_REQUEST", env["error"])
			}
		})
	}
}

func TestHandleTorControlVanityStartStop(t *testing.T) {
	s := newTorControlServer(t, true)

	// A six-character prefix is effectively never found, so the search stays
	// running for the duration of the test.
	start := func(prefix string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"prefix":"` + prefix + `","workers":1}`)
		s.handleTorControlVanityStart(rec, httptest.NewRequest(http.MethodPost, "/server/tor/vanity/start", body))
		return rec
	}

	rec := start("abcdef")
	if rec.Code != http.StatusOK {
		t.Fatalf("first start status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	data, _ := decodeEnvelope(t, rec)["data"].(map[string]interface{})
	if data == nil || data["state"] != "running" || data["prefix"] != "abcdef" {
		t.Fatalf("start data = %v, want running search for abcdef", data)
	}

	// One search at a time (AI.md PART 31.1): a second start must 409.
	rec = start("bcdefa")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second start status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	env := decodeEnvelope(t, rec)
	if env["error"] != "CONFLICT" {
		t.Errorf("error = %v, want CONFLICT", env["error"])
	}
	if msg, _ := env["message"].(string); !strings.Contains(msg, "abcdef") {
		t.Errorf("message = %q, want the running prefix", msg)
	}

	// Stopping a running search reports stopped=true.
	rec = httptest.NewRecorder()
	s.handleTorControlVanityStop(rec, httptest.NewRequest(http.MethodPost, "/server/tor/vanity/stop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", rec.Code)
	}
	data, _ = decodeEnvelope(t, rec)["data"].(map[string]interface{})
	if data == nil || data["stopped"] != true {
		t.Fatalf("stop data = %v, want stopped=true", data)
	}

	// Stopping again is a successful no-op.
	rec = httptest.NewRecorder()
	s.handleTorControlVanityStop(rec, httptest.NewRequest(http.MethodPost, "/server/tor/vanity/stop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("second stop status = %d, want 200", rec.Code)
	}
	data, _ = decodeEnvelope(t, rec)["data"].(map[string]interface{})
	if data == nil || data["stopped"] != false {
		t.Fatalf("second stop data = %v, want stopped=false", data)
	}
}

// ─── vanity apply ────────────────────────────────────────────────────────────

func TestHandleTorControlVanityApply_NoCandidates(t *testing.T) {
	s := newTorControlServer(t, true)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"address":"abc"}`)
	s.handleTorControlVanityApply(rec, httptest.NewRequest(http.MethodPost, "/server/tor/vanity/apply", body))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeEnvelope(t, rec); env["error"] != "NOT_FOUND" {
		t.Errorf("error = %v, want NOT_FOUND", env["error"])
	}
}

// ─── import-keys ─────────────────────────────────────────────────────────────

func TestHandleTorControlImportKeys(t *testing.T) {
	s := newTorControlServer(t, true)
	cases := []struct {
		name        string
		contentType string
		body        string
		wantMessage string
	}{
		{"empty body", "application/json", "", "key path or key data is required"},
		{"json without path", "application/json", `{}`, "key path is required"},
		{"json with blank path", "application/json", `{"path":"   "}`, "key path is required"},
		{"json with missing path", "application/json", `{"path":"/nonexistent/vanity/keys"}`, ""},
		{"raw garbage key data", "application/octet-stream", "not-a-key", "invalid key data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/server/tor/import-keys", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			s.handleTorControlImportKeys(rec, r)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			env := decodeEnvelope(t, rec)
			if env["ok"] != false || env["error"] != "BAD_REQUEST" {
				t.Errorf("envelope = %v, want ok=false error=BAD_REQUEST", env)
			}
			if tc.wantMessage != "" {
				if msg, _ := env["message"].(string); msg != tc.wantMessage {
					t.Errorf("message = %q, want %q", msg, tc.wantMessage)
				}
			}
		})
	}
}
