package pgp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyserverFailureBackoffAndAttemptLimit(t *testing.T) {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	state := KeyserverState{URL: "keys.example", Status: "pending"}
	for attempt := 1; attempt <= maxKeyserverAttempts; attempt++ {
		state.RecordFailure(now, errors.New("network failure"), true)
		if state.Attempts != attempt {
			t.Fatalf("attempt count = %d, want %d", state.Attempts, attempt)
		}
		if attempt == maxKeyserverAttempts {
			if state.Status != "final" || state.NextAttempt != nil {
				t.Fatalf("final attempt state = %#v", state)
			}
			continue
		}
		want := time.Minute * time.Duration(1<<(attempt-1))
		if want > 6*time.Hour {
			want = 6 * time.Hour
		}
		if state.NextAttempt == nil || !state.NextAttempt.Equal(now.Add(want)) {
			t.Fatalf("attempt %d next attempt = %v, want %v", attempt, state.NextAttempt, now.Add(want))
		}
	}
}

func TestKeyserverFailureDoesNotRetryPermanentResponse(t *testing.T) {
	state := KeyserverState{URL: "keys.example", Status: "pending"}
	state.RecordFailure(time.Now(), errors.New("bad request"), false)
	if state.Status != "final" || state.NextAttempt != nil || state.Attempts != 1 {
		t.Fatalf("permanent failure state = %#v", state)
	}
}

func TestKeyserverStatePersistsRetriesAndResetsOnFingerprintChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "security", "keyservers.state")
	next := time.Now().UTC().Add(time.Minute)
	want := KeyserverStateFile{Fingerprint: "NEW", Keyservers: []KeyserverState{{URL: "keys.example", Status: "failed", Attempts: 2, NextAttempt: &next}}}
	if err := WriteKeyserverState(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %o, want 600", info.Mode().Perm())
	}
	got, err := LoadKeyserverState(path, "NEW")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != want.Fingerprint || len(got.Keyservers) != 1 || got.Keyservers[0].Attempts != 2 || got.Keyservers[0].NextAttempt == nil || !got.Keyservers[0].NextAttempt.Equal(next) {
		t.Fatalf("loaded state = %#v, want %#v", got, want)
	}
	reset, err := LoadKeyserverState(path, "ROTATED")
	if err != nil {
		t.Fatal(err)
	}
	if reset.Fingerprint != "ROTATED" || len(reset.Keyservers) != 0 {
		t.Fatalf("fingerprint change retained stale retries: %#v", reset)
	}
}

func TestLoadKeyserverStateMigratesPublishedArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyservers.state")
	if err := os.WriteFile(path, []byte(`[{"url":"keys.example","published_at":"2026-01-01T00:00:00Z"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadKeyserverState(path, "ABC")
	if err != nil {
		t.Fatal(err)
	}
	if state.Fingerprint != "ABC" || len(state.Keyservers) != 1 || state.Keyservers[0].Status != "published" {
		t.Fatalf("legacy state = %#v", state)
	}
}
