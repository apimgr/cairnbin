package i2p

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeI2PdScript writes a POSIX shell "i2pd" stand-in that traps SIGTERM
// and exits cleanly, so startI2Pd/Alive/Close can be exercised without a
// real i2pd binary. Skips on Windows (no /bin/sh, no SIGTERM semantics).
func fakeI2PdScript(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}
	tmp := t.TempDir()
	script := filepath.Join(tmp, "fake-i2pd.sh")
	body := "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// fakeI2PdScriptWithDestination is like fakeI2PdScript, but also parses its
// --datadir argument and writes a fake site-keys.dat file into <datadir>/site
// so callers that wait on waitForKeysAddress (e.g. startI2PdLocked) succeed
// without a real i2pd binary.
func fakeI2PdScriptWithDestination(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "usr", "sbin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(binDir, "fake-i2pd-with-dest.sh")
	certDir := filepath.Join(tmp, "usr", "share", "i2pd", "certificates", "reseed")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fixture.crt"), []byte("test reseed certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `#!/bin/sh
datadir=""
take_next=""
for arg in "$@"; do
  if [ -n "$take_next" ]; then
    datadir="$arg"
    take_next=""
    continue
  fi
  case "$arg" in
    --datadir=*) datadir="${arg#--datadir=}" ;;
    --datadir) take_next="1" ;;
  esac
done
mkdir -p "$datadir/site"
printf 'fake-destination-blob' > "$datadir/site/site-keys.dat"
trap 'exit 0' TERM
while true; do sleep 1; done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestStartI2Pd_AliveThenGracefulClose(t *testing.T) {
	bin := fakeI2PdScript(t)
	tmp := t.TempDir()

	proc, err := startI2Pd(bin, filepath.Join(tmp, "data"), filepath.Join(tmp, "log"), filepath.Join(tmp, "config", "i2p", "tunnels.conf"))
	if err != nil {
		t.Fatalf("startI2Pd: %v", err)
	}

	// Give the process a moment to actually start running.
	time.Sleep(100 * time.Millisecond)
	if !proc.Alive() {
		t.Fatal("expected fake i2pd process to be alive")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- proc.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s (SIGTERM handling stuck)")
	}

	if proc.Alive() {
		t.Error("expected process to be dead after Close()")
	}
}

func TestStartI2Pd_CloseIsIdempotent(t *testing.T) {
	bin := fakeI2PdScript(t)
	tmp := t.TempDir()

	proc, err := startI2Pd(bin, filepath.Join(tmp, "data"), filepath.Join(tmp, "log"), filepath.Join(tmp, "config", "i2p", "tunnels.conf"))
	if err != nil {
		t.Fatalf("startI2Pd: %v", err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("second Close should be a safe no-op, got: %v", err)
	}
}

func TestStartI2Pd_InvalidBinary(t *testing.T) {
	tmp := t.TempDir()
	_, err := startI2Pd(filepath.Join(tmp, "does-not-exist"), tmp, tmp, filepath.Join(tmp, "tunnels.conf"))
	if err == nil {
		t.Error("expected error when the i2pd binary does not exist")
	}
}
