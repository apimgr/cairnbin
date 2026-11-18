package database

import (
	"fmt"
	"time"
)

// SetAppSecretValue stores non-secret application metadata in app_secrets.
func (s *SQLiteDB) SetAppSecretValue(key, value string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO app_secrets (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, now, now)
	if err != nil {
		return fmt.Errorf("app_secrets: update %q: %w", key, err)
	}
	return nil
}

// DeleteAppSecretValue clears non-secret application metadata by key.
func (s *SQLiteDB) DeleteAppSecretValue(key string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM app_secrets WHERE key = ?`, key); err != nil {
		return fmt.Errorf("app_secrets: delete %q: %w", key, err)
	}
	return nil
}
