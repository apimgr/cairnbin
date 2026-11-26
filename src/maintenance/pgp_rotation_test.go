package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

func newPGPRotationManager(t *testing.T, reportBody []byte) (*pgpManager, *pgp.Keypair) {
	t.Helper()
	dir := t.TempDir()
	installSecret := []byte("test-installation-secret-32bytes")
	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.DefaultConfig()
	cfg.Server.Domain = "security.example.com"
	oldKey, err := pgp.Generate(cfg.Web.SiteTitle, cfg.SecurityEmail(), time.Now(), pgpKeypairValidity)
	if err != nil {
		t.Fatal(err)
	}
	m := &pgpManager{cfg: cfg, configDir: dir, db: db, installSecret: installSecret}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.pubPath(), []byte(oldKey.PublicArmored), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.writePrivateKey(oldKey.PrivateArmored); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSecurityKeypair(&database.SecurityKeypair{
		Fingerprint: oldKey.Fingerprint,
		CreatedAt:   oldKey.CreatedAt,
		ExpiresAt:   oldKey.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	if reportBody != nil {
		ciphertext, err := pgp.Encrypt(oldKey.PublicArmored, reportBody)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSecurityReport(&database.SecurityReport{
			TrackingID:    "sec_rotation_12345678",
			Severity:      "high",
			Component:     "api",
			EncryptedBody: []byte(ciphertext),
			EncMethod:     "pgp",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return m, oldKey
}

func TestPGPRotateReencryptsReportsAndRetainsOldPrivateKey(t *testing.T) {
	const reportBody = "private vulnerability details"
	m, oldKey := newPGPRotationManager(t, []byte(reportBody))
	if err := m.rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	newPrivate, err := m.readPrivateKey(m.privPath())
	if err != nil {
		t.Fatal(err)
	}
	report, err := m.db.GetSecurityReport("sec_rotation_12345678")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := pgp.Decrypt(newPrivate, string(report.EncryptedBody))
	if err != nil || string(plain) != reportBody {
		t.Fatalf("new key could not decrypt retained report: %q, %v", plain, err)
	}
	parkedPrivate, err := m.readPrivateKey(m.rotatedPath())
	if err != nil || parkedPrivate != oldKey.PrivateArmored {
		t.Fatalf("previous private key was not retained: %v", err)
	}
	if filepath.Base(m.rotatedPath()) != "pgp.priv.old.asc.enc" {
		t.Fatalf("rotated key path = %q", m.rotatedPath())
	}
	firstRotatedPrivate := newPrivate
	if err := m.rotate(); err != nil {
		t.Fatalf("second rotation: %v", err)
	}
	secondRetiredPrivate, err := m.readPrivateKey(m.rotatedPath())
	if err != nil || secondRetiredPrivate != firstRotatedPrivate {
		t.Fatalf("second rotation did not replace .old with the retiring active key: %v", err)
	}
	meta, err := m.db.GetSecurityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	activePublic, err := os.ReadFile(m.pubPath())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := pgp.FingerprintFromPublic(string(activePublic))
	if err != nil || fingerprint != meta.Fingerprint {
		t.Fatalf("DB rotation marker %q does not match active public key %q: %v", meta.Fingerprint, fingerprint, err)
	}
}

func TestPGPRotateFailureKeepsOldKeyAndReport(t *testing.T) {
	m, oldKey := newPGPRotationManager(t, nil)
	if err := m.db.CreateSecurityReport(&database.SecurityReport{
		TrackingID:    "sec_broken_123456789",
		Severity:      "high",
		Component:     "api",
		EncryptedBody: []byte("not an armored PGP message"),
		EncMethod:     "pgp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.rotate(); err == nil {
		t.Fatal("rotation succeeded despite an undecryptable retained report")
	}
	activePrivate, err := m.readPrivateKey(m.privPath())
	if err != nil || activePrivate != oldKey.PrivateArmored {
		t.Fatalf("old private key is no longer active: %v", err)
	}
	if _, err := os.Stat(m.rotatedPath()); !os.IsNotExist(err) {
		t.Fatalf("old key was parked despite failed rotation: %v", err)
	}
	report, err := m.db.GetSecurityReport("sec_broken_123456789")
	if err != nil || string(report.EncryptedBody) != "not an armored PGP message" {
		t.Fatalf("failed rotation changed report: %+v, %v", report, err)
	}
}

func TestRecoverPGPRotationUsesDatabaseFingerprintAsCommitMarker(t *testing.T) {
	t.Run("committed staged key is installed", func(t *testing.T) {
		m, oldKey := newPGPRotationManager(t, nil)
		next, err := pgp.Generate(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail(), time.Now(), pgpKeypairValidity)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeStagedFile(m.stagedPublicPath(), []byte(next.PublicArmored), 0o644); err != nil {
			t.Fatal(err)
		}
		sealed, err := m.sealPrivateKey(next.PrivateArmored)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeStagedFile(m.stagedPrivatePath(), sealed, 0o600); err != nil {
			t.Fatal(err)
		}
		rotatedAt := next.CreatedAt
		if err := m.db.UpsertSecurityKeypair(&database.SecurityKeypair{
			Fingerprint: next.Fingerprint, CreatedAt: next.CreatedAt,
			ExpiresAt: next.ExpiresAt, LastRotatedAt: &rotatedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if err := RecoverPGPRotation(m.configDir, m.db); err != nil {
			t.Fatalf("recover committed rotation: %v", err)
		}
		activePrivate, err := m.readPrivateKey(m.privPath())
		if err != nil || activePrivate != next.PrivateArmored {
			t.Fatalf("new private key not installed: %v", err)
		}
		retiredPrivate, err := m.readPrivateKey(m.rotatedPath())
		if err != nil || retiredPrivate != oldKey.PrivateArmored {
			t.Fatalf("old private key not retained: %v", err)
		}
	})

	t.Run("uncommitted staged key is discarded", func(t *testing.T) {
		m, oldKey := newPGPRotationManager(t, nil)
		next, err := pgp.Generate(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail(), time.Now(), pgpKeypairValidity)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeStagedFile(m.stagedPublicPath(), []byte(next.PublicArmored), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeStagedFile(m.stagedPrivatePath(), []byte("staged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RecoverPGPRotation(m.configDir, m.db); err != nil {
			t.Fatalf("recover uncommitted rotation: %v", err)
		}
		if _, err := os.Stat(m.stagedPublicPath()); !os.IsNotExist(err) {
			t.Fatalf("uncommitted public key remains staged: %v", err)
		}
		activePrivate, err := m.readPrivateKey(m.privPath())
		if err != nil || activePrivate != oldKey.PrivateArmored {
			t.Fatalf("active private key changed: %v", err)
		}
	})
}
