package scheduler

// Internal tests for unexported scheduler methods that cannot be called from
// the external test package. These tests verify tick() and runMissed() without
// starting a real goroutine loop.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apimgr/pastebin/src/metric"
)

// TestTick_NoTasksDue verifies that tick() on an empty scheduler does not
// panic and does not execute any tasks.
func TestTick_NoTasksDue(t *testing.T) {
	s := New(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("tick() panicked: %v", r)
		}
	}()
	s.tick()
}

// TestTick_SkipsNotYetDueTasks verifies that tick() does not execute a task
// whose nextRun is in the future.
func TestTick_SkipsNotYetDueTasks(t *testing.T) {
	var called int32
	s := New(nil)
	sched, err := ParseSchedule("@every 1h")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	s.tasks["notyet"] = &taskEntry{
		id:       "notyet",
		name:     "Not Yet Due",
		schedule: sched,
		enabled:  true,
		nextRun:  time.Now().Add(1 * time.Hour),
		fn:       func(ctx context.Context) error { atomic.AddInt32(&called, 1); return nil },
	}
	s.tick()
	if atomic.LoadInt32(&called) != 0 {
		t.Error("tick() must not execute tasks whose nextRun is in the future")
	}
}

// TestTick_ExecutesDueTask verifies that tick() executes a task that is past
// its nextRun time. The task sets a flag; tick() launches it in a goroutine,
// so we wait briefly for it to complete.
func TestTick_ExecutesDueTask(t *testing.T) {
	done := make(chan struct{})
	s := New(nil)
	sched, err := ParseSchedule("@every 1m")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	s.tasks["overdue"] = &taskEntry{
		id:       "overdue",
		name:     "Overdue Task",
		schedule: sched,
		enabled:  true,
		nextRun:  time.Now().Add(-1 * time.Second),
		fn: func(ctx context.Context) error {
			close(done)
			return nil
		},
	}
	s.tick()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("tick() did not execute an overdue task within 2 seconds")
	}
}

// TestTick_SkipsDisabledTasks verifies that disabled tasks are not run even
// when they are past their nextRun.
func TestTick_SkipsDisabledTasks(t *testing.T) {
	var called int32
	s := New(nil)
	sched, err := ParseSchedule("@every 1m")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	s.tasks["disabled"] = &taskEntry{
		id:       "disabled",
		name:     "Disabled Task",
		schedule: sched,
		enabled:  false,
		nextRun:  time.Now().Add(-1 * time.Second),
		fn:       func(ctx context.Context) error { atomic.AddInt32(&called, 1); return nil },
	}
	s.tick()
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("tick() must not execute disabled tasks")
	}
}

// TestRunMissed_NoTasksDue verifies that runMissed() on an empty scheduler
// does not panic.
func TestRunMissed_NoTasksDue(t *testing.T) {
	s := New(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("runMissed() panicked: %v", r)
		}
	}()
	s.runMissed()
}

// TestRunMissed_ExecutesMissedTask verifies that runMissed() executes a task
// that was due within the catch-up window while the server was down.
func TestRunMissed_ExecutesMissedTask(t *testing.T) {
	done := make(chan struct{})
	s := New(nil)
	s.catchUpWindow = 2 * time.Hour
	sched, err := ParseSchedule("@every 1h")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	s.tasks["missed"] = &taskEntry{
		id:       "missed",
		name:     "Missed Task",
		schedule: sched,
		enabled:  true,
		nextRun:  time.Now().Add(-30 * time.Minute),
		fn: func(ctx context.Context) error {
			close(done)
			return nil
		},
	}
	s.runMissed()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("runMissed() did not execute a missed task within 2 seconds")
	}
}

// TestRunMissed_SkipsOutsideCatchUpWindow verifies that runMissed() does not
// execute a task whose nextRun is outside the catch-up window.
func TestRunMissed_SkipsOutsideCatchUpWindow(t *testing.T) {
	var called int32
	s := New(nil)
	s.catchUpWindow = 30 * time.Minute
	sched, err := ParseSchedule("@every 1h")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	s.tasks["ancient"] = &taskEntry{
		id:       "ancient",
		name:     "Ancient Task",
		schedule: sched,
		enabled:  true,
		nextRun:  time.Now().Add(-2 * time.Hour),
		fn:       func(ctx context.Context) error { atomic.AddInt32(&called, 1); return nil },
	}
	s.runMissed()
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("runMissed() must not execute tasks outside the catch-up window")
	}
}

