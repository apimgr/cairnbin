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
