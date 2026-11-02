//go:build !windows

// Package daemon provides Unix process daemonization via re-exec.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/apimgr/pastebin/src/common/i18n"
)

// Daemonize forks the process and detaches from the terminal (Unix only).
// If _DAEMON_CHILD=1 is already set, we are the child — return immediately
// and let the caller continue with normal startup. The parent prints the
// child PID (localized via i18n key cli.daemon_started, PART 30) and exits 0.
func Daemonize(lang string) error {
	// Already the daemon child (or already detached from terminal).
	if os.Getenv("_DAEMON_CHILD") != "" || os.Getppid() == 1 {
		return nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}

	// Remove --daemon / -d from the forwarded args to prevent an infinite
	// fork loop.
	args := filterDaemonFlag(os.Args[1:])

	cmd := exec.Command(execPath, args...)
	cmd.Env = append(os.Environ(), "_DAEMON_CHILD=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// create new session — detaches from controlling terminal
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	fmt.Println(i18n.TranslateFormat(lang, "cli.daemon_started", i18n.Args("pid", cmd.Process.Pid)))
	os.Exit(0)
	// unreachable
	return nil
}

// filterDaemonFlag removes --daemon and -d from a slice of CLI arguments.
func filterDaemonFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "--daemon" && a != "-d" {
			out = append(out, a)
		}
	}
	return out
}
