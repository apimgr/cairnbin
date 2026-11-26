package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// LocalCacheConfig describes the optional SQLite read replica for a remote
// libSQL database (AI.md PART 10).
type LocalCacheConfig struct {
	Enabled      bool
	Path         string
	SyncInterval time.Duration
	OfflineMode  bool
	MaxAge       time.Duration
}

// CachedDB keeps the remote libSQL database authoritative and serves paste
// reads from a local SQLite snapshot while that snapshot remains fresh.
type CachedDB struct {
	*SQLiteDB
	remote        *SQLiteDB
	cache         *SQLiteDB
	config        LocalCacheConfig
	dirty         atomic.Bool
	syncMu        sync.Mutex
	closeOnce     sync.Once
	cancel        context.CancelFunc
	loopDone      chan struct{}
	logMu         sync.Mutex
	lastLogText   string
	invalidatedMu sync.RWMutex
	invalidated   map[string]struct{}
}

// NewDatabaseWithCache opens the configured database and optionally adds its
// local SQLite read cache. Cache settings are ignored for the SQLite driver.
func NewDatabaseWithCache(dbType, path, token string, pool PoolConfig, cacheConfig LocalCacheConfig) (DB, error) {
	primary, err := NewDatabaseWithToken(dbType, path, token, pool)
	if err != nil {
		if !cacheConfig.Enabled || !cacheConfig.OfflineMode || !strings.EqualFold(strings.TrimSpace(dbType), "libsql") && !strings.EqualFold(strings.TrimSpace(dbType), "turso") {
			return nil, err
		}
		openErr := err
		primary, err = newLibSQLDBUninitialized(path, token, pool)
		if err != nil {
			return nil, errors.Join(openErr, err)
		}
	}
	if !cacheConfig.Enabled || !strings.EqualFold(strings.TrimSpace(dbType), "libsql") && !strings.EqualFold(strings.TrimSpace(dbType), "turso") {
		return primary, nil
	}
	remote, ok := primary.(*SQLiteDB)
	if !ok {
		_ = primary.Close()
		return nil, fmt.Errorf("database cache requires the libSQL driver")
	}
	cached, err := newCachedDB(remote, cacheConfig, true)
	if err != nil {
		_ = remote.Close()
		return nil, err
	}
	return cached, nil
}

func newCachedDB(remote *SQLiteDB, cfg LocalCacheConfig, startBackgroundSync bool) (*CachedDB, error) {
	if remote == nil {
		return nil, fmt.Errorf("database cache requires a primary database")
	}
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("server.database.cache.path is required when the local cache is enabled")
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 30 * time.Second
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = time.Hour
	}
	cache, err := newSQLiteDB(cfg.Path, DefaultPool())
	if err != nil {
		return nil, fmt.Errorf("open local database cache: %w", err)
	}
	if err := os.Chmod(cfg.Path, 0o600); err != nil {
		_ = cache.Close()
		return nil, fmt.Errorf("secure local database cache: %w", err)
	}
	if _, err := cache.db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS database_cache_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		_ = cache.Close()
		return nil, fmt.Errorf("initialize local database cache state: %w", err)
	}
	cached := &CachedDB{SQLiteDB: remote, remote: remote, cache: cache, config: cfg, loopDone: make(chan struct{}), invalidated: make(map[string]struct{})}
	cached.dirty.Store(!cached.snapshotAgeValid())
	if startBackgroundSync {
		ctx, cancel := context.WithCancel(context.Background())
		cached.cancel = cancel
		go cached.syncLoop(ctx)
	} else {
		close(cached.loopDone)
	}
	return cached, nil
}

func (c *CachedDB) Type() string { return c.remote.Type() }

func (c *CachedDB) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
			<-c.loopDone
		}
		cacheErr := c.cache.Close()
		remoteErr := c.remote.Close()
		closeErr = errors.Join(cacheErr, remoteErr)
	})
	return closeErr
}

func (c *CachedDB) syncLoop(ctx context.Context) {
	defer close(c.loopDone)
	c.syncAndReport()
	ticker := time.NewTicker(c.config.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.syncAndReport()
		}
	}
}

