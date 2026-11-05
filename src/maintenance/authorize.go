// authorize.go implements the sensitive-operation authorization gate for the
// --maintenance setup, restore, and mode subcommands (AI.md "Sensitive
// Operations"): these commands can damage or take over the server, so the
// caller must prove authority (first-run, root, or service user + operator
// password) before the operation runs.
package maintenance

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	// Registers the pure-Go "sqlite" driver for the read-only emptiness check.
	_ "modernc.org/sqlite"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/config"
)

// AuthOptions carries the inputs needed to authorize a sensitive maintenance
// operation. All paths come from the caller so the package stays path-agnostic.
type AuthOptions struct {
	// ConfigDir holds server.yml; the operator token (server.token) is read from it.
	ConfigDir string
	// DBPath is the SQLite database used for the first-run emptiness check.
	// Overridden by database.path from server.yml when that is set.
	DBPath string
	// LogDir receives audit.log entries for completed sensitive operations.
	LogDir string
	// ServiceUser is the dedicated system account name (the project name).
	ServiceUser string
}

// authorizer resolves the state needed by the authorization flows. The
// function fields are seams so unit tests can inject deterministic values.
type authorizer struct {
	opts   AuthOptions
	cfg    *config.Config
	stdin  io.Reader
	stderr io.Writer

	isRootFn      func() bool
	currentUserFn func() (string, error)
	dbEmptyFn     func(string) (bool, error)
}

// newAuthorizer loads server.yml (best-effort — config.Load returns usable
// defaults on error, matching the "backup test" pattern in main.go) and wires
// the default OS-backed detection functions.
func newAuthorizer(opts AuthOptions) *authorizer {
	cfg, _ := config.Load(filepath.Join(opts.ConfigDir, "server.yml"))
	return &authorizer{
		opts:          opts,
		cfg:           cfg,
		stdin:         os.Stdin,
		stderr:        os.Stderr,
		isRootFn:      isRoot,
		currentUserFn: currentUsername,
		dbEmptyFn:     databaseEmpty,
	}
}

// AuthorizeSetup enforces the setup gate (AI.md sensitive-operation flow):
// allowed on first-run (empty database) or as root (with confirmation);
// everyone else is rejected with reconfiguration guidance.
func AuthorizeSetup(opts AuthOptions) error {
	return newAuthorizer(opts).authorizeSetup()
}

// AuthorizeRestore enforces the restore gate (AI.md sensitive-operation flow):
// allowed on first-run (empty database), as root (with confirmation), or as
// the service user with the operator token; everyone else is rejected.
func AuthorizeRestore(opts AuthOptions) error {
	return newAuthorizer(opts).authorizeRestore()
}

// AuthorizeMode enforces the mode-change gate (AI.md sensitive-operation
// flow): allowed as root (with a security warning) or as the service user
// with the operator token; everyone else is rejected.
func AuthorizeMode(opts AuthOptions) error {
	return newAuthorizer(opts).authorizeMode()
}

// AuthorizeDataOp enforces the gate for --maintenance data export/delete
// (AI.md Compliance): allowed as root or as the service user with the
// operator token; everyone else is rejected. No first-run bypass — these
// commands operate on existing resources, not initial setup.
func AuthorizeDataOp(opts AuthOptions) error {
	return newAuthorizer(opts).authorizeDataOp()
}

func (a *authorizer) authorizeSetup() error {
	// First-run: an empty database means there is nothing to protect.
	if empty, err := a.dbEmptyFn(a.dbPath()); err == nil && empty {
		return nil
	}
	if a.isRootFn() {
		if a.confirm("This will reset the server configuration to defaults. Continue? [y/N]: ") {
			return nil
		}
		return errors.New("setup cancelled")
	}
	return fmt.Errorf("setup already completed. To reconfigure:\n"+
		"  1. Edit server.yml directly and restart the server\n"+
		"  2. Run as root: sudo %s --maintenance setup", projectName)
}

func (a *authorizer) authorizeRestore() error {
	// First-run/fresh install: nothing to protect.
	if empty, err := a.dbEmptyFn(a.dbPath()); err == nil && empty {
		return nil
	}
	if a.isRootFn() {
		if a.confirm("This will OVERWRITE all data. Continue? [y/N]: ") {
			return nil
		}
		return errors.New("restore cancelled")
	}
	if a.isServiceUser() {
		return a.requireOperatorToken("This will OVERWRITE all data. Enter operator token to confirm.")
	}
	return errors.New("restore requires administrator authorization: run as root or provide the operator token")
}

