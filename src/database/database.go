package database

import (
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/model"
	_ "modernc.org/sqlite"
)

// APITokenRecord represents a stored resource-owner token row (PART 11).
// The raw token is never stored — only the SHA-256 hex digest.
type APITokenRecord struct {
	ID            int64
	OwnerID       string
	Kind          string
	Name          string
	TokenPrefix   string
	TokenSuffix   string
	TokenEnc      string
	ResourceType  string
	ResourceID    string
	CreatedAt     time.Time
	ExpiresAt     *time.Time
	LastUsedAt    *time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

// TaskState holds the persistent state for a single scheduler task.
type TaskState struct {
	TaskID   string    `json:"task_id"`
	TaskName string    `json:"task_name"`
	Schedule string    `json:"schedule"`
	LastRun  time.Time `json:"last_run"`
	// "pending" | "success" | "failed" | "skipped"
	LastStatus string    `json:"last_status"`
	LastError  string    `json:"last_error"`
	NextRun    time.Time `json:"next_run"`
	RunCount   int64     `json:"run_count"`
	FailCount  int64     `json:"fail_count"`
	Enabled    bool      `json:"enabled"`
}

// TaskHistory records a single execution event.
type TaskHistory struct {
	TaskID     string    `json:"task_id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	ErrorMsg   string    `json:"error_msg"`
	DurationMS int64     `json:"duration_ms"`
}

// DB is the database interface for paste and scheduler operations.
type DB interface {
	Close() error
	Type() string
	Ping() error

	// Paste operations
	CreatePaste(paste *model.Paste) error
	GetPasteByID(id string) (*model.Paste, error)
	GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error)
	SearchPublicPastes(page, limit int, search, sortBy, order string) ([]model.PasteListItem, int, error)
	IncrementViewsAndCheckBurn(id string) (views int, burned bool, err error)
	DeletePaste(id string) error
	DeletePasteByToken(id, deleteTokenHash string) error
	DeleteExpiredPastes() (int64, error)
	DeleteBurnedPastes() (int64, error)

	// Scheduler operations (PART 18)
	UpsertSchedulerTask(t *TaskState) error
	GetSchedulerTask(taskID string) (*TaskState, error)
	ListSchedulerTasks() ([]*TaskState, error)
	UpdateTaskRun(taskID string, lastRun time.Time, status, lastError string, runCount, failCount int64, nextRun time.Time) error
	SetTaskEnabled(taskID string, enabled bool) error
	RecordTaskHistory(h *TaskHistory) error
	ListTaskHistory(taskID string, limit int) ([]*TaskHistory, error)

	// API token operations (PART 11).
	// CreateAPIToken stores a new resource-owner token. tokenHash is the
	// SHA-256 hex digest; tokenPrefix is the first 12 chars of the raw token.
	CreateAPIToken(tokenHash, tokenPrefix, resourceType, resourceID string, expiresAt *time.Time) error
	// VerifyAPIToken checks that tokenHash is active and belongs to the given
	// resource, then updates last_used_at. Returns an error if invalid/revoked.
	VerifyAPIToken(tokenHash [32]byte, resourceType, resourceID string) error
	// ValidateAPIToken checks that tokenHash is active for the given resource_type
	// (any resource_id). Used when reusing a token on paste creation.
	ValidateAPIToken(tokenHash [32]byte, resourceType string) error
	// RevokeAPIToken marks the token with the given prefix as revoked.
	RevokeAPIToken(prefix, reason string) error
	// ListAPITokens returns all non-revoked token records (token_hash omitted).
	ListAPITokens() ([]*APITokenRecord, error)
	// GetAPITokenByPrefix returns the non-revoked token record matching prefix,
	// used to resolve the resource a --maintenance data/token command targets.
	GetAPITokenByPrefix(prefix string) (*APITokenRecord, error)
	// DeleteExpiredAPITokens removes rows that are expired or revoked.
	DeleteExpiredAPITokens() (int64, error)
	// FindResourceIDByToken reverse-looks-up the resource a token grants
	// access to (see implementation doc comment for details).
	FindResourceIDByToken(tokenHash [32]byte, resourceType string) (string, error)
	// App secrets — server-wide HMAC / signing keys stored in the DB (PART 11).
	// EnsureAppSecret returns the raw bytes for key, generating 32 random bytes on
	// first call. The value is stored base64-encoded and never returned in API responses.
	EnsureAppSecret(key string) ([]byte, error)

	// CountPastes returns the total number of pastes stored (for healthz stats).
	CountPastes() (int64, error)

	// Coordinated-disclosure security reports (PART 11). Report bodies are
	// encrypted at rest; only sanitized triage metadata is stored in the clear.
	CreateSecurityReport(r *SecurityReport) error
	GetSecurityReport(trackingID string) (*SecurityReport, error)
	UpdateSecurityReportStatus(trackingID, status, maintainerComment string) error
	ListDisclosedSecurityReports() ([]*SecurityReport, error)
	MarkSecurityReportTokenUsed(trackingID string, at time.Time) error

	// Project PGP keypair metadata (PART 11 → GPG Keypair Management). The keys
	// themselves live on disk; only fingerprint/expiry/publish state is stored.
	GetSecurityKeypair() (*SecurityKeypair, error)
	UpsertSecurityKeypair(kp *SecurityKeypair) error
}

// OwnerDB provides the owner and linked-token lifecycle without expanding the
// general database contract used by services that do not manage credentials.
type OwnerDB interface {
	ListAllAPITokens() ([]*APITokenRecord, error)
	PurgeIdleOwners(days int) (int64, error)
	CreatePasteWithPrimaryToken(paste *model.Paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error
	CreateOwnerWithPrimaryToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error
	GetOwnerTokenByHash(tokenHash [32]byte) (*APITokenRecord, error)
	GetOwnerTokenByID(ownerID string, id int64) (*APITokenRecord, error)
	CreateLinkedToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, name, tokenEnc string, expiresAt *time.Time, maxActive int) (int64, error)
	ListOwnerTokens(ownerID string) ([]*APITokenRecord, error)
	RotateOwnerToken(ownerID string, id int64, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error
	UpdateOwnerToken(ownerID string, id int64, name string, expiresAt *time.Time, maxActive int) error
	RevokeOwnerToken(ownerID string, id int64) error
}

// Query timeout constants per PART 10.
const (
	// dbReadTimeout is for simple SELECT queries.
	dbReadTimeout = 5 * time.Second
	// dbWriteTimeout is for INSERT, UPDATE, DELETE.
	dbWriteTimeout = 10 * time.Second
	// dbTxTimeout bounds a whole multi-statement transaction, BeginTx through
	// Commit/Rollback, rather than a single statement.
	dbTxTimeout = 30 * time.Second
	// dbComplexTimeout is for multi-step or JOIN queries.
	dbComplexTimeout = 15 * time.Second
	// dbBulkTimeout is for bulk delete operations.
	dbBulkTimeout = 60 * time.Second
	// dbMigrationTimeout is for schema creation and idempotent ALTER TABLE statements at startup.
	dbMigrationTimeout = 5 * time.Minute
)

// dbCtx returns a context with the given timeout rooted at Background.
func dbCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// SQLiteDB implements DB for SQLite.
type SQLiteDB struct {
	db *sql.DB
}

// PoolConfig mirrors server.database.pool from server.yml (AI.md PART 10). It
// lives here rather than being imported from the config package so the database
// layer stays free of a dependency on configuration parsing.
type PoolConfig struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// DefaultPool returns the "Development" row of AI.md PART 10's pool sizing
// table, which is what SQLite (single writer plus readers) wants.
func DefaultPool() PoolConfig {
	return PoolConfig{
		MaxOpen:     5,
		MaxIdle:     2,
		MaxLifetime: 5 * time.Minute,
		MaxIdleTime: time.Minute,
	}
}

// NewDatabase creates a database connection using the default pool settings.
// Currently SQLite only.
func NewDatabase(dbType, path string) (DB, error) {
	return NewDatabaseWithPool(dbType, path, DefaultPool())
}

// NewDatabaseWithPool creates a database connection with explicit pool sizing,
// as configured under server.database.pool, without a remote auth token.
func NewDatabaseWithPool(dbType, path string, pool PoolConfig) (DB, error) {
	return NewDatabaseWithToken(dbType, path, "", pool)
}

// NewDatabaseWithToken creates a database connection, passing token through to
// drivers that authenticate against a remote server. The sqlite driver ignores
// it; the libsql driver uses it when the URL carries no authToken parameter.
// Driver aliases follow AI.md PART 3: sqlite/sqlite2/sqlite3 and libsql/turso.
func NewDatabaseWithToken(dbType, path, token string, pool PoolConfig) (DB, error) {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "libsql", "turso":
		return newLibSQLDB(path, token, pool)
	case "sqlite", "sqlite2", "sqlite3", "":
		return newSQLiteDB(path, pool)
	default:
		return newSQLiteDB(path, pool)
	}
}

func newSQLiteDB(path string, pool PoolConfig) (*SQLiteDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	// busy_timeout (via _pragma, modernc.org/sqlite's DSN param for PRAGMA
	// statements) makes concurrent writers block and retry, up to the given
	// milliseconds, on SQLITE_BUSY instead of failing immediately — required
	// for correctness whenever two writers can legitimately race for the same
	// row (e.g. concurrent burn-after-read view increments).
	db, err := sql.Open("sqlite", path+"?_foreign_keys=on&_journal_mode=WAL&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Connection pool settings (PART 10), from server.database.pool.
	// SQLite WAL mode supports 1 writer + concurrent readers, so the default
	// 5 open conns is safe; libsql/remote deployments raise it in server.yml.
	if pool.MaxOpen <= 0 {
		pool = DefaultPool()
	}
	db.SetMaxOpenConns(pool.MaxOpen)
	db.SetMaxIdleConns(pool.MaxIdle)
	db.SetConnMaxLifetime(pool.MaxLifetime)
	db.SetConnMaxIdleTime(pool.MaxIdleTime)

	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteDB{db: db}, nil
}

func ensureSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pastes (
			id               TEXT PRIMARY KEY,
			owner_id         TEXT NOT NULL DEFAULT '',
			title            TEXT NOT NULL DEFAULT 'Untitled',
			content          TEXT NOT NULL,
			language         TEXT NOT NULL DEFAULT 'text',
			content_type     TEXT NOT NULL DEFAULT '',
			visibility       INTEGER NOT NULL DEFAULT 0,
			expires_at       DATETIME,
			burn_after       INTEGER NOT NULL DEFAULT 0,
			delete_token_hash TEXT NOT NULL DEFAULT '',
			views            INTEGER NOT NULL DEFAULT 0,
			created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pastes_visibility   ON pastes(visibility)`,
		`CREATE INDEX IF NOT EXISTS idx_pastes_created_at   ON pastes(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pastes_expires_at   ON pastes(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pastes_burn_after   ON pastes(burn_after)`,

		// Scheduler persistent state (PART 18).
		`CREATE TABLE IF NOT EXISTS scheduler_tasks (
			task_id      TEXT PRIMARY KEY,
			task_name    TEXT NOT NULL,
			schedule     TEXT NOT NULL,
			last_run     DATETIME,
			last_status  TEXT NOT NULL DEFAULT 'pending',
			last_error   TEXT NOT NULL DEFAULT '',
			next_run     DATETIME,
			run_count    INTEGER NOT NULL DEFAULT 0,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			enabled      INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS scheduler_history (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id      TEXT NOT NULL,
			started_at   DATETIME NOT NULL,
			finished_at  DATETIME,
			status       TEXT NOT NULL,
			error_msg    TEXT NOT NULL DEFAULT '',
			duration_ms  INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sched_history_task ON scheduler_history(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sched_history_started ON scheduler_history(started_at)`,

		// App-level server secrets (PART 11): installation_secret, cookie_signing_key,
		// csrf_token_secret. 32 bytes each, stored base64-encoded. Never returned
		// in any API response; always included in backups.
		`CREATE TABLE IF NOT EXISTS app_secrets (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Config key-value storage (PART 11 / PART 5): mirrors YAML structure as flat keys.
		`CREATE TABLE IF NOT EXISTS config (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			type       TEXT NOT NULL DEFAULT 'string',
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_config_key ON config(key)`,

		// Config metadata: single-row version counter, incremented by trigger on change.
		`CREATE TABLE IF NOT EXISTS config_meta (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			version    INTEGER NOT NULL DEFAULT 1,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)`,
		`INSERT OR IGNORE INTO config_meta (id, version) VALUES (1, 1)`,
		// SQLite requires one event per trigger — one each for INSERT, UPDATE, DELETE.
		`CREATE TRIGGER IF NOT EXISTS config_version_bump_ins
			AFTER INSERT ON config
			BEGIN
				UPDATE config_meta SET version = version + 1, updated_at = strftime('%s','now') WHERE id = 1;
			END`,
		`CREATE TRIGGER IF NOT EXISTS config_version_bump_upd
			AFTER UPDATE ON config
			BEGIN
				UPDATE config_meta SET version = version + 1, updated_at = strftime('%s','now') WHERE id = 1;
			END`,
		`CREATE TRIGGER IF NOT EXISTS config_version_bump_del
			AFTER DELETE ON config
			BEGIN
				UPDATE config_meta SET version = version + 1, updated_at = strftime('%s','now') WHERE id = 1;
			END`,

		// Rate limiting: sliding-window counters per IP/key (PART 11).
		`CREATE TABLE IF NOT EXISTS rate_limits (
			key          TEXT PRIMARY KEY,
			count        INTEGER NOT NULL DEFAULT 1,
			window_start INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			updated_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rate_limits_window ON rate_limits(window_start)`,

		// Audit log: config changes, security events, request log (PART 11).
		`CREATE TABLE IF NOT EXISTS audit_log (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp   INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			level       TEXT NOT NULL DEFAULT 'info',
			category    TEXT NOT NULL,
			action      TEXT NOT NULL,
			actor_ip    TEXT,
			target_type TEXT,
			target_id   TEXT,
			details     TEXT,
			success     INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_category  ON audit_log(category)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_actor_ip  ON audit_log(actor_ip)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_target    ON audit_log(target_type, target_id)`,

		// Backup history and metadata (PART 21).
		`CREATE TABLE IF NOT EXISTS backups (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			filename   TEXT NOT NULL UNIQUE,
			filepath   TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			type       TEXT NOT NULL DEFAULT 'auto',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			checksum   TEXT,
			notes      TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_backups_created ON backups(created_at)`,

		// API tokens: server-generated resource-owner tokens (PART 11).
		// The server.token (server.yml) is NOT stored here — validated directly from config.
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			owner_id      TEXT NOT NULL DEFAULT '',
			kind          TEXT NOT NULL DEFAULT 'primary',
			name          TEXT,
			token_hash    TEXT NOT NULL UNIQUE,
			token_prefix  TEXT NOT NULL,
			token_suffix  TEXT NOT NULL DEFAULT '',
			token_enc     TEXT NOT NULL DEFAULT '',
			resource_type TEXT NOT NULL,
			resource_id   TEXT NOT NULL,
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			expires_at    INTEGER,
			last_used_at  INTEGER,
			revoked_at    INTEGER,
			revoked_reason TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_tokens_hash     ON api_tokens(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_api_tokens_prefix   ON api_tokens(token_prefix)`,
		`CREATE INDEX IF NOT EXISTS idx_api_tokens_resource ON api_tokens(resource_type, resource_id)`,
		`CREATE INDEX IF NOT EXISTS idx_api_tokens_active   ON api_tokens(revoked_at) WHERE revoked_at IS NULL`,
		`CREATE TABLE IF NOT EXISTS owners (
			id           TEXT PRIMARY KEY,
			created_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			last_seen_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)`,

		// Coordinated-disclosure security reports (PART 11). The vulnerability
		// content lives only in encrypted_body (PGP or AES-256-GCM); the other
		// columns hold sanitized triage metadata with no PII or report detail.
		`CREATE TABLE IF NOT EXISTS security_reports (
			tracking_id        TEXT PRIMARY KEY,
			status             TEXT NOT NULL DEFAULT 'Received',
			severity           TEXT NOT NULL DEFAULT '',
			component          TEXT NOT NULL DEFAULT '',
			encrypted_body     BLOB NOT NULL,
			enc_method         TEXT NOT NULL DEFAULT 'aes-256-gcm',
			credit_preference  TEXT NOT NULL DEFAULT 'no',
			credit_name        TEXT NOT NULL DEFAULT '',
			token_hash         TEXT NOT NULL DEFAULT '',
			maintainer_comment TEXT NOT NULL DEFAULT '',
			disclosure_days    INTEGER NOT NULL DEFAULT 90,
			created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			disclosed_at       DATETIME,
			token_last_used    DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_secreports_status  ON security_reports(status)`,
		`CREATE INDEX IF NOT EXISTS idx_secreports_created ON security_reports(created_at)`,

		// Project PGP keypair metadata (PART 11 → GPG Keypair Management). Single
		// row (id = 1); the key material lives on disk, never in the DB. The
		// keyservers_published column holds a JSON array of {url, published_at}.
		`CREATE TABLE IF NOT EXISTS security_keypair (
			id                   INTEGER PRIMARY KEY CHECK (id = 1),
			fingerprint          TEXT NOT NULL DEFAULT '',
			created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at           DATETIME,
			last_rotated_at      DATETIME,
			keyservers_published TEXT NOT NULL DEFAULT '',
			revoked              INTEGER NOT NULL DEFAULT 0
		)`,
	}

	// Schema updates — idempotent; ignore "already exists" errors.
	updates := []string{
		`ALTER TABLE pastes ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pastes ADD COLUMN burn_after INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pastes ADD COLUMN delete_token_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pastes ADD COLUMN content_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pastes ADD COLUMN is_link INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE security_reports ADD COLUMN token_last_used DATETIME`,
		`ALTER TABLE api_tokens ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'primary'`,
		`ALTER TABLE api_tokens ADD COLUMN name TEXT`,
		`ALTER TABLE api_tokens ADD COLUMN token_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_tokens ADD COLUMN token_suffix TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE api_tokens ADD COLUMN rotated_at INTEGER`,
	}

	ctx, cancel := dbCtx(dbMigrationTimeout)
	defer cancel()

	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}

	for _, s := range updates {
		if _, err := db.ExecContext(ctx, s); err != nil {
			if !isAlreadyExists(err) {
				return fmt.Errorf("schema update: %w", err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_api_tokens_owner ON api_tokens(owner_id)`); err != nil {
		return fmt.Errorf("schema owner index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_owner_name ON api_tokens(owner_id, name) WHERE kind = 'linked' AND name IS NOT NULL`); err != nil {
		return fmt.Errorf("schema owner token name index: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_owner_primary ON api_tokens(owner_id) WHERE kind = 'primary' AND owner_id <> ''`); err != nil {
		return fmt.Errorf("schema owner primary index: %w", err)
	}
	if err := migrateLegacyOwners(ctx, db); err != nil {
		return fmt.Errorf("schema owner migration: %w", err)
	}

	return nil
}

// migrateLegacyOwners upgrades resource-bound tokens to stable anonymous owners.
// Each legacy token becomes the primary token for its former resource; existing
// paste rows retain access through the newly assigned owner_id.
func migrateLegacyOwners(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, resource_type, resource_id FROM api_tokens WHERE owner_id = ''`)
	if err != nil {
		return err
	}
	type legacyToken struct {
		id           int64
		resourceType string
		resourceID   string
	}
	var records []legacyToken
	for rows.Next() {
		var rec legacyToken
		if err := rows.Scan(&rec.id, &rec.resourceType, &rec.resourceID); err != nil {
			rows.Close()
			return err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, rec := range records {
		ownerID := fmt.Sprintf("legacy-%d", rec.id)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO owners(id) VALUES (?)`, ownerID); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE api_tokens SET owner_id = ?, kind = 'primary', resource_type = 'owner', resource_id = ? WHERE id = ?`, ownerID, ownerID, rec.id)
		}
		if err == nil && rec.resourceType == "paste" {
			_, err = tx.ExecContext(ctx, `UPDATE pastes SET owner_id = ? WHERE id = ? AND owner_id = ''`, ownerID, rec.resourceID)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "Duplicate column name")
}

// Close closes the database connection.
func (s *SQLiteDB) Close() error { return s.db.Close() }

// Type returns the database type name.
func (s *SQLiteDB) Type() string { return "sqlite" }

// Ping verifies the database connection is alive.
func (s *SQLiteDB) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

// CreatePaste inserts a new paste.
func (s *SQLiteDB) CreatePaste(p *model.Paste) error {
	preparePaste(p)
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pastes
			(id, owner_id, title, content, language, content_type, visibility, is_link, expires_at, burn_after, delete_token_hash, views, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.OwnerID, p.Title, p.Content, p.Language, p.ContentType, p.Visibility, p.IsLink, p.ExpiresAt,
		p.BurnAfter, p.DeleteTokenHash, p.Views, p.CreatedAt, p.UpdatedAt,
	)
	return err
}

func preparePaste(p *model.Paste) {
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now
	if p.Title == "" {
		p.Title = "Untitled"
	}
	if p.Language == "" && !p.IsLink {
		p.Language = "text"
	}

}

// CreatePasteWithPrimaryToken atomically creates a paste, its anonymous owner,
// and the owner's primary token row.
func (s *SQLiteDB) CreatePasteWithPrimaryToken(p *model.Paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	preparePaste(p)
	ctx, cancel := dbCtx(dbTxTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO pastes (id, owner_id, title, content, language, content_type, visibility, is_link, expires_at, burn_after, delete_token_hash, views, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, p.OwnerID, p.Title, p.Content, p.Language, p.ContentType, p.Visibility, p.IsLink, p.ExpiresAt, p.BurnAfter, p.DeleteTokenHash, p.Views, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owners(id) VALUES (?)`, p.OwnerID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_tokens(owner_id, kind, token_hash, token_prefix, token_suffix, token_enc, resource_type, resource_id) VALUES (?, 'primary', ?, ?, ?, ?, 'owner', ?)`, p.OwnerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc, p.OwnerID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GetPasteByID retrieves a paste by ID without touching views.
func (s *SQLiteDB) GetPasteByID(id string) (*model.Paste, error) {
	p := &model.Paste{}
	var expiresAt sql.NullTime

	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	err := s.db.QueryRowContext(ctx,
		`SELECT id, owner_id, title, content, language, content_type, visibility, is_link, expires_at, burn_after, delete_token_hash, views, created_at, updated_at
		 FROM pastes WHERE id = ?`, id,
	).Scan(&p.ID, &p.OwnerID, &p.Title, &p.Content, &p.Language, &p.ContentType, &p.Visibility, &p.IsLink,
		&expiresAt, &p.BurnAfter, &p.DeleteTokenHash, &p.Views, &p.CreatedAt, &p.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		p.ExpiresAt = &expiresAt.Time
	}
	return p, nil
}

// GetPublicPastes returns paginated public (visibility=0) non-expired pastes.
func (s *SQLiteDB) GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error) {
	if page < 1 {
		page = 1
	}
	// Defense-in-depth: callers already clamp, but guard against a non-positive
	// limit reaching the query so LIMIT can never be zero or negative.
	if limit < 1 {
		limit = 1
	}
	offset := (page - 1) * limit
	now := time.Now()

	ctx, cancel := dbCtx(dbComplexTimeout)
	defer cancel()

	var total int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pastes WHERE visibility = 0 AND (expires_at IS NULL OR expires_at > ?)`,
		now,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, language, views, expires_at, burn_after, created_at
		 FROM pastes
		 WHERE visibility = 0 AND (expires_at IS NULL OR expires_at > ?)
		 ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		now, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []model.PasteListItem
	for rows.Next() {
		var item model.PasteListItem
		var expiresAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.Title, &item.Language, &item.Views, &expiresAt, &item.BurnAfter, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// publicPasteSortColumns allow-lists the ORDER BY column for
// SearchPublicPastes — column names can never be parameterized, so any
// caller-supplied sort key must be resolved through this map rather than
// interpolated directly into SQL.
var publicPasteSortColumns = map[string]string{
	"date":  "created_at",
	"name":  "title",
	"title": "title",
	"views": "views",
	"id":    "id",
}

// SearchPublicPastes returns paginated public (visibility=0) non-expired
// pastes, optionally filtered by a search term matched against id, title,
// and content, and sorted by an allow-listed column. sortBy and order fall
// back to "date"/"desc" (the same ordering GetPublicPastes always used) for
// any empty or unrecognized value — never an error, since these come
// straight off a query string (TODO.md: search + sort + full listing).
func (s *SQLiteDB) SearchPublicPastes(page, limit int, search, sortBy, order string) ([]model.PasteListItem, int, error) {
	if page < 1 {
		page = 1
	}
	// Defense-in-depth: callers already clamp, but guard against a non-positive
	// limit reaching the query so LIMIT can never be zero or negative.
	if limit < 1 {
		limit = 1
	}
	offset := (page - 1) * limit
	now := time.Now()

	column, ok := publicPasteSortColumns[strings.ToLower(strings.TrimSpace(sortBy))]
	if !ok {
		column = "created_at"
	}
	direction := "DESC"
	if strings.EqualFold(strings.TrimSpace(order), "asc") {
		direction = "ASC"
	}

	search = strings.TrimSpace(search)

	ctx, cancel := dbCtx(dbComplexTimeout)
	defer cancel()

	where := `WHERE visibility = 0 AND (expires_at IS NULL OR expires_at > ?)`
	args := []interface{}{now}
	if search != "" {
		where += ` AND (id LIKE ? ESCAPE '\' OR title LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\')`
		term := "%" + escapeLikeTerm(search) + "%"
		args = append(args, term, term, term)
	}

	var total int
	countArgs := append([]interface{}{}, args...)
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pastes `+where, countArgs...,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// column/direction come only from the allow-list above, never from raw
	// caller input, so this interpolation cannot inject SQL.
	query := `SELECT id, title, language, views, expires_at, burn_after, created_at
		 FROM pastes ` + where + ` ORDER BY ` + column + ` ` + direction + `, id ASC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []model.PasteListItem
	for rows.Next() {
		var item model.PasteListItem
		var expiresAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.Title, &item.Language, &item.Views, &expiresAt, &item.BurnAfter, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// escapeLikeTerm escapes SQLite LIKE wildcard characters (%, _, and the
// escape character itself) in user-supplied search input so a search term
// like "50%" or "a_b" is matched literally instead of as a wildcard pattern.
func escapeLikeTerm(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// IncrementViewsAndCheckBurn atomically increments the view counter and, when
// the paste has burn-after-N-reads enabled and the new count reaches the
// threshold, deletes it in the same transaction. SQLite serializes writers at
// the file level, so the increment+check+delete sequence below cannot
// interleave with a concurrent call for the same id — closing the
// check-then-act race a separate read + increment + in-memory compare would
// have (multiple concurrent readers all passing a stale threshold check).
// Returns the paste's new view count and whether it was burned (deleted).
func (s *SQLiteDB) IncrementViewsAndCheckBurn(id string) (views int, burned bool, err error) {
	ctx, cancel := dbCtx(dbTxTimeout)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var burnAfter int
	err = tx.QueryRowContext(ctx,
		`UPDATE pastes SET views = views + 1, updated_at = ? WHERE id = ? RETURNING views, burn_after`,
		time.Now(), id,
	).Scan(&views, &burnAfter)
	if err == sql.ErrNoRows {
		return 0, false, fmt.Errorf("paste not found")
	}
	if err != nil {
		return 0, false, err
	}

	if burnAfter > 0 && views >= burnAfter {
		if _, err = tx.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id); err != nil {
			return 0, false, err
		}
		burned = true
	}

	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	return views, burned, nil
}

// DeletePaste removes a paste by ID with no token check (admin / internal use).
func (s *SQLiteDB) DeletePaste(id string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("paste not found")
	}
	return nil
}

// DeletePasteByToken removes a paste only when the delete token hash matches.
// The comparison is performed in constant time to prevent timing-based token oracle attacks.
func (s *SQLiteDB) DeletePasteByToken(id, deleteTokenHash string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	var storedHash string
	err := s.db.QueryRowContext(ctx, `SELECT delete_token_hash FROM pastes WHERE id = ?`, id).Scan(&storedHash)
	if err == sql.ErrNoRows {
		return fmt.Errorf("paste not found or invalid token")
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(deleteTokenHash)) != 1 {
		return fmt.Errorf("paste not found or invalid token")
	}
	ctx2, cancel2 := dbCtx(dbWriteTimeout)
	defer cancel2()
	_, err = s.db.ExecContext(ctx2, `DELETE FROM pastes WHERE id = ?`, id)
	return err
}

// DeleteExpiredPastes removes all pastes whose expiry has passed.
func (s *SQLiteDB) DeleteExpiredPastes() (int64, error) {
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM pastes WHERE expires_at IS NOT NULL AND expires_at <= ?`,
		time.Now(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteBurnedPastes removes pastes whose view count has reached their burn_after limit.
func (s *SQLiteDB) DeleteBurnedPastes() (int64, error) {
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM pastes WHERE burn_after > 0 AND views >= burn_after`,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ── Scheduler operations (PART 18) ──────────────────────────────────────────

// UpsertSchedulerTask inserts or replaces a task's persistent state.
func (s *SQLiteDB) UpsertSchedulerTask(t *TaskState) error {
	var lastRun *time.Time
	if !t.LastRun.IsZero() {
		lastRun = &t.LastRun
	}
	var nextRun *time.Time
	if !t.NextRun.IsZero() {
		nextRun = &t.NextRun
	}
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scheduler_tasks
			(task_id, task_name, schedule, last_run, last_status, last_error,
			 next_run, run_count, fail_count, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			task_name   = excluded.task_name,
			schedule    = excluded.schedule,
			last_run    = COALESCE(scheduler_tasks.last_run, excluded.last_run),
			last_status = COALESCE(NULLIF(scheduler_tasks.last_status,''), excluded.last_status),
			last_error  = COALESCE(NULLIF(scheduler_tasks.last_error,''),  excluded.last_error),
			next_run    = COALESCE(scheduler_tasks.next_run, excluded.next_run),
			run_count   = scheduler_tasks.run_count,
			fail_count  = scheduler_tasks.fail_count,
			enabled     = excluded.enabled`,
		t.TaskID, t.TaskName, t.Schedule, lastRun, t.LastStatus, t.LastError,
		nextRun, t.RunCount, t.FailCount, enabled,
	)
	return err
}

// GetSchedulerTask retrieves the persistent state for a single task.
func (s *SQLiteDB) GetSchedulerTask(taskID string) (*TaskState, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	row := s.db.QueryRowContext(ctx, `
		SELECT task_id, task_name, schedule, last_run, last_status, last_error,
		       next_run, run_count, fail_count, enabled
		FROM   scheduler_tasks WHERE task_id = ?`, taskID)
	return scanTaskState(row)
}

// ListSchedulerTasks returns all registered tasks ordered by task_id.
func (s *SQLiteDB) ListSchedulerTasks() ([]*TaskState, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, task_name, schedule, last_run, last_status, last_error,
		       next_run, run_count, fail_count, enabled
		FROM   scheduler_tasks ORDER BY task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*TaskState
	for rows.Next() {
		t, err := scanTaskState(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// SetTaskEnabled enables or disables a scheduled task in the persistent store.
func (s *SQLiteDB) SetTaskEnabled(taskID string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`UPDATE scheduler_tasks SET enabled = ? WHERE task_id = ?`,
		enabledInt, taskID,
	)
	return err
}

// UpdateTaskRun records the outcome of a completed task execution.
func (s *SQLiteDB) UpdateTaskRun(
	taskID string, lastRun time.Time, status, lastError string,
	runCount, failCount int64, nextRun time.Time,
) error {
	var nextRunPtr *time.Time
	if !nextRun.IsZero() {
		nextRunPtr = &nextRun
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		UPDATE scheduler_tasks
		SET last_run = ?, last_status = ?, last_error = ?,
		    run_count = ?, fail_count = ?, next_run = ?
		WHERE task_id = ?`,
		lastRun, status, lastError, runCount, failCount, nextRunPtr, taskID,
	)
	return err
}

// RecordTaskHistory appends a history entry for a task execution.
func (s *SQLiteDB) RecordTaskHistory(h *TaskHistory) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO scheduler_history
			(task_id, started_at, finished_at, status, error_msg, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?)`,
		h.TaskID, h.StartedAt, h.FinishedAt, h.Status, h.ErrorMsg, h.DurationMS,
	)
	return err
}

// ListTaskHistory returns the most recent history entries for a task, newest first.
// If limit <= 0 it defaults to 20.
func (s *SQLiteDB) ListTaskHistory(taskID string, limit int) ([]*TaskHistory, error) {
	if limit <= 0 {
		limit = 20
	}
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, started_at, finished_at, status, error_msg, duration_ms
		FROM   scheduler_history
		WHERE  task_id = ?
		ORDER  BY started_at DESC
		LIMIT  ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskHistory
	for rows.Next() {
		h := &TaskHistory{}
		if err := rows.Scan(&h.TaskID, &h.StartedAt, &h.FinishedAt, &h.Status, &h.ErrorMsg, &h.DurationMS); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ── App secrets (PART 11) ────────────────────────────────────────────────────

// EnsureAppSecret returns the raw bytes for key.
// On first call for a given key, 32 random bytes are generated, stored
// base64-encoded in app_secrets, and returned. On subsequent calls the stored
// value is decoded and returned unchanged.
func (s *SQLiteDB) EnsureAppSecret(key string) ([]byte, error) {
	// Attempt to fetch existing value.
	var encoded string
	rCtx, rCancel := dbCtx(dbReadTimeout)
	defer rCancel()
	err := s.db.QueryRowContext(rCtx,
		`SELECT value FROM app_secrets WHERE key = ?`, key,
	).Scan(&encoded)
	if err == nil {
		raw, decErr := base64.StdEncoding.DecodeString(encoded)
		if decErr != nil {
			return nil, fmt.Errorf("app_secrets: decode %q: %w", key, decErr)
		}
		return raw, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("app_secrets: query %q: %w", key, err)
	}

	// Not found — generate 32 random bytes.
	var buf [32]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, fmt.Errorf("app_secrets: generate %q: %w", key, err)
	}
	encoded = base64.StdEncoding.EncodeToString(buf[:])
	now := time.Now()
	wCtx, wCancel := dbCtx(dbWriteTimeout)
	defer wCancel()
	_, err = s.db.ExecContext(wCtx,
		`INSERT INTO app_secrets (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		key, encoded, now, now,
	)
	if err != nil {
		// Race: another goroutine may have inserted simultaneously.
		var existing string
		rCtx2, rCancel2 := dbCtx(dbReadTimeout)
		defer rCancel2()
		if qErr := s.db.QueryRowContext(rCtx2,
			`SELECT value FROM app_secrets WHERE key = ?`, key,
		).Scan(&existing); qErr == nil {
			raw, decErr := base64.StdEncoding.DecodeString(existing)
			if decErr != nil {
				return nil, fmt.Errorf("app_secrets: decode %q after race: %w", key, decErr)
			}
			return raw, nil
		}
		return nil, fmt.Errorf("app_secrets: insert %q: %w", key, err)
	}
	return buf[:], nil
}

// CountPastes returns the total number of rows in the paste table.
func (s *SQLiteDB) CountPastes() (int64, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pastes`).Scan(&count)
	return count, err
}

// ── API Token operations (PART 11) ──────────────────────────────────────────

// CreateAPIToken inserts a new resource-owner token record.
// tokenHash must be the SHA-256 hex digest of the raw token.
// tokenPrefix must be the first 12 characters of the raw token.
func (s *SQLiteDB) CreateAPIToken(tokenHash, tokenPrefix, resourceType, resourceID string, expiresAt *time.Time) error {
	now := time.Now().Unix()
	var expiresUnix *int64
	if expiresAt != nil {
		v := expiresAt.Unix()
		expiresUnix = &v
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens
			(token_hash, token_prefix, resource_type, resource_id, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, tokenPrefix, resourceType, resourceID, now, expiresUnix,
	)
	return err
}

// CreateOwnerWithPrimaryToken atomically creates an anonymous owner and its primary credential.
func (s *SQLiteDB) CreateOwnerWithPrimaryToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	ctx, cancel := dbCtx(dbTxTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO owners(id) VALUES (?)`, ownerID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_tokens(owner_id, kind, token_hash, token_prefix, token_suffix, token_enc, resource_type, resource_id) VALUES (?, 'primary', ?, ?, ?, ?, 'owner', ?)`, ownerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc, ownerID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GetOwnerTokenByHash validates and returns an active token and touches its owner.
func (s *SQLiteDB) GetOwnerTokenByHash(tokenHash [32]byte) (*APITokenRecord, error) {
	hashHex := hex.EncodeToString(tokenHash[:])
	now := time.Now().Unix()
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	rec := &APITokenRecord{}
	var stored string
	var expires, lastUsed, revoked sql.NullInt64
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, kind, COALESCE(name,''), token_prefix, token_enc, resource_type, resource_id, token_hash, created_at, expires_at, last_used_at, revoked_at FROM api_tokens WHERE token_hash = ?`, hashHex).Scan(&rec.ID, &rec.OwnerID, &rec.Kind, &rec.Name, &rec.TokenPrefix, &rec.TokenEnc, &rec.ResourceType, &rec.ResourceID, &stored, &created, &expires, &lastUsed, &revoked)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hashHex)) != 1 || revoked.Valid || (expires.Valid && expires.Int64 <= now) {
		return nil, fmt.Errorf("token not found")
	}
	rec.CreatedAt = time.Unix(created, 0)
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		rec.ExpiresAt = &t
	}
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0)
		rec.LastUsedAt = &t
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at <= ? THEN ? ELSE last_used_at END WHERE id = ?`, now-60, now, rec.ID); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE owners SET last_seen_at = ? WHERE id = ?`, now, rec.OwnerID); err != nil {
		return nil, err
	}
	return rec, nil
}

// GetOwnerTokenByID loads one owner's token, including its encrypted copy for
// a primary-authorized reveal action. ListOwnerTokens never returns ciphertext.
func (s *SQLiteDB) GetOwnerTokenByID(ownerID string, id int64) (*APITokenRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rec := &APITokenRecord{}
	var created int64
	var expires, used, revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, kind, COALESCE(name,''), token_prefix, token_suffix, token_enc, resource_type, resource_id, created_at, expires_at, last_used_at, revoked_at, COALESCE(revoked_reason,'') FROM api_tokens WHERE owner_id = ? AND id = ?`, ownerID, id).Scan(&rec.ID, &rec.OwnerID, &rec.Kind, &rec.Name, &rec.TokenPrefix, &rec.TokenSuffix, &rec.TokenEnc, &rec.ResourceType, &rec.ResourceID, &created, &expires, &used, &revoked, &rec.RevokedReason)
	if err != nil {
		return nil, err
	}
	rec.CreatedAt = time.Unix(created, 0)
	if expires.Valid {
		v := time.Unix(expires.Int64, 0)
		rec.ExpiresAt = &v
	}
	if used.Valid {
		v := time.Unix(used.Int64, 0)
		rec.LastUsedAt = &v
	}
	if revoked.Valid {
		v := time.Unix(revoked.Int64, 0)
		rec.RevokedAt = &v
	}
	return rec, nil
}

// CreateLinkedToken stores an encrypted linked credential for an owner.
func (s *SQLiteDB) CreateLinkedToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, name, tokenEnc string, expiresAt *time.Time, maxActive int) (int64, error) {
	if maxActive <= 0 {
		return 0, fmt.Errorf("linked token limit reached")
	}
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.Unix()
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(owner_id, kind, name, token_hash, token_prefix, token_suffix, token_enc, resource_type, resource_id, expires_at) SELECT ?, 'linked', ?, ?, ?, ?, ?, 'owner', ?, ? WHERE (SELECT COUNT(*) FROM api_tokens WHERE owner_id = ? AND kind = 'linked' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > strftime('%s','now'))) < ?`, ownerID, name, tokenHash, tokenPrefix, tokenSuffix, tokenEnc, ownerID, expiry, ownerID, maxActive)
	if err != nil {
		return 0, err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return 0, fmt.Errorf("linked token limit reached")
	}
	return res.LastInsertId()
}

// ListOwnerTokens lists credential metadata without hashes or encrypted copies.
func (s *SQLiteDB) ListOwnerTokens(ownerID string) ([]*APITokenRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, kind, COALESCE(name,''), token_prefix, token_suffix, resource_type, resource_id, created_at, expires_at, last_used_at, revoked_at, COALESCE(revoked_reason,'') FROM api_tokens WHERE owner_id = ? ORDER BY kind, created_at`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APITokenRecord
	for rows.Next() {
		rec := &APITokenRecord{}
		var created int64
		var expires, lastUsed, revoked sql.NullInt64
		if err := rows.Scan(&rec.ID, &rec.OwnerID, &rec.Kind, &rec.Name, &rec.TokenPrefix, &rec.TokenSuffix, &rec.ResourceType, &rec.ResourceID, &created, &expires, &lastUsed, &revoked, &rec.RevokedReason); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(created, 0)
		if expires.Valid {
			v := time.Unix(expires.Int64, 0)
			rec.ExpiresAt = &v
		}
		if lastUsed.Valid {
			v := time.Unix(lastUsed.Int64, 0)
			rec.LastUsedAt = &v
		}
		if revoked.Valid {
			v := time.Unix(revoked.Int64, 0)
			rec.RevokedAt = &v
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RotateOwnerToken replaces a token's hash and encrypted copy in place.
func (s *SQLiteDB) RotateOwnerToken(ownerID string, id int64, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET token_hash = ?, token_prefix = ?, token_suffix = ?, token_enc = ?, rotated_at = strftime('%s','now') WHERE id = ? AND owner_id = ? AND revoked_at IS NULL`, tokenHash, tokenPrefix, tokenSuffix, tokenEnc, id, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

// UpdateOwnerToken changes linked-token metadata without changing its credential.
func (s *SQLiteDB) UpdateOwnerToken(ownerID string, id int64, name string, expiresAt *time.Time, maxActive int) error {
	var expiry any
	if expiresAt != nil {
		expiry = expiresAt.Unix()
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET name = ?, expires_at = ? WHERE id = ? AND owner_id = ? AND kind = 'linked' AND revoked_at IS NULL AND ((? IS NOT NULL AND ? <= strftime('%s','now')) OR (SELECT COUNT(*) FROM api_tokens WHERE owner_id = ? AND kind = 'linked' AND id <> ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > strftime('%s','now'))) < ?)`, name, expiry, id, ownerID, expiry, expiry, ownerID, id, maxActive)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found or linked token limit reached")
	}
	return nil
}

// RevokeOwnerToken permanently revokes a linked token.
func (s *SQLiteDB) RevokeOwnerToken(ownerID string, id int64) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = strftime('%s','now'), revoked_reason = 'owner revoked' WHERE id = ? AND owner_id = ? AND kind = 'linked' AND revoked_at IS NULL`, id, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

// VerifyAPIToken checks that the token hash matches an active, non-expired row for the
// given resource, then updates last_used_at. Constant-time hash comparison prevents
// timing oracle attacks.
func (s *SQLiteDB) VerifyAPIToken(tokenHash [32]byte, resourceType, resourceID string) error {
	hashHex := hex.EncodeToString(tokenHash[:])
	now := time.Now().Unix()

	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var storedHash string
	var id int64
	var expiresAt sql.NullInt64
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, t.token_hash, t.expires_at, t.revoked_at
		 FROM api_tokens t JOIN pastes p ON p.id = ?
		 WHERE t.token_hash = ? AND t.owner_id = p.owner_id AND t.owner_id <> ''`,
		resourceID, hashHex,
	).Scan(&id, &storedHash, &expiresAt, &revokedAt)
	// Compatibility for direct legacy rows and older database-level callers;
	// startup migration converts persisted rows before requests are served.
	if err == sql.ErrNoRows {
		err = s.db.QueryRowContext(ctx, `SELECT id, token_hash, expires_at, revoked_at FROM api_tokens WHERE token_hash = ? AND owner_id = '' AND resource_type = ? AND resource_id = ?`, hashHex, resourceType, resourceID).Scan(&id, &storedHash, &expiresAt, &revokedAt)
	}
	if err == sql.ErrNoRows {
		return fmt.Errorf("paste not found or invalid token")
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashHex)) != 1 {
		return fmt.Errorf("paste not found or invalid token")
	}
	if revokedAt.Valid {
		return fmt.Errorf("paste not found or invalid token")
	}
	if expiresAt.Valid && expiresAt.Int64 <= now {
		return fmt.Errorf("paste not found or invalid token")
	}

	// last_used_at is best-effort metadata written after the token has already
	// been validated; a write failure here must not fail an otherwise-valid auth,
	// so the error is intentionally not propagated.
	wCtx, wCancel := dbCtx(dbWriteTimeout)
	defer wCancel()
	_, _ = s.db.ExecContext(wCtx, `UPDATE api_tokens SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at <= ? THEN ? ELSE last_used_at END WHERE id = ?`, now-60, now, id)
	_, _ = s.db.ExecContext(wCtx, `UPDATE owners SET last_seen_at = ? WHERE id = (SELECT owner_id FROM api_tokens WHERE id = ?)`, now, id)
	return nil
}

// ValidateAPIToken checks that the token hash is active and not expired for
// any row with the given resource_type. Used to verify a reusable owner token
// before linking it to a new paste via CreateAPIToken.
func (s *SQLiteDB) ValidateAPIToken(tokenHash [32]byte, resourceType string) error {
	if _, err := s.GetOwnerTokenByHash(tokenHash); err == nil {
		return nil
	}
	hashHex := hex.EncodeToString(tokenHash[:])
	now := time.Now().Unix()
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var stored string
	var expires, revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT token_hash, expires_at, revoked_at FROM api_tokens WHERE token_hash = ? AND owner_id = '' AND resource_type = ? LIMIT 1`, hashHex, resourceType).Scan(&stored, &expires, &revoked)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hashHex)) != 1 || revoked.Valid || (expires.Valid && expires.Int64 <= now) {
		return fmt.Errorf("token is not active")
	}
	return nil
}

// FindResourceIDByToken reverse-looks-up the resource a token grants access
// to, without already knowing the resource id — used by the public header's
// profile/preferences zone (AI.md "Header Layout") to link the owner_token
// cookie straight to the paste it owns. Returns ("", nil) when the token is
// unknown, expired, or revoked, since an invalid cookie is not a server
// error, just "no owned resource" — the caller falls back to the plain
// (non-dropdown) preferences link.
func (s *SQLiteDB) FindResourceIDByToken(tokenHash [32]byte, resourceType string) (string, error) {
	hashHex := hex.EncodeToString(tokenHash[:])
	now := time.Now().Unix()

	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var resourceID string
	var expiresAt sql.NullInt64
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT p.id, t.expires_at, t.revoked_at FROM api_tokens t JOIN pastes p ON p.owner_id = t.owner_id
		 WHERE t.token_hash = ? AND t.owner_id <> '' ORDER BY p.created_at DESC LIMIT 1`, hashHex,
	).Scan(&resourceID, &expiresAt, &revokedAt)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if revokedAt.Valid || (expiresAt.Valid && expiresAt.Int64 <= now) {
		return "", nil
	}
	return resourceID, nil
}

// RevokeAPIToken marks the token whose prefix matches as revoked.
func (s *SQLiteDB) RevokeAPIToken(prefix, reason string) error {
	now := time.Now().Unix()
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	var ownerID, kind string
	err := s.db.QueryRowContext(ctx, `SELECT owner_id, kind FROM api_tokens WHERE token_prefix = ? AND revoked_at IS NULL LIMIT 1`, prefix).Scan(&ownerID, &kind)
	if err == sql.ErrNoRows {
		return fmt.Errorf("token not found or already revoked")
	}
	if err != nil {
		return err
	}
	query := `UPDATE api_tokens SET revoked_at = ?, revoked_reason = ? WHERE token_prefix = ? AND revoked_at IS NULL`
	args := []any{now, reason, prefix}
	if kind == "primary" && ownerID != "" {
		query = `UPDATE api_tokens SET revoked_at = ?, revoked_reason = ? WHERE owner_id = ? AND revoked_at IS NULL`
		args = []any{now, reason, ownerID}
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("token not found or already revoked")
	}
	return nil
}

// ListAllAPITokens returns active, expired, and revoked credential metadata
// for the operator maintenance command. Hashes and encrypted copies are omitted.
func (s *SQLiteDB) ListAllAPITokens() ([]*APITokenRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, kind, COALESCE(name,''), token_prefix, resource_type, resource_id, created_at, expires_at, last_used_at, revoked_at, COALESCE(revoked_reason,'') FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APITokenRecord
	for rows.Next() {
		rec := &APITokenRecord{}
		var created int64
		var expires, used, revoked sql.NullInt64
		if err := rows.Scan(&rec.ID, &rec.OwnerID, &rec.Kind, &rec.Name, &rec.TokenPrefix, &rec.ResourceType, &rec.ResourceID, &created, &expires, &used, &revoked, &rec.RevokedReason); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(created, 0)
		if expires.Valid {
			v := time.Unix(expires.Int64, 0)
			rec.ExpiresAt = &v
		}
		if used.Valid {
			v := time.Unix(used.Int64, 0)
			rec.LastUsedAt = &v
		}
		if revoked.Valid {
			v := time.Unix(revoked.Int64, 0)
			rec.RevokedAt = &v
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PurgeIdleOwners removes token rows and owners with no pastes and no valid
// token use for the configured idle period. A zero or negative value disables it.
func (s *SQLiteDB) PurgeIdleOwners(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	cutoff := now - int64(days)*24*60*60
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE owner_id IN (SELECT o.id FROM owners o WHERE o.last_seen_at <= ? AND NOT EXISTS (SELECT 1 FROM pastes p WHERE p.owner_id = o.id))`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM owners WHERE last_seen_at <= ? AND NOT EXISTS (SELECT 1 FROM pastes p WHERE p.owner_id = owners.id)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// ListAPITokens returns all non-revoked token records, ordered newest first.
// The token_hash is intentionally omitted from the returned struct.
func (s *SQLiteDB) ListAPITokens() ([]*APITokenRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, token_prefix, resource_type, resource_id,
		        created_at, expires_at, last_used_at
		 FROM api_tokens
		 WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > strftime('%s', 'now'))
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*APITokenRecord
	for rows.Next() {
		rec := &APITokenRecord{}
		var createdAt int64
		var expiresAt, lastUsedAt sql.NullInt64
		if err := rows.Scan(
			&rec.ID, &rec.TokenPrefix, &rec.ResourceType, &rec.ResourceID,
			&createdAt, &expiresAt, &lastUsedAt,
		); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(createdAt, 0)
		if expiresAt.Valid {
			t := time.Unix(expiresAt.Int64, 0)
			rec.ExpiresAt = &t
		}
		if lastUsedAt.Valid {
			t := time.Unix(lastUsedAt.Int64, 0)
			rec.LastUsedAt = &t
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetAPITokenByPrefix returns the non-revoked token record matching prefix.
func (s *SQLiteDB) GetAPITokenByPrefix(prefix string) (*APITokenRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rec := &APITokenRecord{}
	var createdAt int64
	var expiresAt, lastUsedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, token_prefix, resource_type, resource_id,
		        created_at, expires_at, last_used_at
		 FROM api_tokens
		 WHERE token_prefix = ? AND revoked_at IS NULL`,
		prefix,
	).Scan(&rec.ID, &rec.TokenPrefix, &rec.ResourceType, &rec.ResourceID,
		&createdAt, &expiresAt, &lastUsedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("token not found or revoked")
	}
	if err != nil {
		return nil, err
	}
	rec.CreatedAt = time.Unix(createdAt, 0)
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0)
		rec.ExpiresAt = &t
	}
	if lastUsedAt.Valid {
		t := time.Unix(lastUsedAt.Int64, 0)
		rec.LastUsedAt = &t
	}
	return rec, nil
}

// DeleteExpiredAPITokens removes rows that are expired or revoked.
func (s *SQLiteDB) DeleteExpiredAPITokens() (int64, error) {
	now := time.Now().Unix()
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens
		 WHERE owner_id = '' AND (revoked_at IS NOT NULL
		    OR (expires_at IS NOT NULL AND expires_at <= ?))`,
		now,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// scanTaskState scans a row into a TaskState.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanTaskState(row rowScanner) (*TaskState, error) {
	t := &TaskState{}
	var lastRun, nextRun *time.Time
	var enabled int
	err := row.Scan(
		&t.TaskID, &t.TaskName, &t.Schedule,
		&lastRun, &t.LastStatus, &t.LastError,
		&nextRun, &t.RunCount, &t.FailCount, &enabled,
	)
	if err != nil {
		return nil, err
	}
	if lastRun != nil {
		t.LastRun = *lastRun
	}
	if nextRun != nil {
		t.NextRun = *nextRun
	}
	t.Enabled = enabled != 0
	return t, nil
}
