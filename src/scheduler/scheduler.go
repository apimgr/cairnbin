package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/metric"
)

var ErrTaskAlreadyRunning = errors.New("scheduler task is already running")

const (
	defaultCatchUpWindow = time.Hour
	// Retry policy defaults (PART 18): 3 attempts, 5m base delay, exponential
	// backoff (5m, 10m, 20m).
	defaultRetryDelay = 5 * time.Minute
	defaultMaxRetries = 3
	// shutdownDrainTimeout bounds how long Stop waits for in-flight task
	// goroutines to finish before marking them interrupted (PART 18 shutdown).
	shutdownDrainTimeout = 30 * time.Second
	schedulerLockLease   = 10 * time.Minute
)

// TaskFunc is the function executed by a scheduled task. ctx is cancelled when
// the scheduler is stopped, giving well-behaved tasks (e.g. network downloads)
// a way to abort promptly instead of holding their slot until they return on
// their own (PART 18 shutdown behavior).
type TaskFunc func(ctx context.Context) error

// Outcome describes the result of a single task execution. It is passed to the
// notifier registered via SetNotifier so the caller can emit audit-log entries
// and failure notifications (PART 18 task execution flow).
type Outcome struct {
	TaskID     string
	TaskName   string
	Status     string
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time
	NextRun    time.Time
	Attempt    int
	WillRetry  bool
}

// NotifyFunc is invoked after every task execution (success or failure).
type NotifyFunc func(Outcome)

// taskEntry is an in-memory representation of a registered task.
type taskEntry struct {
	id       string
	name     string
	schedule Schedule
	fn       TaskFunc
	enabled  bool

	// retry policy (PART 18) — configured via SetRetry.
	retryOnFail bool
	retryMax    int
	retryBase   time.Duration

	// mutable state — protected by Scheduler.mu
	lastRun      time.Time
	lastStatus   string
	lastError    string
	nextRun      time.Time
	runCount     int64
	failCount    int64
	running      bool
	retryAttempt int
}

// Scheduler is the built-in cron-capable scheduler (PART 18).
// It stores persistent state in the database and recovers missed runs on
// startup within the catch-up window.
type Scheduler struct {
	db            database.DB
	catchUpWindow time.Duration
	loc           *time.Location
	lockOwner     string

	mu       sync.Mutex
	tasks    map[string]*taskEntry
	stop     chan struct{}
	running  bool
	stopping bool
	notifier NotifyFunc
	logger   func(string, string, ...string)

	// ctx is cancelled when Stop is called, so in-flight TaskFunc invocations
	// can observe shutdown and abort promptly (PART 18).
	ctx       context.Context
	ctxCancel context.CancelFunc

	// wg tracks in-flight task goroutines so Stop can drain them (PART 18).
	wg sync.WaitGroup
}

// New creates a new Scheduler.
// db may be nil — in that case state is not persisted (useful for tests).
func New(db database.DB) *Scheduler {
	loc := time.Local
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:            db,
		catchUpWindow: defaultCatchUpWindow,
		loc:           loc,
		lockOwner:     fmt.Sprintf("node:%s:%d", host, os.Getpid()),
		tasks:         make(map[string]*taskEntry),
		stop:          make(chan struct{}),
		ctx:           ctx,
		ctxCancel:     cancel,
	}
}

// SetLockOwner sets the persisted lock identity for one-shot maintenance runs.
func (s *Scheduler) SetLockOwner(owner string) {
	if strings.TrimSpace(owner) == "" {
		return
	}
	s.mu.Lock()
	s.lockOwner = owner
	s.mu.Unlock()
}

// SetCatchUpWindow overrides the default 1-hour catch-up window.
func (s *Scheduler) SetCatchUpWindow(d time.Duration) { s.catchUpWindow = d }

// SetLocation sets the timezone used for cron expressions.
func (s *Scheduler) SetLocation(loc *time.Location) { s.loc = loc }

