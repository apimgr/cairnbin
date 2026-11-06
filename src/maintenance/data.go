// data.go implements the --maintenance data export/delete subcommands
// (AI.md Compliance: GDPR "Data export"/"Data deletion", CCPA "Data
// disclosure"/"Right to delete"). This codebase has no user-account table —
// the owner-token prefix (the same identifier used by --maintenance token
// revoke) is the only per-resource-owner handle, so it is the scope for
// data-subject requests.
package maintenance

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

// DataOptions carries the resolved paths RunData needs. Mirrors PGPOptions.
type DataOptions struct {
	ConfigDir string
	DBPath    string
	LogDir    string
}

// dataExportRecord is the JSON shape written to stdout by "data export".
type dataExportRecord struct {
	RequestedAt  time.Time  `json:"requested_at"`
	TokenPrefix  string     `json:"token_prefix"`
	ResourceType string     `json:"resource_type"`
	ResourceID   string     `json:"resource_id"`
	TokenCreated time.Time  `json:"token_created_at"`
	TokenExpires *time.Time `json:"token_expires_at,omitempty"`
	Paste        any        `json:"paste,omitempty"`
}

// RunData executes a `--maintenance data <action> <prefix>` subcommand
// (AI.md Compliance). action is "export" or "delete"; prefix identifies the
// owner token (and therefore the paste) the request targets.
func RunData(action, prefix string, opts DataOptions) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("data: %s requires a token prefix (see --maintenance token list)", action)
	}

	cfgPath := filepath.Join(opts.ConfigDir, "server.yml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("data: load config: %w", err)
	}
	dbPath := cfg.Database.Path
	if strings.TrimSpace(dbPath) == "" {
		dbPath = opts.DBPath
	}
	db, err := database.NewDatabase(cfg.Database.Type, dbPath)
	if err != nil {
		return fmt.Errorf("data: open database: %w", err)
	}
	defer func() { _ = db.Close() }()

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

	switch action {
	case "export":
		return runDataExport(db, auditW, prefix)
	case "delete":
		return runDataDelete(db, auditW, prefix)
	default:
		return fmt.Errorf("data: unknown action %q (want export|delete)", action)
	}
}

func runDataExport(db database.DB, auditW *audit.Writer, prefix string) error {
	auditW.Log(audit.Entry{
		Event:    "compliance.data_export_requested",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  map[string]any{"token_prefix": prefix},
	})

	rec, err := db.GetAPITokenByPrefix(prefix)
	if err != nil {
		return fmt.Errorf("data export: %w", err)
	}

	out := dataExportRecord{
		RequestedAt:  time.Now().UTC(),
		TokenPrefix:  rec.TokenPrefix,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		TokenCreated: rec.CreatedAt,
		TokenExpires: rec.ExpiresAt,
	}
	if rec.ResourceType == "paste" {
		if paste, err := db.GetPasteByID(rec.ResourceID); err == nil {
			out.Paste = paste
		}
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("data export: encode: %w", err)
	}
	fmt.Println(string(b))

	auditW.Log(audit.Entry{
		Event:    "compliance.data_export_completed",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  map[string]any{"token_prefix": prefix, "file_size": len(b)},
	})
	return nil
}

func runDataDelete(db database.DB, auditW *audit.Writer, prefix string) error {
	auditW.Log(audit.Entry{
		Event:    "compliance.data_deletion_requested",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  map[string]any{"token_prefix": prefix},
	})

	rec, err := db.GetAPITokenByPrefix(prefix)
	if err != nil {
		return fmt.Errorf("data delete: %w", err)
	}

	if rec.ResourceType == "paste" {
		if err := db.DeletePaste(rec.ResourceID); err != nil {
			return fmt.Errorf("data delete: delete paste: %w", err)
		}
	}
	if err := db.RevokeAPIToken(prefix, "compliance data deletion"); err != nil {
		return fmt.Errorf("data delete: revoke token: %w", err)
	}

	// Right to erasure: the identifying resource is gone; the audit trail
	// above retains only the anonymous token prefix, satisfying both
	// erasure (PII/content removed) and retention (audit event preserved).
	auditW.Log(audit.Entry{
		Event:    "compliance.data_deletion_completed",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Details:  map[string]any{"token_prefix": prefix},
	})
	fmt.Printf("data: deleted resource for token prefix %s\n", prefix)
	return nil
}
