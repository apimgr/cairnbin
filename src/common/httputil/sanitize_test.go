package httputil

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/mode"
)

func TestSanitizeString(t *testing.T) {
	cases := map[string]string{
		"see https://x.example/p?Token=abc&page=2": "see https://x.example/p?Token=[redacted]&page=2",
		"dial 10.1.2.3:5432 failed":                "dial [redacted]:5432 failed",
		"dial 172.20.0.9 failed":                   "dial [redacted] failed",
		"dial 8.8.8.8 failed":                      "dial 8.8.8.8 failed",
		"open /var/lib/pastebin/server.db: denied": "open [redacted] denied",
		"GET /api/v1/pastes failed":                "GET /api/v1/pastes failed",
	}
	for in, want := range cases {
		if got := SanitizeString(in, 0); got != want {
			t.Errorf("SanitizeString(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SanitizeString(strings.Repeat("x", 500), 200); len(got) != 200 {
		t.Errorf("truncate: got %d chars, want 200", len(got))
	}
}

func TestWriteJSONSanitizesErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 500, map[string]any{
		"ok": false, "error": "SERVER_ERROR",
		"message": "connect 192.168.1.5 failed",
		"details": map[string]any{"path": "/etc/pastebin/server.yml"},
		"_debug":  map[string]any{"stack": "x"},
	})
	body := rec.Body.String()
	for _, bad := range []string{"192.168.1.5", "/etc/pastebin", "_debug"} {
		if strings.Contains(body, bad) {
			t.Errorf("body leaks %q: %s", bad, body)
		}
	}
}

func TestWriteJSONKeepsDebugFieldInDebugMode(t *testing.T) {
	mode.SetDebug(true)
	t.Cleanup(func() { mode.SetDebug(false) })
	rec := httptest.NewRecorder()
	WriteJSON(rec, 403, map[string]any{"ok": false, "error": "X", "message": "m", "_debug": map[string]any{"a": "b"}})
	if !strings.Contains(rec.Body.String(), "_debug") {
		t.Errorf("debug mode must keep _debug: %s", rec.Body.String())
	}
}

func TestWriteJSONLeavesSuccessBodyAlone(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 200, map[string]any{"ok": true, "data": map[string]any{"content": "host 127.0.0.1 /etc/hosts"}})
	if !strings.Contains(rec.Body.String(), "127.0.0.1 /etc/hosts") {
		t.Errorf("success body must be untouched: %s", rec.Body.String())
	}
}
