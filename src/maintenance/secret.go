// secret.go implements `--maintenance secret rotate <name>` (AI.md PART 11 →
// "Secret Rotation"). Only installation_secret and encryption_key support
// manual rotation; there is no web UI or API route for it.
package maintenance

import (
	"bufio"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

// Grace-period app_secrets keys written by a rotation (PART 11).
const (
	// EncryptionKeyPreviousSecret holds the previous at-rest encryption key
	// (hex), kept 30 days after a rotation.
	EncryptionKeyPreviousSecret = "encryption_key" + database.PreviousSecretSuffix
	// InstallationSecretPreviousSecret holds the previous installation_secret,
	// kept 7 days after a rotation.
	InstallationSecretPreviousSecret = "installation_secret" + database.PreviousSecretSuffix
)

// SecretOptions carries the resolved paths a secret rotation needs.
type SecretOptions struct {
	ConfigDir string
	DBPath    string
	LogDir    string
	// Stdin and Stdout default to the process streams; tests inject their own.
	Stdin  io.Reader
	Stdout io.Writer
}

// secretStore is the subset of the SQLite driver a rotation needs. It is
// asserted at run time so the generic database.DB interface stays unchanged.
type secretStore interface {
	database.DB
	RotateAppSecret(key string, newRaw []byte) error
	ParkAppSecret(key string, raw []byte) error
	ReencryptTokens(fn func(old string) (string, error)) (int, int, error)
	ReencryptSecurityReports(fn func(old []byte) ([]byte, error)) (int, int, error)
}

// RunSecret executes a `--maintenance secret <action> <name>` subcommand.
// Authorization (root or operator token) is enforced by the caller through
// AuthorizeSecretOp; this function adds the typed confirmation.
func RunSecret(action, name string, opts SecretOptions) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if action != "rotate" {
		return fmt.Errorf("%s", maintenanceText("secret_unknown_action", "action", action))
	}
	switch name {
	case "installation_secret", "encryption_key":
	case "cookie_signing_key", "csrf_token_secret":
		return fmt.Errorf("%s", maintenanceText("secret_invalid_name", "name", name))
	case "":
		return errors.New(maintenanceText("secret_name_required"))
	default:
		return fmt.Errorf("secret: unknown secret %q (want installation_secret or encryption_key)", name)
	}

	_, _ = fmt.Fprintln(opts.Stdout, maintenanceText("secret_rotate_confirm", "name", name))
	_, _ = fmt.Fprint(opts.Stdout, maintenanceText("secret_confirm_prompt"))
	line, err := bufio.NewReader(opts.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return errors.New(maintenanceText("secret_confirmation_missing"))
	}
	if strings.TrimSpace(line) != "rotate" {
		return errors.New(maintenanceText("secret_not_confirmed"))
	}

	cfgPath := filepath.Join(opts.ConfigDir, "server.yml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("secret rotate: load config: %w", err)
	}
	dbPath := cfg.Database.Path
	if strings.TrimSpace(dbPath) == "" {
		dbPath = opts.DBPath
	}
	rawDB, err := database.NewDatabase(cfg.Database.Type, dbPath)
	if err != nil {
		return fmt.Errorf("secret rotate: open database: %w", err)
	}
	defer func() { _ = rawDB.Close() }()
	db, ok := rawDB.(secretStore)
	if !ok {
		return errors.New("secret rotate: the configured database driver does not support secret rotation")
	}

	auth := AuthOptions{ConfigDir: opts.ConfigDir, DBPath: opts.DBPath, LogDir: opts.LogDir}
	if name == "installation_secret" {
		n, err := rotateInstallationSecret(db, opts.ConfigDir)
		if err != nil {
			return err
		}
		AuditMaintenanceEvent(auth, "security.installation_secret_rotated", map[string]any{
			"pgp_keys_rewrapped": n,
			"grace_days":         7,
		})
		_, _ = fmt.Fprintln(opts.Stdout, maintenanceText("secret_install_rotated", "count", n))
		return nil
	}

	tokens, reports, err := rotateEncryptionKey(db, cfg, cfgPath)
	if err != nil {
		return err
	}
	AuditMaintenanceEvent(auth, "security.encryption_key_rotated", map[string]any{
		"tokens_reencrypted":  tokens,
		"reports_reencrypted": reports,
		"grace_days":          30,
	})
	_, _ = fmt.Fprintln(opts.Stdout, maintenanceText("secret_encryption_rotated", "tokens", tokens, "reports", reports))
	return nil
}

