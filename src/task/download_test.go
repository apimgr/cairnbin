package task_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/task"
)

// TestBlocklistUpdate_DownloadsSource verifies that a configured source is
// downloaded into the blocklists directory and a .last_updated stamp is written.
func TestBlocklistUpdate_DownloadsSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1.2.3.0/24\n5.6.7.0/24\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir, task.Source{Name: "firehol_level1.txt", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate error: %v", err)
	}

	blDir := filepath.Join(dir, "security", "blocklists")
	got, err := os.ReadFile(filepath.Join(blDir, "firehol_level1.txt"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != "1.2.3.0/24\n5.6.7.0/24\n" {
		t.Errorf("downloaded content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(blDir, ".last_updated")); err != nil {
		t.Errorf("expected .last_updated stamp: %v", err)
	}
}

// TestCVEUpdate_DownloadsSource verifies the CVE task downloads its source.
func TestCVEUpdate_DownloadsSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cve":[]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.CVEUpdate(dir, task.Source{Name: "nvd.json", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("CVEUpdate error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "security", "cve", "nvd.json"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != `{"cve":[]}` {
		t.Errorf("downloaded content = %q", got)
	}
}

// TestBlocklistUpdate_GracefulDegradationOnFailure verifies that a failed
// download is logged but does not error, and the existing file is preserved.
func TestBlocklistUpdate_GracefulDegradationOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	blDir := filepath.Join(dir, "security", "blocklists")
	if err := os.MkdirAll(blDir, 0o750); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(blDir, "firehol_level1.txt")
	if err := os.WriteFile(existing, []byte("old-data\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	fn := task.BlocklistUpdate(dir, task.Source{Name: "firehol_level1.txt", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("expected graceful degradation (nil error), got %v", err)
	}

	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(got) != "old-data\n" {
		t.Errorf("existing file should be preserved on failure, got %q", got)
	}
}

// TestBlocklistUpdate_SkipsEmptySource verifies sources missing a name or URL
// are skipped without error.
func TestBlocklistUpdate_SkipsEmptySource(t *testing.T) {
	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir,
		task.Source{Name: "", URL: "http://example.invalid"},
		task.Source{Name: "x.txt", URL: ""},
	)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate error: %v", err)
	}
	// No .last_updated because nothing was updated.
	if _, err := os.Stat(filepath.Join(dir, "security", "blocklists", ".last_updated")); !os.IsNotExist(err) {
		t.Errorf("expected no .last_updated when no source updated, err=%v", err)
	}
}

// TestBlocklistUpdate_EmptyBodyIsError verifies an empty response body leaves
// the destination untouched and degrades gracefully.
func TestBlocklistUpdate_EmptyBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir, task.Source{Name: "firehol_level1.txt", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("expected graceful degradation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "security", "blocklists", "firehol_level1.txt")); !os.IsNotExist(err) {
		t.Errorf("empty body should not create file, err=%v", err)
	}
}
