package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func schedulerTestServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/history"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": []map[string]any{
				{"task_id": "backup_daily", "started_at": time.Now(), "status": "failed", "error_msg": "disk full", "duration_ms": 1500},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schedulers"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": []map[string]any{
				{"task_id": "backup_daily", "schedule": "0 2 * * *", "last_status": "success", "enabled": true, "last_run": time.Now(), "next_run": time.Now()},
				{"task_id": "backup_hourly", "schedule": "@hourly", "enabled": false},
			}})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{
				"task_id": "backup_daily", "schedule": "0 2 * * *", "last_status": "success", "enabled": true, "run_count": 7,
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]string{"status": "ok"}})
		}
	}))
}

func TestCmdScheduler_ListShowHistoryAndActions(t *testing.T) {
	var calls []string
	srv := schedulerTestServer(t, &calls)
	defer srv.Close()
	c := &client{server: srv.URL, token: "tok_x", outputFormat: "table", apiVersion: "v1"}

	list := captureStdout(t, func() { c.cmdScheduler([]string{"list"}) })
	if !strings.Contains(list, "backup_daily") || !strings.Contains(list, "✓") || !strings.Contains(list, "○") {
		t.Errorf("list output missing task or status symbols:\n%s", list)
	}
	show := captureStdout(t, func() { c.cmdScheduler([]string{"show", "backup_daily"}) })
	if !strings.Contains(show, "0 2 * * *") || !strings.Contains(show, "7") {
		t.Errorf("show output:\n%s", show)
	}
	hist := captureStdout(t, func() { c.cmdScheduler([]string{"history", "backup_daily"}) })
	if !strings.Contains(hist, "disk full") || !strings.Contains(hist, "✗") {
		t.Errorf("history output:\n%s", hist)
	}
	for _, action := range []string{"run", "enable", "disable"} {
		out := captureStdout(t, func() { c.cmdScheduler([]string{action, "backup_daily"}) })
		if !strings.Contains(out, "backup_daily") {
			t.Errorf("%s output: %q", action, out)
		}
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"GET /api/v1/server/schedulers auth=Bearer tok_x",
		"POST /api/v1/server/schedulers/backup_daily/run auth=Bearer tok_x",
		"POST /api/v1/server/schedulers/backup_daily/enable",
		"POST /api/v1/server/schedulers/backup_daily/disable",
		"GET /api/v1/server/schedulers/backup_daily/history",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing request %q in:\n%s", want, joined)
		}
	}
}

func TestCmdScheduler_CSVOutput(t *testing.T) {
	var calls []string
	srv := schedulerTestServer(t, &calls)
	defer srv.Close()
	c := &client{server: srv.URL, outputFormat: "table", apiVersion: "v1"}
	out := captureStdout(t, func() { c.cmdScheduler([]string{"list", "--output", "csv"}) })
	if !strings.Contains(out, "backup_hourly,@hourly") {
		t.Errorf("csv output:\n%s", out)
	}
}

func TestSchedulerStatusSymbol(t *testing.T) {
	for status, want := range map[string]string{"success": "✓", "running": "●", "failed": "✗", "skipped": "◐", "pending": "○", "": "○"} {
		if got := schedulerStatusSymbol(status); got != want {
			t.Errorf("%q: got %q want %q", status, got, want)
		}
	}
	if schedulerTime(time.Time{}) != "-" {
		t.Error("zero time must render as -")
	}
}
