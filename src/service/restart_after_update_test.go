package service

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRestartAfterUpdateNoRunningService verifies the fallback contract: when
// no system service is installed and running, RestartAfterUpdate reports
// false with no error so the caller re-execs the new binary in place.
func TestRestartAfterUpdateNoRunningService(t *testing.T) {
	if st := GetStatus(); st.Installed && st.Running {
		t.Skip("a system service is installed and running on this host")
	}
	restarted, err := RestartAfterUpdate()
	if restarted || err != nil {
		t.Fatalf("RestartAfterUpdate() = (%v, %v), want (false, nil)", restarted, err)
	}
}

func TestConsumeUpdateRestartMarker(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	if _, err := writeUpdateRestartMarker(); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !ConsumeUpdateRestartMarker() {
		t.Fatal("ConsumeUpdateRestartMarker() = false, want true")
	}
	if _, err := os.Stat(filepath.Join(dataDir, updateRestartMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("marker still exists after consume: %v", err)
	}
	if ConsumeUpdateRestartMarker() {
		t.Fatal("ConsumeUpdateRestartMarker() = true without a marker")
	}
}
