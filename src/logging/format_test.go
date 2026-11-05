// Tests for the log-line formatters: access formats (apache/nginx/json/
// custom), logfmt, text, JSON lines, RFC 3164 syslog, and sanitization.
package logging

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sampleEntry returns a fully populated AccessEntry in a fixed zone so the
// rendered timestamps are deterministic.
func sampleEntry() AccessEntry {
	loc := time.FixedZone("PDT", -7*3600)
	return AccessEntry{
		RemoteIP:   "127.0.0.1",
		Time:       time.Date(2024, 10, 10, 13, 55, 36, 0, loc),
		Method:     "GET",
		Path:       "/path",
		Protocol:   "HTTP/1.1",
		Status:     200,
		Bytes:      2326,
		UserAgent:  "curl/7.64.1",
		Latency:    15 * time.Millisecond,
		RequestID:  "req-123",
		FQDN:       "paste.example.com",
		TLSVersion: "TLS1.3",
		Country:    "US",
		ASN:        "13335",
	}
}

// ─── Sanitize ────────────────────────────────────────────────────────────────

func TestSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"ansi color", "\x1b[31mred\x1b[0m", "red"},
		{"ansi cursor", "a\x1b[2Jb", "ab"},
		{"newline to space", "line1\nline2", "line1 line2"},
		{"crlf tab", "a\r\nb\tc", "a  b c"},
		{"control chars dropped", "a\x00b\x07c\x7fd", "abcd"},
		{"emoji dropped", "ok \U0001F600 done", "ok  done"},
		{"arrows dropped", "a→b", "ab"},
		{"variation selector dropped", "x️y", "xy"},
		{"zwj dropped", "a\u200db", "ab"},
		{"non-english kept", "héllo wörld 日本語", "héllo wörld 日本語"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sanitize(tt.in); got != tt.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ─── Access formats ──────────────────────────────────────────────────────────

func TestFormatAccessApache(t *testing.T) {
	e := sampleEntry()
	want := `127.0.0.1 - - [10/Oct/2024:13:55:36 -0700] "GET /path HTTP/1.1" 200 2326 "-" "curl/7.64.1"`
	if got := FormatAccessApache(e); got != want {
		t.Errorf("apache line:\n got %s\nwant %s", got, want)
	}
}

func TestFormatAccessApache_QueryAndReferer(t *testing.T) {
	e := sampleEntry()
	e.Query = "a=1&b=2"
	e.Referer = "https://ref.example.com/"
	got := FormatAccessApache(e)
	if !strings.Contains(got, `"GET /path?a=1&b=2 HTTP/1.1"`) {
		t.Errorf("request line missing query: %s", got)
	}
	if !strings.Contains(got, `"https://ref.example.com/"`) {
		t.Errorf("referer missing: %s", got)
	}
}

func TestFormatAccessNginx(t *testing.T) {
	e := sampleEntry()
	want := `127.0.0.1 - - [10/Oct/2024:13:55:36 -0700] "GET /path HTTP/1.1" 200 2326`
	if got := FormatAccessNginx(e); got != want {
		t.Errorf("nginx line:\n got %s\nwant %s", got, want)
	}
}

func TestFormatAccessJSON(t *testing.T) {
	e := sampleEntry()
	line := FormatAccessJSON(e)
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("invalid JSON %q: %v", line, err)
	}
	wantStr := map[string]string{
		"ip":     "127.0.0.1",
		"time":   "2024-10-10T20:55:36Z",
		"method": "GET",
		"path":   "/path",
		"ua":     "curl/7.64.1",
	}
	for k, v := range wantStr {
		if rec[k] != v {
			t.Errorf("%s = %v, want %s", k, rec[k], v)
		}
	}
	if rec["status"] != float64(200) {
		t.Errorf("status = %v, want 200", rec["status"])
	}
	if rec["size"] != float64(2326) {
		t.Errorf("size = %v, want 2326", rec["size"])
	}
}

