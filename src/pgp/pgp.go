// Package pgp implements the project-level OpenPGP keypair used by the
// coordinated-disclosure pipeline (AI.md PART 11 → "GPG Keypair Management").
//
// It provides pure-Go primitives only — keypair generation (Ed25519 signing +
// Curve25519 encryption), ASCII-armored serialization, message encryption to
// the project key, and decryption with the project private key. All key
// material is CGO-free and produced by github.com/ProtonMail/go-crypto, the
// maintained pure-Go OpenPGP implementation that supports modern ECC curves.
//
// Storage orchestration (installation_secret key derivation, on-disk paths,
// DB metadata, keyserver publishing) lives in the server package; this package
// deliberately holds no filesystem or database state.
package pgp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"golang.org/x/crypto/hkdf"
)

// wrapKeyInfo domain-separates the private-key wrapping key from every other
// installation_secret derivation (AI.md 14181/14207).
const wrapKeyInfo = "cairnbin:pgp-private-key:v1"

// WrapKey derives the 32-byte key that seals the project private key on disk
// from the installation_secret via HKDF-SHA256. Both the server (at write/read
// time) and the maintenance backup-test path share this one definition so the
// derivation can never drift between them.
func WrapKey(installSecret []byte) ([]byte, error) {
	if len(installSecret) == 0 {
		return nil, fmt.Errorf("pgp: installation_secret unavailable")
	}
	r := hkdf.New(sha256.New, installSecret, nil, []byte(wrapKeyInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("pgp: derive wrap key: %w", err)
	}
	return key, nil
}

// Keypair is a freshly generated project keypair in ASCII-armored form plus the
// metadata the server persists (AI.md 14189-14198). The armored private key is
// unencrypted here; the caller wraps it with an installation_secret-derived key
// before writing it to disk.
type Keypair struct {
	PublicArmored  string
	PrivateArmored string
	Fingerprint    string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// Identity builds the OpenPGP user identity string for the project key
// (AI.md 14181: "Identity is {app_name} Security <{security_contact}>").
func Identity(appName, email string) string {
	name := strings.TrimSpace(appName) + " Security"
	return fmt.Sprintf("%s <%s>", strings.TrimSpace(name), strings.TrimSpace(email))
}

// Generate creates an Ed25519 (signing) + Curve25519 (encryption) keypair with
// the given identity, valid from now for validFor (AI.md 14181). The returned
// Keypair carries both armored keys, the fingerprint, and the timestamps.
func Generate(appName, email string, now time.Time, validFor time.Duration) (*Keypair, error) {
	if strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("pgp: security contact email is required")
	}
	cfg := &packet.Config{
		Algorithm:       packet.PubKeyAlgoEdDSA,
		Time:            func() time.Time { return now },
		KeyLifetimeSecs: uint32(validFor / time.Second),
	}
	// NewEntity builds the user ID from name/comment/email itself; the name
	// field rejects angle brackets, so pass the bare "{app} Security" name and
	// let it assemble "{app} Security <{email}>" — matching Identity().
	entity, err := openpgp.NewEntity(strings.TrimSpace(appName)+" Security", "", email, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgp: generate entity: %w", err)
	}

	pub, err := armorEntity(entity, false)
	if err != nil {
		return nil, err
	}
	priv, err := armorEntity(entity, true)
	if err != nil {
		return nil, err
	}
	return &Keypair{
		PublicArmored:  pub,
		PrivateArmored: priv,
		Fingerprint:    Fingerprint(entity),
		CreatedAt:      now,
		ExpiresAt:      now.Add(validFor),
	}, nil
}

// Fingerprint returns the primary key's OpenPGP fingerprint as an uppercase hex
// string with no separators (AI.md 14193).
func Fingerprint(entity *openpgp.Entity) string {
	return strings.ToUpper(fmt.Sprintf("%X", entity.PrimaryKey.Fingerprint))
}

// FingerprintFromPublic parses an ASCII-armored public key and returns its
// fingerprint.
func FingerprintFromPublic(pubArmored string) (string, error) {
	entity, err := readEntity(pubArmored)
	if err != nil {
		return "", err
	}
	return Fingerprint(entity), nil
}

// IdentityOf returns the first user identity string on an armored key, used to
// validate an imported key against the project's expected identity (AI.md 14186).
func IdentityOf(keyArmored string) (string, error) {
	entity, err := readEntity(keyArmored)
	if err != nil {
		return "", err
	}
	for name := range entity.Identities {
		return name, nil
	}
	return "", fmt.Errorf("pgp: key has no user identity")
}

// Encrypt encrypts plaintext to the project's public key and returns an
// ASCII-armored PGP message. Used for encryption-at-rest of security report
// bodies (AI.md 14147) and for encrypting outbound notification bodies.
func Encrypt(pubArmored string, plaintext []byte) (string, error) {
	entity, err := readEntity(pubArmored)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	aw, err := armor.Encode(&buf, "PGP MESSAGE", nil)
	if err != nil {
		return "", fmt.Errorf("pgp: armor message: %w", err)
	}
	cfg := &packet.Config{}
	w, err := openpgp.Encrypt(aw, []*openpgp.Entity{entity}, nil, nil, cfg)
	if err != nil {
		_ = aw.Close()
		return "", fmt.Errorf("pgp: encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		_ = w.Close()
		_ = aw.Close()
		return "", fmt.Errorf("pgp: write plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		_ = aw.Close()
		return "", fmt.Errorf("pgp: close encryptor: %w", err)
	}
	if err := aw.Close(); err != nil {
		return "", fmt.Errorf("pgp: close armor: %w", err)
	}
	return buf.String(), nil
}

// Decrypt decrypts an ASCII-armored PGP message produced by Encrypt using the
// project's (already-unwrapped) armored private key.
func Decrypt(privArmored, message string) ([]byte, error) {
	entity, err := readEntity(privArmored)
	if err != nil {
		return nil, err
	}
	block, err := armor.Decode(strings.NewReader(message))
	if err != nil {
		return nil, fmt.Errorf("pgp: decode message: %w", err)
	}
	md, err := openpgp.ReadMessage(block.Body, openpgp.EntityList{entity}, nil, &packet.Config{})
	if err != nil {
		return nil, fmt.Errorf("pgp: read message: %w", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(md.UnverifiedBody); err != nil {
		return nil, fmt.Errorf("pgp: read body: %w", err)
	}
	return out.Bytes(), nil
}

// armorEntity serializes an entity to an ASCII-armored string. When private is
// true the secret key material is included.
func armorEntity(entity *openpgp.Entity, private bool) (string, error) {
	var buf bytes.Buffer
	blockType := "PGP PUBLIC KEY BLOCK"
	if private {
		blockType = "PGP PRIVATE KEY BLOCK"
	}
	aw, err := armor.Encode(&buf, blockType, nil)
	if err != nil {
		return "", fmt.Errorf("pgp: armor encode: %w", err)
	}
	if private {
		err = entity.SerializePrivate(aw, nil)
	} else {
		err = entity.Serialize(aw)
	}
	if err != nil {
		_ = aw.Close()
		return "", fmt.Errorf("pgp: serialize: %w", err)
	}
	if err := aw.Close(); err != nil {
		return "", fmt.Errorf("pgp: close armor: %w", err)
	}
	return buf.String(), nil
}

// readEntity parses a single ASCII-armored key (public or private).
func readEntity(keyArmored string) (*openpgp.Entity, error) {
	list, err := openpgp.ReadArmoredKeyRing(strings.NewReader(keyArmored))
	if err != nil {
		return nil, fmt.Errorf("pgp: read key: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("pgp: no key found in armored block")
	}
	return list[0], nil
}
