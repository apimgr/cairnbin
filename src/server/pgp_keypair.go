package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

// keypairValidity is the default project-key lifetime (AI.md 14181: "Expires 2
// years from generation").
const keypairValidity = 2 * 365 * 24 * time.Hour

// rotatedKeyGrace is how long a rotated-out private key stays on disk so in-flight
// reports encrypted to it remain decryptable (AI.md 14182: "Old key stays valid
// for 30 days for in-flight reports").
const rotatedKeyGrace = 30 * 24 * time.Hour

// pgpPrivateKeyPath is the on-disk location of the installation_secret-wrapped
// private key (AI.md 14181).
func (s *Server) pgpPrivateKeyPath() string {
	return filepath.Join(s.configDir, "security", "pgp.priv.asc.enc")
}

// pgpRotatedKeyPath is where the previous private key is parked after a rotation
// so in-flight reports can still be decrypted during the grace window.
func (s *Server) pgpRotatedKeyPath() string {
	return filepath.Join(s.configDir, "security", "pgp.priv.asc.enc.old")
}

// keyserversStatePath persists per-keyserver publish state so a restart or a
// restore does not double-submit (AI.md 14209).
func (s *Server) keyserversStatePath() string {
	return filepath.Join(s.configDir, "security", "keyservers.state")
}

// pgpWrapKey derives the 32-byte key that wraps the private key on disk from the
// installation_secret via HKDF-SHA256 (AI.md 14181/14207). Domain-separated by an
// info label so it never collides with other installation_secret derivations.
func (s *Server) pgpWrapKey() ([]byte, error) {
	return pgp.WrapKey(s.installSecret)
}

