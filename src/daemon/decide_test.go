package daemon

import "testing"

// TestShouldDaemonizeServiceManaged verifies a systemd-style start never
// daemonizes, even when the flag or config asks for it.
func TestShouldDaemonizeServiceManaged(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123")
	if !ManagedByServiceManager() {
		t.Fatal("INVOCATION_ID set: want ManagedByServiceManager true")
	}
	if ShouldDaemonize(true, true) {
		t.Error("service-managed start must stay in the foreground")
	}
}

// TestShouldDaemonizeUnmanaged verifies the flag and config decide when no
// service manager or container is detected.
func TestShouldDaemonizeUnmanaged(t *testing.T) {
	if ManagedByServiceManager() {
		t.Skip("running inside a container or service manager")
	}
	cases := []struct{ flag, cfg, want bool }{
		{false, false, false},
		{true, false, true},
		{false, true, true},
		{true, true, true},
	}
	for _, tc := range cases {
		if got := ShouldDaemonize(tc.flag, tc.cfg); got != tc.want {
			t.Errorf("ShouldDaemonize(%v,%v) = %v, want %v", tc.flag, tc.cfg, got, tc.want)
		}
	}
}