// failEntry builds a task entry whose function always fails, configured with an
// exponential-backoff retry policy for the retry-behavior tests (PART 18).
func failEntry(t *testing.T, id string, errMsg string, retryOnFail bool, base time.Duration, maxRetries int) *taskEntry {
	t.Helper()
	sched, err := ParseSchedule("@every 1h")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	return &taskEntry{
		id:          id,
		name:        id,
		schedule:    sched,
		enabled:     true,
		nextRun:     time.Now(),
		fn:          func(ctx context.Context) error { return errors.New(errMsg) },
		retryOnFail: retryOnFail,
		retryBase:   base,
		retryMax:    maxRetries,
	}
}

// TestExecute_RetryBackoffSchedulesShortDelay verifies that a failing task with
// retry-on-fail enabled reschedules nextRun using exponential backoff
// (base << attempt) rather than the normal schedule, and increments the attempt
// counter (PART 18 Retry Policy).
func TestExecute_RetryBackoffSchedulesShortDelay(t *testing.T) {
	s := New(nil)
	base := 5 * time.Minute
	e := failEntry(t, "retryme", "boom", true, base, 3)

	before := time.Now()
	_ = s.execute(e)
	after := time.Now()

	if e.retryAttempt != 1 {
		t.Fatalf("retryAttempt = %d, want 1 after first failure", e.retryAttempt)
	}
	if e.failCount != 1 {
		t.Errorf("failCount = %d, want 1", e.failCount)
	}
	// First retry delay is base<<0 = base (5m). nextRun must be roughly
	// finished+base, well before the @every 1h normal schedule.
	minWant := before.Add(base)
	maxWant := after.Add(base + time.Second)
	if e.nextRun.Before(minWant) || e.nextRun.After(maxWant) {
		t.Errorf("nextRun = %v, want between %v and %v (finished+%s)", e.nextRun, minWant, maxWant, base)
	}

	// Second failure: delay must double to base<<1 = 10m.
	mid := time.Now()
	_ = s.execute(e)
	if e.retryAttempt != 2 {
		t.Fatalf("retryAttempt = %d, want 2 after second failure", e.retryAttempt)
	}
	if got := e.nextRun.Sub(mid); got < 2*base-time.Second || got > 2*base+5*time.Second {
		t.Errorf("second retry delay = %v, want ~%v (base<<1)", got, 2*base)
	}
}

// TestExecute_RetryExhaustionFallsBackToSchedule verifies that once retryMax is
// reached, a further failure falls back to the normal schedule and resets the
// attempt counter (PART 18).
func TestExecute_RetryExhaustionFallsBackToSchedule(t *testing.T) {
	s := New(nil)
	e := failEntry(t, "exhaust", "boom", true, time.Minute, 2)

	// attempt 1
	_ = s.execute(e)
	// attempt 2 (retryAttempt now == retryMax)
	_ = s.execute(e)
	if e.retryAttempt != 2 {
		t.Fatalf("retryAttempt = %d, want 2 before exhaustion", e.retryAttempt)
	}

	before := time.Now()
	// retryAttempt == retryMax -> fall back to schedule
	_ = s.execute(e)
	if e.retryAttempt != 0 {
		t.Errorf("retryAttempt = %d, want 0 after exhaustion (reset)", e.retryAttempt)
	}
	// @every 1h schedule: nextRun should be ~1h out, not a short backoff.
	if got := e.nextRun.Sub(before); got < 30*time.Minute {
		t.Errorf("nextRun delta = %v, want ~1h (normal schedule after exhaustion)", got)
	}
}

