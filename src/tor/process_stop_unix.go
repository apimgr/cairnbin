//go:build !windows

package tor

import (
	"os/exec"
	"syscall"
)

func terminateTorProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}
