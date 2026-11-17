//go:build !windows

package service

// IsWindowsService always returns false on non-Windows platforms.
func IsWindowsService() bool { return false }

// isPrivileged is an alias for isElevated for backward compatibility.
func isPrivileged() bool { return isElevated() }

// isWindowsServiceInstalled always returns false on non-Windows platforms;
// system service detection there is done through the on-disk unit paths.
func isWindowsServiceInstalled() bool { return false }

// RunAsWindowsService is a no-op on non-Windows platforms.
// It exists so callers can reference it unconditionally.
func RunAsWindowsService(_ string, _ func()) error { return nil }

// RunAsWindowsServiceWithStop is a no-op on non-Windows platforms.
func RunAsWindowsServiceWithStop(_ string, _ func(<-chan struct{})) error { return nil }
