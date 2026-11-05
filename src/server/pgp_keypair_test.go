package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

func newPGPTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		configDir:     t.TempDir(),
		installSecret: []byte("0123456789abcdef0123456789abcdef"),
	}
}

func TestPGPWrapKeyDeterministic(t *testing.T) {
	s := newPGPTestServer(t)
	k1, err := s.pgpWrapKey()
	if err != nil {
		t.Fatalf("pgpWrapKey: %v", err)
	}
	if len(k1) != 32 {
		t.Fatalf("wrap key length = %d, want 32", len(k1))
	}
	k2, _ := s.pgpWrapKey()
	if string(k1) != string(k2) {
		t.Error("wrap key is not deterministic for the same installation_secret")
	}
	// A different installation_secret must derive a different key.
	s.installSecret = []byte("ffffffffffffffffffffffffffffffff")
	k3, _ := s.pgpWrapKey()
	if string(k1) == string(k3) {
		t.Error("wrap key did not change with installation_secret")
	}
}

func TestPrivateKeyRoundTripOnDisk(t *testing.T) {
	s := newPGPTestServer(t)
	kp, err := pgp.Generate("Cairnbin", "security@example.com", time.Now(), keypairValidity)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := s.writePrivateKey(s.pgpPrivateKeyPath(), kp.PrivateArmored); err != nil {
		t.Fatalf("writePrivateKey: %v", err)
	}
	// On-disk file must be sealed, never plaintext-armored.
	raw, err := os.ReadFile(s.pgpPrivateKeyPath())
	if err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	if len(raw) == 0 || string(raw) == kp.PrivateArmored {
		t.Fatal("private key was not encrypted at rest")
	}
	got, err := s.readPrivateKey(s.pgpPrivateKeyPath())
	if err != nil {
		t.Fatalf("readPrivateKey: %v", err)
	}
	if got != kp.PrivateArmored {
		t.Error("private key round trip mismatch")
	}
}

func TestEncryptSecurityReportPrefersPGP(t *testing.T) {
	s := newPGPTestServer(t)
	kp, err := pgp.Generate("Cairnbin", "security@example.com", time.Now(), keypairValidity)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.pgpPublicKeyPath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.pgpPublicKeyPath(), []byte(kp.PublicArmored), 0o644); err != nil {
		t.Fatalf("write public key: %v", err)
	}
	if err := s.writePrivateKey(s.pgpPrivateKeyPath(), kp.PrivateArmored); err != nil {
		t.Fatalf("writePrivateKey: %v", err)
	}

	aesKey := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("sensitive vulnerability report body")
	sealed, method, err := s.encryptSecurityReport(body, aesKey)
	if err != nil {
		t.Fatalf("encryptSecurityReport: %v", err)
	}
	if method != "pgp" {
		t.Fatalf("enc method = %q, want pgp", method)
	}
	rec := &database.SecurityReport{TrackingID: "sec_test", EncMethod: method, EncryptedBody: sealed}
	plain, err := s.decryptSecurityReport(rec, aesKey)
	if err != nil {
		t.Fatalf("decryptSecurityReport: %v", err)
	}
	if string(plain) != string(body) {
		t.Errorf("round trip = %q, want %q", plain, body)
	}
}

func TestEncryptSecurityReportAESFallback(t *testing.T) {
	s := newPGPTestServer(t)
	aesKey := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("report with no project key present")
	sealed, method, err := s.encryptSecurityReport(body, aesKey)
	if err != nil {
		t.Fatalf("encryptSecurityReport: %v", err)
	}
	if method != "aes-256-gcm" {
		t.Fatalf("enc method = %q, want aes-256-gcm", method)
	}
	rec := &database.SecurityReport{TrackingID: "sec_test", EncMethod: method, EncryptedBody: sealed}
	plain, err := s.decryptSecurityReport(rec, aesKey)
	if err != nil {
		t.Fatalf("decryptSecurityReport: %v", err)
	}
	if string(plain) != string(body) {
		t.Errorf("round trip = %q, want %q", plain, body)
	}
}

