//go:build windows

package paths

import "golang.org/x/sys/windows"

// isRoot returns true when the process is running as an Administrator.
// See AI.md PART 4 "Privileged (Administrator)" path selection.
func isRoot() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	ok, err := windows.Token(0).IsMember(sid)
	if err != nil {
		return false
	}
	return ok
}
