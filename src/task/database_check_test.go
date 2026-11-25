package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDatabaseCheckThresholdNotificationAndRecovery(t *testing.T) {
	fail := true
	var logs []string
	mailer := &mockMailer{enabled: true}
	check := DatabaseCheck(DatabaseCheckConfig{
		Enabled:       true,
		Failures:      3,
		Repeat:        time.Hour,
		OperatorEmail: "admin@example.com",
		Mailer:        mailer,
		Probe: func(context.Context) error {
			if fail {
				return errors.New("connect to libsql://user:pass@example.test?authToken=private: timeout")
			}
			return nil
		},
		Log: func(_ string, message string, _ ...string) { logs = append(logs, message) },
	})
	for range 3 {
		if err := check(context.Background()); err == nil {
			t.Fatal("failed database probe returned nil")
		}
	}
	if len(mailer.calls) != 1 || mailer.calls[0].template != "database_error" {
		t.Fatalf("database alerts = %#v, want one database_error email", mailer.calls)
	}
	message := mailer.calls[0].vars["error"]
	if strings.Contains(message, "user:pass") || strings.Contains(message, "private") || !strings.Contains(message, "[redacted]") {
		t.Fatalf("email error was not sanitized: %q", message)
	}
	fail = false
	if err := check(context.Background()); err != nil {
		t.Fatalf("successful probe failed: %v", err)
	}
	if len(logs) != 2 || logs[1] != "Database connection recovered" {
		t.Fatalf("logs = %#v, want alert and recovery", logs)
	}
	fail = true
	for range 3 {
		_ = check(context.Background())
	}
	if len(mailer.calls) != 2 {
		t.Fatalf("recovery did not reset alert threshold: got %d email(s), want 2", len(mailer.calls))
	}
}

func TestSanitizeDatabaseErrorRemovesCredentialValues(t *testing.T) {
	got := sanitizeDatabaseError("connect libsql://user:pass@example.test/db?password=hidden&token=secret")
	for _, secret := range []string{"user:pass", "hidden", "secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("sanitized error %q includes %q", got, secret)
		}
	}
}
