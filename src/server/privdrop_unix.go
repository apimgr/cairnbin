//go:build !windows

package server

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// defaultServiceUser is the unprivileged account the server drops to when no
// explicit user is configured (PART 23).
const defaultServiceUser = "cairnbin"

// currentlyRoot reports whether the effective UID is 0.
func currentlyRoot() bool {
	return os.Geteuid() == 0
}

// resolvePrivDropTarget resolves the uid/gid for the configured service user/group.
// An empty or "{auto}" username resolves to the default service account. ok is false
// when the target cannot be resolved (e.g. the system user was never created), in
// which case the caller keeps running with current privileges.
func resolvePrivDropTarget(username, group string) (uid, gid int, name string, ok bool) {
	name = username
	if name == "" || name == "{auto}" {
		name = defaultServiceUser
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, "", false
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, "", false
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, "", false
	}
	// An explicit group overrides the user's primary group when resolvable.
	if group != "" && group != "{auto}" {
		if g, gerr := user.LookupGroup(group); gerr == nil {
			if ggid, gerr2 := strconv.Atoi(g.Gid); gerr2 == nil {
				gid = ggid
			}
		}
	}
	return uid, gid, name, true
}

// dropPrivileges permanently switches the process to gid/uid in the canonical order:
// supplementary groups first, then gid, then uid. Reversing the order would briefly
// leave the process holding root's group memberships after the uid switch.
func dropPrivileges(uid, gid int) error {
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}
	return nil
}
