//go:build !windows

package pid

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// isProcessRunning sends signal 0 to the process to check existence (Unix).
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds — signal 0 is the real check.
	// EPERM means the process exists but is owned by a different user (e.g. the
	// root-to-pastebin privilege drop), so treat it as running, not dead.
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// isOurProcess verifies that the running process is our binary, not a
// PID-reuse collision. Reads /proc/{pid}/exe on Linux; falls back to ps on
// macOS/BSD.
func isOurProcess(pid int) bool {
	exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return isOurProcessDarwin(pid)
	}
	return isOurBinaryName(exePath)
}

// isOurProcessDarwin checks the running process name via ps on macOS/BSD.
func isOurProcessDarwin(pid int) bool {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return isOurBinaryName(string(output))
}
