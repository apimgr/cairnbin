// Package logging implements the server-owned log files (AI.md "Logging"):
// access.log, server.log, error.log, app.log, auth.log, and debug.log, plus
// scheduled rotation for audit.log and security.log whose lines are written
// by other packages. All files are strict raw text (no ANSI, no emoji), and
// every file rotates/prunes per its server.logs policy. A nil *Manager is
// safe: every method is a no-op, so callers never need nil checks.
package logging

import (
	"log"
	"os"
	"strings"
	"time"
)

// FileOptions configures one log file. Rotate/Keep are the raw policy strings
// from server.logs; invalid values warn to stderr and fall back to
// never/none so startup never fails on a bad policy (config warns-and-defaults).
type FileOptions struct {
	// Enabled gates files that can be switched off (auth.log, debug.log,
	// audit.log). Always-on files ignore it when Filename is set.
	Enabled bool
	// Filename is the file name inside the log directory.
	Filename string
	// Format selects the line renderer for the file type.
	Format string
	// Custom is the custom format string (access.log format "custom").
	Custom string
	// Rotate is the rotation policy string ("never", "daily", "weekly,50MB", ...).
	Rotate string
	// Keep is the retention policy string ("none", "5", "30d", "forever", ...).
	Keep string
	// Compress gzips rotated files when retention keeps them (audit.log).
	Compress bool
}

// Options configures a Manager.
type Options struct {
	// Dir is the log directory (empty disables all file logging).
	Dir string
	// Level is the global minimum level for server/app/debug lines
	// ("debug", "info", "warn", "error").
	Level string
	// Hostname is used in RFC 3164 auth.log lines.
	Hostname string
	// Tag is the syslog tag (the binary name).
	Tag string
	// DebugGate reports whether debug logging is active (mode.IsDebugEnabled).
	DebugGate func() bool

	Access LogFileSet
	Server LogFileSet
	Error  LogFileSet
	App    LogFileSet
	Auth   LogFileSet
	Debug  LogFileSet
	// Audit and Security are rotation-only: their lines are written by the
	// audit and server packages, but this manager owns their rotate/keep.
	Audit    LogFileSet
	Security LogFileSet
}

// LogFileSet is an alias kept distinct for docs clarity; identical to FileOptions.
type LogFileSet = FileOptions

// Manager owns the writers for all server log files.
type Manager struct {
	level     int
	hostname  string
	tag       string
	pid       int
	debugGate func() bool

	accessFormat string
	accessCustom string
	serverJSON   bool
	errorJSON    bool
	appJSON      bool
	authJSON     bool
	debugJSON    bool
	debugEnabled bool

	access   *fileWriter
	server   *fileWriter
	errorW   *fileWriter
	app      *fileWriter
	auth     *fileWriter
	debug    *fileWriter
	audit    *fileWriter
	security *fileWriter
}

// levelRank maps a level name to its ordering; unknown levels rank as info.
func levelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn", "warning":
		return 2
	case "error":
		return 3
	}
	return 1
}

// policies parses the rotate/keep strings, warning and defaulting on error.
func policies(name string, o FileOptions) (RotatePolicy, KeepPolicy) {
	rp, err := ParseRotate(o.Rotate)
	if err != nil {
		log.Printf("logging: %s: %v (rotation disabled)", name, err)
		rp = RotatePolicy{}
	}
	kp, err := ParseKeep(o.Keep)
	if err != nil {
		log.Printf("logging: %s: %v (keeping none)", name, err)
		kp = KeepPolicy{Mode: "none"}
	}
	return rp, kp
}

// writer builds a fileWriter for one configured file, or nil when disabled.
func writer(dir, name string, o FileOptions, perm os.FileMode, holdOpen bool) *fileWriter {
	if dir == "" || o.Filename == "" {
		return nil
	}
	rp, kp := policies(name, o)
	return newFileWriter(dir, o.Filename, perm, rp, kp, o.Compress, holdOpen)
}

// New builds a Manager from opts. It never fails: files that cannot be
// configured are disabled and writes report errors to stderr.
func New(opts Options) *Manager {
	m := &Manager{
		level:        levelRank(opts.Level),
		hostname:     opts.Hostname,
		tag:          opts.Tag,
		pid:          os.Getpid(),
		debugGate:    opts.DebugGate,
		accessFormat: strings.ToLower(opts.Access.Format),
		accessCustom: opts.Access.Custom,
		serverJSON:   strings.EqualFold(opts.Server.Format, "json"),
		errorJSON:    strings.EqualFold(opts.Error.Format, "json"),
		appJSON:      strings.EqualFold(opts.App.Format, "json"),
		authJSON:     strings.EqualFold(opts.Auth.Format, "json"),
		debugJSON:    strings.EqualFold(opts.Debug.Format, "json"),
		debugEnabled: opts.Debug.Enabled,
	}
	if m.hostname == "" {
		if h, err := os.Hostname(); err == nil {
			m.hostname = h
		} else {
			m.hostname = "localhost"
		}
	}
	if m.tag == "" {
		m.tag = "cairnbin"
	}
	m.access = writer(opts.Dir, "access.log", opts.Access, 0o640, true)
	m.server = writer(opts.Dir, "server.log", opts.Server, 0o640, true)
	m.errorW = writer(opts.Dir, "error.log", opts.Error, 0o640, true)
	m.app = writer(opts.Dir, "app.log", opts.App, 0o640, true)
	if opts.Auth.Enabled {
		m.auth = writer(opts.Dir, "auth.log", opts.Auth, 0o600, true)
	}
	m.debug = writer(opts.Dir, "debug.log", opts.Debug, 0o640, true)
	// Rotation-only writers: files are appended by other packages that open
	// per write, so rename-based rotation is safe without coordination.
	if opts.Audit.Enabled {
		m.audit = writer(opts.Dir, "audit.log", opts.Audit, 0o640, false)
	}
	m.security = writer(opts.Dir, "security.log", opts.Security, 0o600, false)
	return m
}

