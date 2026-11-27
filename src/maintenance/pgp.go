package maintenance

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/secretbox"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/pgp"
)

// pgpKeypairValidity is the default project-key lifetime (AI.md 14181).
const pgpKeypairValidity = 2 * 365 * 24 * time.Hour

// PGPOptions carries the resolved paths and version metadata the pgp maintenance
// commands need. main.go fills these before dispatching so this package never
// depends on the server package (avoiding an import cycle).
type PGPOptions struct {
	ConfigDir string
	DataDir   string
	DBPath    string
	LogDir    string
}

// RunPGP executes a `--maintenance pgp <action>` subcommand (AI.md 14180-14188).
// It loads the config, opens the database, derives the installation_secret, and
// routes to the requested action. All keypair management runs here — there is no
// web UI or admin API route for it.
func RunPGP(action string, actionArgs []string, opts PGPOptions) error {
	cfgPath := filepath.Join(opts.ConfigDir, "server.yml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("pgp: load config: %w", err)
	}
	dbPath := cfg.Server.Database.Path
	if strings.TrimSpace(dbPath) == "" {
		dbPath = opts.DBPath
	}
	cfg.Server.Database.Path = dbPath
	db, err := OpenConfiguredDatabase(cfg, opts.DataDir)
	if err != nil {
		return fmt.Errorf("pgp: open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	installSecret, err := db.EnsureAppSecret("installation_secret")
	if err != nil {
		return fmt.Errorf("pgp: installation_secret: %w", err)
	}

	ac := cfg.Server.Logging.Audit
	auditW := audit.New(audit.Config{
		Enabled:          ac.Enabled,
		Dir:              opts.LogDir,
		Filename:         ac.Filename,
		IncludeUserAgent: ac.IncludeUserAgent,
		MaskEmails:       ac.MaskEmails,
		Events: audit.EventCategories{
			Configuration: ac.Events.Configuration,
			Security:      ac.Events.Security,
			Backup:        ac.Events.Backup,
			Server:        ac.Events.Server,
		},
	})

	m := &pgpManager{
		cfg:           cfg,
		cfgPath:       cfgPath,
		configDir:     opts.ConfigDir,
		db:            db,
		installSecret: installSecret,
		auditW:        auditW,
	}
	if err := RecoverPGPRotation(opts.ConfigDir, db); err != nil {
		return fmt.Errorf("pgp: recover interrupted key rotation: %w", err)
	}

	switch action {
	case "generate":
		return m.generate()
	case "rotate":
		return m.rotate()
	case "publish":
		return m.publish()
	case "export":
		return m.export(actionArgs)
	case "import":
		return m.importKey(actionArgs)
	case "delete":
		return m.delete()
	default:
		return fmt.Errorf("%s", maintenanceText("pgp_unknown_action", "action", action))
	}
}

// pgpManager holds the resolved dependencies for one pgp maintenance invocation.
type pgpManager struct {
	cfg           *config.Config
	cfgPath       string
	configDir     string
	db            database.DB
	installSecret []byte
	auditW        *audit.Writer
}

func (m *pgpManager) stagedPublicPath() string  { return m.pubPath() + ".new" }
func (m *pgpManager) stagedPrivatePath() string { return m.privPath() + ".new" }

func writeStagedFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (m *pgpManager) finishStagedRotation() error {
	stagedPrivate, stagedPublic := m.stagedPrivatePath(), m.stagedPublicPath()
	if _, err := os.Stat(stagedPrivate); err == nil {
		if _, activeErr := os.Stat(m.privPath()); activeErr == nil {
			if err := os.Remove(m.rotatedPath()); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(m.privPath(), m.rotatedPath()); err != nil {
				return err
			}
		} else if !os.IsNotExist(activeErr) {
			return activeErr
		}
		if err := os.Rename(stagedPrivate, m.privPath()); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(stagedPublic); err == nil {
		if err := os.Remove(m.pubPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(stagedPublic, m.pubPath()); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(m.securityDir()); err != nil {
		return err
	}
	return nil
}

// RecoverPGPRotation completes a committed rotation or removes uncommitted
// staged files before the server can accept new security reports.
func RecoverPGPRotation(configDir string, db database.DB) error {
	m := &pgpManager{configDir: configDir, db: db}
	_, pubErr := os.Stat(m.stagedPublicPath())
	_, privErr := os.Stat(m.stagedPrivatePath())
	if os.IsNotExist(pubErr) && os.IsNotExist(privErr) {
		return nil
	}
	if pubErr != nil && !os.IsNotExist(pubErr) {
		return pubErr
	}
	if privErr != nil && !os.IsNotExist(privErr) {
		return privErr
	}
	meta, err := db.GetSecurityKeypair()
	if err != nil {
		return fmt.Errorf("read PGP rotation marker: %w", err)
	}
	if meta == nil {
		for _, path := range []string{m.stagedPublicPath(), m.stagedPrivatePath()} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	if os.IsNotExist(pubErr) {
		if err := os.Remove(m.stagedPrivatePath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	staged, err := os.ReadFile(m.stagedPublicPath())
	if err != nil {
		return err
	}
	fingerprint, err := pgp.FingerprintFromPublic(string(staged))
	if err != nil {
		return fmt.Errorf("read staged PGP fingerprint: %w", err)
	}
	committed := fingerprint == meta.Fingerprint
	if !committed {
		if err := os.Remove(m.stagedPublicPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(m.stagedPrivatePath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := m.finishStagedRotation(); err != nil {
		return fmt.Errorf("finish committed PGP rotation: %w", err)
	}
	return nil
}

// ── on-disk paths (mirror the server package so the two never drift) ──────────

func (m *pgpManager) securityDir() string { return filepath.Join(m.configDir, "security") }
func (m *pgpManager) pubPath() string     { return filepath.Join(m.securityDir(), "pgp.pub.asc") }
func (m *pgpManager) privPath() string    { return filepath.Join(m.securityDir(), "pgp.priv.asc.enc") }

func (m *pgpManager) rotatedPath() string {
	return filepath.Join(m.securityDir(), "pgp.priv.old.asc.enc")
}

func (m *pgpManager) keyserversStatePath() string {
	return filepath.Join(m.securityDir(), "keyservers.state")
}

func (m *pgpManager) exportStatePath() string {
	return filepath.Join(m.securityDir(), "private_export.state")
}

// ── key material read/write ───────────────────────────────────────────────────

// writePrivateKey wraps an armored private key with the installation_secret-derived
// key and writes it to disk with 0o600 permissions.
func (m *pgpManager) writePrivateKey(armored string) error {
	sealed, err := m.sealPrivateKey(armored)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return fmt.Errorf("pgp: create security dir: %w", err)
	}
	if err := os.WriteFile(m.privPath(), sealed, 0o600); err != nil {
		return fmt.Errorf("pgp: write private key: %w", err)
	}
	return nil
}

func (m *pgpManager) sealPrivateKey(armored string) ([]byte, error) {
	key, err := pgp.WrapKey(m.installSecret)
	if err != nil {
		return nil, err
	}
	sealed, err := secretbox.Seal(key, []byte(armored))
	if err != nil {
		return nil, fmt.Errorf("pgp: seal private key: %w", err)
	}
	return sealed, nil
}

// readPrivateKey reads and unwraps the armored private key from path.
func (m *pgpManager) readPrivateKey(path string) (string, error) {
	sealed, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key, err := pgp.WrapKey(m.installSecret)
	if err != nil {
		return "", err
	}
	plain, err := secretbox.Open(key, sealed)
	if err != nil {
		return "", fmt.Errorf("pgp: open private key: %w", err)
	}
	return string(plain), nil
}

// installKeypair writes both key files and upserts the DB metadata row.
func (m *pgpManager) installKeypair(kp *pgp.Keypair, rotated bool) error {
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return fmt.Errorf("pgp: create security dir: %w", err)
	}
	if err := os.WriteFile(m.pubPath(), []byte(kp.PublicArmored), 0o644); err != nil {
		return fmt.Errorf("pgp: write public key: %w", err)
	}
	if err := m.writePrivateKey(kp.PrivateArmored); err != nil {
		return err
	}
	meta := &database.SecurityKeypair{
		Fingerprint: kp.Fingerprint,
		CreatedAt:   kp.CreatedAt,
		ExpiresAt:   kp.ExpiresAt,
	}
	if rotated {
		now := kp.CreatedAt
		meta.LastRotatedAt = &now
	}
	return m.db.UpsertSecurityKeypair(meta)
}

// ── actions ───────────────────────────────────────────────────────────────────

// generate creates the project keypair, writes both key files, records DB
// metadata, and auto-publishes to configured keyservers (AI.md 14181).
func (m *pgpManager) generate() error {
	meta, err := m.db.GetSecurityKeypair()
	if err != nil {
		return fmt.Errorf("pgp generate: read metadata: %w", err)
	}
	if _, statErr := os.Stat(m.pubPath()); statErr == nil && meta != nil && !meta.Revoked {
		return fmt.Errorf("pgp generate: a keypair already exists (%s); use 'pgp rotate' to replace it", meta.Fingerprint)
	}
	kp, err := pgp.Generate(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail(), time.Now(), pgpKeypairValidity)
	if err != nil {
		return fmt.Errorf("pgp generate: %w", err)
	}
	if err := m.installKeypair(kp, false); err != nil {
		return err
	}
	fmt.Printf("%s\n  %s\n  %s\n  %s\n",
		maintenanceText("pgp_generated"),
		maintenanceText("pgp_fingerprint", "fingerprint", kp.Fingerprint),
		maintenanceText("pgp_identity", "identity", pgp.Identity(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail())),
		maintenanceText("pgp_expires", "expires", kp.ExpiresAt.Format(time.RFC3339)))
	m.audit(audit.Entry{
		Event:    "security.keypair_generated",
		Severity: audit.SeverityInfo,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: kp.Fingerprint},
	})
	m.publishAll(kp.PublicArmored)
	return nil
}

// rotate generates a fresh keypair, cross-signs the new pubkey with the old key,
// parks the outgoing private key for the 30-day in-flight grace window, installs
// the new keypair, and republishes (AI.md 14182).
func (m *pgpManager) rotate() error {
	database.SecurityReportEncryptionMu.Lock()
	defer database.SecurityReportEncryptionMu.Unlock()

	prev, err := m.db.GetSecurityKeypair()
	if err != nil {
		return fmt.Errorf("pgp rotate: read metadata: %w", err)
	}
	if prev == nil || prev.Revoked {
		return fmt.Errorf("pgp rotate: no active keypair to rotate; run 'pgp generate' first")
	}
	kp, err := pgp.Generate(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail(), time.Now(), pgpKeypairValidity)
	if err != nil {
		return fmt.Errorf("pgp rotate: %w", err)
	}
	oldPrivate, err := m.readPrivateKey(m.privPath())
	if err != nil {
		return fmt.Errorf("pgp rotate: read outgoing private key: %w", err)
	}
	signed, err := pgp.SignPublicKey(oldPrivate, kp.PublicArmored)
	if err != nil {
		return fmt.Errorf("pgp rotate: cross-sign new public key: %w", err)
	}
	kp.PublicArmored = signed
	rotator, ok := m.db.(interface {
		RotatePGPSecurityKeypair(func(old []byte) ([]byte, error), *database.SecurityKeypair) (int, error)
	})
	if !ok {
		return fmt.Errorf("pgp rotate: database does not support atomic key rotation")
	}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return fmt.Errorf("pgp rotate: create security directory: %w", err)
	}
	stagedPublic, stagedPrivate := m.stagedPublicPath(), m.stagedPrivatePath()
	if err := writeStagedFile(stagedPublic, []byte(kp.PublicArmored), 0o644); err != nil {
		return fmt.Errorf("pgp rotate: stage public key: %w", err)
	}
	sealedPrivate, err := m.sealPrivateKey(kp.PrivateArmored)
	if err != nil {
		_ = os.Remove(stagedPublic)
		return fmt.Errorf("pgp rotate: seal staged private key: %w", err)
	}
	if err := writeStagedFile(stagedPrivate, sealedPrivate, 0o600); err != nil {
		_ = os.Remove(stagedPublic)
		_ = os.Remove(stagedPrivate)
		return fmt.Errorf("pgp rotate: stage private key: %w", err)
	}
	now := kp.CreatedAt
	meta := &database.SecurityKeypair{Fingerprint: kp.Fingerprint, CreatedAt: kp.CreatedAt, ExpiresAt: kp.ExpiresAt, LastRotatedAt: &now}
	updated, err := rotator.RotatePGPSecurityKeypair(func(ciphertext []byte) ([]byte, error) {
		plain, err := pgp.Decrypt(oldPrivate, string(ciphertext))
		if err != nil {
			return nil, err
		}
		reencrypted, err := pgp.Encrypt(kp.PublicArmored, plain)
		if err != nil {
			return nil, err
		}
		return []byte(reencrypted), nil
	}, meta)
	if err != nil {
		_ = os.Remove(stagedPrivate)
		_ = os.Remove(stagedPublic)
		return fmt.Errorf("pgp rotate: re-encrypt stored reports: %w", err)
	}
	if err := m.finishStagedRotation(); err != nil {
		return fmt.Errorf("pgp rotate: finish committed key rotation: %w; staged recovery will retry at startup", err)
	}
	fmt.Printf("%s\n  %s\n  %s\n",
		maintenanceText("pgp_rotated"),
		maintenanceText("pgp_new_fingerprint", "fingerprint", kp.Fingerprint),
		maintenanceText("pgp_old_fingerprint", "fingerprint", prev.Fingerprint))
	m.audit(audit.Entry{
		Event:    "security.keypair_rotated",
		Severity: audit.SeverityWarn,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: kp.Fingerprint},
		Details:  map[string]any{"previous_fingerprint": prev.Fingerprint},
	})
	if updated > 0 {
		fmt.Fprintf(os.Stderr, "Re-encrypted %d retained security report(s).\n", updated)
	}
	m.publishAll(kp.PublicArmored)
	return nil
}

// publish submits the current public key to every configured keyserver (AI.md 14183).
func (m *pgpManager) publish() error {
	pub, err := os.ReadFile(m.pubPath())
	if err != nil || len(pub) == 0 {
		return fmt.Errorf("pgp publish: no public key found; run 'pgp generate' first")
	}
	m.publishAll(string(pub))
	return nil
}

// export routes `pgp export public|private`.
func (m *pgpManager) export(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pgp export: specify 'public' or 'private'")
	}
	switch args[0] {
	case "public":
		return m.exportPublic(args[1:])
	case "private":
		return m.exportPrivate(args[1:])
	default:
		return fmt.Errorf("pgp export: unknown target %q (want 'public' or 'private')", args[0])
	}
}

// exportPublic writes the public key to [path], or stdout when omitted (AI.md 14184).
func (m *pgpManager) exportPublic(args []string) error {
	pub, err := os.ReadFile(m.pubPath())
	if err != nil {
		return fmt.Errorf("pgp export public: no public key found; run 'pgp generate' first")
	}
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		path := args[0]
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("pgp export public: create dir: %w", err)
		}
		if err := os.WriteFile(path, pub, 0o644); err != nil {
			return fmt.Errorf("pgp export public: write %s: %w", path, err)
		}
		fmt.Println(maintenanceText("pgp_public_written", "path", path))
		return nil
	}
	_, err = os.Stdout.Write(pub)
	return err
}

// exportPrivate performs the sensitive-operation full private-key export
// (AI.md 14185): operator authorization, a typed reason, a 1/hour rate limit,
// a mode-0600 write, and an audit.log entry with the operator IP and reason.
func (m *pgpManager) exportPrivate(args []string) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("pgp export private: destination path required")
	}
	path := args[0]
	if last, ok := m.lastPrivateExport(); ok && time.Since(last) < time.Hour {
		return fmt.Errorf("pgp export private: rate-limited; last export was %s ago (limit 1 per hour)", time.Since(last).Round(time.Second))
	}
	if err := m.authorize("export the private key"); err != nil {
		return err
	}
	reason, err := m.promptReason()
	if err != nil {
		return err
	}
	priv, err := m.readPrivateKey(m.privPath())
	if err != nil {
		return fmt.Errorf("pgp export private: read/decrypt private key: %w", err)
	}
	fp, _ := pgp.FingerprintFromPublic(priv)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("pgp export private: create dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(priv), 0o600); err != nil {
		return fmt.Errorf("pgp export private: write %s: %w", path, err)
	}
	m.recordPrivateExport(time.Now())
	operator, userErr := currentUsername()
	if userErr != nil || strings.TrimSpace(operator) == "" {
		operator = "unknown"
	}
	m.audit(audit.Entry{
		Event:    "security.private_key_exported",
		Severity: audit.SeverityCritical,
		Actor:    audit.Actor{IP: "local"},
		Target:   &audit.Target{Type: "pgp_keypair", ID: fp},
		Details:  map[string]any{"path": path, "operator_os_user": operator},
		Reason:   reason,
	})
	fmt.Println(maintenanceText("pgp_private_exported", "path", path))
	return nil
}

// importKey imports an existing armored private key from a file under the same
// sensitive-operation gate, validating its identity against the project's
// expected identity (AI.md 14186).
func (m *pgpManager) importKey(args []string) error {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("pgp import: source file path required")
	}
	file := args[0]
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("pgp import: read %s: %w", file, err)
	}
	armored := string(raw)
	identity, err := pgp.IdentityOf(armored)
	if err != nil {
		return fmt.Errorf("pgp import: invalid key: %w", err)
	}
	pub, err := pgp.PublicFromPrivate(armored)
	if err != nil {
		return fmt.Errorf("pgp import: key has no private material: %w", err)
	}
	fp, err := pgp.FingerprintFromPublic(armored)
	if err != nil {
		return fmt.Errorf("pgp import: read fingerprint: %w", err)
	}
	if err := m.authorize("import a private key"); err != nil {
		return err
	}
	expected := pgp.Identity(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail())
	if identity != expected {
		fmt.Fprintf(os.Stderr, "warning: imported key identity %q does not match expected %q\n", identity, expected)
		if !m.confirm("Import anyway? type 'yes' to override: ", "yes") {
			return fmt.Errorf("pgp import: aborted (identity mismatch)")
		}
	}
	if err := m.writePrivateKey(armored); err != nil {
		return err
	}
	if err := os.WriteFile(m.pubPath(), []byte(pub), 0o644); err != nil {
		return fmt.Errorf("pgp import: write public key: %w", err)
	}
	created, expires, ok, _ := pgp.KeyLifetime(armored)
	if created.IsZero() {
		created = time.Now()
	}
	if !ok || expires.IsZero() {
		expires = created.Add(pgpKeypairValidity)
	}
	if err := m.db.UpsertSecurityKeypair(&database.SecurityKeypair{
		Fingerprint: fp,
		CreatedAt:   created,
		ExpiresAt:   expires,
	}); err != nil {
		return fmt.Errorf("pgp import: record metadata: %w", err)
	}
	m.audit(audit.Entry{
		Event:    "security.private_key_imported",
		Severity: audit.SeverityWarn,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: fp},
		Details:  map[string]any{"identity": identity, "source": file},
	})
	fmt.Println(maintenanceText("pgp_imported", "fingerprint", fp, "identity", identity))
	return nil
}

