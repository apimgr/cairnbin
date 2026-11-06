package service

import (
	"os"
	"strings"
	"testing"
)

// ─── isPrivileged ─────────────────────────────────────────────────────────────

// TestIsPrivileged_ReturnsEuidZeroCheck verifies that isPrivileged() reflects
// the real effective UID at the time the test runs. In a normal non-root test
// environment Geteuid() != 0, so the function must return false. When the test
// runs as root (e.g. in a privileged container), it must return true.
func TestIsPrivileged_ReturnsEuidZeroCheck(t *testing.T) {
	got := isPrivileged()
	want := os.Geteuid() == 0
	if got != want {
		t.Errorf("isPrivileged() = %v; want %v (Geteuid=%d)", got, want, os.Geteuid())
	}
}

// TestIsPrivileged_ConsistentWithGetuid verifies repeated calls return the same
// value (the process UID does not change between calls).
func TestIsPrivileged_ConsistentWithGetuid(t *testing.T) {
	a := isPrivileged()
	b := isPrivileged()
	if a != b {
		t.Errorf("isPrivileged() is not idempotent: first=%v second=%v", a, b)
	}
}

// ─── Install / Uninstall privilege-guard early-return path ────────────────────

// TestInstall_NonRoot_ReturnsSudoHint tests that Install() returns an error
// containing a "sudo" hint when the process is not root. This exercises the
// isPrivileged() guard branch inside Install().
func TestInstall_NonRoot_ReturnsSudoHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; non-root guard path cannot be tested")
	}
	err := Install()
	if err == nil {
		t.Fatal("Install() must return an error when not running as root")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("Install() error = %q; expected it to contain 'sudo' hint", err.Error())
	}
}

// TestUninstall_NonRoot_ReturnsSudoHint tests that Uninstall() returns an error
// containing a "sudo" hint when the process is not root. This exercises the
// isPrivileged() guard branch inside Uninstall().
func TestUninstall_NonRoot_ReturnsSudoHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; non-root guard path cannot be tested")
	}
	err := Uninstall()
	if err == nil {
		t.Fatal("Uninstall() must return an error when not running as root")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("Uninstall() error = %q; expected it to contain 'sudo' hint", err.Error())
	}
}

// ─── confirmDestructive ───────────────────────────────────────────────────────

// withStdinContent replaces os.Stdin with a pipe whose write end has been
// pre-populated with data, calls fn, then restores the original stdin.
// The write end is closed before fn runs so ReadString('\n') can finish.
func withStdinContent(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(data); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = w.Close()

	orig := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = orig
		_ = r.Close()
	}()
	fn()
}

// TestConfirmDestructive_AcceptsY calls the real confirmDestructive() function
// with "y\n" injected into stdin. This covers the answer-parsing success path
// (statements 5 and 6 of the function body that are unreachable from EOF tests).
func TestConfirmDestructive_AcceptsY(t *testing.T) {
	var got bool
	withStdinContent(t, "y\n", func() {
		got = confirmDestructive()
	})
	if !got {
		t.Error("confirmDestructive() with 'y\\n' input must return true")
	}
}

// TestConfirmDestructive_AcceptsYes verifies "yes" is also accepted.
func TestConfirmDestructive_AcceptsYes(t *testing.T) {
	var got bool
	withStdinContent(t, "yes\n", func() {
		got = confirmDestructive()
	})
	if !got {
		t.Error("confirmDestructive() with 'yes\\n' input must return true")
	}
}

// TestConfirmDestructive_CaseInsensitive verifies uppercase variants confirm.
func TestConfirmDestructive_CaseInsensitive(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"YES\n", true},
		{"Y\n", true},
		{"Yes\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(strings.TrimSpace(tc.input), func(t *testing.T) {
			var got bool
			withStdinContent(t, tc.input, func() {
				got = confirmDestructive()
			})
			if got != tc.want {
				t.Errorf("confirmDestructive() with %q = %v; want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ─── purgeData ────────────────────────────────────────────────────────────────

// TestPurgeData_ToleratesMissingPaths verifies that purgeData() does not panic
// or return a fatal error when the system directories it tries to remove do not
// exist. The real system paths (/etc/apimgr/…, /var/lib/…) are absent in the
// Docker test container, so RemoveAll is called with non-existent paths, which
// is a no-op. We call purgeData() and only verify it completes without a panic.
//
// NOTE: purgeData also calls userdel/groupdel; those will fail silently in
// containers (the user does not exist). This is the safe, observable behavior
// we can exercise without touching real filesystem state.
func TestPurgeData_ToleratesMissingSystemPaths(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("windows path is not tested here")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("purgeData panicked: %v", r)
		}
	}()
	// Call purgeData. In a non-root Docker container the system paths do not
	// exist; RemoveAll on non-existent paths returns nil, so no error output
	// is printed and userdel/groupdel exit non-zero but those errors are
	// intentionally swallowed by the function.
	purgeData()
}

// TestPurgeData_IdempotentDoubleCall verifies calling purgeData() twice in a
// row does not panic or otherwise fail, confirming idempotent removal logic.
func TestPurgeData_IdempotentDoubleCall(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second purgeData call panicked: %v", r)
		}
	}()
	purgeData()
	purgeData()
}
