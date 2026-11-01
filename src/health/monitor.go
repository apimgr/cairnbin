// Package health implements the runtime self-healing Maintenance Mode state
// machine (AI.md PART 20). Only two classes of error are treated as critical —
// a database connection failure and an inability to write files. When a critical
// error is detected the server enters maintenance mode: write operations are
// rejected with HTTP 503 while the monitor continuously attempts self-healing in
// the background and automatically returns to normal operation once the issue
// clears.
package health

import (
	"context"
	"log"
	"sync"
	"time"
)

// State enumerates the maintenance state machine values (AI.md PART 20).
type State string

const (
	// StateStarting is the initial state while the first health probe runs.
	StateStarting State = "starting"
	// StateNormal indicates all critical systems are healthy.
	StateNormal State = "normal"
	// StateMaintenance indicates a critical error was detected; writes are rejected.
	StateMaintenance State = "maintenance"
)

// Reason codes describe why the server entered maintenance mode. They match the
// X-Maintenance-Reason header and healthz "reason" field in AI.md PART 20.
const (
	ReasonDatabaseConnection = "database_connection"
	ReasonFileWrite          = "file_write"
)

// Config configures the maintenance monitor.
type Config struct {
	// SelfHealingEnabled gates the background retry loop (server.maintenance.self_healing.enabled).
	SelfHealingEnabled bool
	// RetryInterval is the delay between self-healing attempts. Defaults to 30s.
	RetryInterval time.Duration
	// MaxAttempts caps self-healing attempts; 0 means unlimited (keep trying forever).
	MaxAttempts int
	// NotifyOnEnter fires the notifier when entering maintenance mode.
	NotifyOnEnter bool
	// NotifyOnExit fires the notifier when exiting maintenance mode.
	NotifyOnExit bool
}

// Snapshot is an immutable copy of the monitor state, safe to read without locks.
type Snapshot struct {
	State              State
	Reason             string
	Message            string
	Since              time.Time
	SelfHealingEnabled bool
	Attempts           int
	LastAttempt        time.Time
	NextAttempt        time.Time
	RetryInterval      time.Duration
}

// RetrySeconds returns the Retry-After header value in whole seconds (minimum 1).
func (s Snapshot) RetrySeconds() int {
	secs := int(s.RetryInterval.Seconds())
	if secs < 1 {
		return 1
	}
	return secs
}

// Checker probes critical systems. It returns ok=true when all systems are
// healthy; otherwise ok=false with a reason code and human-readable message.
type Checker func() (ok bool, reason, message string)

// Cleaner attempts to free resources (stale temp files, old backups, rotated
// logs) before a disk-related self-healing re-check. It is optional.
type Cleaner func()

// Notifier is invoked on maintenance transitions. event is "enter" or "exit".
type Notifier func(event string, snap Snapshot)

// Monitor implements the maintenance-mode state machine.
type Monitor struct {
	mu            sync.Mutex
	state         State
	reason        string
	message       string
	since         time.Time
	attempts      int
	lastAttempt   time.Time
	nextAttempt   time.Time
	selfHealing   bool
	retryInterval time.Duration
	maxAttempts   int
	notifyOnEnter bool
	notifyOnExit  bool
	checker       Checker
	cleaner       Cleaner
	notifier      Notifier
	onTransition  func(entered bool, reason string)
}

// New constructs a Monitor in the Starting state.
func New(cfg Config) *Monitor {
	interval := cfg.RetryInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Monitor{
		state:         StateStarting,
		selfHealing:   cfg.SelfHealingEnabled,
		retryInterval: interval,
		maxAttempts:   cfg.MaxAttempts,
		notifyOnEnter: cfg.NotifyOnEnter,
		notifyOnExit:  cfg.NotifyOnExit,
	}
}

// SetChecker registers the critical-systems probe.
func (m *Monitor) SetChecker(c Checker) {
	m.mu.Lock()
	m.checker = c
	m.mu.Unlock()
}

// SetCleaner registers the optional resource-cleanup callback.
func (m *Monitor) SetCleaner(c Cleaner) {
	m.mu.Lock()
	m.cleaner = c
	m.mu.Unlock()
}

// SetNotifier registers the optional transition notifier.
func (m *Monitor) SetNotifier(n Notifier) {
	m.mu.Lock()
	m.notifier = n
	m.mu.Unlock()
}

