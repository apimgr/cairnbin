package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/go-chi/chi/v5"
)

func cliTestServer(t *testing.T, requireAuth bool) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cli", "binaries"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Server.CLI.BinaryDownload.RequireAuth = requireAuth
	s := &Server{cfg: cfg, dataDir: dir, version: "1.2.3"}
	s.operatorTokenHash = sha256.Sum256([]byte("tok_operator"))
	r := chi.NewRouter()
	r.Get("/cli/binaries/{name}", s.handleCLIBinary)
	return s, r, filepath.Join(dir, "cli", "binaries")
}

func TestCLIBinary_IndexAndDownload(t *testing.T) {
	s, router, binDir := cliTestServer(t, false)
	body := []byte("cli-binary-bytes")
	if err := os.WriteFile(filepath.Join(binDir, "cairnbin-cli-linux-arm64"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	want := sha256.Sum256(body)
	got := s.cliVersionsPayload()["linux-arm64"].(map[string]interface{})
	if got["sha256"] != hex.EncodeToString(want[:]) || got["version"] != "1.2.3" {
		t.Errorf("cli_versions entry = %v", got)
	}
	if _, bogus := s.cliVersionsPayload()["notes-txt"]; bogus {
		t.Error("non-binary file listed in cli_versions")
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cli/binaries/cairnbin-cli-linux-arm64", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Fatalf("download = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("download headers missing")
	}
}

func TestCLIBinary_RejectsUnknownAndTraversal(t *testing.T) {
	_, router, binDir := cliTestServer(t, false)
	_ = os.WriteFile(filepath.Join(binDir, "cairnbin-cli-linux-amd64"), []byte("x"), 0o755)
	for _, name := range []string{"cairnbin-cli-linux-riscv", "..%2Fserver.yml", "cairnbin-cli-linux-amd64.sh", "cairnbin-cli-darwin-arm64"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cli/binaries/"+name, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, rec.Code)
		}
	}
}

func TestCLIBinary_RequireAuth(t *testing.T) {
	_, router, binDir := cliTestServer(t, true)
	_ = os.WriteFile(filepath.Join(binDir, "cairnbin-cli-linux-amd64"), []byte("x"), 0o755)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cli/binaries/cairnbin-cli-linux-amd64", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/cli/binaries/cairnbin-cli-linux-amd64", nil)
	req.Header.Set("Authorization", "Bearer tok_operator")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("with token = %d, want 200", rec.Code)
	}
}
