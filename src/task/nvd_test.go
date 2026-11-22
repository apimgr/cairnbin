package task_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/task"
)

func TestNVDIncrementalUpdateReplacesRecordsAndPreservesOnFailure(t *testing.T) {
	var allowFailure bool
	var sawIncrementalRequest bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apiKey") != "test-secret" {
			t.Errorf("apiKey header = %q", r.Header.Get("apiKey"))
		}
		if r.URL.Query().Get("lastModStartDate") == "" || r.URL.Query().Get("lastModEndDate") == "" {
			t.Errorf("expected incremental date range: %s", r.URL.RawQuery)
		}
		sawIncrementalRequest = true
		if allowFailure {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"totalResults":1,"resultsPerPage":2000,"startIndex":0,"vulnerabilities":[{"cve":{"id":"CVE-2026-1","updated":"new"}}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cvePath := filepath.Join(dir, "security", "cve", "nvd.json")
	if err := os.MkdirAll(filepath.Dir(cvePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cvePath, []byte(`[{"id":"CVE-2026-1","updated":"old"},{"id":"CVE-2026-2"}]`), 0o640); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(cvePath), ".last_updated"), []byte(stamp), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := task.NVDConfig{Source: srv.URL, APIKey: "test-secret"}
	if err := task.NVDUpdate(dir, cfg, srv.Client())(context.Background()); err != nil {
		t.Fatalf("incremental update: %v", err)
	}
	if !sawIncrementalRequest {
		t.Fatal("no incremental request made")
	}
	content, err := os.ReadFile(cvePath)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["updated"] != "new" || got[1]["id"] != "CVE-2026-2" {
		t.Fatalf("incremental merge = %s", content)
	}

	goodCopy := append([]byte(nil), content...)
	stampPath := filepath.Join(filepath.Dir(cvePath), ".last_updated")
	oldSuccessfulStamp := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) + "\n"
	if err := os.WriteFile(stampPath, []byte(oldSuccessfulStamp), 0o640); err != nil {
		t.Fatal(err)
	}
	allowFailure = true
	if err := task.NVDUpdate(dir, cfg, srv.Client())(context.Background()); err == nil || !strings.Contains(err.Error(), "next scheduled run") {
		t.Fatalf("429 error = %v, want retry-next-run error", err)
	}
	content, _ = os.ReadFile(cvePath)
	if string(content) != string(goodCopy) {
		t.Fatalf("rate limit changed last good data: %s", content)
	}
	stampAfter, _ := os.ReadFile(stampPath)
	if string(stampAfter) != oldSuccessfulStamp {
		t.Fatalf("rate limit changed successful-update timestamp: %s", stampAfter)
	}
}

func TestNVDIncrementalDateRangeIsSplitAt120Days(t *testing.T) {
	var starts, ends []time.Time
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parse := func(key string) time.Time {
			value, err := time.Parse("2006-01-02T15:04:05.000Z", r.URL.Query().Get(key))
			if err != nil {
				t.Errorf("parse %s: %v", key, err)
			}
			return value
		}
		starts = append(starts, parse("lastModStartDate"))
		ends = append(ends, parse("lastModEndDate"))
		_, _ = w.Write([]byte(`{"totalResults":0,"resultsPerPage":2000,"startIndex":0,"vulnerabilities":[]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cveDir := filepath.Join(dir, "security", "cve")
	if err := os.MkdirAll(cveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cveDir, "nvd.json"), []byte(`[{"id":"CVE-2026-1"}]`), 0o640); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().Add(-241 * 24 * time.Hour).Truncate(time.Second)
	if err := os.WriteFile(filepath.Join(cveDir, ".last_updated"), []byte(from.Format(time.RFC3339)+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := task.NVDConfig{Source: srv.URL}
	if err := task.NVDUpdate(dir, cfg, srv.Client())(context.Background()); err != nil {
		t.Fatalf("incremental update: %v", err)
	}
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("requests = %d, want 3 windows", len(starts))
	}
	for i := range starts {
		if ends[i].Sub(starts[i]) > 120*24*time.Hour {
			t.Errorf("window %d exceeds 120 days: %s to %s", i, starts[i], ends[i])
		}
		if i > 0 && !starts[i].Equal(ends[i-1]) {
			t.Errorf("window %d starts at %s, previous ended at %s", i, starts[i], ends[i-1])
		}
	}
}