func (c *CachedDB) syncAndReport() {
	if err := c.syncOnce(); err != nil {
		message := err.Error()
		c.logMu.Lock()
		if message != c.lastLogText {
			log.Printf("WARN: database cache synchronization failed: %v", err)
			c.lastLogText = message
		}
		c.logMu.Unlock()
		return
	}
	c.logMu.Lock()
	c.lastLogText = ""
	c.logMu.Unlock()
}

// syncOnce replaces the local snapshot from one remote read transaction. All
// table deletes, changed rows, removals, and the freshness marker commit in one
// local transaction; a failure leaves the prior snapshot and dirty state intact.
func (c *CachedDB) syncOnce() error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	if !c.remote.schemaReady.Load() {
		if err := ensureSchema(c.remote.db.DB); err != nil {
			return fmt.Errorf("initialize remote database schema: %w", err)
		}
		c.remote.schemaReady.Store(true)
	}
	remoteTx, err := c.remote.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin remote snapshot: %w", err)
	}
	defer func() { _ = remoteTx.Rollback() }()
	localTx, err := c.cache.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local snapshot: %w", err)
	}
	defer func() { _ = localTx.Rollback() }()
	if _, err := localTx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return fmt.Errorf("defer local cache foreign keys: %w", err)
	}

	tables, err := remoteTx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("list remote snapshot tables: %w", err)
	}
	var tableNames []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			_ = tables.Close()
			return fmt.Errorf("read remote snapshot table name: %w", err)
		}
		tableNames = append(tableNames, name)
	}
	if err := tables.Close(); err != nil {
		return fmt.Errorf("finish remote snapshot table list: %w", err)
	}
	for _, table := range tableNames {
		quoted := quoteIdentifier(table)
		rows, err := remoteTx.QueryContext(ctx, `SELECT * FROM `+quoted)
		if err != nil {
			return fmt.Errorf("read remote snapshot table %q: %w", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("read remote snapshot columns for %q: %w", table, err)
		}
		if _, err := localTx.ExecContext(ctx, `DELETE FROM `+quoted); err != nil {
			_ = rows.Close()
			return fmt.Errorf("clear local snapshot table %q: %w", table, err)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
		columnList := make([]string, len(columns))
		for i, column := range columns {
			columnList[i] = quoteIdentifier(column)
		}
		insert := `INSERT OR REPLACE INTO ` + quoted + ` (` + strings.Join(columnList, ",") + `) VALUES (` + placeholders + `)`
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan remote snapshot row from %q: %w", table, err)
			}
			if _, err := localTx.ExecContext(ctx, insert, values...); err != nil {
				_ = rows.Close()
				return fmt.Errorf("apply local snapshot row to %q: %w", table, err)
			}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("finish remote snapshot table %q: %w", table, err)
		}
	}
	if err := remoteTx.Commit(); err != nil {
		return fmt.Errorf("finish remote snapshot: %w", err)
	}
	if _, err := localTx.ExecContext(ctx, `INSERT INTO database_cache_state(key, value) VALUES('last_sync', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(time.Now().UnixNano())); err != nil {
		return fmt.Errorf("record local snapshot freshness: %w", err)
	}
	if err := localTx.Commit(); err != nil {
		c.dirty.Store(true)
		return fmt.Errorf("commit local snapshot: %w", err)
	}
	c.dirty.Store(false)
	c.invalidatedMu.Lock()
	c.invalidated = make(map[string]struct{})
	c.invalidatedMu.Unlock()
	return nil
}

// SyncCache refreshes the local snapshot from the authoritative remote database.
// Callers that archive the snapshot use this to ensure it represents a complete
// remote read transaction before it becomes a restore seed.
func (c *CachedDB) SyncCache() error {
	return c.syncOnce()
}

// SnapshotTo refreshes the remote read replica and then snapshots it while
// synchronization and paste-cache updates are excluded. A cache with a known
// invalidated or dirty row is retried rather than archived as a restore seed.
func (c *CachedDB) SnapshotTo(path string) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.SyncCache(); err != nil {
			return fmt.Errorf("refresh local cache before snapshot: %w", err)
		}
		c.syncMu.Lock()
		if c.dirty.Load() || c.hasInvalidated() {
			c.syncMu.Unlock()
			continue
		}
		err := c.cache.SnapshotTo(path)
		c.syncMu.Unlock()
		return err
	}
	return fmt.Errorf("database cache remained inconsistent while creating a snapshot")
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (c *CachedDB) snapshotAgeValid() bool {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var value string
	if err := c.cache.db.QueryRowContext(ctx, `SELECT value FROM database_cache_state WHERE key = 'last_sync'`).Scan(&value); err != nil {
		return false
	}
	var syncedAt int64
	if _, err := fmt.Sscan(value, &syncedAt); err != nil || syncedAt <= 0 {
		return false
	}
	age := time.Since(time.Unix(0, syncedAt))
	return age >= 0 && age <= c.config.MaxAge
}

func (c *CachedDB) cacheFresh() bool { return !c.dirty.Load() && c.snapshotAgeValid() }

func (c *CachedDB) offlineReadAllowed() bool {
	return c.config.OfflineMode && !c.dirty.Load() && c.snapshotAgeValid()
}

func (c *CachedDB) isInvalidated(id string) bool {
	c.invalidatedMu.RLock()
	defer c.invalidatedMu.RUnlock()
	_, ok := c.invalidated[id]
	return ok
}

func (c *CachedDB) hasInvalidated() bool {
	c.invalidatedMu.RLock()
	defer c.invalidatedMu.RUnlock()
	return len(c.invalidated) != 0
}

func (c *CachedDB) GetPasteByID(id string) (*model.Paste, error) {
	if c.cacheFresh() && !c.isInvalidated(id) {
		if paste, err := c.cache.GetPasteByID(id); err == nil {
			return paste, nil
		}
	}
	paste, err := c.remote.GetPasteByID(id)
	if err != nil && c.offlineReadAllowed() {
		return c.cache.GetPasteByID(id)
	}
	return paste, err
}

func (c *CachedDB) GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error) {
	if c.cacheFresh() && !c.hasInvalidated() {
		if items, total, err := c.cache.GetPublicPastes(page, limit); err == nil {
			return items, total, nil
		}
	}
	items, total, err := c.remote.GetPublicPastes(page, limit)
	if err != nil && c.offlineReadAllowed() {
		return c.cache.GetPublicPastes(page, limit)
	}
	return items, total, err
}

func (c *CachedDB) SearchPublicPastes(page, limit int, search, sortBy, order string, pasteType ...string) ([]model.PasteListItem, int, error) {
	if c.cacheFresh() && !c.hasInvalidated() {
		if items, total, err := c.cache.SearchPublicPastes(page, limit, search, sortBy, order, pasteType...); err == nil {
			return items, total, nil
		}
	}
	items, total, err := c.remote.SearchPublicPastes(page, limit, search, sortBy, order, pasteType...)
	if err != nil && c.offlineReadAllowed() {
		return c.cache.SearchPublicPastes(page, limit, search, sortBy, order, pasteType...)
	}
	return items, total, err
}

func (c *CachedDB) CountPastes() (int64, error) {
	if c.cacheFresh() && !c.hasInvalidated() {
		if count, err := c.cache.CountPastes(); err == nil {
			return count, nil
		}
	}
	count, err := c.remote.CountPastes()
	if err != nil && c.offlineReadAllowed() {
		return c.cache.CountPastes()
	}
	return count, err
}

func (c *CachedDB) CreatePaste(paste *model.Paste) error {
	if err := c.remote.CreatePaste(paste); err != nil {
		return err
	}
	c.refreshAfterWrite(paste.ID)
	return nil
}

func (c *CachedDB) CreatePasteWithPrimaryToken(paste *model.Paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	if err := c.remote.CreatePasteWithPrimaryToken(paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc); err != nil {
		return err
	}
	c.refreshAfterWrite(paste.ID)
	return nil
}

func (c *CachedDB) IncrementViewsAndCheckBurn(id string) (int, bool, error) {
	views, burned, err := c.remote.IncrementViewsAndCheckBurn(id)
	if err != nil {
		return views, burned, err
	}
	c.refreshAfterWrite(id)
	return views, burned, nil
}

func (c *CachedDB) DeletePaste(id string) error {
	if err := c.remote.DeletePaste(id); err != nil {
		return err
	}
	c.refreshAfterWrite(id)
	return nil
}

func (c *CachedDB) DeletePasteByToken(id, deleteTokenHash string) error {
	if err := c.remote.DeletePasteByToken(id, deleteTokenHash); err != nil {
		return err
	}
	c.refreshAfterWrite(id)
	return nil
}

func (c *CachedDB) DeleteExpiredPastes() (int64, error) {
	deleted, err := c.remote.DeleteExpiredPastes()
	if err == nil && deleted > 0 {
		c.refreshAfterBulkWrite()
	}
	return deleted, err
}

func (c *CachedDB) DeleteBurnedPastes() (int64, error) {
	deleted, err := c.remote.DeleteBurnedPastes()
	if err == nil && deleted > 0 {
		c.refreshAfterBulkWrite()
	}
	return deleted, err
}

func (c *CachedDB) refreshAfterWrite(id string) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	paste, readErr := c.remote.GetPasteByID(id)
	var updateErr error
	if readErr != nil {
		updateErr = readErr
	} else if paste == nil {
		ctx, cancel := dbCtx(dbWriteTimeout)
		_, updateErr = c.cache.db.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id)
		cancel()
	} else {
		updateErr = c.writeCachedPaste(paste)
	}
	if updateErr == nil {
		c.invalidatedMu.Lock()
		delete(c.invalidated, id)
		c.invalidatedMu.Unlock()
		return
	}
	log.Printf("WARN: database cache could not apply a successful remote write; invalidating cached row")
	ctx, cancel := dbCtx(dbWriteTimeout)
	_, deleteErr := c.cache.db.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id)
	cancel()
	if deleteErr == nil {
		c.invalidatedMu.Lock()
		c.invalidated[id] = struct{}{}
		c.invalidatedMu.Unlock()
		return
	}
	c.dirty.Store(true)
	log.Printf("WARN: database cache row invalidation failed; bypassing the cache until reconciliation")
}

func (c *CachedDB) writeCachedPaste(paste *model.Paste) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := c.cache.db.ExecContext(ctx, `INSERT OR REPLACE INTO pastes (id, owner_id, title, content, language, content_type, visibility, is_link, expires_at, burn_after, delete_token_hash, views, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, paste.ID, paste.OwnerID, paste.Title, paste.Content, paste.Language, paste.ContentType, paste.Visibility, paste.IsLink, paste.ExpiresAt, paste.BurnAfter, paste.DeleteTokenHash, paste.Views, paste.CreatedAt, paste.UpdatedAt)
	return err
}

