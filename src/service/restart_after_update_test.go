package service

import "testing"

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