// nextRun evaluates wall-clock cron fields in the configured scheduler zone.
// Schedule.Next returns an absolute time, so converting the input preserves the
// instant while making cron hour/day matching use the configured location.
func (s *Scheduler) nextRun(schedule Schedule, after time.Time) time.Time {
	if s.loc != nil {
		after = after.In(s.loc)
	}
	return schedule.Next(after)
}

// SetNotifier registers a callback invoked after every task execution. Use it to
// write audit-log entries and send failure notifications (PART 18).
func (s *Scheduler) SetNotifier(fn NotifyFunc) {
	s.mu.Lock()
	s.notifier = fn
	s.mu.Unlock()
}

func (s *Scheduler) SetLogger(fn func(string, string, ...string)) {
	s.mu.Lock()
	s.logger = fn
	s.mu.Unlock()
}

func (s *Scheduler) writeLog(level, message string, fields ...string) {
	s.mu.Lock()
	logger := s.logger
	s.mu.Unlock()
	if logger != nil {
		logger(level, message, fields...)
		return
	}
	log.Printf("%s: %s %s", strings.ToUpper(level), message, strings.Join(fields, " "))
}

// SetRetry configures the retry policy for a registered task (PART 18). When
// enabled, a failed run is re-scheduled after an exponentially increasing delay
// (base, 2×base, 4×base…) for up to maxRetries attempts before falling back to
// the normal schedule. A non-positive base defaults to 5m; a non-positive
// maxRetries defaults to 3.
func (s *Scheduler) SetRetry(id string, enabled bool, base time.Duration, maxRetries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok {
		return
	}
	e.retryOnFail = enabled
	if base <= 0 {
		base = defaultRetryDelay
	}
	e.retryBase = base
	if maxRetries <= 0 {
		maxRetries = defaultMaxRetries
	}
	e.retryMax = maxRetries
}

// Register adds a task. Call before Start. schedule is any expression
// understood by ParseSchedule. enabled=false means the task is registered but
// will not run until explicitly enabled.
func (s *Scheduler) Register(id, name, schedExpr string, enabled bool, fn TaskFunc) error {
	sched, err := ParseSchedule(schedExpr)
	if err != nil {
		return fmt.Errorf("scheduler.Register %q: %w", id, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	e := &taskEntry{
		id:         id,
		name:       name,
		schedule:   sched,
		fn:         fn,
		enabled:    enabled,
		lastStatus: "pending",
	}
	s.tasks[id] = e
	return nil
}

// Start loads persistent state from DB, runs any missed tasks within the
// catch-up window, then starts the scheduler loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopping = false
	s.stop = make(chan struct{})
	stop := s.stop
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	s.mu.Unlock()

	// Load persistent state and compute initial nextRun values.
	s.loadState()

	// Run any missed tasks (within catch-up window) immediately.
	s.runMissed()

	s.mu.Lock()
	names := make([]string, 0, len(s.tasks))
	for _, task := range s.tasks {
		names = append(names, task.name)
	}
	s.mu.Unlock()
	sort.Strings(names)
	s.writeLog("info", "scheduler started", "tasks", strings.Join(names, ", "))

	go s.loop(stop)
}

// Running reports whether the scheduler loop is currently active.
func (s *Scheduler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// LoadPersistedState restores task history without registering or running
// missed tasks. It is used by one-shot maintenance commands.
func (s *Scheduler) LoadPersistedState() error {
	if s.db == nil {
		return nil
	}
	stored, err := s.db.ListSchedulerTasks()
	if err != nil {
		return err
	}
	stateByID := make(map[string]*database.TaskState, len(stored))
	for _, state := range stored {
		stateByID[state.TaskID] = state
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.tasks {
		state, ok := stateByID[entry.id]
		if !ok {
			entry.nextRun = s.nextRun(entry.schedule, now)
			continue
		}
		entry.lastRun = state.LastRun
		entry.lastStatus = state.LastStatus
		entry.lastError = state.LastError
		entry.runCount = state.RunCount
		entry.failCount = state.FailCount
		if entry.lastRun.IsZero() {
			entry.nextRun = s.nextRun(entry.schedule, now)
		} else {
			entry.nextRun = s.nextRun(entry.schedule, entry.lastRun)
		}
		if err := s.db.UpsertSchedulerTask(toTaskState(entry)); err != nil {
			return fmt.Errorf("register scheduler task %s: %w", entry.id, err)
		}
	}
	return nil
}

// Stop signals the scheduler loop to exit, then drains in-flight task
// goroutines within a bounded window (PART 18 shutdown behavior). Tasks still
// running after the timeout are marked interrupted and scheduled to retry on
// the next start.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.stopping = true
	close(s.stop)
	cancel := s.ctxCancel
	s.mu.Unlock()

	// Cancel the shared task context so well-behaved in-flight TaskFunc
	// invocations can abort promptly instead of holding their slot until they
	// return on their own.
	if cancel != nil {
		cancel()
	}

	// Wait for running tasks to complete (max 30 seconds). On timeout, mark the
	// still-running tasks for retry on next start instead of blocking shutdown.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownDrainTimeout):
		s.markInterrupted()
	}
	log.Printf("scheduler: stopped")
}

// markInterrupted flags any task still running at shutdown timeout so it is
// re-run within the catch-up window on the next start (PART 18).
func (s *Scheduler) markInterrupted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, e := range s.tasks {
		if !e.running {
			continue
		}
		e.lastStatus = "interrupted"
		e.nextRun = now
		if s.db != nil {
			if err := s.db.UpdateTaskRun(e.id, s.lockOwner, e.lastRun, e.lastStatus, e.lastError,
				e.runCount, e.failCount, e.nextRun); err != nil {
				log.Printf("scheduler: release interrupted task %s lock: %v", e.id, err)
			}
		}
		log.Printf("scheduler: task %s interrupted by shutdown; marked for retry", e.id)
	}
}

