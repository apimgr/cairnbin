package pgp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const maxKeyserverAttempts = 8

type KeyserverStateFile struct {
	Fingerprint string           `json:"fingerprint"`
	Keyservers  []KeyserverState `json:"keyservers"`
}

type KeyserverState struct {
	URL         string     `json:"url"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	NextAttempt *time.Time `json:"next_attempt,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

func LoadKeyserverState(path, fingerprint string) (KeyserverStateFile, error) {
	state := KeyserverStateFile{Fingerprint: fingerprint}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(contents, &state); err != nil {
		var legacy []struct {
			URL         string    `json:"url"`
			PublishedAt time.Time `json:"published_at"`
		}
		if legacyErr := json.Unmarshal(contents, &legacy); legacyErr != nil {
			return KeyserverStateFile{}, err
		}
		state = KeyserverStateFile{Fingerprint: fingerprint}
		for _, item := range legacy {
			published := item.PublishedAt
			state.Keyservers = append(state.Keyservers, KeyserverState{URL: item.URL, Status: "published", PublishedAt: &published})
		}
		return state, nil
	}
	if state.Fingerprint != fingerprint {
		return KeyserverStateFile{Fingerprint: fingerprint}, nil
	}
	return state, nil
}

func WriteKeyserverState(path string, state KeyserverStateFile) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".keyservers-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() {
		if err := os.Remove(tempPath); retErr == nil && err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = err
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(contents, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (state *KeyserverState) RecordFailure(now time.Time, err error, retryable bool) {
	state.Attempts++
	state.LastError = err.Error()
	state.Status = "failed"
	state.NextAttempt = nil
	if !retryable || state.Attempts >= maxKeyserverAttempts {
		state.Status = "final"
		return
	}
	delay := time.Minute * time.Duration(1<<min(state.Attempts-1, 9))
	if delay > 6*time.Hour {
		delay = 6 * time.Hour
	}
	next := now.Add(delay)
	state.NextAttempt = &next
}
