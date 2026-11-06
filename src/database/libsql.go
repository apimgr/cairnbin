package database

import (
	"database/sql"
	"fmt"
	"strings"

	// libSQL/Turso remote driver, registered as "libsql" (AI.md PART 3).
	// Pure Go and remote-only, so it never pulls in cgo.
	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// buildLibSQLURL appends the separately configured auth token to the connection
// URL when the URL does not already carry one, matching the two documented
// forms: "libsql://host?authToken=xxx" and "https://host" plus a token field.
func buildLibSQLURL(url, token string) string {
	if token == "" || strings.Contains(url, "authToken=") {
		return url
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return url + sep + "authToken=" + token
}

// validateLibSQL rejects a libsql configuration with no server URL. libSQL is
// remote-only, so there is no local path to fall back to.
func validateLibSQL(url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("libsql driver requires url: use libsql://host?authToken=xxx or https://host with token field")
	}
	return nil
}

// newLibSQLDB opens a remote libSQL/Turso database and ensures the schema. The
// schema and every query in this package are portable SQLite dialect, so the
// same SQLiteDB wrapper serves both drivers.
func newLibSQLDB(url, token string, pool PoolConfig) (*SQLiteDB, error) {
	if err := validateLibSQL(url); err != nil {
		return nil, err
	}

	db, err := sql.Open("libsql", buildLibSQLURL(url, token))
	if err != nil {
		return nil, fmt.Errorf("open libsql: %w", err)
	}

	// Connection pool settings (PART 10), from server.database.pool. Remote
	// deployments are the case the larger sizing rows exist for.
	if pool.MaxOpen <= 0 {
		pool = DefaultPool()
	}
	db.SetMaxOpenConns(pool.MaxOpen)
	db.SetMaxIdleConns(pool.MaxIdle)
	db.SetConnMaxLifetime(pool.MaxLifetime)
	db.SetConnMaxIdleTime(pool.MaxIdleTime)

	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteDB{db: db}, nil
}
