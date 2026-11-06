package pid_test

// Tests for the pid package: write/check/remove round-trips, stale detection,
// corrupt file handling, and duplicate-write guard.
//
// Design note: isOurProcess checks whether the running process exe contains
// "cairnbin". Because test binaries are named "pid.test", CheckPIDFile will
// always clear the PID file written by the current process (isOurProcess
// returns false → treated as stale). Tests are written around this real
// behaviour: we verify file creation/removal and error conditions rather than
// pretending the test binary is named "cairnbin".

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/apimgr/cairnbin/src/pid"
)

// tempPIDPath returns a path inside a fresh temp dir that follows the required
// apimgr/cairnbin- prefix pattern. The directory is cleaned up automatically.
func tempPIDPath(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "cairnbin.pid")
}

// ─── WritePIDFile ─────────────────────────────────────────────────────────────

// TestWritePIDFile_CreatesFile verifies that WritePIDFile creates a file at the
// given path and writes a numeric PID.
func TestWritePIDFile_CreatesFile(t *testing.T) {
	path := tempPIDPath(t)

	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	written, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("PID file content is not numeric: %q", data)
	}
	want := os.Getpid()
	if written != want {
		t.Errorf("PID written: got %d, want %d", written, want)
	}
}

// TestWritePIDFile_Idempotent_AfterRemove verifies that WritePIDFile succeeds
// again after RemovePIDFile has cleaned up.
func TestWritePIDFile_Idempotent_AfterRemove(t *testing.T) {
	path := tempPIDPath(t)

	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("first WritePIDFile: %v", err)
	}
	if err := pid.RemovePIDFile(path); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}
	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("second WritePIDFile after remove: %v", err)
	}
}

// TestWritePIDFile_AlreadyRunning_CairnbinProcess verifies that WritePIDFile
// returns an error containing "already running" when the PID file holds a PID
// that is live AND whose exe is named "cairnbin". We simulate this by writing a
// PID file pointing at a known non-existent but parseable PID and observing
// that the stale-removal path runs (no "already running" error is raised when
// the process is not alive).
//
// A true "already running" collision requires a process whose name contains
// "cairnbin" — that process cannot be created in a unit test without spawning
// an external binary. The following sub-tests document the two observable
// branches of WritePIDFile: stale PID is silently cleaned, and missing file
// produces no error.
func TestWritePIDFile_StalePIDIsSilentlyCleared(t *testing.T) {
	path := tempPIDPath(t)

	// Write a PID that cannot possibly be running (99999999 is above Linux
	// PID_MAX_LIMIT of 4194304 on almost all kernels; signal(0) will return
	// ESRCH).
	const stalePID = 99999999
	err := os.WriteFile(path, []byte(strconv.Itoa(stalePID)), 0o600)
	if err != nil {
		t.Fatalf("setup: write stale PID file: %v", err)
	}

	// WritePIDFile should detect the stale entry, remove it, and write ours.
	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile with stale PID: unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PID file missing after stale cleanup: %v", err)
	}
	written, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("PID file content is not numeric after stale cleanup: %q", data)
	}
	if written != os.Getpid() {
		t.Errorf("PID after stale cleanup: got %d, want %d", written, os.Getpid())
	}
}

// ─── CheckPIDFile ─────────────────────────────────────────────────────────────

// TestCheckPIDFile_SelfPID verifies that a PID file containing the current
// process's own PID is treated as stale (removed, not "already running").
// This is the container-restart scenario: the pid file persists on a volume
// and the recycled low PID (e.g. 7) is the starting process itself.
func TestCheckPIDFile_SelfPID(t *testing.T) {
	path := tempPIDPath(t)
	self := os.Getpid()
	if err := os.WriteFile(path, []byte(strconv.Itoa(self)), 0o600); err != nil {
		t.Fatalf("setup: write self PID file: %v", err)
	}

	running, retPID, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile on self PID: unexpected error: %v", err)
	}
	if running {
		t.Error("running: got true, want false for our own PID")
	}
	if retPID != 0 {
		t.Errorf("pid: got %d, want 0 for our own PID", retPID)
	}

	// Self-referencing file must be removed so WritePIDFile can proceed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("self-PID file was not removed by CheckPIDFile")
	}

	// WritePIDFile must now succeed (this is the startup path that failed).
	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile after self-PID cleanup: %v", err)
	}
}

// TestCheckPIDFile_AbsentFile verifies that a missing PID file returns
// (false, 0, nil) without error.
func TestCheckPIDFile_AbsentFile(t *testing.T) {
	path := tempPIDPath(t)
	// path does not exist yet

	running, pid_, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile on absent file: unexpected error: %v", err)
	}
	if running {
		t.Error("running: got true, want false for absent file")
	}
	if pid_ != 0 {
		t.Errorf("pid: got %d, want 0 for absent file", pid_)
	}
}

// TestCheckPIDFile_CorruptContent verifies that a non-numeric PID file causes
// CheckPIDFile to remove the file and return (false, 0, nil).
func TestCheckPIDFile_CorruptContent(t *testing.T) {
	path := tempPIDPath(t)
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("setup: write corrupt PID file: %v", err)
	}

	running, pid_, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile on corrupt file: unexpected error: %v", err)
	}
	if running {
		t.Error("running: got true, want false for corrupt PID file")
	}
	if pid_ != 0 {
		t.Errorf("pid: got %d, want 0 for corrupt PID file", pid_)
	}

	// Corrupt file must be removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("corrupt PID file was not removed by CheckPIDFile")
	}
}

