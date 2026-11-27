//go:build windows

package app

import "os"

// shutdownSignals returns the only interrupt Windows delivers (PART 8). SIGQUIT,
// SIGUSR1/2, SIGHUP, and real-time signals do not exist on Windows.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// installPlatformSignals is a no-op on Windows: the Unix-only signals it wires
// elsewhere (SIGHUP/SIGUSR1/SIGUSR2) are unavailable (PART 8).
func installPlatformSignals(reopenLogs, dumpStatus func()) {}