// RunNow executes a task immediately (bypasses the schedule but still updates
// persistent state).
func (s *Scheduler) RunNow(id string) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return fmt.Errorf("scheduler is stopping")
	}
	e, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("task %q not found", id)
	}
	if !e.enabled {
		s.mu.Unlock()
		return fmt.Errorf("task %q is disabled", id)
	}
	if e.running {
		s.mu.Unlock()
		return fmt.Errorf("task %q is already running", id)
	}
	e.running = true
	s.wg.Add(1)
	s.mu.Unlock()

	defer s.wg.Done()
	err := s.execute(e)

	s.mu.Lock()
	e.running = false
	s.mu.Unlock()
	return err
}

// SetSchedule updates a registered task's schedule and persists its next run.
func (s *Scheduler) SetSchedule(id, expression string) error {
	schedule, err := ParseSchedule(expression)
	if err != nil {
		return fmt.Errorf("scheduler.SetSchedule %q: %w", id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok {
		return fmt.Errorf("task %q not found", id)
	}
	e.schedule = schedule
	if e.lastRun.IsZero() {
		e.nextRun = s.nextRun(schedule, time.Now())
	} else {
		e.nextRun = s.nextRun(schedule, e.lastRun)
	}
	s.persistState(e)
	return nil
}

// EnableTask enables a registered task.
func (s *Scheduler) EnableTask(id string) {
	s.mu.Lock()
	if e, ok := s.tasks[id]; ok {
		e.enabled = true
		e.nextRun = s.nextRun(e.schedule, time.Now())
		s.persistState(e)
	}
	s.mu.Unlock()
}

// DisableTask disables a registered task (it will no longer fire on schedule).
func (s *Scheduler) DisableTask(id string) {
	s.mu.Lock()
	if e, ok := s.tasks[id]; ok {
		e.enabled = false
		s.persistState(e)
	}
	s.mu.Unlock()
}

// GetTask returns the state for a single task by ID, or false if not found.
func (s *Scheduler) GetTask(id string) (database.TaskState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tasks[id]
	if !ok {
		return database.TaskState{}, false
	}
	return taskEntryToState(e), true
}

// GetTasks returns a snapshot of all registered tasks.
func (s *Scheduler) GetTasks() []database.TaskState {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]database.TaskState, 0, len(s.tasks))
	for _, e := range s.tasks {
		out = append(out, taskEntryToState(e))
	}
	return out
}