// delete removes both keys, disables PGP publishing, and marks the keypair
// revoked in the DB (AI.md 14187). In-flight encrypted reports become
// un-decryptable, so the operator must type a confirmation.
func (m *pgpManager) delete() error {
	meta, err := m.db.GetSecurityKeypair()
	if err != nil {
		return fmt.Errorf("pgp delete: read metadata: %w", err)
	}
	if err := m.authorize("delete the keypair"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, maintenanceText("pgp_delete_warning"))
	if !m.confirm(maintenanceText("pgp_confirm_delete"), "delete") {
		return fmt.Errorf("pgp delete: aborted")
	}
	for _, p := range []string{m.pubPath(), m.privPath(), m.rotatedPath(), m.keyserversStatePath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: %v\n", maintenanceText("service_warning", "error", err.Error()), p)
		}
	}
	m.cfg.Web.Security.PublishPGPKey = "false"
	if err := config.Save(m.cfgPath, m.cfg); err != nil {
		return fmt.Errorf("pgp delete: disable publishing in config: %w", err)
	}
	fp := ""
	if meta != nil {
		fp = meta.Fingerprint
		meta.Revoked = true
		if err := m.db.UpsertSecurityKeypair(meta); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", maintenanceText("service_warning", "error", err.Error()))
		}
	}
	m.audit(audit.Entry{
		Event:    "security.keypair_deleted",
		Severity: audit.SeverityCritical,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: fp},
	})
	fmt.Println(maintenanceText("pgp_deleted"))
	return nil
}