// SetTransitionHook registers a callback fired on every maintenance enter and
// exit regardless of the notification settings (used for audit events).
func (m *Monitor) SetTransitionHook(fn func(entered bool, reason string)) {
	m.mu.Lock()
	m.onTransition = fn
	m.mu.Unlock()
}

// State returns the current state.
func (m *Monitor) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// InMaintenance reports whether the server is currently in maintenance mode.
func (m *Monitor) InMaintenance() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == StateMaintenance
}

// Snapshot returns an immutable copy of the current state.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Monitor) snapshotLocked() Snapshot {
	return Snapshot{
		State:              m.state,
		Reason:             m.reason,
		Message:            m.message,
		Since:              m.since,
		SelfHealingEnabled: m.selfHealing,
		Attempts:           m.attempts,
		LastAttempt:        m.lastAttempt,
		NextAttempt:        m.nextAttempt,
		RetryInterval:      m.retryInterval,
	}
}

// Enter transitions to maintenance mode. It is idempotent: re-entering while
// already in maintenance preserves the original "since" timestamp and attempt
// count and does not re-fire the notifier.
func (m *Monitor) Enter(reason, message string) {
	m.mu.Lock()
	if m.state == StateMaintenance {
		m.mu.Unlock()
		return
	}
	m.state = StateMaintenance
	m.reason = reason
	m.message = message
	m.since = time.Now()
	m.attempts = 0
	m.lastAttempt = time.Time{}
	m.nextAttempt = m.since.Add(m.retryInterval)
	notify := m.notifier
	fire := m.notifyOnEnter
	hook := m.onTransition
	snap := m.snapshotLocked()
	m.mu.Unlock()

	if hook != nil {
		hook(true, reason)
	}

	log.Printf("maintenance: entering maintenance mode (reason=%s): %s", reason, message)
	if notify != nil && fire {
		notify("enter", snap)
	}
}

// Exit transitions back to normal operation and fires the exit notifier.
func (m *Monitor) Exit() {
	m.mu.Lock()
	if m.state != StateMaintenance {
		m.state = StateNormal
		m.mu.Unlock()
		return
	}
	dur := time.Since(m.since).Round(time.Second)
	reason := m.reason
	m.state = StateNormal
	m.reason = ""
	m.message = ""
	m.since = time.Time{}
	m.nextAttempt = time.Time{}
	notify := m.notifier
	fire := m.notifyOnExit
	hook := m.onTransition
	snap := m.snapshotLocked()
	m.mu.Unlock()

	if hook != nil {
		hook(false, reason)
	}

	log.Printf("maintenance: issue resolved, exiting maintenance mode (downtime=%s)", dur)
	if notify != nil && fire {
		notify("exit", snap)
	}
}

// Start runs the monitor loop until ctx is cancelled. It probes critical systems
// immediately, then every RetryInterval: a failing probe in normal operation
// enters maintenance mode, while a passing probe in maintenance mode exits it.
func (m *Monitor) Start(ctx context.Context) {
	m.tick()
	ticker := time.NewTicker(m.retryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

// tick performs a single probe-and-transition cycle.
func (m *Monitor) tick() {
	m.mu.Lock()
	checker := m.checker
	cleaner := m.cleaner
	state := m.state
	reason := m.reason
	selfHealing := m.selfHealing
	m.mu.Unlock()

	if checker == nil {
		return
	}

	// While healing, run cleanup before re-probing when the fault is disk-related.
	if state == StateMaintenance && reason == ReasonFileWrite && cleaner != nil {
		cleaner()
	}

	ok, newReason, newMessage := checker()

	switch state {
	case StateMaintenance:
		if !selfHealing {
			return
		}
		m.mu.Lock()
		capped := m.maxAttempts > 0 && m.attempts >= m.maxAttempts
		if !capped {
			m.attempts++
			m.lastAttempt = time.Now()
			m.nextAttempt = m.lastAttempt.Add(m.retryInterval)
		}
		m.mu.Unlock()
		if ok {
			m.Exit()
		}
	default:
		// Starting or Normal.
		if ok {
			m.mu.Lock()
			if m.state == StateStarting {
				m.state = StateNormal
			}
			m.mu.Unlock()
			return
		}
		m.Enter(newReason, newMessage)
	}
}
