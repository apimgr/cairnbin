package database

import (
	"fmt"
	"time"
)

// AppSecretRecord is one server-owned secret row carried by a backup archive.
type AppSecretRecord struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExportAppSecrets returns the rows needed to restore application cryptographic
// state without exporting any other remote database tables.
func (s *SQLiteDB) ExportAppSecrets() ([]AppSecretRecord, error) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, created_at, updated_at FROM app_secrets ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("export app secrets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []AppSecretRecord
	for rows.Next() {
		var record AppSecretRecord
		if err := rows.Scan(&record.Key, &record.Value, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read app secret row: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finish app secret export: %w", err)
	}
	return records, nil
}

// ImportAppSecrets replaces matching rows while retaining unrelated application
// state in the configured database.
func (s *SQLiteDB) ImportAppSecrets(records []AppSecretRecord) error {
	if len(records) == 0 {
		return nil
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin app secret restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, record := range records {
		if record.Key == "" {
			return fmt.Errorf("restore app secret: key is empty")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_secrets(key, value, created_at, updated_at) VALUES(?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, created_at = excluded.created_at, updated_at = excluded.updated_at`, record.Key, record.Value, record.CreatedAt, record.UpdatedAt); err != nil {
			return fmt.Errorf("restore app secret %q: %w", record.Key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit app secret restore: %w", err)
	}
	return nil
}
