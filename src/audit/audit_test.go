package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWriter(t *testing.T, cfg Config) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	cfg.Dir = dir
	if cfg.Filename == "" {
		cfg.Filename = "audit.log"
	}
	return New(cfg), filepath.Join(dir, cfg.Filename)
}

func readEntries(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func allEnabled() EventCategories {
	return EventCategories{Configuration: true, Security: true, Backup: true, Server: true}
}

func TestLog_FillsDefaults(t *testing.T) {
	w, path := newTestWriter(t, Config{Enabled: true, Events: allEnabled()})
	w.Log(Entry{Event: "security.status_viewed", Actor: Actor{IP: "203.0.113.9"}})

	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !strings.HasPrefix(e.ID, "audit_") {
		t.Errorf("id should have audit_ prefix, got %q", e.ID)
	}
	if len(e.ID) != len("audit_")+26 {
		t.Errorf("id length = %d, want %d", len(e.ID), len("audit_")+26)
	}
	if e.Category != "security" {
		t.Errorf("category derived from event = %q, want security", e.Category)
	}
	if e.Severity != SeverityInfo {
		t.Errorf("severity default = %q, want info", e.Severity)
	}
	if e.Result != ResultSuccess {
		t.Errorf("result default = %q, want success", e.Result)
	}
	if !strings.HasPrefix(e.Time, "20") || !strings.HasSuffix(e.Time, "Z") {
		t.Errorf("time not ISO8601 UTC: %q", e.Time)
	}
}

func TestLog_Append(t *testing.T) {
	w, path := newTestWriter(t, Config{Enabled: true, Events: allEnabled()})
	w.Log(Entry{Event: "server.started"})
	w.Log(Entry{Event: "server.stopped"})
	if got := len(readEntries(t, path)); got != 2 {
		t.Fatalf("want 2 appended entries, got %d", got)
	}
}

func TestLog_DisabledDropsAll(t *testing.T) {
	w, path := newTestWriter(t, Config{Enabled: false, Events: allEnabled()})
	w.Log(Entry{Event: "security.x"})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled writer should not create the log file")
	}
}

func TestLog_CategoryGating(t *testing.T) {
	w, path := newTestWriter(t, Config{
		Enabled: true,
		Events:  EventCategories{Configuration: true, Security: false, Backup: true, Server: true},
	})
	w.Log(Entry{Event: "security.report_received"})
	w.Log(Entry{Event: "config.changed"})
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (security gated off), got %d", len(entries))
	}
	if entries[0].Category != "config" {
		t.Errorf("surviving entry category = %q, want config", entries[0].Category)
	}
}

func TestLog_MaskEmails(t *testing.T) {
	w, path := newTestWriter(t, Config{
		Enabled: true, MaskEmails: true, IncludeUserAgent: true, Events: allEnabled(),
	})
	w.Log(Entry{
		Event:   "security.report_received",
		Reason:  "reported by jane.doe@example.com today",
		Details: map[string]any{"contact": "a@b.com"},
	})
	e := readEntries(t, path)[0]
	if strings.Contains(e.Reason, "jane.doe@example.com") {
		t.Errorf("email not masked in reason: %q", e.Reason)
	}
	if !strings.Contains(e.Reason, "j***@example.com") {
		t.Errorf("masked form missing in reason: %q", e.Reason)
	}
	if c, _ := e.Details["contact"].(string); c != "*@b.com" {
		t.Errorf("single-char local mask = %q, want *@b.com", c)
	}
}

func TestLog_IncludeUserAgentToggle(t *testing.T) {
	w, path := newTestWriter(t, Config{
		Enabled: true, IncludeUserAgent: false, Events: allEnabled(),
	})
	w.Log(Entry{Event: "security.x", Actor: Actor{IP: "1.2.3.4", UserAgent: "curl/8"}})
	e := readEntries(t, path)[0]
	if e.Actor.UserAgent != "" {
		t.Errorf("user agent should be stripped, got %q", e.Actor.UserAgent)
	}
}

func TestLog_NilWriterSafe(t *testing.T) {
	var w *Writer
	if w.Enabled() {
		t.Fatal("nil writer must report disabled")
	}
	w.Log(Entry{Event: "server.started"})
}

func TestMaskEmails_NonEmail(t *testing.T) {
	in := "no address here"
	if got := maskEmails(in); got != in {
		t.Errorf("maskEmails(%q) = %q, want unchanged", in, got)
	}
}
