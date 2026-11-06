package scheduler_test

// Tests for the scheduler package — task registration, immediate execution,
// enable/disable, state inspection, and error resilience.
// The database is mocked so no external dependencies are needed.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/model"
	"github.com/apimgr/cairnbin/src/scheduler"
)

// ── Minimal DB mock ──────────────────────────────────────────────────────────
//
// Only scheduler-relevant methods need real implementations. All paste and
// token operations return zero values (they are never exercised here).

type mockDB struct {
	mu    sync.Mutex
	tasks map[string]*database.TaskState
	hist  []*database.TaskHistory

	// updateRuns tracks calls to UpdateTaskRun keyed by taskID.
	updateRuns map[string]*database.TaskState

	// Optionally inject failures.
	listErr   error
	upsertErr error
}

func newMockDB() *mockDB {
	return &mockDB{
		tasks:      make(map[string]*database.TaskState),
		updateRuns: make(map[string]*database.TaskState),
	}
}

func (m *mockDB) UpsertSchedulerTask(t *database.TaskState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return m.upsertErr
	}
	cp := *t
	m.tasks[t.TaskID] = &cp
	return nil
}

func (m *mockDB) GetSchedulerTask(taskID string) (*database.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[taskID]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, nil
}

func (m *mockDB) ListSchedulerTasks() ([]*database.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]*database.TaskState, 0, len(m.tasks))
	for _, t := range m.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

func (m *mockDB) UpdateTaskRun(taskID string, lastRun time.Time, status, lastError string, runCount, failCount int64, nextRun time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := &database.TaskState{
		TaskID:     taskID,
		LastRun:    lastRun,
		LastStatus: status,
		LastError:  lastError,
		RunCount:   runCount,
		FailCount:  failCount,
		NextRun:    nextRun,
	}
	m.updateRuns[taskID] = st
	// Also update the upserted record if it exists.
	if t, ok := m.tasks[taskID]; ok {
		t.LastRun = lastRun
		t.LastStatus = status
		t.LastError = lastError
		t.RunCount = runCount
		t.FailCount = failCount
		t.NextRun = nextRun
	}
	return nil
}

func (m *mockDB) SetTaskEnabled(taskID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[taskID]; ok {
		t.Enabled = enabled
	}
	return nil
}

func (m *mockDB) RecordTaskHistory(h *database.TaskHistory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *h
	m.hist = append(m.hist, &cp)
	return nil
}

