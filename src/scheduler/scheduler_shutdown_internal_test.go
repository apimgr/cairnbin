package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestStopDrainsTickTaskAddedBeforeWait(t *testing.T) {
	assertStopDrainsTask(t, func(s *Scheduler) {
		s.tick()
	})
}

func TestStopDrainsCatchUpTaskAddedBeforeWait(t *testing.T) {
	assertStopDrainsTask(t, func(s *Scheduler) {
		s.runMissed()
	})
}

func assertStopDrainsTask(t *testing.T, launch func(*Scheduler)) {
	t.Helper()

	started := make(chan struct{})
	release := make(chan struct{})
	s := New(nil)
	if err := s.Register("blocked", "Blocked", "@every 1m", true, func(context.Context) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	s.mu.Lock()
	s.running = true
	s.tasks["blocked"].nextRun = time.Now().Add(-time.Second)
	s.mu.Unlock()

	launch(s)
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("task did not start")
	}

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned before the in-flight task completed")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after the in-flight task completed")
	}
}
