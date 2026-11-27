//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

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

// enforcePrivateACL replaces inherited access on private system-mode paths
// under ProgramData with the service, Local System and Administrators ACL.
// User-mode paths retain their inherited per-user ACL.
func enforcePrivateACL(path string, isDir bool) error {
	if isUnderProgramData(path) {
		return enforceServicePrivateACL(path, isDir)
	}
	if isUnderUserData(path) {
		return nil
	}
	return enforceCurrentUserPrivateACL(path, isDir)
}

func enforceUserPrivateACL(path string) error {
	if isUnderUserData(path) {
		return nil
	}
	return enforceCurrentUserPrivateACL(path, false)
}

func enforceServicePrivateACL(path string, isDir bool) error {
	serviceSID, _, _, err := windows.LookupSID("", "NT SERVICE\\"+projectBinaryName)
	if err != nil {
		return err
	}
	systemSID, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	adminSID, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return err
	}

	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(serviceSID)
	pinner.Pin(systemSID)
	pinner.Pin(adminSID)

	inheritance := uint32(windows.NO_INHERITANCE)
	if isDir {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		serviceAccess(serviceSID, windows.TRUSTEE_IS_USER, inheritance),
		serviceAccess(systemSID, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inheritance),
		serviceAccess(adminSID, windows.TRUSTEE_IS_ALIAS, inheritance),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func enforceCurrentUserPrivateACL(path string, isDir bool) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	userSID := tokenUser.User.Sid
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(userSID)
	inheritance := uint32(windows.NO_INHERITANCE)
	if isDir {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		serviceAccess(userSID, windows.TRUSTEE_IS_USER, inheritance),
	}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func serviceAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func isUnderProgramData(path string) bool {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = filepath.Join(os.Getenv("SystemDrive"), "ProgramData")
		if os.Getenv("SystemDrive") == "" {
			programData = `C:\ProgramData`
		}
	}
	return isUnderDirectory(programData, path)
}

func isUnderUserData(path string) bool {
	for _, variable := range []string{"APPDATA", "LOCALAPPDATA"} {
		base := os.Getenv(variable)
		if base != "" && isUnderDirectory(base, path) {
			return true
		}
	}
	return false
}

func isUnderDirectory(base, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
