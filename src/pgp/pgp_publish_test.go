package pgp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── WrapKey ─────────────────────────────────────────────────────────────────

func TestWrapKey_ReturnsKey(t *testing.T) {
	secret := []byte("test-installation-secret-32bytes")
	key, err := WrapKey(secret)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
}

func TestWrapKey_Deterministic(t *testing.T) {
	secret := []byte("stable-secret")
	k1, err := WrapKey(secret)
	if err != nil {
		t.Fatalf("first WrapKey: %v", err)
	}
	k2, err := WrapKey(secret)
	if err != nil {
		t.Fatalf("second WrapKey: %v", err)
	}
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatalf("WrapKey not deterministic: k1[%d]=%d k2[%d]=%d", i, k1[i], i, k2[i])
		}
	}
}

func TestWrapKey_DifferentSecretsProduceDifferentKeys(t *testing.T) {
	k1, _ := WrapKey([]byte("secret-a"))
	k2, _ := WrapKey([]byte("secret-b"))
	same := true
	for i := range k1 {
		if k1[i] != k2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different secrets produced the same key")
	}
}

func TestWrapKey_EmptySecret_ReturnsError(t *testing.T) {
	_, err := WrapKey(nil)
	if err == nil {
		t.Fatal("expected error for nil installSecret")
	}
	_, err = WrapKey([]byte{})
	if err == nil {
		t.Fatal("expected error for empty installSecret")
	}
}

// ─── PublicFromPrivate ────────────────────────────────────────────────────────

func TestPublicFromPrivate_ExtractsPublicKey(t *testing.T) {
	kp := testKeypair(t)
	pubArmored, err := PublicFromPrivate(kp.PrivateArmored)
	if err != nil {
		t.Fatalf("PublicFromPrivate: %v", err)
	}
	if !strings.Contains(pubArmored, "PGP PUBLIC KEY BLOCK") {
		t.Errorf("result not a public key block: %q", pubArmored[:min(100, len(pubArmored))])
	}
}

func TestPublicFromPrivate_FingerprintMatches(t *testing.T) {
	kp := testKeypair(t)
	pubArmored, err := PublicFromPrivate(kp.PrivateArmored)
	if err != nil {
		t.Fatalf("PublicFromPrivate: %v", err)
	}
	fp, err := FingerprintFromPublic(pubArmored)
	if err != nil {
		t.Fatalf("FingerprintFromPublic after PublicFromPrivate: %v", err)
	}
	if fp != kp.Fingerprint {
		t.Errorf("fingerprint mismatch: got %q, want %q", fp, kp.Fingerprint)
	}
}

func TestPublicFromPrivate_RejectsGarbage(t *testing.T) {
	_, err := PublicFromPrivate("not a PGP key")
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
}

func TestPublicFromPrivate_RejectsPublicKey(t *testing.T) {
	kp := testKeypair(t)
	_, err := PublicFromPrivate(kp.PublicArmored)
	if err == nil {
		t.Fatal("expected error when passing public key as private key argument")
	}
}

// ─── KeyLifetime ──────────────────────────────────────────────────────────────

func TestKeyLifetime_WithExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifetime := 2 * 365 * 24 * time.Hour
	kp, err := Generate("Cairnbin", "sec@example.com", now, lifetime)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	created, expires, ok, err := KeyLifetime(kp.PublicArmored)
	if err != nil {
		t.Fatalf("KeyLifetime: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true (key has expiry)")
	}
	if !created.Equal(now) {
		t.Errorf("created = %v, want %v", created, now)
	}
	if expires.IsZero() {
		t.Error("expires is zero, want non-zero")
	}
	if !expires.After(created) {
		t.Errorf("expires %v not after created %v", expires, created)
	}
}

func TestKeyLifetime_CreatedTimeMatchesKeypair(t *testing.T) {
	kp := testKeypair(t)
	created, _, _, err := KeyLifetime(kp.PublicArmored)
	if err != nil {
		t.Fatalf("KeyLifetime: %v", err)
	}
	if !created.Equal(kp.CreatedAt) {
		t.Errorf("created = %v, want %v", created, kp.CreatedAt)
	}
}

func TestKeyLifetime_RejectsGarbage(t *testing.T) {
	_, _, _, err := KeyLifetime("not a key")
	if err == nil {
		t.Fatal("expected error for garbage input")
	}
}

func TestKeyLifetime_WorksOnPrivateKey(t *testing.T) {
	kp := testKeypair(t)
	created, _, ok, err := KeyLifetime(kp.PrivateArmored)
	if err != nil {
		t.Fatalf("KeyLifetime from private key: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !created.Equal(kp.CreatedAt) {
		t.Errorf("created from private = %v, want %v", created, kp.CreatedAt)
	}
}

// ─── SignPublicKey ────────────────────────────────────────────────────────────

func TestSignPublicKey_ProducesSignedKey(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oldKP, err := Generate("OldCairnbin", "sec@example.com", now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("Generate old keypair: %v", err)
	}
	newKP, err := Generate("NewCairnbin", "sec@example.com", now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("Generate new keypair: %v", err)
	}
	signed, err := SignPublicKey(oldKP.PrivateArmored, newKP.PublicArmored)
	if err != nil {
		t.Fatalf("SignPublicKey: %v", err)
	}
	if !strings.Contains(signed, "PGP PUBLIC KEY BLOCK") {
		t.Errorf("result not a public key block")
	}
}

func TestSignPublicKey_RejectsPublicKeyAsSigner(t *testing.T) {
	kp := testKeypair(t)
	kp2 := testKeypair(t)
	_, err := SignPublicKey(kp.PublicArmored, kp2.PublicArmored)
	if err == nil {
		t.Fatal("expected error when signer has no private key material")
	}
}

func TestSignPublicKey_RejectsGarbageSigner(t *testing.T) {
	kp := testKeypair(t)
	_, err := SignPublicKey("not a key", kp.PublicArmored)
	if err == nil {
		t.Fatal("expected error for garbage signer")
	}
}

func TestSignPublicKey_RejectsGarbageTarget(t *testing.T) {
	kp := testKeypair(t)
	_, err := SignPublicKey(kp.PrivateArmored, "not a key")
	if err == nil {
		t.Fatal("expected error for garbage target")
	}
}

// ─── PostKey ──────────────────────────────────────────────────────────────────

func TestPostKey_VKSSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	kp := testKeypair(t)
	err := PostKey(srv.URL+"/vks/v1/upload", kp.PublicArmored)
	if err != nil {
		t.Fatalf("PostKey VKS: %v", err)
	}
}

func TestPostKey_VKSError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	kp := testKeypair(t)
	err := PostKey(srv.URL+"/vks/v1/upload", kp.PublicArmored)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPostKey_HKPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pks/add" {
			t.Errorf("HKP path = %q, want /pks/add", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	kp := testKeypair(t)
	err := PostKey(srv.URL, kp.PublicArmored)
	if err != nil {
		t.Fatalf("PostKey HKP: %v", err)
	}
}

func TestPostKey_HKPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	kp := testKeypair(t)
	err := PostKey(srv.URL, kp.PublicArmored)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}

func TestPostKey_Unreachable(t *testing.T) {
	kp := testKeypair(t)
	err := PostKey("http://127.0.0.1:0", kp.PublicArmored)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}