func (c *CachedDB) refreshAfterBulkWrite() {
	if err := c.syncOnce(); err != nil {
		c.dirty.Store(true)
		log.Printf("WARN: database cache could not reconcile a successful bulk write; bypassing the cache")
	}
}

// SeedRemoteFromSQLite copies a restored SQLite database into an empty remote
// libSQL database. A non-empty remote database is left untouched. The return
// value reports whether rows were copied. When remote is a CachedDB, a
// successful seed is immediately reconciled into its local read replica.
func SeedRemoteFromSQLite(remote DB, seedPath string) (bool, error) {
	if strings.TrimSpace(seedPath) == "" {
		return false, fmt.Errorf("restored SQLite database path is required")
	}
	var primary *SQLiteDB
	var cached *CachedDB
	switch db := remote.(type) {
	case *CachedDB:
		cached = db
		primary = db.remote
	case *SQLiteDB:
		primary = db
	default:
		return false, fmt.Errorf("restore seeding requires a libSQL database")
	}
	if primary.Type() != "libsql" {
		return false, fmt.Errorf("restore seeding requires a libSQL database")
	}
	if !primary.schemaReady.Load() {
		if err := ensureSchema(primary.db.DB); err != nil {
			return false, fmt.Errorf("initialize remote database schema: %w", err)
		}
		primary.schemaReady.Store(true)
	}
	source, err := sql.Open("sqlite", seedPath)
	if err != nil {
		return false, fmt.Errorf("open restored SQLite database: %w", err)
	}
	defer func() { _ = source.Close() }()
	source.SetMaxOpenConns(1)
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("begin restored SQLite snapshot: %w", err)
	}
	defer func() { _ = sourceTx.Rollback() }()
	targetTx, err := primary.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin remote restore seed: %w", err)
	}
	defer func() { _ = targetTx.Rollback() }()
	if _, err := targetTx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return false, fmt.Errorf("defer remote restore foreign keys: %w", err)
	}
	tables, err := targetTx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return false, fmt.Errorf("list remote restore tables: %w", err)
	}
	var tableNames []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			_ = tables.Close()
			return false, fmt.Errorf("read remote restore table name: %w", err)
		}
		tableNames = append(tableNames, name)
	}
	if err := tables.Close(); err != nil {
		return false, fmt.Errorf("finish remote restore table list: %w", err)
	}
	for _, table := range tableNames {
		var count int64
		if err := targetTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&count); err != nil {
			return false, fmt.Errorf("check remote table %q: %w", table, err)
		}
		if table == "config_meta" && count == 1 {
			var version int64
			if err := targetTx.QueryRowContext(ctx, `SELECT version FROM config_meta WHERE id = 1`).Scan(&version); err != nil {
				return false, fmt.Errorf("check remote configuration metadata: %w", err)
			}
			if version == 1 {
				continue
			}
		}
		if table == "owners" && count == 1 {
			var nonSystemOwners int64
			if err := targetTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM owners WHERE id <> 'system'`).Scan(&nonSystemOwners); err != nil {
				return false, fmt.Errorf("check remote owner state: %w", err)
			}
			if nonSystemOwners == 0 {
				continue
			}
		}
		if count != 0 {
			return false, nil
		}
	}
	for _, table := range tableNames {
		quoted := quoteIdentifier(table)
		rows, err := sourceTx.QueryContext(ctx, `SELECT * FROM `+quoted)
		if err != nil {
			return false, fmt.Errorf("read restored table %q: %w", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("read restored columns for %q: %w", table, err)
		}
		if len(columns) == 0 {
			_ = rows.Close()
			continue
		}
		columnList := make([]string, len(columns))
		for i, column := range columns {
			columnList[i] = quoteIdentifier(column)
		}
		insert := `INSERT OR REPLACE INTO ` + quoted + ` (` + strings.Join(columnList, ",") + `) VALUES (` + strings.TrimRight(strings.Repeat("?,", len(columns)), ",") + `)`
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return false, fmt.Errorf("scan restored row from %q: %w", table, err)
			}
			if _, err := targetTx.ExecContext(ctx, insert, values...); err != nil {
				_ = rows.Close()
				return false, fmt.Errorf("seed remote table %q: %w", table, err)
			}
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("finish restored table %q: %w", table, err)
		}
	}
	if err := sourceTx.Commit(); err != nil {
		return false, fmt.Errorf("finish restored SQLite snapshot: %w", err)
	}
	if err := targetTx.Commit(); err != nil {
		return false, fmt.Errorf("commit remote restore seed: %w", err)
	}
	if cached != nil {
		if err := cached.syncOnce(); err != nil {
			cached.dirty.Store(true)
			return true, fmt.Errorf("seeded remote database but could not refresh local cache: %w", err)
		}
	}
	return true, nil
}

// ResolvePathFromConfig resolves a relative configured filesystem path against
// its owning data directory and leaves absolute or empty paths unchanged.
func ResolvePathFromConfig(path, dataDir string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dataDir, path)
}

// CachePathFromConfig resolves a relative cache database path against dataDir.
func CachePathFromConfig(path, dataDir string) string {
	return ResolvePathFromConfig(path, dataDir)
}
