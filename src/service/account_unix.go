//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/apimgr/cairnbin/src/paths"
)

// EnsureServiceAccount creates the unprivileged system account the server drops
// privileges to, when it does not already exist. It is a no-op unless the process
// is running as root and is idempotent when the account is already present.
//
// Per PART 23, normal startup provisions the account before dropping privileges.
// System-service installation calls the same idempotent preparation before the
// init system starts the binary for the first time.
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
		if err := createMacOSServiceUser(name, id, homeDir); err != nil {
			return err
		}
		return markServiceAccountCreated(name)

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
		if err := os.Chown(homeDir, id, id); err != nil {
			return err
		}
		return markServiceAccountCreated(name)
	case "linux":
		if err := createLinuxServiceAccount(name); err != nil {
			return err
		}
		return markServiceAccountCreated(name)

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
		if err := os.Chown(homeDir, id, id); err != nil {
			return err
		}
		return markServiceAccountCreated(name)
	}
}

func createLinuxServiceAccount(name string) error {
	id, err := findAvailableSystemID()
	if err != nil {
		return fmt.Errorf("service account: %w", err)
	}
	idStr := strconv.Itoa(id)
	homeDir := paths.GetDataDir(appName)
	groupAdd, groupErr := exec.LookPath("groupadd")
	userAdd, userErr := exec.LookPath("useradd")
	if groupErr == nil && userErr == nil {
		if output, err := exec.Command(groupAdd, "--system", "--gid", idStr, name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service group %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command(userAdd, "--system", "--uid", idStr, "--gid", idStr,
			"--home-dir", homeDir, "--no-create-home", "--shell", "/sbin/nologin",
			"--comment", appName+" service account", name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service user %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	addGroup, addGroupErr := exec.LookPath("addgroup")
	addUser, addUserErr := exec.LookPath("adduser")
	if addGroupErr == nil && addUserErr == nil {
		if output, err := exec.Command(addGroup, "-S", "-g", idStr, name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service group %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command(addUser, "-S", "-D", "-H", "-u", idStr, "-G", name,
			"-h", homeDir, "-s", "/sbin/nologin", "-g", appName+" service account", name).CombinedOutput(); err != nil {
			return fmt.Errorf("create service user %s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	return fmt.Errorf("service account: no supported Linux account tools found (need groupadd/useradd or addgroup/adduser)")
}

// PrepareSystemServiceAccount provisions the startup paths and service account
// before an init system starts the binary for the first time.
func PrepareSystemServiceAccount(username string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	if err := EnsureServiceAccount(username); err != nil {
		return err
	}
	accountName := username
	if accountName == "" || accountName == "{auto}" {
		accountName = serviceUser
	}
	account, err := user.Lookup(accountName)
	if err != nil {
		return fmt.Errorf("look up service account %s: %w", accountName, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return fmt.Errorf("parse service account UID: %w", err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return fmt.Errorf("parse service account GID: %w", err)
	}
	directories := []string{
		paths.GetConfigDir(appName),
		paths.GetDataDir(appName),
		paths.GetLogsDir(appName),
		paths.GetCacheDir(appName),
		paths.GetBackupDir(appName),
		filepath.Dir(paths.GetPIDFile(appName)),
	}
	for _, directory := range directories {
		_, statErr := os.Stat(directory)
		if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect service directory %s: %w", directory, statErr)
		}
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create service directory %s: %w", directory, err)
		}
		if os.IsNotExist(statErr) {
			if err := os.Chown(directory, uid, gid); err != nil {
				return fmt.Errorf("set service directory ownership %s: %w", directory, err)
			}
		}
	}
	return nil
}
