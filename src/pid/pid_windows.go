//go:build windows

package pid

import (
	"golang.org/x/sys/windows"
)

// stillActive is the Windows exit code returned by GetExitCodeProcess when
// the process has not yet terminated (STILL_ACTIVE = 259 = 0x103).
const stillActive = 259

// isProcessRunning opens a query handle for pid and calls GetExitCodeProcess.
// Returns true only when the process exists and its exit code is STILL_ACTIVE.
func isProcessRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	return err == nil && exitCode == stillActive
}

// isOurProcess uses QueryFullProcessImageName to verify the running process
// is our binary, not a PID-reuse collision (Windows).
func isOurProcess(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle) //nolint:errcheck

	var buf [windows.MAX_PATH]uint16
	size := uint32(windows.MAX_PATH)
	err = windows.QueryFullProcessImageName(handle, 0, &buf[0], &size)
	if err != nil {
		return false
	}
	exePath := windows.UTF16ToString(buf[:size])
	return isOurBinaryName(exePath)
}
