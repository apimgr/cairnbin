package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/metric"
)

const (
	defaultCatchUpWindow = time.Hour
	// Retry policy defaults (PART 18): 3 attempts, 5m base delay, exponential
	// backoff (5m, 10m, 20m).
	defaultRetryDelay = 5 * time.Minute
	defaultMaxRetries = 3
	// shutdownDrainTimeout bounds how long Stop waits for in-flight task
	// goroutines to finish before marking them interrupted (PART 18 shutdown).
	shutdownDrainTimeout = 30 * time.Second
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

	mu       sync.Mutex
	tasks    map[string]*taskEntry
	stop     chan struct{}
	running  bool
	notifier NotifyFunc

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
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db:            db,
		catchUpWindow: defaultCatchUpWindow,
		loc:           loc,
		tasks:         make(map[string]*taskEntry),
		stop:          make(chan struct{}),
		ctx:           ctx,
		ctxCancel:     cancel,
	}
}

// SetCatchUpWindow overrides the default 1-hour catch-up window.
func (s *Scheduler) SetCatchUpWindow(d time.Duration) { s.catchUpWindow = d }

// SetLocation sets the timezone used for cron expressions.
func (s *Scheduler) SetLocation(loc *time.Location) { s.loc = loc }

// SetNotifier registers a callback invoked after every task execution. Use it to
// write audit-log entries and send failure notifications (PART 18).
func (s *Scheduler) SetNotifier(fn NotifyFunc) {
	s.mu.Lock()
	s.notifier = fn
	s.mu.Unlock()
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
	s.stop = make(chan struct{})
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	s.mu.Unlock()

	// Load persistent state and compute initial nextRun values.
	s.loadState()

	// Run any missed tasks (within catch-up window) immediately.
	s.runMissed()

	log.Printf("scheduler: started with %d tasks", len(s.tasks))

	go s.loop()
}

// Running reports whether the scheduler loop is currently active.
func (s *Scheduler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
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
		s.persistState(e)
		log.Printf("scheduler: task %s interrupted by shutdown; marked for retry", e.id)
	}
}

// RunNow executes a task immediately (bypasses the schedule but still updates
// persistent state).
func (s *Scheduler) RunNow(id string) error {
	s.mu.Lock()
	e, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("task %q not found", id)
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

// EnableTask enables a registered task.
func (s *Scheduler) EnableTask(id string) {
	s.mu.Lock()
	if e, ok := s.tasks[id]; ok {
		e.enabled = true
		e.nextRun = e.schedule.Next(time.Now())
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

func (s *Scheduler) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	now := time.Now()

	s.mu.Lock()
	var due []*taskEntry
	for _, e := range s.tasks {
		if e.enabled && !e.running && !e.nextRun.IsZero() && now.After(e.nextRun) {
			due = append(due, e)
			e.running = true
		}
	}
	s.mu.Unlock()

	for _, e := range due {
		s.wg.Add(1)
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
			log.Printf("scheduler: task %s panicked: %v", id, r)
			err = fmt.Errorf("task panicked: %v", r)
		}
	}()
	return fn(ctx)
}

func (s *Scheduler) execute(e *taskEntry) error {
	start := time.Now()
	log.Printf("scheduler: running %s", e.id)

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
		log.Printf("scheduler: task %s failed: %v", e.id, taskErr)
	} else {
		log.Printf("scheduler: task %s completed (%dms)", e.id, durationMS)
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
	e.lastRun = start
	e.lastStatus = status
	e.lastError = errMsg
	attempt := e.retryAttempt
	willRetry := false
	var next time.Time
	if taskErr != nil {
		e.failCount++
		if e.retryOnFail && e.retryAttempt < e.retryMax {
			// Exponential backoff: base × 2^attempt (e.g. 5m, 10m, 20m).
			delay := e.retryBase << e.retryAttempt
			next = finished.Add(delay)
			e.retryAttempt++
			willRetry = true
			log.Printf("scheduler: task %s retry %d/%d scheduled in %s", e.id, e.retryAttempt, e.retryMax, delay)
		} else {
			next = e.schedule.Next(finished)
			e.retryAttempt = 0
		}
	} else {
		e.runCount++
		e.retryAttempt = 0
		next = e.schedule.Next(finished)
	}
	e.nextRun = next

	if s.db != nil {
		s.db.UpdateTaskRun(e.id, e.lastRun, e.lastStatus, e.lastError, //nolint:errcheck
			e.runCount, e.failCount, e.nextRun)
		s.db.RecordTaskHistory(&database.TaskHistory{ //nolint:errcheck
			TaskID:     e.id,
			StartedAt:  start,
			FinishedAt: finished,
			Status:     status,
			ErrorMsg:   errMsg,
			DurationMS: durationMS,
		})
	}
	notifier := s.notifier
	s.mu.Unlock()

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
			e.nextRun = e.schedule.Next(now)
		}
		s.mu.Unlock()
		return
	}

	stored, err := s.db.ListSchedulerTasks()
	if err != nil {
		log.Printf("scheduler: could not load state from DB: %v", err)
	}

	// Index stored tasks by ID for quick lookup.
	stateByID := make(map[string]*database.TaskState, len(stored))
	for _, st := range stored {
		stateByID[st.TaskID] = st
	}

	now := time.Now()

	s.mu.Lock()
	for _, e := range s.tasks {
		if st, ok := stateByID[e.TaskID()]; ok {
			// Restore persistent counters and timestamps.
			e.lastRun = st.LastRun
			e.lastStatus = st.LastStatus
			e.lastError = st.LastError
			e.runCount = st.RunCount
			e.failCount = st.FailCount
			// Recompute nextRun from the stored lastRun (the persisted nextRun
			// may be stale after a schedule change).
			if !e.lastRun.IsZero() {
				e.nextRun = e.schedule.Next(e.lastRun)
			} else {
				e.nextRun = e.schedule.Next(now)
			}
		} else {
			// First registration — compute initial nextRun and upsert.
			e.nextRun = e.schedule.Next(now)
		}
		// Always upsert to register new tasks and reflect schedule changes.
		s.db.UpsertSchedulerTask(toTaskState(e)) //nolint:errcheck
	}
	s.mu.Unlock()
}

// runMissed executes tasks that were due within the catch-up window while the
// server was down.
func (s *Scheduler) runMissed() {
	now := time.Now()
	cutoff := now.Add(-s.catchUpWindow)

	s.mu.Lock()
	var missed []*taskEntry
	for _, e := range s.tasks {
		if !e.enabled {
			continue
		}
		if e.nextRun.IsZero() {
			continue
		}
		// Task was due after cutoff but before now.
		if e.nextRun.After(cutoff) && e.nextRun.Before(now) {
			missed = append(missed, e)
			e.running = true
		}
	}
	s.mu.Unlock()

	for _, e := range missed {
		log.Printf("scheduler: catch-up run for %s (was due %s)", e.id, e.nextRun.Format(time.RFC3339))
		s.wg.Add(1)
		go func(entry *taskEntry) {
			defer s.wg.Done()
			s.execute(entry) //nolint:errcheck

			s.mu.Lock()
			entry.running = false
			s.mu.Unlock()
		}(e)
	}
}

// persistState writes the current in-memory state for a task to the DB.
// Must be called with s.mu held.
func (s *Scheduler) persistState(e *taskEntry) {
	if s.db == nil {
		return
	}
	s.db.UpsertSchedulerTask(toTaskState(e)) //nolint:errcheck
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
	return database.TaskState{
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