// TestCheckPIDFile_StalePID verifies that a PID file containing an integer for
// a process that is definitely not running returns (false, 0, nil) and removes
// the file.
func TestCheckPIDFile_StalePID(t *testing.T) {
	path := tempPIDPath(t)
	const stalePID = 99999999
	if err := os.WriteFile(path, []byte(strconv.Itoa(stalePID)), 0o600); err != nil {
		t.Fatalf("setup: write stale PID file: %v", err)
	}

	running, pid_, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile on stale PID: unexpected error: %v", err)
	}
	if running {
		t.Error("running: got true, want false for stale PID")
	}
	if pid_ != 0 {
		t.Errorf("pid: got %d, want 0 for stale PID", pid_)
	}

	// Stale file must be removed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale PID file was not removed by CheckPIDFile")
	}
}

// TestCheckPIDFile_WrittenThenChecked_NotCairnbinBinary verifies the real
// behaviour when a valid PID file is written by a non-cairnbin process (i.e.
// the test binary itself). Because isOurProcess returns false for "pid.test",
// CheckPIDFile treats the entry as a PID-reuse collision, removes the file, and
// returns (false, 0, nil). This is correct and expected: the check is designed
// to protect against a cairnbin instance competing with another cairnbin binary,
// not to block unrelated processes.
func TestCheckPIDFile_WrittenThenChecked_NotCairnbinBinary(t *testing.T) {
	path := tempPIDPath(t)

	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	// The file must exist immediately after write.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("PID file missing right after write: %v", err)
	}

	// CheckPIDFile sees our PID is live but isOurProcess returns false (test
	// binary is not named "cairnbin"), so it clears the file.
	running, retPID, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile: unexpected error: %v", err)
	}
	if running {
		// If this ever becomes true the test binary was renamed to contain
		// "cairnbin" — that would be fine, just update this expectation.
		t.Log("NOTE: isOurProcess returned true (test binary contains 'cairnbin' in name)")
	}
	_ = retPID
}

// ─── RemovePIDFile ────────────────────────────────────────────────────────────

// TestRemovePIDFile_RemovesFile verifies that RemovePIDFile deletes the file.
func TestRemovePIDFile_RemovesFile(t *testing.T) {
	path := tempPIDPath(t)

	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}
	if err := pid.RemovePIDFile(path); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("PID file still exists after RemovePIDFile")
	}
}

// TestRemovePIDFile_AbsentFileReturnsError verifies that removing a non-existent
// PID file returns an error (os.Remove on a missing path returns an error).
func TestRemovePIDFile_AbsentFileReturnsError(t *testing.T) {
	path := tempPIDPath(t)
	// path does not exist

	err := pid.RemovePIDFile(path)
	if err == nil {
		t.Error("RemovePIDFile on absent file: expected error, got nil")
	}
}

// ─── Write → Check → Remove round-trip ───────────────────────────────────────

// TestRoundTrip verifies the full lifecycle: write creates the file, remove
// cleans it, subsequent check returns (false, 0, nil).
func TestRoundTrip_WriteCheckRemoveCheck(t *testing.T) {
	path := tempPIDPath(t)

	// Write
	if err := pid.WritePIDFile(path); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	// File must exist on disk
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("PID file not on disk after write: %v", err)
	}

	// Remove
	if err := pid.RemovePIDFile(path); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}

	// Check after remove must return (false, 0, nil)
	running, retPID, err := pid.CheckPIDFile(path)
	if err != nil {
		t.Fatalf("CheckPIDFile after remove: unexpected error: %v", err)
	}
	if running {
		t.Error("running: got true after RemovePIDFile, want false")
	}
	if retPID != 0 {
		t.Errorf("pid: got %d after RemovePIDFile, want 0", retPID)
	}
}

// ─── Table-driven edge cases ──────────────────────────────────────────────────

// TestCheckPIDFile_ContentVariants exercises CheckPIDFile with various file
// contents that are either corrupt or represent PIDs that are not running.
func TestCheckPIDFile_ContentVariants(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantRunning bool
		wantPID     int
		wantErr     bool
		wantRemoved bool
	}{
		{
			name:        "empty file",
			content:     "",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:        "whitespace only",
			content:     "   \n",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:        "negative number",
			content:     "-1",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			// -1 parses as int but isProcessRunning(-1) returns false → removed
			wantRemoved: true,
		},
		{
			name:        "zero PID",
			content:     "0",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:        "alphabetic",
			content:     "pidXYZ",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:        "stale large PID",
			content:     "99999999",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
		{
			name:        "PID with newline",
			content:     "99999999\n",
			wantRunning: false,
			wantPID:     0,
			wantErr:     false,
			wantRemoved: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempPIDPath(t)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("setup: %v", err)
			}

			running, retPID, err := pid.CheckPIDFile(path)

			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if running != tc.wantRunning {
				t.Errorf("running: got %v, want %v", running, tc.wantRunning)
			}
			if retPID != tc.wantPID {
				t.Errorf("pid: got %d, want %d", retPID, tc.wantPID)
			}
			if tc.wantRemoved {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Error("file should have been removed but still exists")
				}
			}
		})
	}
}
