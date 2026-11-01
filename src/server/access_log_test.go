package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/logging"
	"github.com/apimgr/pastebin/src/mode"
)

// TestIsHealthzPath covers every suppressible endpoint (AI.md "Health-Check
// Log Suppression") plus a representative non-healthz path.
func TestIsHealthzPath(t *testing.T) {
	cases := map[string]bool{
		"/healthz":               true,
		"/server/healthz":        true,
		"/api/healthz":           true,
		"/api/v1/server/healthz": true,
		"/api/v2/server/healthz": true,
		"/api/v1/server/version": false,
		"/paste/abc123":          false,
		"/server/healthz/extra":  false,
	}
	for path, want := range cases {
		if got := isHealthzPath(path); got != want {
			t.Errorf("isHealthzPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// newAccessLogTestServer builds a Server wired to a real logging.Manager
// writing into a fresh temp directory, so access.log content can be
// inspected directly.
func newAccessLogTestServer(t *testing.T, logHealthChecks bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Server.Logging.Access.LogHealthChecks = logHealthChecks
	s := &Server{cfg: cfg}
	s.SetLogManager(logging.New(logging.Options{
		Dir:       dir,
		Level:     "info",
		Tag:       "pastebin-test",
		DebugGate: mode.IsDebugEnabled,
		Access: logging.FileOptions{
			Enabled:  true,
			Filename: "access.log",
			Format:   "apache",
		},
		Error: logging.FileOptions{
			Enabled:  true,
			Filename: "error.log",
			Format:   "apache",
		},
	}))
	return s, dir
}

func readAccessLog(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "access.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read access.log: %v", err)
	}
	return string(b)
}

// TestAccessLogMiddleware_SuppressesSuccessfulHealthz verifies a 2xx healthz
// request is excluded from access.log by default.
func TestAccessLogMiddleware_SuppressesSuccessfulHealthz(t *testing.T) {
	s, dir := newAccessLogTestServer(t, false)
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/api/v1/server/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if out := readAccessLog(t, dir); out != "" {
		t.Errorf("expected suppressed healthz line, got: %q", out)
	}
}

// TestAccessLogMiddleware_NeverSuppressesFailedHealthz verifies a non-2xx
// healthz response is always logged, suppression setting notwithstanding.
func TestAccessLogMiddleware_NeverSuppressesFailedHealthz(t *testing.T) {
	s, dir := newAccessLogTestServer(t, false)
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	r := httptest.NewRequest("GET", "/server/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if out := readAccessLog(t, dir); out == "" {
		t.Error("expected failed healthz request to be logged, got nothing")
	}
}

// TestAccessLogMiddleware_LogHealthChecksOverride verifies
// server.logs.access.log_health_checks: true forces logging of successful
// health checks.
func TestAccessLogMiddleware_LogHealthChecksOverride(t *testing.T) {
	s, dir := newAccessLogTestServer(t, true)
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if out := readAccessLog(t, dir); out == "" {
		t.Error("expected healthz request to be logged when log_health_checks is true, got nothing")
	}
}

// TestAccessLogMiddleware_NonHealthzAlwaysLogged verifies ordinary requests
// are unaffected by the suppression logic.
func TestAccessLogMiddleware_NonHealthzAlwaysLogged(t *testing.T) {
	s, dir := newAccessLogTestServer(t, false)
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/paste/abc123", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)

	if out := readAccessLog(t, dir); out == "" {
		t.Error("expected non-healthz request to be logged, got nothing")
	}
}

// TestAccessLogMiddleware_5xxLogsErrorContext verifies a 5xx response writes
// error.log with error_code, http_status and request_id (PART 9 "Error
// Logging").
func TestAccessLogMiddleware_5xxLogsErrorContext(t *testing.T) {
	s, dir := newAccessLogTestServer(t, false)
	h := s.accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-123")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/boom", nil))
	s.logManager.Close()

	data, err := os.ReadFile(filepath.Join(dir, "error.log"))
	if err != nil {
		t.Fatalf("read error.log: %v", err)
	}
	out := string(data)
	for _, want := range []string{"SERVER_ERROR", "500"} {
		if !strings.Contains(out, want) {
			t.Errorf("error.log missing %q: %s", want, out)
		}
	}
}