// writePrivateKey wraps an armored private key with the installation_secret-derived
// key and writes it to path with 0o600 permissions.
func (s *Server) writePrivateKey(path, armored string) error {
	key, err := s.pgpWrapKey()
	if err != nil {
		return err
	}
	sealed, err := secretbox.Seal(key, []byte(armored))
	if err != nil {
		return fmt.Errorf("pgp: seal private key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("pgp: create security dir: %w", err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		return fmt.Errorf("pgp: write private key: %w", err)
	}
	return nil
}

// readPrivateKey reads and unwraps an armored private key from path.
func (s *Server) readPrivateKey(path string) (string, error) {
	sealed, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key, err := s.pgpWrapKey()
	if err != nil {
		return "", err
	}
	plain, err := secretbox.Open(key, sealed)
	if err != nil {
		return "", fmt.Errorf("pgp: open private key: %w", err)
	}
	return string(plain), nil
}

// projectPGPPublicKey returns the armored project public key, or "" when no
// keypair has been generated. Used for report encryption-at-rest and the
// security.txt Encryption line.
func (s *Server) projectPGPPublicKey() string {
	body, err := os.ReadFile(s.pgpPublicKeyPath())
	if err != nil || len(body) == 0 {
		return ""
	}
	return string(body)
}

// ensureSecurityKeypair generates the project keypair on first start when PGP
// publishing is enabled, writes both key files, records DB metadata, and kicks
// off keyserver publishing. It never fails startup — problems are logged and the
// pipeline falls back to AES-256-GCM report encryption.
func (s *Server) ensureSecurityKeypair() {
	if !s.liveCfg().PublishPGPKeyEnabled() {
		return
	}
	meta, err := s.db.GetSecurityKeypair()
	if err != nil {
		log.Printf("pgp: read keypair metadata: %v", err)
		return
	}
	_, statErr := os.Stat(s.pgpPublicKeyPath())
	if meta != nil && !meta.Revoked && statErr == nil {
		return
	}

	cfg := s.liveCfg()
	kp, err := pgp.Generate(cfg.Web.SiteTitle, cfg.SecurityEmail(), time.Now(), keypairValidity)
	if err != nil {
		log.Printf("pgp: generate keypair: %v", err)
		return
	}
	if err := s.installKeypair(kp, nil); err != nil {
		log.Printf("pgp: install keypair: %v", err)
		return
	}
	log.Printf("pgp: generated project security keypair %s", kp.Fingerprint)
	go s.publishToKeyservers(kp.PublicArmored)
}

// installKeypair writes both key files and upserts the DB metadata row. When
// rotatedFrom is non-nil the previous metadata's fingerprint/created_at are used
// to stamp last_rotated_at.
func (s *Server) installKeypair(kp *pgp.Keypair, rotatedFrom *database.SecurityKeypair) error {
	if err := os.MkdirAll(filepath.Join(s.configDir, "security"), 0o700); err != nil {
		return fmt.Errorf("pgp: create security dir: %w", err)
	}
	if err := os.WriteFile(s.pgpPublicKeyPath(), []byte(kp.PublicArmored), 0o644); err != nil {
		return fmt.Errorf("pgp: write public key: %w", err)
	}
	if err := s.writePrivateKey(s.pgpPrivateKeyPath(), kp.PrivateArmored); err != nil {
		return err
	}
	meta := &database.SecurityKeypair{
		Fingerprint: kp.Fingerprint,
		CreatedAt:   kp.CreatedAt,
		ExpiresAt:   kp.ExpiresAt,
	}
	if rotatedFrom != nil {
		now := kp.CreatedAt
		meta.LastRotatedAt = &now
	}
	return s.db.UpsertSecurityKeypair(meta)
}

// pruneRotatedKey removes the parked previous private key once the 30-day grace
// window has elapsed since the last rotation.
func (s *Server) pruneRotatedKey() {
	info, err := os.Stat(s.pgpRotatedKeyPath())
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > rotatedKeyGrace {
		if err := os.Remove(s.pgpRotatedKeyPath()); err != nil {
			log.Printf("pgp: prune rotated key: %v", err)
		}
	}
}

// encryptSecurityReport seals a report body for at-rest storage. It prefers the
// project PGP key and falls back to AES-256-GCM keyed by the encryption_key when
// no keypair is available. Returns the ciphertext and the enc_method label.
func (s *Server) encryptSecurityReport(body []byte, aesKey []byte) ([]byte, string, error) {
	if pub := s.projectPGPPublicKey(); pub != "" {
		msg, err := pgp.Encrypt(pub, body)
		if err == nil {
			return []byte(msg), "pgp", nil
		}
		// A malformed key must not lose the report — log and fall back to AES.
		log.Printf("pgp: encrypt report, falling back to AES: %v", err)
	}
	sealed, err := secretbox.Seal(aesKey, body)
	if err != nil {
		return nil, "", err
	}
	return sealed, "aes-256-gcm", nil
}

// decryptSecurityReport reverses encryptSecurityReport. PGP-sealed bodies are
// tried against the current private key and then the parked rotated key (grace
// window); AES bodies use the supplied key.
func (s *Server) decryptSecurityReport(rec *database.SecurityReport, aesKey []byte) ([]byte, error) {
	if rec.EncMethod == "pgp" {
		for _, path := range []string{s.pgpPrivateKeyPath(), s.pgpRotatedKeyPath()} {
			priv, err := s.readPrivateKey(path)
			if err != nil {
				continue
			}
			if plain, err := pgp.Decrypt(priv, string(rec.EncryptedBody)); err == nil {
				return plain, nil
			}
		}
		return nil, fmt.Errorf("pgp: no private key could decrypt report %s", rec.TrackingID)
	}
	return s.openAtRest(aesKey, rec.EncryptedBody)
}

// publishToKeyservers submits the armored public key to every configured
// keyserver, retrying each with exponential backoff, and persists per-keyserver
// publish state to keyservers.state and the DB (AI.md 14183, 14209).
func (s *Server) publishToKeyservers(pubArmored string) {
	keyservers := s.liveCfg().Web.Security.Keyservers
	if len(keyservers) == 0 {
		return
	}
	var published []database.KeyserverPublish
	for _, ks := range keyservers {
		ks = strings.TrimSpace(ks)
		if ks == "" {
			continue
		}
		if s.submitToKeyserver(ks, pubArmored) {
			published = append(published, database.KeyserverPublish{URL: ks, PublishedAt: time.Now()})
		}
	}
	if len(published) == 0 {
		return
	}
	s.writeKeyserversState(published)
	if meta, err := s.db.GetSecurityKeypair(); err == nil && meta != nil {
		meta.KeyserversPublished = mergeKeyserverPublishes(meta.KeyserversPublished, published)
		if err := s.db.UpsertSecurityKeypair(meta); err != nil {
			log.Printf("pgp: record keyserver publish: %v", err)
		}
	}
}

// submitToKeyserver POSTs the public key to one keyserver, retrying with
// exponential backoff (1s, 2s, 4s, 8s, 16s). Returns true on success.
func (s *Server) submitToKeyserver(keyserver, pubArmored string) bool {
	delay := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		if err := pgp.PostKey(keyserver, pubArmored); err != nil {
			log.Printf("pgp: keyserver %s publish attempt %d failed: %v", keyserver, attempt+1, err)
			continue
		}
		log.Printf("pgp: published public key to %s", keyserver)
		return true
	}
	return false
}

// writeKeyserversState persists the keyserver publish state to disk (0o600).
func (s *Server) writeKeyserversState(published []database.KeyserverPublish) {
	b, err := json.MarshalIndent(published, "", "  ")
	if err != nil {
		log.Printf("pgp: marshal keyservers state: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Join(s.configDir, "security"), 0o700); err != nil {
		log.Printf("pgp: create security dir: %v", err)
		return
	}
	if err := os.WriteFile(s.keyserversStatePath(), b, 0o600); err != nil {
		log.Printf("pgp: write keyservers state: %v", err)
	}
}

// mergeKeyserverPublishes returns prior publishes with each keyserver's timestamp
// replaced by the newer publish when present (de-duplicated by URL).
func mergeKeyserverPublishes(prior, fresh []database.KeyserverPublish) []database.KeyserverPublish {
	byURL := make(map[string]database.KeyserverPublish, len(prior)+len(fresh))
	for _, p := range prior {
		byURL[p.URL] = p
	}
	for _, p := range fresh {
		byURL[p.URL] = p
	}
	out := make([]database.KeyserverPublish, 0, len(byURL))
	for _, p := range byURL {
		out = append(out, p)
	}
	return out
}
