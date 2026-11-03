package retry

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"
)

func fast(t *testing.T) {
	t.Helper()
	old := Delays
	Delays = []time.Duration{0, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { Delays = old })
}

func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	fast(t)
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoDoesNotRetryClientErrors(t *testing.T) {
	fast(t)
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		return &StatusError{Code: 404, URL: "u"}
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoRetries503AndReturnsLastError(t *testing.T) {
	fast(t)
	calls := 0
	err := Do(context.Background(), func() error {
		calls++
		return &StatusError{Code: 503, URL: "u"}
	})
	var se *StatusError
	if !errors.As(err, &se) || calls != len(Delays) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoStopsOnContextCancel(t *testing.T) {
	old := Delays
	Delays = []time.Duration{0, time.Hour}
	defer func() { Delays = old }()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := Do(ctx, func() error { return &StatusError{Code: 503} })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	if IsRetryable(nil) || IsRetryable(context.Canceled) || IsRetryable(errors.New("x")) {
		t.Error("non-transient errors must not retry")
	}
	if !IsRetryable(context.DeadlineExceeded) {
		t.Error("deadline must retry")
	}
}