// ── keyserver publishing ──────────────────────────────────────────────────────

// publishAll starts one attempt per configured keyserver and persists retry state
// for the running server's keyserver_retry scheduler task.
func (m *pgpManager) publishAll(pubArmored string) {
	keyservers := m.cfg.Web.Security.Keyservers
	if len(keyservers) == 0 {
		fmt.Println(maintenanceText("pgp_no_keyservers"))
		return
	}
	meta, err := m.db.GetSecurityKeypair()
	if err != nil || meta == nil || meta.Fingerprint == "" {
		fmt.Fprintln(os.Stderr, maintenanceText("pgp_publish_failed", "server", "keypair metadata unavailable"))
		return
	}
	state, err := pgp.LoadKeyserverState(m.keyserversStatePath(), meta.Fingerprint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgp: read keyserver state: %v\n", err)
		state = pgp.KeyserverStateFile{Fingerprint: meta.Fingerprint}
	}
	var published []database.KeyserverPublish
	now := time.Now().UTC()
	for _, ks := range keyservers {
		ks = strings.TrimSpace(ks)
		if ks == "" {
			continue
		}
		item := maintenanceKeyserverStateFor(&state, ks)
		if item.Status == "published" || item.Status == "final" || (item.NextAttempt != nil && item.NextAttempt.After(now)) {
			continue
		}
		if err := submitKeyserver(ks, pubArmored); err == nil {
			fmt.Println(maintenanceText("pgp_publish_success", "server", ks))
			publishedAt := now
			item.Status = "published"
			item.PublishedAt = &publishedAt
			item.NextAttempt = nil
			item.LastError = ""
			published = append(published, database.KeyserverPublish{URL: ks, PublishedAt: publishedAt})
		} else {
			fmt.Fprintln(os.Stderr, maintenanceText("pgp_publish_failed", "server", ks))
			item.RecordFailure(now, err, pgp.KeyserverErrorRetryable(err))
			if item.NextAttempt != nil {
				fmt.Fprintf(os.Stderr, "pgp: retry scheduled for %s at %s\n", ks, item.NextAttempt.UTC().Format(time.RFC3339))
			}
		}
	}
	if err := pgp.WriteKeyserverState(m.keyserversStatePath(), state); err != nil {
		fmt.Fprintf(os.Stderr, "pgp: write keyserver state: %v\n", err)
	}
	if len(published) > 0 {
		meta.KeyserversPublished = mergeKeyservers(meta.KeyserversPublished, published)
		if err := m.db.UpsertSecurityKeypair(meta); err != nil {
			fmt.Fprintln(os.Stderr, maintenanceText("service_warning", "error", err.Error()))
		}
	}
}