func (m *mockDB) ListTaskHistory(taskID string, limit int) ([]*database.TaskHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*database.TaskHistory
	for _, h := range m.hist {
		if h.TaskID == taskID {
			cp := *h
			out = append(out, &cp)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ── Unused interface methods (paste / token / misc) ──────────────────────────

func (m *mockDB) Close() error                                 { return nil }
func (m *mockDB) Type() string                                 { return "mock" }
func (m *mockDB) Ping() error                                  { return nil }
func (m *mockDB) CreatePaste(p *model.Paste) error             { return nil }
func (m *mockDB) GetPasteByID(id string) (*model.Paste, error) { return nil, nil }
func (m *mockDB) GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error) {
	return nil, 0, nil
}
func (m *mockDB) SearchPublicPastes(page, limit int, search, sortBy, order string) ([]model.PasteListItem, int, error) {
	return nil, 0, nil
}
func (m *mockDB) IncrementViewsAndCheckBurn(id string) (int, bool, error) { return 0, false, nil }
func (m *mockDB) DeletePaste(id string) error                             { return nil }
func (m *mockDB) DeletePasteByToken(id, hash string) error                { return nil }
func (m *mockDB) DeleteExpiredPastes() (int64, error)                     { return 0, nil }
func (m *mockDB) DeleteBurnedPastes() (int64, error)                      { return 0, nil }
func (m *mockDB) CreateAPIToken(hash, prefix, rType, rID string, expiresAt *time.Time) error {
	return nil
}
func (m *mockDB) VerifyAPIToken(hash [32]byte, rType, rID string) error { return nil }
func (m *mockDB) ValidateAPIToken(hash [32]byte, rType string) error    { return nil }
func (m *mockDB) RevokeAPIToken(prefix, reason string) error            { return nil }
func (m *mockDB) ListAPITokens() ([]*database.APITokenRecord, error)    { return nil, nil }
func (m *mockDB) GetAPITokenByPrefix(prefix string) (*database.APITokenRecord, error) {
	return nil, nil
}
func (m *mockDB) DeleteExpiredAPITokens() (int64, error) { return 0, nil }
func (m *mockDB) FindResourceIDByToken(tokenHash [32]byte, resourceType string) (string, error) {
	return "", nil
}
func (m *mockDB) EnsureAppSecret(key string) ([]byte, error)            { return nil, nil }
func (m *mockDB) CountPastes() (int64, error)                           { return 0, nil }
func (m *mockDB) CreateSecurityReport(r *database.SecurityReport) error { return nil }
func (m *mockDB) GetSecurityReport(id string) (*database.SecurityReport, error) {
	return nil, nil
}
func (m *mockDB) UpdateSecurityReportStatus(id, status, comment string) error { return nil }
func (m *mockDB) ListDisclosedSecurityReports() ([]*database.SecurityReport, error) {
	return nil, nil
}
func (m *mockDB) MarkSecurityReportTokenUsed(id string, at time.Time) error { return nil }
func (m *mockDB) GetSecurityKeypair() (*database.SecurityKeypair, error)    { return nil, nil }
func (m *mockDB) UpsertSecurityKeypair(kp *database.SecurityKeypair) error  { return nil }

// ── Test helpers ──────────────────────────────────────────────────────────────

// nopTask returns a TaskFunc that does nothing and succeeds.
func nopTask() scheduler.TaskFunc { return func(ctx context.Context) error { return nil } }

// errTask returns a TaskFunc that always fails with the provided message.
func errTask(msg string) scheduler.TaskFunc {
	return func(ctx context.Context) error { return errors.New(msg) }
}

// countTask returns a TaskFunc that increments *n each time it is called.
func countTask(n *int64) scheduler.TaskFunc {
	return func(ctx context.Context) error {
		atomic.AddInt64(n, 1)
		return nil
	}
}

// ── ParseSchedule tests (cron.go) ─────────────────────────────────────────────

func TestParseSchedule_Valid(t *testing.T) {
	cases := []struct {
		expr string
	}{
		{"@hourly"},
		{"@daily"},
		{"@midnight"},
		{"@weekly"},
		{"@monthly"},
		{"@yearly"},
		{"@annually"},
		{"@every 5m"},
		{"@every 2h"},
		{"@every 1d"},
		{"* * * * *"},
		{"0 * * * *"},
		{"0 3 * * *"},
		{"*/5 * * * *"},
		{"0 4 * * 0"},
		{"0,30 * * * *"},
		{"1-5 * * * *"},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			sched, err := scheduler.ParseSchedule(tc.expr)
			if err != nil {
				t.Fatalf("ParseSchedule(%q) unexpected error: %v", tc.expr, err)
			}
			if sched == nil {
				t.Fatalf("ParseSchedule(%q) returned nil schedule", tc.expr)
			}
			next := sched.Next(time.Now())
			if next.IsZero() {
				t.Errorf("ParseSchedule(%q).Next returned zero time", tc.expr)
			}
		})
	}
}

func TestParseSchedule_Invalid(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"only_spaces", "   "},
		{"too_few_fields", "* * * *"},
		{"too_many_fields", "* * * * * *"},
		{"invalid_minute", "60 * * * *"},
		{"invalid_hour", "* 24 * * *"},
		{"invalid_step", "*/0 * * * *"},
		{"invalid_range", "5-4 * * * *"},
		{"bad_every", "@every 0m"},
		{"bad_every_negative", "@every -5m"},
		{"bad_every_unit", "@every 3x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scheduler.ParseSchedule(tc.expr)
			if err == nil {
				t.Errorf("ParseSchedule(%q) expected error, got nil", tc.expr)
			}
		})
	}
}

func TestParseSchedule_Next_IsInFuture(t *testing.T) {
	exprs := []string{"@hourly", "@daily", "*/5 * * * *", "0 3 * * *"}
	now := time.Now()

	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			sched, err := scheduler.ParseSchedule(expr)
			if err != nil {
				t.Fatalf("ParseSchedule: %v", err)
			}
			next := sched.Next(now)
			if !next.After(now) {
				t.Errorf("Next(%v) = %v — should be strictly after now", now, next)
			}
		})
	}
}