// ── Internal helpers ─────────────────────────────────────────────────────────

func (s *Scheduler) loop(stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	now := time.Now()

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	var due []*taskEntry
	for _, e := range s.tasks {
		if e.enabled && !e.running && !e.nextRun.IsZero() && now.After(e.nextRun) {
			due = append(due, e)
			e.running = true
			s.wg.Add(1)
		}
	}
	s.mu.Unlock()

	for _, e := range due {
		go func(entry *taskEntry) {
			defer s.wg.Done()
			s.execute(entry) //nolint:errcheck

			s.mu.Lock()
			entry.running = false
			s.mu.Unlock()
		}(e)
	}
}

// execute runs the task function and updates state.
// runIsolated runs fn behind a recover boundary so a panicking task is
// reported as a failed run and never takes down the scheduler or server.
func runIsolated(ctx context.Context, id string, fn TaskFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task panicked: %v", r)
		}
	}()
	return fn(ctx)
}

func (s *Scheduler) execute(e *taskEntry) error {
	if s.db != nil {
		if locker, ok := s.db.(interface {
			TryAcquireSchedulerTaskLock(string, string, time.Time, time.Duration) (bool, error)
		}); ok {
			s.mu.Lock()
			lockOwner := s.lockOwner
			s.mu.Unlock()
			acquired, err := locker.TryAcquireSchedulerTaskLock(e.id, lockOwner, time.Now(), schedulerLockLease)
			if err != nil {
				return fmt.Errorf("acquire scheduler task lock for %s: %w", e.id, err)
			}
			if !acquired {
				return fmt.Errorf("%w: %s", ErrTaskAlreadyRunning, e.id)
			}
		}
	}

	start := time.Now()
	s.mu.Lock()
	e.lastStatus = "running"
	s.mu.Unlock()
	s.writeLog("debug", "scheduler task running", "task_id", e.id)

	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	metric.SchedulerTasksRunning.WithLabelValues(e.id).Inc()
	taskErr := runIsolated(ctx, e.id, e.fn)
	metric.SchedulerTasksRunning.WithLabelValues(e.id).Dec()

	finished := time.Now()
	durationMS := finished.Sub(start).Milliseconds()

	status := "success"
	errMsg := ""
	if taskErr != nil {
		status = "failed"
		errMsg = taskErr.Error()
	}

	// PART 20 defines the scheduler_tasks_total status label values as
	// success|error, while persisted run state keeps "failed".
	metricStatus := status
	if taskErr != nil {
		metricStatus = "error"
	}
	metric.SchedulerTasksTotal.WithLabelValues(e.id, metricStatus).Inc()
	metric.SchedulerTaskDuration.WithLabelValues(e.id).Observe(finished.Sub(start).Seconds())
	metric.SchedulerLastRunTimestamp.WithLabelValues(e.id).Set(float64(finished.Unix()))

	s.mu.Lock()
	e.lastRun = finished
	e.lastStatus = status
	e.lastError = errMsg
	attempt := e.retryAttempt
	willRetry := false
	logLevel := "debug"
	logMessage := fmt.Sprintf("scheduler: task %s completed (%dms)", e.id, durationMS)
	var retryMessage []string
	var next time.Time
	if taskErr != nil {
		logLevel = "warn"
		logMessage = fmt.Sprintf("scheduler: task %s failed: %v", e.id, taskErr)
		e.failCount++
		if e.retryOnFail && e.retryAttempt < e.retryMax {
			// Exponential backoff: base × 2^attempt (e.g. 5m, 10m, 20m).
			delay := e.retryBase << e.retryAttempt
			next = finished.Add(delay)
			e.retryAttempt++
			willRetry = true
			retryMessage = []string{"task_id", e.id, "attempt", fmt.Sprint(e.retryAttempt), "max_attempts", fmt.Sprint(e.retryMax), "delay", delay.String()}
		} else {
			logLevel = "error"
			next = s.nextRun(e.schedule, finished)
			e.retryAttempt = 0
		}
	} else {
		e.runCount++
		e.retryAttempt = 0
		next = s.nextRun(e.schedule, finished)
	}
	e.nextRun = next

	if s.db != nil {
		if err := s.db.UpdateTaskRun(e.id, s.lockOwner, e.lastRun, e.lastStatus, e.lastError,
			e.runCount, e.failCount, e.nextRun); err != nil {
			log.Printf("scheduler: persist run state for %s: %v", e.id, err)
		}
		if err := s.db.RecordTaskHistory(&database.TaskHistory{
			TaskID:     e.id,
			StartedAt:  start,
			FinishedAt: finished,
			Status:     status,
			ErrorMsg:   errMsg,
			DurationMS: durationMS,
		}); err != nil {
			log.Printf("scheduler: persist history for %s: %v", e.id, err)
		}
	}
	notifier := s.notifier
	s.mu.Unlock()
	if len(retryMessage) > 0 {
		s.writeLog("warn", "scheduler task retry scheduled", retryMessage...)
	}
	s.writeLog(logLevel, logMessage, "task_id", e.id)

	if notifier != nil {
		notifier(Outcome{
			TaskID:     e.id,
			TaskName:   e.name,
			Status:     status,
			Err:        errMsg,
			StartedAt:  start,
			FinishedAt: finished,
			NextRun:    next,
			Attempt:    attempt,
			WillRetry:  willRetry,
		})
	}

	return taskErr
}