func submitKeyserver(keyserver, pubArmored string) error {
	return pgp.PostKey(keyserver, pubArmored)
}

func maintenanceKeyserverStateFor(state *pgp.KeyserverStateFile, keyserver string) *pgp.KeyserverState {
	for i := range state.Keyservers {
		if state.Keyservers[i].URL == keyserver {
			return &state.Keyservers[i]
		}
	}
	state.Keyservers = append(state.Keyservers, pgp.KeyserverState{URL: keyserver, Status: "pending"})
	return &state.Keyservers[len(state.Keyservers)-1]
}

// mergeKeyservers returns prior publishes with each keyserver's timestamp
// replaced by the newer publish when present (de-duplicated by URL).
func mergeKeyservers(prior, fresh []database.KeyserverPublish) []database.KeyserverPublish {
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

// ── sensitive-operation gate ──────────────────────────────────────────────────

// authorize enforces the sensitive-operation gate (AI.md 14180/14185): the
// operator proves authorization with the configured server.token, or by running
// as root when no token is set.
func (m *pgpManager) authorize(reason string) error {
	token := strings.TrimSpace(m.cfg.Server.Token)
	if token == "" {
		if isRoot() {
			fmt.Fprintln(os.Stderr, maintenanceText("pgp_root_authorize_warning"))
			return nil
		}
		return fmt.Errorf("pgp: cannot authorize %s — operator token not configured and not running as root", reason)
	}
	fmt.Fprintln(os.Stderr, maintenanceText("pgp_operator_required"))
	fmt.Fprint(os.Stderr, maintenanceText("pgp_operator_prompt"))
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("pgp: read operator token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(string(pw))), []byte(token)) != 1 {
		return fmt.Errorf("pgp: operator token mismatch")
	}
	return nil
}

// promptReason reads a required, non-empty reason string the operator must type
// (AI.md 14185: "reason text the operator must type").
func (m *pgpManager) promptReason() (string, error) {
	fmt.Fprint(os.Stderr, maintenanceText("pgp_export_reason_prompt"))
	reason, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("pgp: read reason: %w", err)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("pgp: a reason is required")
	}
	return reason, nil
}