func TestParseSchedule_String_Roundtrip(t *testing.T) {
	exprs := []string{"@hourly", "@daily", "0 3 * * *", "*/5 * * * *", "@every 10m"}
	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			sched, err := scheduler.ParseSchedule(expr)
			if err != nil {
				t.Fatalf("ParseSchedule: %v", err)
			}
			if sched.String() != expr {
				t.Errorf("String() = %q, want %q", sched.String(), expr)
			}
		})
	}
}

// ── Scheduler — registration ─────────────────────────────────────────────────

func TestScheduler_Register_Success(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("task1", "Task One", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	tasks := s.GetTasks()
	if len(tasks) != 1 {
		t.Fatalf("GetTasks: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].TaskID != "task1" {
		t.Errorf("TaskID = %q, want %q", tasks[0].TaskID, "task1")
	}
	if tasks[0].TaskName != "Task One" {
		t.Errorf("TaskName = %q, want %q", tasks[0].TaskName, "Task One")
	}
}

func TestScheduler_Register_MultipleTasks(t *testing.T) {
	s := scheduler.New(nil)

	ids := []string{"a", "b", "c"}
	for _, id := range ids {
		if err := s.Register(id, "name-"+id, "* * * * *", true, nopTask()); err != nil {
			t.Fatalf("Register %q: %v", id, err)
		}
	}

	tasks := s.GetTasks()
	if len(tasks) != len(ids) {
		t.Fatalf("expected %d tasks, got %d", len(ids), len(tasks))
	}
}

func TestScheduler_Register_InvalidSchedule(t *testing.T) {
	s := scheduler.New(nil)
	err := s.Register("bad", "Bad Task", "not-a-cron", false, nopTask())
	if err == nil {
		t.Error("expected error for invalid schedule, got nil")
	}
}

func TestScheduler_Register_Overwrite(t *testing.T) {
	// Registering the same ID twice should silently overwrite (the code does
	// s.tasks[id] = e without a duplicate-check guard).
	s := scheduler.New(nil)
	if err := s.Register("dup", "Original", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := s.Register("dup", "Overwritten", "@hourly", false, nopTask()); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	tasks := s.GetTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after overwrite, got %d", len(tasks))
	}
	if tasks[0].TaskName != "Overwritten" {
		t.Errorf("TaskName = %q, want %q", tasks[0].TaskName, "Overwritten")
	}
}

// ── Scheduler — GetTask ───────────────────────────────────────────────────────

func TestScheduler_GetTask_Found(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("t1", "T1", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	state, ok := s.GetTask("t1")
	if !ok {
		t.Fatal("GetTask returned false for registered task")
	}
	if state.TaskID != "t1" {
		t.Errorf("TaskID = %q, want %q", state.TaskID, "t1")
	}
}

func TestScheduler_GetTask_NotFound(t *testing.T) {
	s := scheduler.New(nil)
	_, ok := s.GetTask("ghost")
	if ok {
		t.Error("GetTask returned true for unknown task ID")
	}
}

// ── Scheduler — RunNow ────────────────────────────────────────────────────────

func TestScheduler_RunNow_ExecutesTask(t *testing.T) {
	var count int64
	s := scheduler.New(nil)
	if err := s.Register("run1", "Run1", "* * * * *", true, countTask(&count)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := s.RunNow("run1"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	if atomic.LoadInt64(&count) != 1 {
		t.Errorf("task executed %d times, expected 1", count)
	}
}

func TestScheduler_RunNow_UpdatesState(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("run2", "Run2", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	before := time.Now()
	if err := s.RunNow("run2"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	state, ok := s.GetTask("run2")
	if !ok {
		t.Fatal("task not found after RunNow")
	}
	if state.LastStatus != "success" {
		t.Errorf("LastStatus = %q, want %q", state.LastStatus, "success")
	}
	if state.LastRun.Before(before) {
		t.Errorf("LastRun (%v) is before test start (%v)", state.LastRun, before)
	}
	if state.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1", state.RunCount)
	}
}

func TestScheduler_RunNow_TaskError_MarksAsFailed(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("fail1", "Fail1", "* * * * *", true, errTask("oops")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// RunNow should surface the task error.
	err := s.RunNow("fail1")
	if err == nil {
		t.Fatal("expected error from failing task, got nil")
	}

	state, ok := s.GetTask("fail1")
	if !ok {
		t.Fatal("task not found")
	}
	if state.LastStatus != "failed" {
		t.Errorf("LastStatus = %q, want %q", state.LastStatus, "failed")
	}
	if state.LastError != "oops" {
		t.Errorf("LastError = %q, want %q", state.LastError, "oops")
	}
	if state.FailCount != 1 {
		t.Errorf("FailCount = %d, want 1", state.FailCount)
	}
}

func TestScheduler_RunNow_UnknownID(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.RunNow("no-such-task"); err == nil {
		t.Error("expected error for unknown task ID, got nil")
	}
}

func TestScheduler_RunNow_WithDB_PersistsState(t *testing.T) {
	db := newMockDB()
	s := scheduler.New(db)
	if err := s.Register("persist1", "Persist1", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := s.RunNow("persist1"); err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	// execute() calls UpdateTaskRun (not UpsertSchedulerTask) for run results.
	db.mu.Lock()
	ts, found := db.updateRuns["persist1"]
	histLen := len(db.hist)
	db.mu.Unlock()

	if !found {
		t.Error("UpdateTaskRun not called on mock DB after RunNow")
	} else if ts.LastStatus != "success" {
		t.Errorf("DB LastStatus = %q, want %q", ts.LastStatus, "success")
	}
	if histLen < 1 {
		t.Error("no history record written to mock DB")
	}
}

func TestScheduler_RunNow_FailingTask_WithDB_PersistsFailure(t *testing.T) {
	db := newMockDB()
	s := scheduler.New(db)
	if err := s.Register("persist-fail", "PersistFail", "* * * * *", true, errTask("db-fail")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := s.RunNow("persist-fail"); err == nil {
		t.Fatal("expected persistence error")
	}

	// execute() calls UpdateTaskRun with status="failed" for the run result.
	db.mu.Lock()
	ts, found := db.updateRuns["persist-fail"]
	db.mu.Unlock()

	if !found {
		t.Error("UpdateTaskRun not called after failing task")
	} else if ts.LastStatus != "failed" {
		t.Errorf("DB LastStatus = %q, want %q", ts.LastStatus, "failed")
	}
}

// ── Scheduler — EnableTask / DisableTask ──────────────────────────────────────

func TestScheduler_EnableDisable(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("tog1", "Tog1", "* * * * *", false, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	state, _ := s.GetTask("tog1")
	if state.Enabled {
		t.Error("task should start disabled")
	}

	s.EnableTask("tog1")
	state, _ = s.GetTask("tog1")
	if !state.Enabled {
		t.Error("task should be enabled after EnableTask")
	}

	s.DisableTask("tog1")
	state, _ = s.GetTask("tog1")
	if state.Enabled {
		t.Error("task should be disabled after DisableTask")
	}
}

func TestScheduler_EnableTask_SetsNextRun(t *testing.T) {
	s := scheduler.New(nil)
	before := time.Now()
	if err := s.Register("enable-next", "EnableNext", "* * * * *", false, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	s.EnableTask("enable-next")

	state, ok := s.GetTask("enable-next")
	if !ok {
		t.Fatal("task not found")
	}
	if !state.NextRun.After(before) {
		t.Errorf("NextRun (%v) should be in the future after EnableTask", state.NextRun)
	}
}

func TestScheduler_EnableDisable_UnknownID_NoOp(t *testing.T) {
	// Enabling or disabling a non-existent task must not panic.
	s := scheduler.New(nil)
	s.EnableTask("ghost")
	s.DisableTask("ghost")
}

// ── Scheduler — Start / Stop / Running ────────────────────────────────────────

func TestScheduler_StartStop(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("tick", "Tick", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if s.Running() {
		t.Error("scheduler should not be running before Start")
	}

	s.Start()
	if !s.Running() {
		t.Error("scheduler should be running after Start")
	}

	// Double-start must be idempotent.
	s.Start()
	if !s.Running() {
		t.Error("scheduler should still be running after second Start")
	}

	s.Stop()
	if s.Running() {
		t.Error("scheduler should not be running after Stop")
	}

	// Double-stop must be a no-op.
	s.Stop()
}

func TestScheduler_LoadState_DBError_Continues(t *testing.T) {
	// If ListSchedulerTasks returns an error, Start should still proceed
	// (log the error, compute nextRun from scratch, not panic).
	db := newMockDB()
	db.listErr = errors.New("db unavailable")

	s := scheduler.New(db)
	if err := s.Register("resilient", "Resilient", "* * * * *", true, nopTask()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Start should not panic even if DB list fails.
	s.Start()
	if !s.Running() {
		t.Error("scheduler should be running despite DB error on start")
	}
	s.Stop()
}

// ── Scheduler — RunNow prevents concurrent duplicate runs ────────────────────

func TestScheduler_RunNow_AlreadyRunning(t *testing.T) {
	// If a task is already marked running, RunNow must return an error
	// rather than starting a second concurrent execution.
	s := scheduler.New(nil)

	// A task that blocks until told to finish.
	gate := make(chan struct{})
	started := make(chan struct{}, 1)

	if err := s.Register("concurrent", "Concurrent", "* * * * *", true, func(ctx context.Context) error {
		started <- struct{}{}
		<-gate
		return nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// First run in a goroutine — it will block on gate.
	done := make(chan error, 1)
	go func() { done <- s.RunNow("concurrent") }()

	// Wait for the task to actually start.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not start within 2s")
	}

	// While the task is running, a second RunNow must fail.
	err := s.RunNow("concurrent")
	if err == nil {
		t.Error("expected error running already-running task, got nil")
	}

	// Unblock the first run and wait for it to finish.
	close(gate)
	select {
	case firstErr := <-done:
		if firstErr != nil {
			t.Errorf("first RunNow returned unexpected error: %v", firstErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first RunNow did not complete within 2s")
	}
}

// ── Scheduler — multiple executions increment counters ────────────────────────

func TestScheduler_RunCount_Accumulates(t *testing.T) {
	var count int64
	s := scheduler.New(nil)
	if err := s.Register("counter", "Counter", "* * * * *", true, countTask(&count)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	const runs = 5
	for i := 0; i < runs; i++ {
		if err := s.RunNow("counter"); err != nil {
			t.Fatalf("RunNow iteration %d: %v", i, err)
		}
	}

	state, _ := s.GetTask("counter")
	if state.RunCount != runs {
		t.Errorf("RunCount = %d, want %d", state.RunCount, runs)
	}
	if atomic.LoadInt64(&count) != runs {
		t.Errorf("task invocation count = %d, want %d", count, runs)
	}
}

func TestScheduler_FailCount_Accumulates(t *testing.T) {
	s := scheduler.New(nil)
	if err := s.Register("failing", "Failing", "* * * * *", true, errTask("boom")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	const fails = 3
	for i := 0; i < fails; i++ {
		s.RunNow("failing") //nolint:errcheck
	}

	state, _ := s.GetTask("failing")
	if state.FailCount != fails {
		t.Errorf("FailCount = %d, want %d", state.FailCount, fails)
	}
	if state.RunCount != 0 {
		t.Errorf("RunCount should be 0 for a purely failing task, got %d", state.RunCount)
	}
}

// TestScheduler_SetCatchUpWindow verifies that SetCatchUpWindow is accepted
// without panicking. The setter is a one-liner with a single statement; calling
// it is sufficient to add coverage to that statement.
func TestScheduler_SetCatchUpWindow(t *testing.T) {
	s := scheduler.New(nil)
	s.SetCatchUpWindow(30 * time.Minute)
}

// TestScheduler_SetLocation verifies that SetLocation accepts a valid
// *time.Location without panicking. Coverage target: the single assignment
// statement inside SetLocation.
func TestScheduler_SetLocation(t *testing.T) {
	s := scheduler.New(nil)
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	s.SetLocation(loc)
}

// TestScheduler_SetLocation_Local verifies passing time.Local is also safe.
func TestScheduler_SetLocation_Local(t *testing.T) {
	s := scheduler.New(nil)
	s.SetLocation(time.Local)
}
