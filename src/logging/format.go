// Line formatters for the log files the server owns (AI.md "Logging" >
// Format examples). All output is strict raw text: ANSI sequences, emoji,
// and control characters are stripped before anything reaches disk.
package logging

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AccessEntry carries one HTTP request/response for access.log rendering.
type AccessEntry struct {
	RemoteIP   string
	Time       time.Time
	Method     string
	Path       string
	Query      string
	Protocol   string
	Status     int
	Bytes      int64
	Referer    string
	UserAgent  string
	Latency    time.Duration
	RequestID  string
	FQDN       string
	TLSVersion string
	Country    string
	ASN        string
}

// Sanitize strips ANSI escape sequences, control characters, and non-ASCII
// runes (emoji etc.) from s so log files stay strict raw text. Newlines are
// replaced with spaces; the writer adds the single trailing newline.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	// Escape state: 0 = normal, 1 = saw ESC, 2 = inside a CSI sequence.
	esc := 0
	for _, r := range s {
		if esc == 1 {
			// ESC [ opens a CSI sequence; any other single rune (ESC c,
			// ESC 7, ...) completes the escape and is dropped.
			if r == '[' {
				esc = 2
			} else {
				esc = 0
			}
			continue
		}
		if esc == 2 {
			// CSI bodies are parameter/intermediate bytes (0x20-0x3F) and
			// terminate on a final byte in @-~.
			if r >= '@' && r <= '~' {
				esc = 0
			}
			continue
		}
		switch {
		case r == 0x1b:
			esc = 1
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Drop other control characters entirely.
		case r > 0x7f && !isPrintableText(r):
			// Drop emoji and symbol runes; keep letters/digits/marks so
			// non-English text (paths, UAs) survives.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isPrintableText reports whether a non-ASCII rune is ordinary text (letters,
// digits, punctuation) rather than emoji/symbols/private-use.
func isPrintableText(r rune) bool {
	// Emoji and pictographs live in the SMP and the misc-symbol BMP blocks.
	if r >= 0x1F000 || (r >= 0x2190 && r <= 0x2BFF) || (r >= 0xFE00 && r <= 0xFE0F) || r == 0x200D {
		return false
	}
	return true
}

// clfString returns s for CLF fields, substituting "-" when empty.
func clfString(s string) string {
	s = Sanitize(s)
	if s == "" {
		return "-"
	}
	return s
}

// clfTime formats t in Apache common-log time format.
func clfTime(t time.Time) string {
	return t.Format("02/Jan/2006:15:04:05 -0700")
}

// fullPath joins path and query for request-line rendering.
func (e AccessEntry) fullPath() string {
	if e.Query != "" {
		return e.Path + "?" + e.Query
	}
	return e.Path
}

// FormatAccessApache renders Apache Combined Log Format:
// 127.0.0.1 - - [10/Oct/2024:13:55:36 -0700] "GET /path HTTP/1.1" 200 2326 "-" "curl/7.64.1"
func FormatAccessApache(e AccessEntry) string {
	return fmt.Sprintf("%s - - [%s] \"%s %s %s\" %d %d %q %q",
		clfString(e.RemoteIP), clfTime(e.Time),
		Sanitize(e.Method), Sanitize(e.fullPath()), clfString(e.Protocol),
		e.Status, e.Bytes, clfString(e.Referer), clfString(e.UserAgent))
}

// FormatAccessNginx renders the Common Log Format (nginx default minus
// referer and user agent):
// 127.0.0.1 - - [10/Oct/2024:13:55:36 -0700] "GET /path HTTP/1.1" 200 2326
func FormatAccessNginx(e AccessEntry) string {
	return fmt.Sprintf("%s - - [%s] \"%s %s %s\" %d %d",
		clfString(e.RemoteIP), clfTime(e.Time),
		Sanitize(e.Method), Sanitize(e.fullPath()), clfString(e.Protocol),
		e.Status, e.Bytes)
}

// FormatAccessJSON renders the JSON access format:
// {"ip":"127.0.0.1","time":"2024-10-10T13:55:36Z","method":"GET","path":"/path","status":200,"size":2326,"ua":"curl/7.64.1"}
func FormatAccessJSON(e AccessEntry) string {
	rec := struct {
		IP     string `json:"ip"`
		Time   string `json:"time"`
		Method string `json:"method"`
		Path   string `json:"path"`
		Status int    `json:"status"`
		Size   int64  `json:"size"`
		UA     string `json:"ua"`
	}{
		IP:     Sanitize(e.RemoteIP),
		Time:   e.Time.UTC().Format(time.RFC3339),
		Method: Sanitize(e.Method),
		Path:   Sanitize(e.fullPath()),
		Status: e.Status,
		Size:   e.Bytes,
		UA:     Sanitize(e.UserAgent),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return ""
	}
	return string(b)
}

// FormatAccessCustom renders a custom access-log format string, substituting
// the {variable} placeholders documented in AI.md "Custom format variables".
func FormatAccessCustom(format string, e AccessEntry) string {
	repl := strings.NewReplacer(
		"{time}", e.Time.Format("15:04:05"),
		"{date}", e.Time.Format("2006-01-02"),
		"{datetime}", e.Time.Format(time.RFC3339),
		"{remote_ip}", Sanitize(e.RemoteIP),
		"{method}", Sanitize(e.Method),
		"{path}", Sanitize(e.Path),
		"{query}", Sanitize(e.Query),
		"{status}", strconv.Itoa(e.Status),
		"{bytes}", strconv.FormatInt(e.Bytes, 10),
		"{latency}", e.Latency.String(),
		"{latency_ms}", strconv.FormatInt(e.Latency.Milliseconds(), 10),
		"{user_agent}", Sanitize(e.UserAgent),
		"{referer}", Sanitize(e.Referer),
		"{request_id}", Sanitize(e.RequestID),
		"{fqdn}", Sanitize(e.FQDN),
		"{protocol}", Sanitize(e.Protocol),
		"{tls_version}", Sanitize(e.TLSVersion),
		"{country}", Sanitize(e.Country),
		"{asn}", Sanitize(e.ASN),
	)
	return Sanitize(repl.Replace(format))
}

// logfmtValue quotes v for logfmt output when it contains spaces, quotes, or
// equals signs, and strips control characters.
func logfmtValue(v string) string {
	v = Sanitize(v)
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \"=") {
		return strconv.Quote(v)
	}
	return v
}

// FormatLogfmt renders an app.log line:
// time=2026-05-13T10:58:00-04:00 level=INFO msg="user created" id=abc123 ip=1.2.3.4
// Extra fields are appended as key=value pairs in the order given.
func FormatLogfmt(t time.Time, level, msg string, fields ...string) string {
	var b strings.Builder
	b.WriteString("time=")
	b.WriteString(t.Format(time.RFC3339))
	b.WriteString(" level=")
	b.WriteString(strings.ToUpper(Sanitize(level)))
	b.WriteString(" msg=")
	b.WriteString(logfmtValue(msg))
	for i := 0; i+1 < len(fields); i += 2 {
		b.WriteByte(' ')
		b.WriteString(Sanitize(fields[i]))
		b.WriteByte('=')
		b.WriteString(logfmtValue(fields[i+1]))
	}
	return b.String()
}

// FormatText renders a plain text line for server.log / error.log / debug.log:
// 2026-05-13T10:58:00-04:00 [INFO] message key=value ...
func FormatText(t time.Time, level, msg string, fields ...string) string {
	var b strings.Builder
	b.WriteString(t.Format(time.RFC3339))
	b.WriteString(" [")
	b.WriteString(strings.ToUpper(Sanitize(level)))
	b.WriteString("] ")
	b.WriteString(Sanitize(msg))
	for i := 0; i+1 < len(fields); i += 2 {
		b.WriteByte(' ')
		b.WriteString(Sanitize(fields[i]))
		b.WriteByte('=')
		b.WriteString(logfmtValue(fields[i+1]))
	}
	return b.String()
}

// FormatJSONLine renders a JSON object line for the json variants of
// server.log / error.log / app.log / debug.log.
func FormatJSONLine(t time.Time, level, msg string, fields ...string) string {
	var b strings.Builder
	b.WriteString(`{"time":`)
	b.WriteString(strconv.Quote(t.Format(time.RFC3339)))
	b.WriteString(`,"level":`)
	b.WriteString(strconv.Quote(strings.ToLower(Sanitize(level))))
	b.WriteString(`,"msg":`)
	b.WriteString(strconv.Quote(Sanitize(msg)))
	for i := 0; i+1 < len(fields); i += 2 {
		b.WriteByte(',')
		b.WriteString(strconv.Quote(Sanitize(fields[i])))
		b.WriteByte(':')
		b.WriteString(strconv.Quote(Sanitize(fields[i+1])))
	}
	b.WriteByte('}')
	return b.String()
}

// FormatSyslog renders an RFC 3164 auth.log line:
// May 13 10:58:00 hostname cairnbin[123]: auth: token_id=xxx ip=1.2.3.4 result=fail reason=invalid_token
func FormatSyslog(t time.Time, hostname, tag string, pid int, msg string) string {
	// RFC 3164 day-of-month is space-padded.
	stamp := t.Format("Jan _2 15:04:05")
	return fmt.Sprintf("%s %s %s[%d]: %s", stamp, Sanitize(hostname), Sanitize(tag), pid, Sanitize(msg))
}

// AuthMessage builds the structured payload of an auth.log line. tokenID
// identifies the credential class ("operator", "owner") — raw tokens are
// never logged. result is "success" or "fail"; reason is a stable machine
// code such as "invalid_token" (empty for successes).
func AuthMessage(tokenID, ip, result, reason string) string {
	if tokenID == "" {
		tokenID = "-"
	}
	msg := fmt.Sprintf("auth: token_id=%s ip=%s result=%s", Sanitize(tokenID), Sanitize(ip), Sanitize(result))
	if reason != "" {
		msg += " reason=" + Sanitize(reason)
	}
	return msg
}
