package pgp

import (
	"strings"
	"testing"
	"time"
)

func testKeypair(t *testing.T) *Keypair {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	kp, err := Generate("Cairnbin", "security@example.com", now, 2*365*24*time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return kp
}

func TestIdentity(t *testing.T) {
	got := Identity("Cairnbin", "security@example.com")
	want := "Cairnbin Security <security@example.com>"
	if got != want {
		t.Fatalf("Identity = %q, want %q", got, want)
	}
}

func TestGenerateProducesArmoredKeys(t *testing.T) {
	kp := testKeypair(t)
	if !strings.Contains(kp.PublicArmored, "PGP PUBLIC KEY BLOCK") {
		t.Errorf("public key not armored: %q", kp.PublicArmored)
	}
	if !strings.Contains(kp.PrivateArmored, "PGP PRIVATE KEY BLOCK") {
		t.Errorf("private key not armored")
	}
	if kp.Fingerprint == "" || kp.Fingerprint != strings.ToUpper(kp.Fingerprint) {
		t.Errorf("fingerprint not uppercase hex: %q", kp.Fingerprint)
	}
	if !kp.ExpiresAt.After(kp.CreatedAt) {
		t.Errorf("ExpiresAt %v not after CreatedAt %v", kp.ExpiresAt, kp.CreatedAt)
	}
}

func TestGenerateRequiresEmail(t *testing.T) {
	if _, err := Generate("Cairnbin", "  ", time.Now(), time.Hour); err == nil {
		t.Fatal("expected error for empty email")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	kp := testKeypair(t)
	plaintext := []byte("top secret vulnerability report")
	msg, err := Encrypt(kp.PublicArmored, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.Contains(msg, "PGP MESSAGE") {
		t.Errorf("ciphertext not armored PGP message")
	}
	got, err := Decrypt(kp.PrivateArmored, msg)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round trip = %q, want %q", got, plaintext)
	}
}

func TestFingerprintFromPublicMatches(t *testing.T) {
	kp := testKeypair(t)
	fp, err := FingerprintFromPublic(kp.PublicArmored)
	if err != nil {
		t.Fatalf("FingerprintFromPublic: %v", err)
	}
	if fp != kp.Fingerprint {
		t.Errorf("fingerprint mismatch: %q vs %q", fp, kp.Fingerprint)
	}
}

func TestIdentityOf(t *testing.T) {
	kp := testKeypair(t)
	id, err := IdentityOf(kp.PublicArmored)
	if err != nil {
		t.Fatalf("IdentityOf: %v", err)
	}
	if !strings.Contains(id, "security@example.com") {
		t.Errorf("identity %q missing email", id)
	}
}

func TestReadEntityRejectsGarbage(t *testing.T) {
	if _, err := FingerprintFromPublic("not a key"); err == nil {
		t.Fatal("expected error for invalid armored key")
	}
}
