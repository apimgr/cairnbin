package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// securityLog appends one structured event line to security.log (PART 11
// Coordinated Disclosure Pipeline, "Logging"). The line is logfmt-style:
// an RFC 3339 timestamp, a "[security]" tag, the stable event code, and the
// caller-supplied key=value fields. Values containing spaces, quotes, or "="
// are double-quoted so downstream fail2ban/SIEM parsers stay stable.
//
// The file is raw text only — no ANSI, no emojis, one event per line ending in
// "\n" (AI.md "All log files — raw text only"). Failure to write is non-fatal:
// it is reported to stderr and the request proceeds, so logging never blocks a
// security submission.
func (s *Server) securityLog(event string, fields ...string) {
	if s.logDir == "" {
		return
	}
	var b strings.Builder
	b.WriteString(time.Now().Format(time.RFC3339))
	b.WriteString(" [security] event=")
	b.WriteString(event)
	// fields arrive as alternating key, value entries.
	for i := 0; i+1 < len(fields); i += 2 {
		b.WriteByte(' ')
		b.WriteString(fields[i])
		b.WriteByte('=')
		b.WriteString(logfmtValue(fields[i+1]))
	}
	b.WriteByte('\n')

	path := filepath.Join(s.logDir, "security.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("security.log: open failed: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		log.Printf("security.log: write failed: %v", err)
	}
}

// logfmtValue renders a logfmt value, stripping control characters (log files
// must contain no control chars except the line separator) and quoting when the
// value contains whitespace, a quote, or an "=".
func logfmtValue(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, v)
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \"=") {
		return fmt.Sprintf("%q", v)
	}
	return v
}