// confirm reads a line and reports whether it exactly matches want.
func (m *pgpManager) confirm(prompt, want string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(line) == want
}

// ── export rate-limit state ───────────────────────────────────────────────────

type exportState struct {
	LastExport time.Time `json:"last_export"`
}

func (m *pgpManager) lastPrivateExport() (time.Time, bool) {
	b, err := os.ReadFile(m.exportStatePath())
	if err != nil {
		return time.Time{}, false
	}
	var st exportState
	if json.Unmarshal(b, &st) != nil || st.LastExport.IsZero() {
		return time.Time{}, false
	}
	return st.LastExport, true
}

func (m *pgpManager) recordPrivateExport(t time.Time) {
	b, err := json.MarshalIndent(exportState{LastExport: t}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(m.exportStatePath(), b, 0o600)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (m *pgpManager) audit(e audit.Entry) {
	if m.auditW == nil {
		return
	}
	m.auditW.Log(e)
}

// isRoot reports whether the process runs with root privileges. On platforms
// without a uid concept (Windows) Geteuid returns -1, so this is false there.
func isRoot() bool {
	return os.Geteuid() == 0
}

// outboundIP returns the local IP the host would use for outbound traffic. It
// sends no packets (a UDP "connection" only resolves a local address) and
// returns "" when no route is available.
func outboundIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
