package health

import (
	"testing"
	"time"
)

func TestCheckRegistryCachesOrderedDatasetStatuses(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	var registry CheckRegistry
	for _, check := range []struct {
		key      string
		critical bool
		maxAge   time.Duration
		probe    func() (time.Time, bool)
	}{
		{"blocklist_ads", false, time.Hour, func() (time.Time, bool) { calls++; return now.Add(-2 * time.Hour), true }},
		{"geoip_country", true, 7 * 24 * time.Hour, func() (time.Time, bool) { calls++; return time.Time{}, false }},
	} {
		if err := registry.Register(check.key, check.critical, check.maxAge, check.probe); err != nil {
			t.Fatalf("Register(%q): %v", check.key, err)
		}
	}
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("initial Snapshot() = %v, want no results before Refresh", got)
	}
	if calls != 0 {
		t.Fatalf("Snapshot ran %d probes", calls)
	}
	got := registry.Refresh(now)
	if len(got) != 2 || got[0].Key != "blocklist_ads" || got[0].Status != "degraded" || got[0].Critical || got[1].Key != "geoip_country" || got[1].Status != "error" || !got[1].Critical {
		t.Fatalf("Refresh() = %+v", got)
	}
	if calls != 2 {
		t.Fatalf("Refresh ran %d probes, want 2", calls)
	}
	cached := registry.Snapshot()
	if len(cached) != 2 || cached[0] != got[0] || calls != 2 {
		t.Fatalf("Snapshot = %+v; calls = %d, want cached results and no new probe", cached, calls)
	}
}

func TestCheckRegistryRejectsInvalidOrDuplicateKeys(t *testing.T) {
	var registry CheckRegistry
	probe := func() (time.Time, bool) { return time.Now(), true }
	for _, key := range []string{"", "Blocklist_ads", "blocklist-ads", "9data"} {
		if err := registry.Register(key, false, time.Hour, probe); err == nil {
			t.Errorf("Register(%q) succeeded, want validation error", key)
		}
	}
	if err := registry.Register("blocklist_ads", false, time.Hour, probe); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("blocklist_ads", false, time.Hour, probe); err == nil {
		t.Fatal("duplicate registration succeeded")
	}
}
