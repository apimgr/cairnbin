package task

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

var databaseCredentialPattern = regexp.MustCompile(`(?i)(password|token|authtoken|secret)=([^&\s]+)`)
var databaseUserInfoPattern = regexp.MustCompile(`://[^/@\s]+@`)

type DatabaseCheckConfig struct {
	Enabled       bool
	Failures      int
	Repeat        time.Duration
	OperatorEmail string
	Mailer        Mailer
	Probe         func(context.Context) error
	Log           func(string, string, ...string)
}

type databaseFailureState struct {
	count        int
	since        time.Time
	lastNotified time.Time
}

func DatabaseCheck(cfg DatabaseCheckConfig) func(context.Context) error {
	if cfg.Failures <= 0 {
		cfg.Failures = 3
	}
	if cfg.Repeat <= 0 {
		cfg.Repeat = 24 * time.Hour
	}
	var mu sync.Mutex
	state := databaseFailureState{}
	return func(ctx context.Context) error {
		if !cfg.Enabled {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if cfg.Probe == nil {
			return fmt.Errorf("database_check: probe is unavailable")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := cfg.Probe(probeCtx)
		cancel()
		if err == nil {
			mu.Lock()
			recovered := state.count > 0
			state = databaseFailureState{}
			mu.Unlock()
			if recovered {
				databaseCheckLog(cfg.Log, "info", "Database connection recovered")
			}
			return nil
		}

		now := time.Now().UTC()
		message := sanitizeDatabaseError(err.Error())
		mu.Lock()
		if state.count == 0 {
			state.since = now
		}
		state.count++
		shouldNotify := state.count >= cfg.Failures && (state.lastNotified.IsZero() || now.Sub(state.lastNotified) >= cfg.Repeat)
		if shouldNotify {
			state.lastNotified = now
		}
		count, since := state.count, state.since
		mu.Unlock()
		if shouldNotify {
			databaseCheckLog(cfg.Log, "warn", "Database connection issue", "since", since.Format(time.RFC3339), "failures", fmt.Sprint(count), "error", message)
			if cfg.Mailer != nil && cfg.Mailer.Enabled() && strings.TrimSpace(cfg.OperatorEmail) != "" {
				if sendErr := cfg.Mailer.Send(cfg.OperatorEmail, "database_error", map[string]string{
					"since": since.Format(time.RFC3339),
					"error": message,
				}); sendErr != nil {
					log.Printf("database_check: send database error email: %v", sendErr)
				}
			}
		}
		return fmt.Errorf("database_check: %s", message)
	}
}

func sanitizeDatabaseError(message string) string {
	message = databaseCredentialPattern.ReplaceAllString(message, "$1=[redacted]")
	return databaseUserInfoPattern.ReplaceAllString(message, ":[redacted]@")
}

func databaseCheckLog(logger func(string, string, ...string), level, message string, fields ...string) {
	if logger != nil {
		logger(level, message, fields...)
		return
	}
	log.Printf("%s: %s %s", strings.ToUpper(level), message, strings.Join(fields, " "))
}
