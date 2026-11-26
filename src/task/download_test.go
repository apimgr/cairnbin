package task_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/task"
)

type downloadRoundTripper func(*http.Request) (*http.Response, error)

func (f downloadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBlocklistUpdateWithClientUsesInjectedClient(t *testing.T) {
	var requested string
	client := &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		requested = r.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("127.0.0.1\n")),
			Request:    r,
		}, nil
	})}
	fn := task.BlocklistUpdateWithClient(t.TempDir(), client,
		task.Source{Name: "blocklist.txt", URL: "https://app-owned.invalid/blocklist"})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdateWithClient: %v", err)
	}
	if requested != "https://app-owned.invalid/blocklist" {
		t.Fatalf("injected client request URL = %q", requested)
	}
}

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
	if _, err := os.Stat(filepath.Join(blDir, "firehol_level1.last_updated")); err != nil {
		t.Errorf("expected per-source timestamp: %v", err)
	}
}

func TestBlocklistUpdateWritesIndependentSourceTimestamps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failed" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("8.8.8.8\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir,
		task.Source{Name: "good.txt", Type: "ip", URL: srv.URL + "/good"},
		task.Source{Name: "failed.txt", Type: "ip", URL: srv.URL + "/failed"},
	)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate: %v", err)
	}
	blocklists := filepath.Join(dir, "security", "blocklists")
	if _, err := os.Stat(filepath.Join(blocklists, "good.last_updated")); err != nil {
		t.Fatalf("successful source timestamp missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(blocklists, "failed.last_updated")); !os.IsNotExist(err) {
		t.Fatalf("failed source must not receive a fresh timestamp, err=%v", err)
	}
}

// TestCVEUpdate_DownloadsSource verifies the NVD API response is stored as a
// validated array of CVE objects rather than as the API response envelope.
func TestCVEUpdate_DownloadsSource(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resultsPerPage") != "2000" || r.URL.Query().Get("startIndex") != "0" {
			t.Errorf("unexpected pagination query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"totalResults":1,"resultsPerPage":2000,"startIndex":0,"vulnerabilities":[{"cve":{"id":"CVE-2026-1234","descriptions":[]}}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.CVEUpdateWithClient(dir, srv.Client(), task.Source{Name: "nvd.json", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("CVEUpdate error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "security", "cve", "nvd.json"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(got, &records); err != nil {
		t.Fatalf("stored NVD records are not a JSON array: %v", err)
	}
	if len(records) != 1 || records[0]["id"] != "CVE-2026-1234" {
		t.Errorf("stored records = %s", got)
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
	// No per-source timestamps because nothing was updated.
	if _, err := os.Stat(filepath.Join(dir, "security", "blocklists", "x.last_updated")); !os.IsNotExist(err) {
		t.Errorf("expected no source timestamp when no source updated, err=%v", err)
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

func TestBlocklistUpdate_RejectsFileWithoutValidIPEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-an-ip\n# a comment only\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	blDir := filepath.Join(dir, "security", "blocklists")
	if err := os.MkdirAll(blDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blDir, "firehol_level1.txt")
	if err := os.WriteFile(path, []byte("192.0.2.0/24\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fn := task.BlocklistUpdate(dir, task.Source{Name: "firehol_level1.txt", URL: srv.URL})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("expected graceful degradation, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "192.0.2.0/24\n" {
		t.Fatalf("existing valid blocklist was replaced: %q", got)
	}
}

func TestBlocklistUpdateAcceptsValidDomainEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# domains\nExample.COM.\n*.example.co.uk\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir, task.Source{Name: "domains.txt", URL: srv.URL, Type: "domain"})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "security", "blocklists", "domains.txt"))
	if err != nil {
		t.Fatalf("read downloaded domain list: %v", err)
	}
	if string(got) != "# domains\nExample.COM.\n*.example.co.uk\n" {
		t.Fatalf("downloaded content changed: %q", got)
	}
}

func TestBlocklistUpdateRejectsDomainFileWithoutValidEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("*.com\nhttp://bad host\n127.0.0.1\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "security", "blocklists", "domains.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("safe.example\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fn := task.BlocklistUpdate(dir, task.Source{Name: "domains.txt", URL: srv.URL, Type: "domain"})
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate should degrade gracefully: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "safe.example\n" {
		t.Fatalf("invalid update replaced the current list: %q", got)
	}
}
