package maintenance

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/pastebin/src/common/secretbox"
	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/database"
	"github.com/apimgr/pastebin/src/pgp"
)

func secretFixture(t *testing.T) (SecretOptions, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "server.yml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "server.db")
	cfg.Database.Path = dbPath
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	return SecretOptions{
		ConfigDir: cfgDir,
		DBPath:    dbPath,
		LogDir:    filepath.Join(dir, "log"),
		Stdout:    &bytes.Buffer{},
	}, cfg, cfgPath
}

func TestRunSecret_Validation(t *testing.T) {
	opts := SecretOptions{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	for _, c := range []struct{ action, name, want string }{
		{"nope", "encryption_key", "unknown action"},
		{"rotate", "", "requires a name"},
		{"rotate", "csrf_token_secret", "rotated automatically"},
		{"rotate", "cookie_signing_key", "rotated automatically"},
		{"rotate", "bogus", "unknown secret"},
	} {
		err := RunSecret(c.action, c.name, opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: got %v, want %q", c.action, c.name, err, c.want)
		}
	}
}

func TestRunSecret_NotConfirmed(t *testing.T) {
	opts, _, _ := secretFixture(t)
	opts.Stdin = strings.NewReader("no\n")
	if err := RunSecret("rotate", "encryption_key", opts); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("got %v", err)
	}
}

func TestRotateEncryptionKey(t *testing.T) {
	opts, cfg, cfgPath := secretFixture(t)
	oldKey, err := cfg.EncryptionKey()
	if err != nil {
		t.Fatal(err)
	}

	rawDB, err := database.NewDatabase("sqlite", opts.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	sealedTok, _ := secretbox.Seal(oldKey, []byte("tok_secretvalue"))
	encTok := base64.StdEncoding.EncodeToString(sealedTok)
	sealedRep, _ := secretbox.Seal(oldKey, []byte("report body"))
	if err := rawDB.(interface {
		CreateSecurityReport(*database.SecurityReport) error
	}).CreateSecurityReport(&database.SecurityReport{
		TrackingID: "sec_test", Status: "new", Severity: "High", Component: "web",
		EncryptedBody: sealedRep, EncMethod: "aes-256-gcm", CreditPreference: "no",
		TokenHash: "h", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rawDB.Close()

	sdb, err := sql.Open("sqlite", opts.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`INSERT INTO api_tokens(owner_id, kind, token_hash, token_prefix, token_suffix, token_enc, resource_type, resource_id) VALUES ('o1','primary','hash1','tok_secr','alue',?,'owner','o1')`, encTok); err != nil {
		t.Fatal(err)
	}
	sdb.Close()

	opts.Stdin = strings.NewReader("rotate\n")
	if err := RunSecret("rotate", "encryption_key", opts); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	newCfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := newCfg.EncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(oldKey, newKey) {
		t.Fatal("encryption key unchanged")
	}

	sdb, _ = sql.Open("sqlite", opts.DBPath)
	defer sdb.Close()
	var enc string
	if err := sdb.QueryRow(`SELECT token_enc FROM api_tokens WHERE owner_id='o1'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(enc)
	if plain, err := secretbox.Open(newKey, raw); err != nil || string(plain) != "tok_secretvalue" {
		t.Fatalf("token not re-encrypted: %q %v", plain, err)
	}
	var body []byte
	if err := sdb.QueryRow(`SELECT encrypted_body FROM security_reports WHERE tracking_id='sec_test'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if plain, err := secretbox.Open(newKey, body); err != nil || string(plain) != "report body" {
		t.Fatalf("report not re-encrypted: %q %v", plain, err)
	}
	var prev string
	if err := sdb.QueryRow(`SELECT value FROM app_secrets WHERE key=?`, EncryptionKeyPreviousSecret).Scan(&prev); err != nil {
		t.Fatalf("previous key not parked: %v", err)
	}
}

func TestRotateInstallationSecret(t *testing.T) {
	opts, _, _ := secretFixture(t)
	rawDB, err := database.NewDatabase("sqlite", opts.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, err := rawDB.EnsureAppSecret("installation_secret")
	if err != nil {
		t.Fatal(err)
	}
	rawDB.Close()

	oldWrap, _ := pgp.WrapKey(oldSecret)
	sealed, _ := secretbox.Seal(oldWrap, []byte("ARMORED PRIVATE KEY"))
	keyFile := filepath.Join(opts.ConfigDir, "security", "pgp.priv.asc.enc")
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, sealed, 0o600); err != nil {
		t.Fatal(err)
	}

	opts.Stdin = strings.NewReader("rotate\n")
	if err := RunSecret("rotate", "installation_secret", opts); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	rawDB, _ = database.NewDatabase("sqlite", opts.DBPath)
	defer rawDB.Close()
	newSecret, _ := rawDB.EnsureAppSecret("installation_secret")
	if bytes.Equal(oldSecret, newSecret) {
		t.Fatal("installation_secret unchanged")
	}
	prev, ok := rawDB.(interface {
		FreshAppSecret(string, time.Duration) ([]byte, bool)
	}).FreshAppSecret(InstallationSecretPreviousSecret, time.Hour)
	if !ok || !bytes.Equal(prev, oldSecret) {
		t.Fatal("previous secret not parked")
	}
	after, _ := os.ReadFile(keyFile)
	newWrap, _ := pgp.WrapKey(newSecret)
	if plain, err := secretbox.Open(newWrap, after); err != nil || string(plain) != "ARMORED PRIVATE KEY" {
		t.Fatalf("pgp key not re-wrapped: %q %v", plain, err)
	}
}

func TestRotateInstallationSecret_BadKeyFileAborts(t *testing.T) {
	opts, _, _ := secretFixture(t)
	keyFile := filepath.Join(opts.ConfigDir, "security", "pgp.priv.asc.enc")
	_ = os.MkdirAll(filepath.Dir(keyFile), 0o700)
	_ = os.WriteFile(keyFile, []byte("not a sealed key at all, long enough to look like data"), 0o600)
	opts.Stdin = strings.NewReader("rotate\n")
	if err := RunSecret("rotate", "installation_secret", opts); err == nil {
		t.Fatal("expected failure for undecryptable key file")
	}
	rawDB, _ := database.NewDatabase("sqlite", opts.DBPath)
	defer rawDB.Close()
	if _, ok := rawDB.(interface {
		FreshAppSecret(string, time.Duration) ([]byte, bool)
	}).FreshAppSecret(InstallationSecretPreviousSecret, time.Hour); ok {
		t.Fatal("secret rotated despite failure")
	}
}