func (a *authorizer) authorizeMode() error {
	if a.isRootFn() {
		fmt.Fprintln(a.stderr, "warning: changing the server mode alters security-relevant behavior (debug endpoints, error detail)")
		return nil
	}
	if a.isServiceUser() {
		return a.requireOperatorToken("Changing the server mode requires operator authorization.")
	}
	return errors.New("mode change requires administrator authorization: run as root or provide the operator token")
}

// AuthorizeSecretOp enforces the secret-rotation gate: root, or the service
// user with the operator token (AI.md PART 11 → Secret Rotation).
func AuthorizeSecretOp(opts AuthOptions) error {
	a := newAuthorizer(opts)
	if a.isRootFn() {
		return nil
	}
	if a.isServiceUser() {
		return a.requireOperatorToken("Rotating a server secret requires operator authorization.")
	}
	return errors.New("secret rotation requires administrator authorization: run as root or provide the operator token")
}

func (a *authorizer) authorizeDataOp() error {
	if a.isRootFn() {
		return nil
	}
	if a.isServiceUser() {
		return a.requireOperatorToken("This will access/modify data-subject records. Enter operator token to confirm.")
	}
	return errors.New("data operations require administrator authorization: run as root or provide the operator token")
}

// dbPath resolves the database path: database.path from server.yml wins,
// falling back to the caller-supplied default (mirrors RunPGP).
func (a *authorizer) dbPath() string {
	if p := strings.TrimSpace(a.cfg.Database.Path); p != "" {
		return p
	}
	return a.opts.DBPath
}

// isServiceUser reports whether the current OS user is the dedicated service
// account. Windows usernames carry a DOMAIN\ prefix, which is stripped.
func (a *authorizer) isServiceUser() bool {
	name, err := a.currentUserFn()
	if err != nil {
		return false
	}
	if i := strings.LastIndexByte(name, '\\'); i >= 0 {
		name = name[i+1:]
	}
	return name == a.opts.ServiceUser
}

// requireOperatorToken prompts for the operator token and validates it
// against server.token by comparing SHA-256 digests with a constant-time
// compare (PART 11), matching the server's requireOperatorToken middleware.
func (a *authorizer) requireOperatorToken(prompt string) error {
	token := strings.TrimSpace(a.cfg.Server.Token)
	if token == "" {
		return errors.New("operator token not configured — run as root instead")
	}
	fmt.Fprintf(a.stderr, "%s\nOperator token: ", prompt)
	entered, err := a.readSecret()
	if err != nil {
		return fmt.Errorf("read operator token: %w", err)
	}
	want := sha256.Sum256([]byte(token))
	got := sha256.Sum256([]byte(strings.TrimSpace(entered)))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		return errors.New("operator token mismatch — authorization rejected")
	}
	return nil
}

// readSecret reads the operator token: without echo on a real terminal,
// as a plain line otherwise (pipes, tests).
func (a *authorizer) readSecret() (string, error) {
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		pw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.stderr)
		if err != nil {
			return "", err
		}
		return string(pw), nil
	}
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// confirm prints prompt and reads one line; any config truthy value ("y",
// "yes", "1", ...) confirms.
func (a *authorizer) confirm(prompt string) bool {
	fmt.Fprint(a.stderr, prompt)
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return false
	}
	return config.IsTruthy(strings.TrimSpace(line))
}

// currentUsername returns the current OS username.
func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	return u.Username, nil
}

// databaseEmpty reports whether the server database holds no pastes and no
// API tokens (first-run). A missing file or missing tables count as empty;
// the database is opened read-only so the check never mutates state.
func databaseEmpty(dbPath string) (bool, error) {
	if strings.TrimSpace(dbPath) == "" {
		return true, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("stat database: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return false, fmt.Errorf("open database read-only: %w", err)
	}
	defer db.Close()

	var pastes int64
	if err := db.QueryRow(`SELECT COUNT(id) FROM pastes`).Scan(&pastes); err != nil {
		// A missing table means a fresh, never-initialized database.
		if strings.Contains(err.Error(), "no such table") {
			return true, nil
		}
		return false, fmt.Errorf("count pastes: %w", err)
	}
	var tokens int64
	if err := db.QueryRow(`SELECT COUNT(id) FROM api_tokens`).Scan(&tokens); err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			return false, fmt.Errorf("count api tokens: %w", err)
		}
	}
	return pastes == 0 && tokens == 0, nil
}

// AuditMaintenanceEvent records one audit.log entry for a completed sensitive
// maintenance operation (e.g. backup.restored, config.updated). It builds a
// writer from server.yml the same way RunPGP does; a disabled audit config
// silently drops the entry.
func AuditMaintenanceEvent(opts AuthOptions, event string, details map[string]any) {
	cfg, _ := config.Load(filepath.Join(opts.ConfigDir, "server.yml"))
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
	auditW.Log(audit.Entry{
		Event:    event,
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  details,
	})
}
