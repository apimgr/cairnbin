// Package retry implements the PART 9 "Retry and Backoff" policy: transient
// failures are retried with exponential backoff (0s, 1s, 2s, 4s, 8s, capped at
// 30s); client errors and other permanent failures are returned immediately.
package retry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// MaxBackoff caps any single wait between attempts.
const MaxBackoff = 30 * time.Second

// Delays is the wait before each attempt. It is a variable so tests can
// shorten it.
var Delays = []time.Duration{0, 1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// StatusError reports an unexpected HTTP status from an outbound request.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("get %s: status %d", e.URL, e.Code)
}

// IsRetryable reports whether err is transient: a deadline or timeout, a
// refused or reset connection, or an HTTP 503. 4xx responses never retry.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var se *StatusError
	return errors.As(err, &se) && se.Code == 503
}

// Do runs fn until it succeeds, returns a non-retryable error, the schedule is
// exhausted (the last error is returned) or ctx ends.
func Do(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt, wait := range Delays {
		if attempt > 0 {
			if wait > MaxBackoff {
				wait = MaxBackoff
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		if !IsRetryable(err) {
			return err
		}
		lastErr = err
	}
	return lastErr
}
