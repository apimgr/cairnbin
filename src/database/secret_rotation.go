package database

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// PreviousSecretSuffix is appended to an app_secrets key to hold the secret
// that was in force before the last manual rotation (PART 11 → Secret Rotation).
const PreviousSecretSuffix = "_previous"

// RotateAppSecret atomically replaces the app_secrets row for key with newRaw
// and parks the old value under key+PreviousSecretSuffix so in-flight material
// can still be validated during the grace period. The row must already exist.
func (s *SQLiteDB) RotateAppSecret(key string, newRaw []byte) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("app_secrets: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var old string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM app_secrets WHERE key = ?`, key).Scan(&old); err != nil {
		return fmt.Errorf("app_secrets: read %q: %w", key, err)
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO app_secrets (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key+PreviousSecretSuffix, old, now, now); err != nil {
		return fmt.Errorf("app_secrets: park previous %q: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE app_secrets SET value = ?, updated_at = ? WHERE key = ?`,
		base64.StdEncoding.EncodeToString(newRaw), now, key); err != nil {
		return fmt.Errorf("app_secrets: update %q: %w", key, err)
	}
	return tx.Commit()
}

// ParkAppSecret stores raw under key, replacing any existing row. It is used to
// keep a previous secret that does not live in app_secrets itself (the
// at-rest encryption key, which lives in server.yml).
func (s *SQLiteDB) ParkAppSecret(key string, raw []byte) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO app_secrets (key, value, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, base64.StdEncoding.EncodeToString(raw), now, now)
	if err != nil {
		return fmt.Errorf("app_secrets: park %q: %w", key, err)
	}
	return nil
}

// FreshAppSecret returns the secret stored under key only when it was written
// within maxAge. Expired or missing rows report ok=false with no error.
func (s *SQLiteDB) FreshAppSecret(key string, maxAge time.Duration) ([]byte, bool) {
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	var enc string
	var updated time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT value, updated_at FROM app_secrets WHERE key = ?`, key).Scan(&enc, &updated)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	if time.Since(updated) > maxAge {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ReencryptTokens rewrites every non-empty api_tokens.token_enc through fn in
// one transaction. Rows fn rejects are left unchanged and counted as skipped.
func (s *SQLiteDB) ReencryptTokens(fn func(old string) (string, error)) (updated, skipped int, err error) {
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, token_enc FROM api_tokens WHERE token_enc != ''`)
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		id  int64
		enc string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.enc); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, err
	}
	_ = rows.Close()

	for _, r := range all {
		next, ferr := fn(r.enc)
		if ferr != nil {
			skipped++
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET token_enc = ? WHERE id = ?`, next, r.id); err != nil {
			return 0, 0, err
		}
		updated++
	}
	return updated, skipped, tx.Commit()
}

// ReencryptSecurityReports rewrites every AES-sealed security report body
// through fn in one transaction. PGP-sealed bodies are encrypted to the public
// key and are never touched. Rows fn rejects are left unchanged and counted as
// skipped.
func (s *SQLiteDB) ReencryptSecurityReports(fn func(old []byte) ([]byte, error)) (updated, skipped int, err error) {
	ctx, cancel := dbCtx(dbBulkTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT tracking_id, encrypted_body FROM security_reports WHERE enc_method = 'aes-256-gcm'`)
	if err != nil {
		return 0, 0, err
	}
	type row struct {
		id   string
		body []byte
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.body); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, err
	}
	_ = rows.Close()

	for _, r := range all {
		next, ferr := fn(r.body)
		if ferr != nil {
			skipped++
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE security_reports SET encrypted_body = ? WHERE tracking_id = ?`, next, r.id); err != nil {
			return 0, 0, err
		}
		updated++
	}
	return updated, skipped, tx.Commit()
}