func TestFormatAccessCustom(t *testing.T) {
	e := sampleEntry()
	tests := []struct {
		name   string
		format string
		want   string
	}{
		{"basic", "{remote_ip} {method} {path} {status}", "127.0.0.1 GET /path 200"},
		{"date time", "{date}T{time}", "2024-10-10T13:55:36"},
		{"datetime", "{datetime}", "2024-10-10T13:55:36-07:00"},
		{"bytes latency", "{bytes} {latency_ms}", "2326 15"},
		{"geo tls", "{country}/{asn} {tls_version}", "US/13335 TLS1.3"},
		{"request meta", "{request_id} {fqdn} {protocol}", "req-123 paste.example.com HTTP/1.1"},
		{"ua referer query", "{user_agent}|{referer}|{query}", "curl/7.64.1||"},
		{"latency", "{latency}", "15ms"},
		{"unknown left alone", "{nope}", "{nope}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatAccessCustom(tt.format, e); got != tt.want {
				t.Errorf("custom %q = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

// ─── Structured line formats ─────────────────────────────────────────────────

func TestFormatLogfmt(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 58, 0, 0, time.FixedZone("EDT", -4*3600))
	got := FormatLogfmt(ts, "info", "user created", "id", "abc123", "ip", "1.2.3.4")
	want := `time=2026-05-13T10:58:00-04:00 level=INFO msg="user created" id=abc123 ip=1.2.3.4`
	if got != want {
		t.Errorf("logfmt:\n got %s\nwant %s", got, want)
	}
}

func TestFormatLogfmt_QuotingRules(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 58, 0, 0, time.UTC)
	got := FormatLogfmt(ts, "warn", "plain", "empty", "", "eq", "a=b", "quoted", `say "hi"`)
	for _, want := range []string{`msg=plain`, `empty=""`, `eq="a=b"`, `quoted="say \"hi\""`, "level=WARN"} {
		if !strings.Contains(got, want) {
			t.Errorf("logfmt %q missing %q", got, want)
		}
	}
}

func TestFormatText(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 58, 0, 0, time.UTC)
	got := FormatText(ts, "error", "backend down", "code", "502")
	want := `2026-05-13T10:58:00Z [ERROR] backend down code=502`
	if got != want {
		t.Errorf("text:\n got %s\nwant %s", got, want)
	}
}

func TestFormatJSONLine(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 58, 0, 0, time.UTC)
	line := FormatJSONLine(ts, "INFO", "started", "port", "8080")
	var rec map[string]string
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("invalid JSON %q: %v", line, err)
	}
	want := map[string]string{
		"time":  "2026-05-13T10:58:00Z",
		"level": "info",
		"msg":   "started",
		"port":  "8080",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %q, want %q", k, rec[k], v)
		}
	}
}

// ─── Syslog / auth ───────────────────────────────────────────────────────────

func TestFormatSyslog(t *testing.T) {
	ts := time.Date(2026, 5, 13, 10, 58, 0, 0, time.UTC)
	got := FormatSyslog(ts, "hostname", "cairnbin", 123, "auth: token_id=xxx ip=1.2.3.4 result=fail reason=invalid_token")
	want := "May 13 10:58:00 hostname cairnbin[123]: auth: token_id=xxx ip=1.2.3.4 result=fail reason=invalid_token"
	if got != want {
		t.Errorf("syslog:\n got %s\nwant %s", got, want)
	}
}

func TestFormatSyslog_DayPadding(t *testing.T) {
	// RFC 3164 pads single-digit days with a space.
	ts := time.Date(2026, 7, 5, 1, 2, 3, 0, time.UTC)
	got := FormatSyslog(ts, "h", "t", 1, "m")
	if !strings.HasPrefix(got, "Jul  5 01:02:03 ") {
		t.Errorf("day padding wrong: %q", got)
	}
}

func TestAuthMessage(t *testing.T) {
	tests := []struct {
		tokenID, ip, result, reason string
		want                        string
	}{
		{"operator", "1.2.3.4", "fail", "invalid_token", "auth: token_id=operator ip=1.2.3.4 result=fail reason=invalid_token"},
		{"operator", "1.2.3.4", "success", "", "auth: token_id=operator ip=1.2.3.4 result=success"},
		{"", "::1", "fail", "missing_bearer_token", "auth: token_id=- ip=::1 result=fail reason=missing_bearer_token"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.result, tt.reason), func(t *testing.T) {
			if got := AuthMessage(tt.tokenID, tt.ip, tt.result, tt.reason); got != tt.want {
				t.Errorf("AuthMessage = %q, want %q", got, tt.want)
			}
		})
	}
}
