//go:build !windows

package paths

import "os"

// isRoot returns true when the process is running as the privileged user.
// See AI.md PART 4 "Privileged (Administrator)" path selection.
func isRoot() bool {
	return os.Geteuid() == 0
}
