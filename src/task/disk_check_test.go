package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskCheckAlertsOncePerVolumeAndOnRepeat(t *testing.T) {
	first := t.TempDir()
	second := filepath.Join(first, "nested")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	readings := []struct{ free, total uint64 }{
		{15, 100}, {15, 100}, {20, 100}, {15, 100},
	}
	call := 0
	mailer := &mockMailer{enabled: true}
	task := DiskCheck(DiskLowConfig{
		Paths:            []string{first, second},
		ThresholdPercent: 85,
		Repeat:           time.Hour,
		OperatorEmail:    "admin@example.com",
		Mailer:           mailer,
		stats: func(string) (uint64, uint64, error) {
			reading := readings[call]
			call++
			return reading.free, reading.total, nil
		},
	})

	for range 4 {
		if err := task(context.Background()); err != nil {
			t.Fatalf("disk check: %v", err)
		}
	}
	if call != 4 {
		t.Fatalf("filesystem reads = %d, want one per invocation", call)
	}
	if len(mailer.calls) != 2 {
		t.Fatalf("emails = %d, want initial alert and alert after recovery", len(mailer.calls))
	}
	for _, sent := range mailer.calls {
		if sent.template != "disk_low" || sent.to != "admin@example.com" {
			t.Errorf("email = %+v, want disk_low to admin@example.com", sent)
		}
		if sent.vars["threshold_percent"] != "85" || sent.vars["used_percent"] != "85" {
			t.Errorf("email vars = %v, want 85%% threshold and usage", sent.vars)
		}
	}
}

func TestDiskCheckReturnsFilesystemErrors(t *testing.T) {
	root := t.TempDir()
	wantErr := errors.New("stat failed")
	task := DiskCheck(DiskLowConfig{
		Paths: []string{root},
		stats: func(string) (uint64, uint64, error) {
			return 0, 0, wantErr
		},
	})
	if err := task(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
}
