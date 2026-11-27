//go:build !windows && !linux

package app

import "os"

// platformStopSignals is empty on non-Linux Unix (macOS, BSD): the SIGRTMIN+3
// real-time stop signal is a Linux/Docker construct not present there (PART 8).
func platformStopSignals() []os.Signal { return nil }
