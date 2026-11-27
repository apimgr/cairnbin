//go:build !windows

package app

import (
	"os"
	"os/signal"
	"syscall"
)

// shutdownSignals returns the signals that trigger graceful shutdown on Unix
// (PART 8): SIGINT, SIGTERM, SIGQUIT, plus the platform real-time stop signal
// (Docker STOPSIGNAL SIGRTMIN+3) where the OS defines it.
func shutdownSignals() []os.Signal {
	sigs := []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT}
	return append(sigs, platformStopSignals()...)
}

// installPlatformSignals wires the Unix-only, non-shutdown signals (PART 8):
// SIGHUP is ignored (configuration auto-reloads via the file watcher), SIGUSR1
// reopens log files, and SIGUSR2 dumps a status summary.
func installPlatformSignals(reopenLogs, dumpStatus func()) {
	signal.Ignore(syscall.SIGHUP)
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for s := range ch {
			switch s {
			case syscall.SIGUSR1:
				if reopenLogs != nil {
					reopenLogs()
				}
			case syscall.SIGUSR2:
				if dumpStatus != nil {
					dumpStatus()
				}
			}
		}
	}()
}
