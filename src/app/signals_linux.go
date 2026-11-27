//go:build linux

package app

import (
	"os"
	"syscall"
)

// platformStopSignals returns Linux's real-time stop signal SIGRTMIN+3, which
// Docker sends as STOPSIGNAL for graceful container shutdown (PART 8). Go's
// syscall package does not export SIGRTMIN on Linux; it is 34, so SIGRTMIN+3 is
// 37 (0x25).
func platformStopSignals() []os.Signal {
	return []os.Signal{syscall.Signal(0x25)}
}