// loadState reads persistent task state from the DB and merges it into the
// in-memory task entries. For tasks not yet in the DB, upserts an initial row.
func (s *Scheduler) loadState() {
	if s.db == nil {
		// No DB — just compute initial nextRun values from scratch.
		s.mu.Lock()
		now := time.Now()
		for _, e := range s.tasks {
			e.nextRun = s.nextRun(e.schedule, now)
		}
		s.mu.Unlock()
		return
	}

	stored, err := s.db.ListSchedulerTasks()
	if err != nil {
		log.Printf("scheduler: could not load state from DB: %v", err)
		// Keep the scheduler available, but do not overwrite persistent rows
		// from a failed read with incomplete in-memory defaults.
		s.mu.Lock()
		for _, e := range s.tasks {
			e.nextRun = s.nextRun(e.schedule, time.Now())
		}
		s.mu.Unlock()
		return
	}

	// Index stored tasks by ID for quick lookup.
	stateByID := make(map[string]*database.TaskState, len(stored))
	for _, st := range stored {
		stateByID[st.TaskID] = st
	}

	now := time.Now()

	s.mu.Lock()
	for _, e := range s.tasks {
		interrupted := false
		liveLease := false
		staleOwner := ""
		if st, ok := stateByID[e.TaskID()]; ok {
			// Restore persistent counters and timestamps.
			e.lastRun = st.LastRun
			e.lastStatus = st.LastStatus
			e.lastError = st.LastError
			e.runCount = st.RunCount
			e.failCount = st.FailCount
			liveLease = st.LastStatus == "running" && st.LockedBy != "" && !st.LockedAt.IsZero() && now.Sub(st.LockedAt) < schedulerLockLease
			interrupted = st.LastStatus == "running" && !liveLease
			staleOwner = st.LockedBy
			if interrupted {
				e.lastStatus = "interrupted"
				e.nextRun = now.Add(-time.Second)
			} else if liveLease {
				// A different live node owns the lease. Queue an immediate attempt;
				// failed acquisition leaves the task due so the next tick retries.
				e.nextRun = now.Add(-time.Second)
			}
			// Recompute nextRun from the stored lastRun (the persisted nextRun
			// may be stale after a schedule change).
			if liveLease {
				e.nextRun = now.Add(-time.Second)
			} else if !interrupted && !e.lastRun.IsZero() {
				e.nextRun = s.nextRun(e.schedule, e.lastRun)
			} else if !interrupted {
				e.nextRun = s.nextRun(e.schedule, now)
			}
		} else {
			// First registration — compute initial nextRun and upsert.
			e.nextRun = s.nextRun(e.schedule, now)
		}
		// Always upsert to register new tasks and reflect schedule changes.
		if err := s.db.UpsertSchedulerTask(toTaskState(e)); err != nil {
			log.Printf("scheduler: register task state for %s: %v", e.id, err)
		}
		if interrupted {
			if err := s.db.UpdateTaskRun(e.id, staleOwner, e.lastRun, e.lastStatus, e.lastError,
				e.runCount, e.failCount, e.nextRun); err != nil {
				log.Printf("scheduler: mark interrupted task %s: %v", e.id, err)
			}
			log.Printf("scheduler: task %s was interrupted by a previous process; queued for recovery", e.id)
		}
	}
	s.mu.Unlock()
}

