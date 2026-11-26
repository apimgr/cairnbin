//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// EnsureServiceAccount creates the unprivileged system account the server drops
// privileges to, when it does not already exist. It is a no-op unless the process
// is running as root and is idempotent when the account is already present.
//
// Per PART 23 the service account (together with runtime directory setup and the
// privilege drop) is provisioned at NORMAL STARTUP, never at "--service --install"
// time — the install step only writes, enables, and starts the service unit.
func EnsureServiceAccount(username string) error {
	if os.Geteuid() != 0 {
		return nil
	}

	name := username
	if name == "" || name == "{auto}" {
		name = serviceUser
	}

	// Already provisioned — nothing to do.
	if _, err := user.Lookup(name); err == nil {
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		id, err := findAvailableMacOSSystemID()
		if err != nil {
			return fmt.Errorf("service account: %w", err)
		}
		homeDir := fmt.Sprintf("/usr/local/var/%s/%s", orgName, appName)
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			return fmt.Errorf("service account home %s: %w", homeDir, err)
		}
		return createMacOSServiceUser(name, id, homeDir)

	case "freebsd", "openbsd", "netbsd", "dragonfly":
		id, err := findAvailableSystemID()
		if err != nil {
			return fmt.Errorf("service account: %w", err)
		}
		idStr := strconv.Itoa(id)
		homeDir := fmt.Sprintf("/usr/local/etc/%s/%s", orgName, appName)
		// PART 23: the home directory must exist before the user is created.
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			return fmt.Errorf("service account home %s: %w", homeDir, err)
		}
		exec.Command("pw", "groupadd", name, "-g", idStr).Run() //nolint:errcheck
		if err := exec.Command("pw", "useradd", name, "-u", idStr, "-g", idStr,
			"-d", homeDir, "-s", "/usr/sbin/nologin",
			"-c", appName+" service account").Run(); err != nil {
			return err
		}
		// PART 23: ownership is applied after the account exists.
		return os.Chown(homeDir, id, id)

	default:
		id, err := findAvailableSystemID()
		if err != nil {
			return fmt.Errorf("service account: %w", err)
		}
		idStr := strconv.Itoa(id)
		homeDir := fmt.Sprintf("/etc/%s/%s", orgName, appName)
		// PART 23: the home directory must exist before the user is created.
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			return fmt.Errorf("service account home %s: %w", homeDir, err)
		}
		if output, err := exec.Command("groupadd", "-r", "-g", idStr, name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service group %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command("useradd", "-r", "-u", idStr, "-g", idStr,
			"-d", homeDir, "-s", "/sbin/nologin",
			"-c", appName+" service account", name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service user %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		// PART 23: ownership is applied after the account exists.
		return os.Chown(homeDir, id, id)
	}
}
