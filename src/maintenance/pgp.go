package maintenance

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("pgp: load config: %w", err)
	}
	dbPath := cfg.Database.Path
	if strings.TrimSpace(dbPath) == "" {
		dbPath = opts.DBPath
	}
	db, err := database.NewDatabase(cfg.Database.Type, dbPath)
	if err != nil {
		return fmt.Errorf("pgp: open database: %w", err)
	}
	defer db.Close()

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
		return fmt.Errorf("pgp: unknown action %q (want generate|rotate|publish|export|import|delete)", action)
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

// ── on-disk paths (mirror the server package so the two never drift) ──────────

func (m *pgpManager) securityDir() string { return filepath.Join(m.configDir, "security") }
func (m *pgpManager) pubPath() string     { return filepath.Join(m.securityDir(), "pgp.pub.asc") }
func (m *pgpManager) privPath() string    { return filepath.Join(m.securityDir(), "pgp.priv.asc.enc") }
func (m *pgpManager) rotatedPath() string {
	return filepath.Join(m.securityDir(), "pgp.priv.asc.enc.old")
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
	key, err := pgp.WrapKey(m.installSecret)
	if err != nil {
		return err
	}
	sealed, err := secretbox.Seal(key, []byte(armored))
	if err != nil {
		return fmt.Errorf("pgp: seal private key: %w", err)
	}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return fmt.Errorf("pgp: create security dir: %w", err)
	}
	if err := os.WriteFile(m.privPath(), sealed, 0o600); err != nil {
		return fmt.Errorf("pgp: write private key: %w", err)
	}
	return nil
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
	fmt.Printf("Generated project security keypair\n  fingerprint: %s\n  identity:    %s\n  expires:     %s\n",
		kp.Fingerprint, pgp.Identity(m.cfg.Web.SiteTitle, m.cfg.SecurityEmail()), kp.ExpiresAt.Format(time.RFC3339))
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
	// Cross-sign the new public key with the outgoing private key (AI.md 14182).
	if oldPriv, rerr := m.readPrivateKey(m.privPath()); rerr == nil {
		if signed, serr := pgp.SignPublicKey(oldPriv, kp.PublicArmored); serr == nil {
			kp.PublicArmored = signed
		} else {
			fmt.Fprintf(os.Stderr, "warning: cross-sign rotated public key: %v\n", serr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: read outgoing private key for cross-sign: %v\n", rerr)
	}
	// Park the outgoing private key so in-flight reports stay decryptable.
	if _, statErr := os.Stat(m.privPath()); statErr == nil {
		if err := os.Rename(m.privPath(), m.rotatedPath()); err != nil {
			return fmt.Errorf("pgp rotate: park previous private key: %w", err)
		}
	}
	if err := m.installKeypair(kp, true); err != nil {
		return err
	}
	fmt.Printf("Rotated project security keypair\n  new fingerprint: %s\n  old fingerprint: %s (valid 30 more days for in-flight reports)\n",
		kp.Fingerprint, prev.Fingerprint)
	m.audit(audit.Entry{
		Event:    "security.keypair_rotated",
		Severity: audit.SeverityWarn,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: kp.Fingerprint},
		Details:  map[string]any{"previous_fingerprint": prev.Fingerprint},
	})
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
		fmt.Printf("Public key written to %s\n", path)
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
	m.audit(audit.Entry{
		Event:    "security.private_key_exported",
		Severity: audit.SeverityCritical,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: fp},
		Details:  map[string]any{"path": path},
		Reason:   reason,
	})
	fmt.Printf("Private key exported to %s (mode 0600)\n", path)
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
	fmt.Printf("Imported private key %s (identity: %s)\n", fp, identity)
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
	fmt.Fprintln(os.Stderr, "WARNING: deleting the keypair makes all in-flight encrypted security reports permanently un-decryptable.")
	if !m.confirm("type 'delete' to confirm: ", "delete") {
		return fmt.Errorf("pgp delete: aborted")
	}
	for _, p := range []string{m.pubPath(), m.privPath(), m.rotatedPath(), m.keyserversStatePath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", p, err)
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
			fmt.Fprintf(os.Stderr, "warning: mark keypair revoked: %v\n", err)
		}
	}
	m.audit(audit.Entry{
		Event:    "security.keypair_deleted",
		Severity: audit.SeverityCritical,
		Actor:    audit.Actor{IP: outboundIP()},
		Target:   &audit.Target{Type: "pgp_keypair", ID: fp},
	})
	fmt.Println("Keypair deleted; PGP publishing disabled. The Encryption: line no longer appears in security.txt.")
	return nil
}

// ── keyserver publishing ──────────────────────────────────────────────────────

// publishAll submits pubArmored to every configured keyserver with exponential
// backoff, then persists per-keyserver publish state to disk and the DB.
func (m *pgpManager) publishAll(pubArmored string) {
	keyservers := m.cfg.Web.Security.Keyservers
	if len(keyservers) == 0 {
		fmt.Println("No keyservers configured (web.security.keyservers); skipping publish.")
		return
	}
	var published []database.KeyserverPublish
	for _, ks := range keyservers {
		ks = strings.TrimSpace(ks)
		if ks == "" {
			continue
		}
		if submitKeyserver(ks, pubArmored) {
			fmt.Printf("Published public key to %s\n", ks)
			published = append(published, database.KeyserverPublish{URL: ks, PublishedAt: time.Now()})
		} else {
			fmt.Fprintf(os.Stderr, "Failed to publish to %s after retries\n", ks)
		}
	}
	if len(published) == 0 {
		return
	}
	m.writeKeyserversState(published)
	if meta, err := m.db.GetSecurityKeypair(); err == nil && meta != nil {
		meta.KeyserversPublished = mergeKeyservers(meta.KeyserversPublished, published)
		if err := m.db.UpsertSecurityKeypair(meta); err != nil {
			fmt.Fprintf(os.Stderr, "warning: record keyserver publish: %v\n", err)
		}
	}
}

// submitKeyserver POSTs to one keyserver, retrying with exponential backoff
// (1s, 2s, 4s, 8s, 16s). Returns true on success.
func submitKeyserver(keyserver, pubArmored string) bool {
	delay := time.Second
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		if err := pgp.PostKey(keyserver, pubArmored); err != nil {
			fmt.Fprintf(os.Stderr, "pgp: keyserver %s attempt %d failed: %v\n", keyserver, attempt+1, err)
			continue
		}
		return true
	}
	return false
}

// writeKeyserversState persists the keyserver publish state to disk (0o600).
func (m *pgpManager) writeKeyserversState(published []database.KeyserverPublish) {
	b, err := json.MarshalIndent(published, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.securityDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(m.keyserversStatePath(), b, 0o600)
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
			fmt.Fprintln(os.Stderr, "warning: no operator token configured; authorizing via root privileges")
			return nil
		}
		return fmt.Errorf("pgp: cannot authorize %s — operator token not configured and not running as root", reason)
	}
	fmt.Fprintf(os.Stderr, "Operator token required to %s.\nOperator token: ", reason)
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
	fmt.Fprint(os.Stderr, "Reason for this export (required): ")
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
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