// runMissed executes tasks that were due within the catch-up window while the
// server was down.
func (s *Scheduler) runMissed() {
	now := time.Now()
	cutoff := now.Add(-s.catchUpWindow)

	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	var missed []*taskEntry
	for _, e := range s.tasks {
		if !e.enabled || e.nextRun.IsZero() {
			continue
		}
		// Task was due after cutoff but before now.
		if e.nextRun.After(cutoff) && e.nextRun.Before(now) {
			missed = append(missed, e)
			e.running = true
		}
	}
	if len(missed) > 0 {
		s.wg.Add(1)
	}
	s.mu.Unlock()

	if len(missed) == 0 {
		return
	}
	sort.Slice(missed, func(i, j int) bool {
		return missed[i].nextRun.Before(missed[j].nextRun)
	})
	go func() {
		defer s.wg.Done()
		for _, entry := range missed {
			log.Printf("scheduler: catch-up run for %s (was due %s)", entry.id, entry.nextRun.Format(time.RFC3339))
			// execute logs and persists task failures internally; later missed tasks must still run.
			_ = s.execute(entry)
			s.mu.Lock()
			entry.running = false
			s.mu.Unlock()
		}
	}()
}

// persistState writes the current in-memory state for a task to the DB.
// Must be called with s.mu held.
func (s *Scheduler) persistState(e *taskEntry) {
	if s.db == nil {
		return
	}
	if err := s.db.UpsertSchedulerTask(toTaskState(e)); err != nil {
		log.Printf("scheduler: persist task state for %s: %v", e.id, err)
	}
}

// taskEntry.TaskID returns the task ID (helper to satisfy interface method).
func (e *taskEntry) TaskID() string { return e.id }

func toTaskState(e *taskEntry) *database.TaskState {
	return &database.TaskState{
		TaskID:     e.id,
		TaskName:   e.name,
		Schedule:   e.schedule.String(),
		LastRun:    e.lastRun,
		LastStatus: e.lastStatus,
		LastError:  e.lastError,
		NextRun:    e.nextRun,
		RunCount:   e.runCount,
		FailCount:  e.failCount,
		Enabled:    e.enabled,
	}
}

func taskEntryToState(e *taskEntry) database.TaskState {
	status := e.lastStatus
	if e.running {
		status = "running"
	}
	return database.TaskState{
		TaskID:     e.id,
		TaskName:   e.name,
		Schedule:   e.schedule.String(),
		LastRun:    e.lastRun,
		LastStatus: status,
		LastError:  e.lastError,
		NextRun:    e.nextRun,
		RunCount:   e.runCount,
		FailCount:  e.failCount,
		Enabled:    e.enabled,
	}
}
