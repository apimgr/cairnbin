// Package pid provides PID file management with stale-PID detection.
// A crash or kill -9 leaves stale PID files; CheckPIDFile handles that case.
package pid

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// projectBinaryName is the hardcoded project name; a PID is "ours" only when
// the process image name matches it exactly (or the running binary's own,
// possibly renamed, name) — never by substring, which would also match
// "cairnbin-cli".
const projectBinaryName = "cairnbin"

// isOurBinaryName reports whether an executable path or name refers to this
// server binary. Comparison is exact on the base name, case-insensitive, with
// a Windows ".exe" suffix ignored.
func isOurBinaryName(nameOrPath string) bool {
	norm := func(s string) string {
		s = strings.ToLower(filepath.Base(strings.TrimSpace(s)))
		return strings.TrimSuffix(s, ".exe")
	}
	got := norm(nameOrPath)
	if got == "" || got == "." {
		return false
	}
	if got == projectBinaryName {
		return true
	}
	if self, err := os.Executable(); err == nil {
		return got == norm(self)
	}
	return false
}

// CheckPIDFile checks whether a PID file exists and whether the recorded
// process is still running and belongs to our binary.
// Returns (isRunning, pid, err). When the file is absent or stale it is
// removed and (false, 0, nil) is returned.
func CheckPIDFile(pidPath string) (bool, int, error) {
	data, err := os.ReadFile(pidPath)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("reading pid file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Corrupt PID file — remove it.
		os.Remove(pidPath) //nolint:errcheck
		return false, 0, nil
	}

	// A recorded PID equal to our own PID is always stale: the file survived
	// a previous run (e.g. a pid file persisted on a container volume) and the
	// PID was recycled to this very process. It cannot be another instance —
	// without this check startup would falsely report "already running".
	if pid == os.Getpid() {
		os.Remove(pidPath) //nolint:errcheck
		return false, 0, nil
	}

	if !isProcessRunning(pid) {
		os.Remove(pidPath) //nolint:errcheck
		return false, 0, nil
	}

	if !isOurProcess(pid) {
		// PID was reused by a different binary — remove stale file.
		os.Remove(pidPath) //nolint:errcheck
		return false, 0, nil
	}

	return true, pid, nil
}

// WritePIDFile writes the current process PID to pidPath.
// It calls CheckPIDFile first and returns an error if another instance is
// already running.
func WritePIDFile(pidPath string) error {
	running, existingPID, err := CheckPIDFile(pidPath)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("already running (pid %d)", existingPID)
	}

	perm := os.FileMode(0600)
	if os.Geteuid() == 0 {
		perm = 0644
	}
	pid := os.Getpid()
	return os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), perm)
}

// RemovePIDFile removes the PID file on graceful shutdown.
func RemovePIDFile(pidPath string) error {
	return os.Remove(pidPath)
}