// rotateInstallationSecret generates a new installation_secret, re-wraps the
// PGP private key files under it, then swaps the secret in the database (the
// old one is parked for the 7-day grace period). File writes are undone if the
// database swap fails.
func rotateInstallationSecret(db secretStore, configDir string) (int, error) {
	oldSecret, err := db.EnsureAppSecret("installation_secret")
	if err != nil {
		return 0, fmt.Errorf("secret rotate: read installation_secret: %w", err)
	}
	newSecret := make([]byte, 32)
	if _, err := crand.Read(newSecret); err != nil {
		return 0, fmt.Errorf("secret rotate: generate: %w", err)
	}
	oldWrap, err := pgp.WrapKey(oldSecret)
	if err != nil {
		return 0, err
	}
	newWrap, err := pgp.WrapKey(newSecret)
	if err != nil {
		return 0, err
	}

	secDir := filepath.Join(configDir, "security")
	files := []string{
		filepath.Join(secDir, "pgp.priv.asc.enc"),
		filepath.Join(secDir, "pgp.priv.old.asc.enc"),
	}
	type rewrap struct {
		path     string
		original []byte
		updated  []byte
	}
	var plan []rewrap
	for _, p := range files {
		sealed, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, fmt.Errorf("secret rotate: read %s: %w", p, err)
		}
		plain, err := secretbox.Open(oldWrap, sealed)
		if err != nil {
			return 0, fmt.Errorf("secret rotate: %s does not decrypt with the current installation_secret: %w", filepath.Base(p), err)
		}
		next, err := secretbox.Seal(newWrap, plain)
		if err != nil {
			return 0, err
		}
		plan = append(plan, rewrap{path: p, original: sealed, updated: next})
	}

	for i, r := range plan {
		if err := atomicWriteFile(r.path, r.updated, 0o600); err != nil {
			undoRewrap(plan[:i], func(r rewrap) (string, []byte) { return r.path, r.original })
			return 0, fmt.Errorf("secret rotate: write %s: %w", r.path, err)
		}
	}
	if err := db.RotateAppSecret("installation_secret", newSecret); err != nil {
		undoRewrap(plan, func(r rewrap) (string, []byte) { return r.path, r.original })
		return 0, fmt.Errorf("secret rotate: %w", err)
	}
	return len(plan), nil
}

// undoRewrap restores original file contents after a failed rotation step.
func undoRewrap[T any](done []T, get func(T) (string, []byte)) {
	for _, d := range done {
		p, orig := get(d)
		_ = atomicWriteFile(p, orig, 0o600)
	}
}

// rotateEncryptionKey generates a new AES-256-GCM key, parks the old one for
// the 30-day grace period, writes the new key to secrets.yml and re-encrypts
// every at-rest value (owner/linked token copies, AES-sealed security report
// bodies and the DNS-01 credentials) under it.
func rotateEncryptionKey(db secretStore, cfg *config.Config, cfgPath string) (int, int, error) {
	oldKey, err := cfg.EncryptionKey()
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: %w", err)
	}
	newKey := make([]byte, 32)
	if _, err := crand.Read(newKey); err != nil {
		return 0, 0, fmt.Errorf("secret rotate: generate: %w", err)
	}

	// Re-encrypt DNS credentials in memory first so a bad value aborts before
	// the key or any database secret is changed.
	plainDNS, err := cfg.DecryptDNSCredentials()
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: dns credentials could not be decrypted")
	}
	newDNS := make(map[string]string, len(plainDNS))
	for name, value := range plainDNS {
		sealed, sealErr := secretbox.Seal(newKey, []byte(value))
		if sealErr != nil {
			return 0, 0, fmt.Errorf("secret rotate: dns credential %s could not be encrypted", name)
		}
		newDNS[name] = "enc:v1:" + base64.StdEncoding.EncodeToString(sealed)
	}

	if err := db.ParkAppSecret(EncryptionKeyPreviousSecret, []byte(hex.EncodeToString(oldKey))); err != nil {
		return 0, 0, fmt.Errorf("secret rotate: %w", err)
	}

	secretsPath := filepath.Join(filepath.Dir(cfgPath), "secrets.yml")
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: read secrets: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return 0, 0, fmt.Errorf("secret rotate: parse secrets: %w", err)
	}
	if len(root.Content) == 0 {
		return 0, 0, fmt.Errorf("secret rotate: secrets file is empty")
	}
	if err := setYAMLSecretPath(root.Content[0], []string{"server", "security", "encryption_key"}, hex.EncodeToString(newKey)); err != nil {
		return 0, 0, fmt.Errorf("secret rotate: update encryption key: %w", err)
	}
	for name, value := range newDNS {
		if err := setYAMLSecretPath(root.Content[0], []string{"server", "ssl", "letsencrypt", "dns_credentials", name}, value); err != nil {
			return 0, 0, fmt.Errorf("secret rotate: update DNS credential %s: %w", name, err)
		}
	}
	encoded, err := yaml.Marshal(&root)
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: encode config: %w", err)
	}
	if err := atomicWriteFile(secretsPath, encoded, 0o600); err != nil {
		return 0, 0, fmt.Errorf("secret rotate: write secrets: %w", err)
	}

	tokens, tSkip, err := db.ReencryptTokens(func(old string) (string, error) {
		sealed, err := base64.StdEncoding.DecodeString(old)
		if err != nil {
			return "", err
		}
		plain, err := secretbox.Open(oldKey, sealed)
		if err != nil {
			return "", err
		}
		next, err := secretbox.Seal(newKey, plain)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(next), nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: re-encrypt tokens (values still under the previous key stay readable for 30 days): %w", err)
	}
	reports, rSkip, err := db.ReencryptSecurityReports(func(old []byte) ([]byte, error) {
		plain, err := secretbox.Open(oldKey, old)
		if err != nil {
			return nil, err
		}
		return secretbox.Seal(newKey, plain)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("secret rotate: re-encrypt reports (values still under the previous key stay readable for 30 days): %w", err)
	}
	if tSkip+rSkip > 0 {
		fmt.Fprintln(os.Stderr, maintenanceText("secret_partial_warning", "count", tSkip+rSkip))
	}
	return tokens, reports, nil
}

func setYAMLSecretPath(node *yaml.Node, path []string, value string) error {
	if len(path) == 0 || node.Kind != yaml.MappingNode {
		return fmt.Errorf("invalid YAML path")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != path[0] {
			continue
		}
		if len(path) == 1 {
			node.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return nil
		}
		return setYAMLSecretPath(node.Content[i+1], path[1:], value)
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(path) == 1 {
		child = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	} else if err := setYAMLSecretPath(child, path[1:], value); err != nil {
		return err
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}, child)
	return nil
}
