// Package audit implements the JSON Lines audit-log writer (AI.md PART 11,
// "Audit Log"). Each event is one JSON object per line with a ULID id, an
// ISO 8601 UTC timestamp with milliseconds, and the actor/target/result fields
// the spec requires. The writer is append-only, concurrency-safe, and never
// blocks a request: write failures are reported to stderr and swallowed.
package audit

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Severity levels (AI.md "Severity Levels").
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// Result values (AI.md required field `result`).
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// Actor identifies who performed an action.
type Actor struct {
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent,omitempty"`
}

// Target identifies what was acted upon (optional).
type Target struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Entry is a single audit record serialized as one JSON line. Field order in
// the struct is the canonical JSON key order (id, time, event, ...).
type Entry struct {
	ID       string         `json:"id"`
	Time     string         `json:"time"`
	Event    string         `json:"event"`
	Category string         `json:"category"`
	Severity string         `json:"severity"`
	Actor    Actor          `json:"actor"`
	Target   *Target        `json:"target,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
	Result   string         `json:"result"`
	Reason   string         `json:"reason,omitempty"`
}

// EventCategories toggles which event categories are recorded
// (AI.md `server.logs.audit.events`).
type EventCategories struct {
	Configuration bool
	Security      bool
	Backup        bool
	Server        bool
}

// Config configures the audit writer (resolved from server.logs.audit).
type Config struct {
	Enabled          bool
	Dir              string
	Filename         string
	IncludeUserAgent bool
	MaskEmails       bool
	Events           EventCategories
}

// Writer appends audit entries to the audit log file.
type Writer struct {
	mu  sync.Mutex
	cfg Config
}

// New returns a Writer for the given config. A nil Writer, or one whose config
// is disabled, silently drops all entries — callers never need a nil check.
func New(cfg Config) *Writer {
	if cfg.Filename == "" {
		cfg.Filename = "audit.log"
	}
	return &Writer{cfg: cfg}
}

// Enabled reports whether the writer will record entries.
func (w *Writer) Enabled() bool {
	return w != nil && w.cfg.Enabled && w.cfg.Dir != ""
}

// categoryEnabled maps an event category to its config toggle. Unknown
// categories default to enabled so new event types are never silently dropped.
func (w *Writer) categoryEnabled(category string) bool {
	switch category {
	case "config":
		return w.cfg.Events.Configuration
	case "security":
		return w.cfg.Events.Security
	case "backup":
		return w.cfg.Events.Backup
	case "server", "scheduler":
		return w.cfg.Events.Server
	default:
		return true
	}
}

// Log records one entry. Missing id/time/severity/result are filled with
// sensible defaults. The call is non-blocking-safe and never returns an error;
// write failures are logged to stderr and swallowed so auditing never breaks a
// request (AI.md: logging must not block the operation).
func (w *Writer) Log(e Entry) {
	if !w.Enabled() {
		return
	}
	if e.Category == "" {
		if i := strings.IndexByte(e.Event, '.'); i > 0 {
			e.Category = e.Event[:i]
		}
	}
	if !w.categoryEnabled(e.Category) {
		return
	}

	now := time.Now().UTC()
	if e.ID == "" {
		if id, err := newULID(now); err == nil {
			e.ID = "audit_" + id
		}
	}
	if e.Time == "" {
		e.Time = now.Format("2006-01-02T15:04:05.000Z07:00")
	}
	if e.Severity == "" {
		e.Severity = SeverityInfo
	}
	if e.Result == "" {
		e.Result = ResultSuccess
	}
	if !w.cfg.IncludeUserAgent {
		e.Actor.UserAgent = ""
	}
	if w.cfg.MaskEmails {
		e.Actor.UserAgent = maskEmails(e.Actor.UserAgent)
		e.Reason = maskEmails(e.Reason)
		for k, v := range e.Details {
			if s, ok := v.(string); ok {
				e.Details[k] = maskEmails(s)
			}
		}
	}

	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit: marshal failed: %v", err)
		return
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	path := filepath.Join(w.cfg.Dir, w.cfg.Filename)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Printf("audit: open failed: %v", err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("audit: close failed: %v", err)
		}
	}()
	if _, err := f.Write(line); err != nil {
		log.Printf("audit: write failed: %v", err)
	}
}

// maskEmails replaces the local part of any email-like token with its first
// character plus "***", preserving the domain (e.g. "jane@x.com" -> "j***@x.com").
// Single-character local parts become "*@domain".
func maskEmails(s string) string {
	if s == "" || !strings.Contains(s, "@") {
		return s
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == ';' || r == '<' || r == '>' || r == '"'
	})
	for _, tok := range fields {
		at := strings.IndexByte(tok, '@')
		if at <= 0 || at == len(tok)-1 {
			continue
		}
		domain := tok[at+1:]
		if !strings.Contains(domain, ".") {
			continue
		}
		local := tok[:at]
		var masked string
		if len(local) == 1 {
			masked = "*@" + domain
		} else {
			masked = local[:1] + "***@" + domain
		}
		s = strings.Replace(s, tok, masked, 1)
	}
	return s
}