// write sends a line to w, reporting failures to stderr (never to the file).
func (m *Manager) write(w *fileWriter, line string) {
	if m == nil || w == nil || line == "" {
		return
	}
	if err := w.writeLine(line); err != nil {
		log.Printf("logging: write %s: %v", w.path, err)
	}
}

// Access records one HTTP request in access.log using the configured format.
func (m *Manager) Access(e AccessEntry) {
	if m == nil || m.access == nil {
		return
	}
	var line string
	switch m.accessFormat {
	case "nginx":
		line = FormatAccessNginx(e)
	case "json":
		line = FormatAccessJSON(e)
	case "custom":
		line = FormatAccessCustom(m.accessCustom, e)
	default:
		line = FormatAccessApache(e)
	}
	m.write(m.access, line)
}

// Server records a lifecycle event (startup, shutdown, config warnings) in
// server.log, honoring the global log level.
func (m *Manager) Server(level, msg string, fields ...string) {
	if m == nil || m.server == nil || levelRank(level) < m.level {
		return
	}
	now := time.Now()
	if m.serverJSON {
		m.write(m.server, FormatJSONLine(now, level, msg, fields...))
		return
	}
	m.write(m.server, FormatText(now, level, msg, fields...))
}

// Error records an error (5xx, panic, internal failure) in error.log.
// Errors always write regardless of the global level.
func (m *Manager) Error(msg string, fields ...string) {
	if m == nil || m.errorW == nil {
		return
	}
	now := time.Now()
	if m.errorJSON {
		m.write(m.errorW, FormatJSONLine(now, "error", msg, fields...))
		return
	}
	m.write(m.errorW, FormatText(now, "error", msg, fields...))
}

// App records a general application event in app.log (logfmt by default),
// honoring the global log level.
func (m *Manager) App(level, msg string, fields ...string) {
	if m == nil || m.app == nil || levelRank(level) < m.level {
		return
	}
	now := time.Now()
	if m.appJSON {
		m.write(m.app, FormatJSONLine(now, level, msg, fields...))
		return
	}
	m.write(m.app, FormatLogfmt(now, level, msg, fields...))
}

// Auth records an authentication event in auth.log as an RFC 3164 line (or
// JSON when configured). tokenID identifies the credential class
// ("operator", "owner") — raw tokens must never be passed here. result is
// "success" or "fail"; reason is a stable machine code (empty for successes).
func (m *Manager) Auth(tokenID, ip, result, reason string) {
	if m == nil || m.auth == nil {
		return
	}
	now := time.Now()
	if m.authJSON {
		fields := []string{"token_id", tokenID, "ip", ip, "result", result}
		if reason != "" {
			fields = append(fields, "reason", reason)
		}
		m.write(m.auth, FormatJSONLine(now, "info", "auth", fields...))
		return
	}
	m.write(m.auth, FormatSyslog(now, m.hostname, m.tag, m.pid, AuthMessage(tokenID, ip, result, reason)))
}

// Debug records a diagnostic line in debug.log. Lines are written only when
// the debug file is enabled and the runtime debug gate is on.
func (m *Manager) Debug(msg string, fields ...string) {
	if m == nil || m.debug == nil || !m.debugEnabled {
		return
	}
	if m.debugGate != nil && !m.debugGate() {
		return
	}
	now := time.Now()
	if m.debugJSON {
		m.write(m.debug, FormatJSONLine(now, "debug", msg, fields...))
		return
	}
	m.write(m.debug, FormatText(now, "debug", msg, fields...))
}

// RotateCheck runs the time/size rotation check and retention pruning for
// every configured log file. It is called by the scheduler's log_rotation
// task; the first error is returned after all files are attempted.
func (m *Manager) RotateCheck() error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, w := range []*fileWriter{m.access, m.server, m.errorW, m.app, m.auth, m.debug, m.audit, m.security} {
		if w == nil {
			continue
		}
		if err := w.rotateCheck(); err != nil {
			log.Printf("logging: rotate %s: %v", w.path, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Reopen closes and reopens every log file handle. This backs the SIGUSR1
// signal (PART 8): after external log rotation renames the live files, reopening
// recreates them so writes continue into fresh files.
func (m *Manager) Reopen() error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, w := range []*fileWriter{m.access, m.server, m.errorW, m.app, m.auth, m.debug, m.audit, m.security} {
		if w == nil {
			continue
		}
		if err := w.reopen(); err != nil {
			log.Printf("logging: reopen %s: %v", w.path, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Close releases every open file handle.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	for _, w := range []*fileWriter{m.access, m.server, m.errorW, m.app, m.auth, m.debug, m.audit, m.security} {
		if w != nil {
			w.close()
		}
	}
}