// TestExecute_SuccessResetsRetryAttempt verifies that a successful run clears any
// accumulated retry attempts and uses the normal schedule (PART 18).
func TestExecute_SuccessResetsRetryAttempt(t *testing.T) {
	s := New(nil)
	e := failEntry(t, "recover", "boom", true, time.Minute, 3)

	// one failure -> retryAttempt == 1
	_ = s.execute(e)
	if e.retryAttempt != 1 {
		t.Fatalf("retryAttempt = %d, want 1", e.retryAttempt)
	}

	// Swap in a succeeding function and re-run.
	e.fn = func(ctx context.Context) error { return nil }
	before := time.Now()
	_ = s.execute(e)
	if e.retryAttempt != 0 {
		t.Errorf("retryAttempt = %d, want 0 after success", e.retryAttempt)
	}
	if e.runCount != 1 {
		t.Errorf("runCount = %d, want 1", e.runCount)
	}
	if got := e.nextRun.Sub(before); got < 30*time.Minute {
		t.Errorf("nextRun delta = %v, want ~1h (normal schedule)", got)
	}
}

// TestExecute_NotifierFiresWithOutcome verifies that the SetNotifier callback is
// invoked after execution with a correctly populated Outcome for both failure
// (WillRetry true) and success (PART 18 Task Execution Flow: send notification).
func TestExecute_NotifierFiresWithOutcome(t *testing.T) {
	s := New(nil)
	var mu sync.Mutex
	var outcomes []Outcome
	s.SetNotifier(func(o Outcome) {
		mu.Lock()
		outcomes = append(outcomes, o)
		mu.Unlock()
	})

	e := failEntry(t, "notifyme", "kaboom", true, time.Minute, 3)
	// failure, will retry
	_ = s.execute(e)

	e.fn = func(ctx context.Context) error { return nil }
	// success
	_ = s.execute(e)

	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	fail := outcomes[0]
	if fail.Status != "failed" || fail.Err != "kaboom" || !fail.WillRetry || fail.TaskID != "notifyme" {
		t.Errorf("failure outcome = %+v, want failed/kaboom/WillRetry=true/notifyme", fail)
	}
	if fail.Attempt != 0 {
		t.Errorf("failure Attempt = %d, want 0 (attempt index before increment)", fail.Attempt)
	}
	ok := outcomes[1]
	if ok.Status != "success" || ok.WillRetry || ok.Err != "" {
		t.Errorf("success outcome = %+v, want success/WillRetry=false/no error", ok)
	}
}

// TestStop_DrainsRunningTask verifies that Stop waits for an in-flight task
// goroutine to finish (PART 18 graceful shutdown) rather than returning while
// the task is still executing.
func TestStop_DrainsRunningTask(t *testing.T) {
	s := New(nil)
	sched, err := ParseSchedule("@every 1m")
	if err != nil {
		t.Fatalf("ParseSchedule: %v", err)
	}
	release := make(chan struct{})
	var finished int32
	s.tasks["slow"] = &taskEntry{
		id:       "slow",
		name:     "Slow Task",
		schedule: sched,
		enabled:  true,
		nextRun:  time.Now().Add(-1 * time.Second),
		fn: func(ctx context.Context) error {
			<-release
			atomic.AddInt32(&finished, 1)
			return nil
		},
	}

	// Mark running so Stop proceeds, and launch the due task via tick (adds to wg).
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.tick()

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()

	// Stop must still be blocked on the running task.
	select {
	case <-stopped:
		t.Fatal("Stop returned before the running task completed")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the task; Stop must now return promptly.
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the task finished draining")
	}
	if atomic.LoadInt32(&finished) != 1 {
		t.Errorf("task finished count = %d, want 1", atomic.LoadInt32(&finished))
	}
}

// TestExecute_PanicIsolated verifies a panicking task is recorded as failed
// and does not propagate the panic out of execute().
func TestExecute_PanicIsolated(t *testing.T) {
	s := New(nil)
	e := failEntry(t, "panicky", "x", false, time.Minute, 0)
	e.fn = func(ctx context.Context) error { panic("boom") }
	err := s.execute(e)
	if err == nil || e.lastStatus != "failed" {
		t.Fatalf("expected failed run, got err=%v status=%q", err, e.lastStatus)
	}
	if got := testutil.ToFloat64(metric.SchedulerTasksTotal.WithLabelValues("panicky", "error")); got != 1 {
		t.Errorf("scheduler_tasks_total{status=error} = %v, want 1", got)
	}
	if e.failCount != 1 || e.nextRun.IsZero() {
		t.Errorf("state not updated: failCount=%d next=%v", e.failCount, e.nextRun)
	}
}
