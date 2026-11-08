package database

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/metric"
)

// instrumentedSQLDB records SQL calls while preserving database/sql pool
// behavior. Query text is parsed only for low-cardinality operation/table
// labels; neither SQL text nor parameter values leave this package.
type instrumentedSQLDB struct {
	*sql.DB
}

func (db *instrumentedSQLDB) record(query string, started time.Time, err error) {
	stats := db.Stats()
	metric.RecordDBOperation(query, time.Since(started), err, stats.OpenConnections, stats.InUse)
}

func (db *instrumentedSQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	started := time.Now()
	result, err := db.DB.ExecContext(ctx, query, args...)
	db.record(query, started, err)
	return result, err
}

func (db *instrumentedSQLDB) QueryContext(ctx context.Context, query string, args ...any) (*instrumentedSQLRows, error) {
	started := time.Now()
	rows, err := db.DB.QueryContext(ctx, query, args...)
	if err != nil {
		db.record(query, started, err)
		return nil, err
	}
	return &instrumentedSQLRows{Rows: rows, db: db, query: query, started: started}, nil
}

func (db *instrumentedSQLDB) QueryRowContext(ctx context.Context, query string, args ...any) *instrumentedSQLRow {
	return &instrumentedSQLRow{Row: db.DB.QueryRowContext(ctx, query, args...), db: db, query: query, started: time.Now()}
}

func (db *instrumentedSQLDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*instrumentedSQLTx, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		db.record("BEGIN", time.Now(), err)
		return nil, err
	}
	return &instrumentedSQLTx{Tx: tx, db: db}, nil
}

type instrumentedSQLTx struct {
	*sql.Tx
	db *instrumentedSQLDB
}

func (tx *instrumentedSQLTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	started := time.Now()
	result, err := tx.Tx.ExecContext(ctx, query, args...)
	tx.db.record(query, started, err)
	return result, err
}

func (tx *instrumentedSQLTx) QueryContext(ctx context.Context, query string, args ...any) (*instrumentedSQLRows, error) {
	started := time.Now()
	rows, err := tx.Tx.QueryContext(ctx, query, args...)
	if err != nil {
		tx.db.record(query, started, err)
		return nil, err
	}
	return &instrumentedSQLRows{Rows: rows, db: tx.db, query: query, started: started}, nil
}

func (tx *instrumentedSQLTx) QueryRowContext(ctx context.Context, query string, args ...any) *instrumentedSQLRow {
	return &instrumentedSQLRow{Row: tx.Tx.QueryRowContext(ctx, query, args...), db: tx.db, query: query, started: time.Now()}
}

type instrumentedSQLRows struct {
	*sql.Rows
	db      *instrumentedSQLDB
	query   string
	started time.Time
	record  sync.Once
}

func (rows *instrumentedSQLRows) Close() error {
	err := rows.Rows.Close()
	if err == nil {
		err = rows.Err()
	}
	rows.record.Do(func() { rows.db.record(rows.query, rows.started, err) })
	return err
}

type instrumentedSQLRow struct {
	*sql.Row
	db      *instrumentedSQLDB
	query   string
	started time.Time
}

func (row *instrumentedSQLRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	row.db.record(row.query, row.started, err)
	return err
}
