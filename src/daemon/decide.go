package daemon

import (
	"os"
	"runtime"

	"github.com/apimgr/pastebin/src/path"
)

// ManagedByServiceManager reports whether the process was started by a
// service manager or container runtime that supervises it. Such a process
// must always stay in the foreground (PART 8, Daemonization): systemd
// (INVOCATION_ID), launchd (macOS with PPID 1), runit (SVDIR), s6
// (S6_LOGGING), or any container.
func ManagedByServiceManager() bool {
	if path.IsContainer() {
		return true
	}
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("SVDIR") != "" || os.Getenv("S6_LOGGING") != "" {
		return true
	}
	return runtime.GOOS == "darwin" && os.Getppid() == 1
}

// ShouldDaemonize decides whether a manual start should detach from the
// terminal. Priority: --daemon flag, then server.daemonize config, then the
// foreground default; a service-manager or container start is always
// foreground regardless of either setting.
func ShouldDaemonize(flag, configured bool) bool {
	if ManagedByServiceManager() {
		return false
	}
	return flag || configured
}