func TestMergeKeyserverPublishesDeduplicates(t *testing.T) {
	prior := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org", PublishedAt: time.Unix(1000, 0)},
	}
	fresh := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org", PublishedAt: time.Unix(2000, 0)},
		{URL: "https://keyserver.ubuntu.com", PublishedAt: time.Unix(2000, 0)},
	}
	merged := mergeKeyserverPublishes(prior, fresh)
	if len(merged) != 2 {
		t.Fatalf("merged length = %d, want 2", len(merged))
	}
	for _, p := range merged {
		if p.URL == "https://keys.openpgp.org" && !p.PublishedAt.Equal(time.Unix(2000, 0)) {
			t.Error("newer publish timestamp did not win")
		}
	}
}

func TestMergeKeyserverPublishesEmptyInputs(t *testing.T) {
	if got := mergeKeyserverPublishes(nil, nil); len(got) != 0 {
		t.Errorf("mergeKeyserverPublishes(nil, nil) length = %d, want 0", len(got))
	}
	fresh := []database.KeyserverPublish{{URL: "https://keys.openpgp.org", PublishedAt: time.Unix(1000, 0)}}
	if got := mergeKeyserverPublishes(nil, fresh); len(got) != 1 {
		t.Errorf("merge(nil, fresh) length = %d, want 1", len(got))
	}
}

// ─── projectPGPPublicKey ──────────────────────────────────────────────────────

func TestProjectPGPPublicKeyNoFile(t *testing.T) {
	s := newPGPTestServer(t)
	if got := s.projectPGPPublicKey(); got != "" {
		t.Errorf("expected empty string when no key file; got %q", got[:min(len(got), 40)])
	}
}

