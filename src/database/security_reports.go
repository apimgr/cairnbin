package database

import (
	"database/sql"
	"time"
)

// Coordinated-disclosure triage states (PART 11 → Coordinated Disclosure
// Pipeline). A report advances through these as maintainers work it.
const (
	SecStatusReceived  = "Received"
	SecStatusTriaged   = "Triaged"
	SecStatusConfirmed = "Confirmed"
	SecStatusPatching  = "Patching"
	SecStatusDisclosed = "Disclosed"
	SecStatusWontFix   = "Won't Fix"
)

// SecurityReport is the at-rest record for a coordinated-disclosure security
// report (PART 11, AI.md 14143-14161). The vulnerability content itself lives
// only in EncryptedBody (PGP or AES-256-GCM); no plaintext PII or vulnerability
// detail is stored in the other columns. The sanitized metadata columns support
// the researcher status page and the public acknowledgments page.
type SecurityReport struct {
	TrackingID string
	Status     string
	// Severity is the researcher's self-assessment (Critical/High/Medium/Low/Informational).
	Severity string
	// Component is the sanitized affected-component label (no free-text PII).
	Component string
	// EncryptedBody is the full report payload encrypted at rest.
	EncryptedBody []byte
	// EncMethod records how EncryptedBody was sealed: "pgp" or "aes-256-gcm".
	EncMethod string
	// CreditPreference drives the acknowledgments page: "name", "handle",
	// "no", or "anonymous".
	CreditPreference string
	// CreditName is the sanitized display credit (real name or handle) shown on
	// the acknowledgments page when the researcher opted in. Empty for anonymous.
	CreditName string
	// TokenHash is the SHA-256 hex of the researcher's one-shot status-page token.
	TokenHash string
	// MaintainerComment is the note visible to the researcher on the status page.
	MaintainerComment string
	// DisclosureDays is the researcher's preferred coordinated-disclosure window.
	DisclosureDays int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// DisclosedAt is set when the report reaches the Disclosed state.
	DisclosedAt *time.Time
	// TokenLastUsed is the last time the researcher's status-page token was
	// accepted. It enforces the single-use-per-day rule (AI.md 14161).
	TokenLastUsed *time.Time
}

// CreateSecurityReport inserts a new coordinated-disclosure report.
func (s *SQLiteDB) CreateSecurityReport(r *SecurityReport) error {
	now := time.Now()
	r.CreatedAt = now
	r.UpdatedAt = now
	if r.Status == "" {
		r.Status = SecStatusReceived
	}
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO security_reports
			(tracking_id, status, severity, component, encrypted_body, enc_method,
			 credit_preference, credit_name, token_hash, maintainer_comment,
			 disclosure_days, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TrackingID, r.Status, r.Severity, r.Component, r.EncryptedBody, r.EncMethod,
		r.CreditPreference, r.CreditName, r.TokenHash, r.MaintainerComment,
		r.DisclosureDays, r.CreatedAt, r.UpdatedAt,
	)
	return err
}

// GetSecurityReport retrieves a report by tracking id. Returns (nil, nil) when
// not found.
func (s *SQLiteDB) GetSecurityReport(trackingID string) (*SecurityReport, error) {
	r := &SecurityReport{}
	var disclosedAt, tokenLastUsed sql.NullTime
	ctx, cancel := dbCtx(dbReadTimeout)
	defer cancel()
	err := s.db.QueryRowContext(ctx,
		`SELECT tracking_id, status, severity, component, encrypted_body, enc_method,
			credit_preference, credit_name, token_hash, maintainer_comment,
			disclosure_days, created_at, updated_at, disclosed_at, token_last_used
		 FROM security_reports WHERE tracking_id = ?`, trackingID,
	).Scan(&r.TrackingID, &r.Status, &r.Severity, &r.Component, &r.EncryptedBody,
		&r.EncMethod, &r.CreditPreference, &r.CreditName, &r.TokenHash,
		&r.MaintainerComment, &r.DisclosureDays, &r.CreatedAt, &r.UpdatedAt,
		&disclosedAt, &tokenLastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if disclosedAt.Valid {
		r.DisclosedAt = &disclosedAt.Time
	}
	if tokenLastUsed.Valid {
		r.TokenLastUsed = &tokenLastUsed.Time
	}
	return r, nil
}

// MarkSecurityReportTokenUsed records the moment the researcher's status-page
// token was accepted, enforcing the single-use-per-day rule (AI.md 14161).
func (s *SQLiteDB) MarkSecurityReportTokenUsed(trackingID string, at time.Time) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`UPDATE security_reports SET token_last_used = ? WHERE tracking_id = ?`,
		at, trackingID,
	)
	return err
}

// UpdateSecurityReportStatus advances a report's triage state and researcher-
// visible comment. When status is Disclosed, disclosed_at is stamped.
func (s *SQLiteDB) UpdateSecurityReportStatus(trackingID, status, maintainerComment string) error {
	ctx, cancel := dbCtx(dbWriteTimeout)
	defer cancel()
	now := time.Now()
	var disclosedAt *time.Time
	if status == SecStatusDisclosed {
		disclosedAt = &now
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE security_reports
			SET status = ?, maintainer_comment = ?, updated_at = ?,
			    disclosed_at = COALESCE(?, disclosed_at)
			WHERE tracking_id = ?`,
		status, maintainerComment, now, disclosedAt, trackingID,
	)
	return err
}

// ListDisclosedSecurityReports returns disclosed reports whose researcher opted
// into credit, newest first — the source for the public acknowledgments page.
func (s *SQLiteDB) ListDisclosedSecurityReports() ([]*SecurityReport, error) {
	ctx, cancel := dbCtx(dbComplexTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx,
		`SELECT tracking_id, status, severity, component, credit_preference,
			credit_name, disclosed_at
		 FROM security_reports
		 WHERE status = ? AND credit_preference IN ('name','handle','anonymous')
		 ORDER BY disclosed_at DESC`, SecStatusDisclosed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*SecurityReport
	for rows.Next() {
		r := &SecurityReport{}
		var disclosedAt sql.NullTime
		if err := rows.Scan(&r.TrackingID, &r.Status, &r.Severity, &r.Component,
			&r.CreditPreference, &r.CreditName, &disclosedAt); err != nil {
			return nil, err
		}
		if disclosedAt.Valid {
			r.DisclosedAt = &disclosedAt.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
