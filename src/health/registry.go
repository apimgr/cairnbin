package health

import (
	"fmt"
	"regexp"
	"sync"
	"time"
)

var checkKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// DatasetCheck is a public-safe snapshot of one optional data source check.
type DatasetCheck struct {
	Key      string `json:"-"`
	Status   string `json:"-"`
	Critical bool   `json:"-"`
}

type datasetCheckEntry struct {
	key      string
	critical bool
	maxAge   time.Duration
	probe    func() (time.Time, bool)
}

// CheckRegistry stores dynamically registered dataset checks and their last
// refreshed results. Snapshot never runs probes; only Refresh does.
type CheckRegistry struct {
	mu      sync.RWMutex
	refresh sync.Mutex
	entries []datasetCheckEntry
	results []DatasetCheck
}

// Register adds a dataset check in stable registration order. probe returns the
// last successful load time and whether a usable dataset is currently loaded.
func (r *CheckRegistry) Register(key string, critical bool, maxAge time.Duration, probe func() (time.Time, bool)) error {
	if !checkKeyPattern.MatchString(key) || probe == nil || maxAge < 0 {
		return fmt.Errorf("invalid health check registration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.entries {
		if entry.key == key {
			return fmt.Errorf("health check %q already registered", key)
		}
	}
	r.entries = append(r.entries, datasetCheckEntry{key: key, critical: critical, maxAge: maxAge, probe: probe})
	return nil
}

// Reset removes all registrations and cached results before a configuration
// reload rebuilds the enabled dataset registry.
func (r *CheckRegistry) Reset() {
	r.refresh.Lock()
	defer r.refresh.Unlock()
	r.mu.Lock()
	r.entries = nil
	r.results = nil
	r.mu.Unlock()
}

// Refresh evaluates every registered check and atomically publishes statuses.
func (r *CheckRegistry) Refresh(now time.Time) []DatasetCheck {
	r.refresh.Lock()
	defer r.refresh.Unlock()
	r.mu.RLock()
	entries := append([]datasetCheckEntry(nil), r.entries...)
	r.mu.RUnlock()

	results := make([]DatasetCheck, 0, len(entries))
	for _, entry := range entries {
		loadedAt, loaded := entry.probe()
		status := "ok"
		if !loaded || loadedAt.IsZero() {
			status = "error"
		} else if entry.maxAge > 0 && now.Sub(loadedAt) > entry.maxAge {
			status = "degraded"
		}
		results = append(results, DatasetCheck{Key: entry.key, Status: status, Critical: entry.critical})
	}
	r.mu.Lock()
	r.results = results
	r.mu.Unlock()
	return append([]DatasetCheck(nil), results...)
}

// Snapshot returns cached results in registration order.
func (r *CheckRegistry) Snapshot() []DatasetCheck {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]DatasetCheck(nil), r.results...)
}