func TestProjectPGPPublicKeyWithFile(t *testing.T) {
	s := newPGPTestServer(t)
	if err := os.MkdirAll(filepath.Dir(s.pgpPublicKeyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	want := "-----BEGIN PGP PUBLIC KEY BLOCK-----\nfake\n-----END PGP PUBLIC KEY BLOCK-----\n"
	if err := os.WriteFile(s.pgpPublicKeyPath(), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.projectPGPPublicKey(); got != want {
		t.Errorf("projectPGPPublicKey = %q, want %q", got, want)
	}
}

// ─── path helpers ─────────────────────────────────────────────────────────────

func TestPGPKeyPathHelpers(t *testing.T) {
	s := newPGPTestServer(t)
	secDir := filepath.Join(s.configDir, "security")

	privPath := s.pgpPrivateKeyPath()
	if !strings.HasPrefix(privPath, secDir) || !strings.HasSuffix(privPath, "pgp.priv.asc.enc") {
		t.Errorf("pgpPrivateKeyPath = %q; want under %s with suffix pgp.priv.asc.enc", privPath, secDir)
	}

	rotPath := s.pgpRotatedKeyPath()
	if !strings.HasPrefix(rotPath, secDir) || !strings.HasSuffix(rotPath, "pgp.priv.asc.enc.old") {
		t.Errorf("pgpRotatedKeyPath = %q; want under %s with suffix pgp.priv.asc.enc.old", rotPath, secDir)
	}

	statePath := s.keyserversStatePath()
	if !strings.HasPrefix(statePath, secDir) || !strings.HasSuffix(statePath, "keyservers.state") {
		t.Errorf("keyserversStatePath = %q; want under %s with suffix keyservers.state", statePath, secDir)
	}
}

// ─── pruneRotatedKey ──────────────────────────────────────────────────────────

func TestPruneRotatedKeyNoFileNoPanic(t *testing.T) {
	s := newPGPTestServer(t)
	// Rotated key file does not exist; pruneRotatedKey must be a silent no-op.
	s.pruneRotatedKey()
}

func TestPruneRotatedKeyKeepsNewFile(t *testing.T) {
	s := newPGPTestServer(t)
	if err := os.MkdirAll(filepath.Dir(s.pgpRotatedKeyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.pgpRotatedKeyPath(), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	// File modification time is ~now; well within the 30-day grace window.
	s.pruneRotatedKey()
	if _, err := os.Stat(s.pgpRotatedKeyPath()); err != nil {
		t.Errorf("rotated key file was removed before grace window expired: %v", err)
	}
}

func TestPruneRotatedKeyDeletesOldFile(t *testing.T) {
	s := newPGPTestServer(t)
	if err := os.MkdirAll(filepath.Dir(s.pgpRotatedKeyPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.pgpRotatedKeyPath(), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Back-date the modification time past the rotatedKeyGrace (30 days).
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(s.pgpRotatedKeyPath(), old, old); err != nil {
		t.Fatal(err)
	}
	s.pruneRotatedKey()
	if _, err := os.Stat(s.pgpRotatedKeyPath()); !os.IsNotExist(err) {
		t.Error("expected old rotated key to be deleted after grace window")
	}
}

// ─── writeKeyserversState ─────────────────────────────────────────────────────

func TestWriteKeyserversStateWritesJSON(t *testing.T) {
	s := newPGPTestServer(t)
	published := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org", PublishedAt: time.Unix(2000, 0)},
		{URL: "https://keyserver.ubuntu.com", PublishedAt: time.Unix(3000, 0)},
	}
	s.writeKeyserversState(published)

	data, err := os.ReadFile(s.keyserversStatePath())
	if err != nil {
		t.Fatalf("read keyservers state: %v", err)
	}
	var parsed []database.KeyserverPublish
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse keyservers state JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("parsed length = %d, want 2", len(parsed))
	}
	found := false
	for _, p := range parsed {
		if p.URL == "https://keys.openpgp.org" {
			found = true
		}
	}
	if !found {
		t.Error("keyservers.state missing expected URL")
	}
}

func TestWriteKeyserversStateEmptySlice(t *testing.T) {
	s := newPGPTestServer(t)
	// Empty slice must write a valid (empty) JSON array, not crash.
	s.writeKeyserversState([]database.KeyserverPublish{})
}

// ─── installKeypair ──────────────────────────────────────────────────────────

func TestInstallKeypairCreatesFiles(t *testing.T) {
	s := newPGPTestServer(t)
	db := &stubDB{}
	s.db = db

	kp, err := pgp.Generate("Cairnbin", "security@example.com", time.Now(), keypairValidity)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := s.installKeypair(kp, nil); err != nil {
		t.Fatalf("installKeypair: %v", err)
	}

	// Public key file must exist and contain the armored block.
	pubBytes, err := os.ReadFile(s.pgpPublicKeyPath())
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	if !strings.Contains(string(pubBytes), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Error("public key file does not look like an armored PGP key")
	}

	// Private key file must exist but NOT be plaintext.
	privBytes, err := os.ReadFile(s.pgpPrivateKeyPath())
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	if strings.Contains(string(privBytes), "BEGIN PGP PRIVATE KEY BLOCK") {
		t.Error("private key file should be sealed, not plaintext")
	}
}

func TestInstallKeypairWithRotatedFrom(t *testing.T) {
	s := newPGPTestServer(t)
	s.db = &stubDB{}

	kp, err := pgp.Generate("Cairnbin", "security@example.com", time.Now(), keypairValidity)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	now := time.Now()
	prev := &database.SecurityKeypair{
		Fingerprint: "OLDFINGERPRINT",
		CreatedAt:   now.Add(-24 * time.Hour),
	}
	// rotatedFrom non-nil → LastRotatedAt should be set in the upserted metadata.
	// We just verify installKeypair does not error; the actual LastRotatedAt
	// value is an internal DB field set from kp.CreatedAt.
	if err := s.installKeypair(kp, prev); err != nil {
		t.Fatalf("installKeypair with rotatedFrom: %v", err)
	}
}

// ─── decryptSecurityReport edge cases ─────────────────────────────────────────

func TestDecryptSecurityReportPGPNoKeyFiles(t *testing.T) {
	s := newPGPTestServer(t)
	// No private key files on disk → every path in the loop fails → error returned.
	rec := &database.SecurityReport{
		TrackingID:    "sec_test",
		EncMethod:     "pgp",
		EncryptedBody: []byte("-----BEGIN PGP MESSAGE-----\nfake\n-----END PGP MESSAGE-----"),
	}
	_, err := s.decryptSecurityReport(rec, nil)
	if err == nil {
		t.Error("expected error when no PGP private key files exist")
	}
}
