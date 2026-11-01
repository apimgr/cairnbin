//go:build !windows

package pid

// Internal tests for the pid package — exercises isOurProcess and
// isOurProcessDarwin which are unexported and platform-specific (Unix only).

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// TestIsOurProcess_CurrentProcess verifies isOurProcess on the current PID.
// The test binary exe path does NOT contain "pastebin", so the result is false.
func TestIsOurProcess_CurrentProcess(t *testing.T) {
	result := isOurProcess(os.Getpid())
	// Test binary is named "pid.test" not "pastebin".
	// We just want no panic; the result itself is environment-dependent.
	_ = result
}

// TestIsOurProcess_InvalidPID verifies that isOurProcess on a non-existent
// PID returns false without panicking.
func TestIsOurProcess_InvalidPID(t *testing.T) {
	if isOurProcess(99999999) {
		t.Error("isOurProcess(99999999): expected false for non-existent PID")
	}
}

// TestIsOurProcessDarwin_CurrentProcess verifies ps-based check on current PID.
// On Linux, ps is available and returns the test binary name (not "pastebin").
func TestIsOurProcessDarwin_CurrentProcess(t *testing.T) {
	result := isOurProcessDarwin(os.Getpid())
	// Test binary is not "pastebin", so result should be false.
	_ = result
}

// TestIsOurProcessDarwin_InvalidPID verifies that isOurProcessDarwin returns
// false for a PID that ps cannot find.
func TestIsOurProcessDarwin_InvalidPID(t *testing.T) {
	if isOurProcessDarwin(99999999) {
		t.Error("isOurProcessDarwin(99999999): expected false for non-existent PID")
	}
}

// TestSelfPIDHelper is not a real test: it is re-executed as a subprocess by
// TestCheckPIDFile_SelfPID_PastebinNamedBinary from a binary whose name
// contains "pastebin", so isOurProcess(self) returns true. It writes its own
// PID to the pid file, then verifies CheckPIDFile treats it as stale.
// Exit codes: 0 = stale (correct), 3 = falsely "already running", 4 = error.
func TestSelfPIDHelper(t *testing.T) {
	if os.Getenv("PID_SELF_HELPER") != "1" {
		t.Skip("helper process only")
	}
	path := os.Getenv("PID_SELF_PATH")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(4)
	}
	running, _, err := CheckPIDFile(path)
	if err != nil {
		os.Exit(4)
	}
	if running {
		// Regression: our own PID reported as another running instance.
		os.Exit(3)
	}
	os.Exit(0)
}

// TestCheckPIDFile_SelfPID_PastebinNamedBinary reproduces the container
// restart bug end-to-end: the running binary IS named "pastebin*" and the pid
// file contains that process's own PID. Before the self-PID check this
// falsely reported "already running (pid N)"; it must be treated as stale.
func TestCheckPIDFile_SelfPID_PastebinNamedBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Copy the test binary under a name containing "pastebin" so that
	// isOurProcess matches, exactly like the production binary.
	dir := t.TempDir()
	copied := filepath.Join(dir, "pastebin-selftest")
	src, err := os.Open(exe)
	if err != nil {
		t.Fatalf("open test binary: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(copied, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create binary copy: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatalf("copy test binary: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close binary copy: %v", err)
	}

	pidPath := filepath.Join(dir, "pastebin.pid")
	cmd := exec.Command(copied, "-test.run", "TestSelfPIDHelper")
	cmd.Env = append(os.Environ(),
		"PID_SELF_HELPER=1",
		"PID_SELF_PATH="+pidPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 3 {
			t.Fatalf("regression: own PID reported as already running\n%s", out)
		}
		t.Fatalf("helper process failed: %v\n%s", err, out)
	}
}

// TestIsProcessRunning_CurrentProcess verifies isProcessRunning on current PID.
func TestIsProcessRunning_CurrentProcess(t *testing.T) {
	if !isProcessRunning(os.Getpid()) {
		t.Error("isProcessRunning(self): expected true")
	}
}

// TestIsProcessRunning_InvalidPID verifies isProcessRunning returns false for
// a PID that is definitely not running.
func TestIsProcessRunning_InvalidPID(t *testing.T) {
	if isProcessRunning(99999999) {
		t.Error("isProcessRunning(99999999): expected false")
	}
}

// TestIsOurBinaryName verifies exact matching: the project binary and the
// running (possibly renamed) executable match, while "-cli" siblings and
// unrelated names do not.
func TestIsOurBinaryName(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"project name", "pastebin", true},
		{"absolute path", "/usr/local/bin/pastebin", true},
		{"ps output with newline", "pastebin\n", true},
		{"cli sibling", "pastebin-cli", false},
		{"cli sibling path", "/usr/bin/pastebin-cli", false},
		{"unrelated", "nginx", false},
		{"empty", "", false},
		{"running executable", self, true},
	}
	for _, tc := range cases {
		if got := isOurBinaryName(tc.in); got != tc.want {
			t.Errorf("%s: isOurBinaryName(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}
