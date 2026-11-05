package maintenance_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/maintenance"
	"github.com/apimgr/cairnbin/src/pgp"
)

// writeSecurityKeypair generates a real project keypair and writes the public
// key, secretbox-sealed private key, and a keyservers.state file into
// {configDir}/security, mirroring the server's on-disk layout.
func writeSecurityKeypair(t *testing.T, configDir string, installSecret []byte) {
	t.Helper()
	kp, err := pgp.Generate("Cairnbin", "security@example.com", time.Now(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	wrapKey, err := pgp.WrapKey(installSecret)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	sealed, err := secretbox.Seal(wrapKey, []byte(kp.PrivateArmored))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	secDir := filepath.Join(configDir, "security")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "pgp.pub.asc"), []byte(kp.PublicArmored), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "pgp.priv.asc.enc"), sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "keyservers.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityKeypair_DecryptsSuccessfully(t *testing.T) {
	cfgDir, _, _ := makeTestDirs(t)
	installSecret := []byte("0123456789abcdef0123456789abcdef")
	writeSecurityKeypair(t, cfgDir, installSecret)

	if err := maintenance.TestSecurityKeypair(cfgDir, installSecret); err != nil {
		t.Fatalf("TestSecurityKeypair: %v", err)
	}
}

func TestSecurityKeypair_MissingKey(t *testing.T) {
	cfgDir, _, _ := makeTestDirs(t)
	err := maintenance.TestSecurityKeypair(cfgDir, []byte("0123456789abcdef0123456789abcdef"))
	if err == nil {
		t.Fatal("expected error for missing keypair, got nil")
	}
}

func TestSecurityKeypair_WrongSecret(t *testing.T) {
	cfgDir, _, _ := makeTestDirs(t)
	writeSecurityKeypair(t, cfgDir, []byte("0123456789abcdef0123456789abcdef"))

	err := maintenance.TestSecurityKeypair(cfgDir, []byte("ffffffffffffffffffffffffffffffff"))
	if err == nil {
		t.Fatal("expected decrypt failure with wrong secret, got nil")
	}
}

// TestSecurityKeypair_BackupRestoreRoundTrip proves the security keypair
// survives a full backup+restore cycle and still decrypts afterward
// (AI.md 14206-14213).
func TestSecurityKeypair_BackupRestoreRoundTrip(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)
	installSecret := []byte("0123456789abcdef0123456789abcdef")
	writeSecurityKeypair(t, cfgDir, installSecret)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup file created: %v", err)
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	// Restore into fresh config/data directories.
	root := t.TempDir()
	newCfg := filepath.Join(root, "config")
	newData := filepath.Join(root, "data")
	if err := maintenance.Restore(bkpPath, newCfg, newData, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// The restored keypair must still decrypt with the same installation_secret.
	if err := maintenance.TestSecurityKeypair(newCfg, installSecret); err != nil {
		t.Fatalf("TestSecurityKeypair after restore: %v", err)
	}
	// Public key and keyserver state must also be present.
	for _, name := range []string{"pgp.pub.asc", "keyservers.state"} {
		if _, err := os.Stat(filepath.Join(newCfg, "security", name)); err != nil {
			t.Errorf("restored security/%s missing: %v", name, err)
		}
	}
}
