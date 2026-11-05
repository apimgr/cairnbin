package maintenance

// Tests for RunData's internal export/delete logic (AI.md Compliance: GDPR
// "Data export"/"Data deletion", CCPA "Data disclosure"/"Right to delete").

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/model"
)

func newDataTestDB(t *testing.T) database.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func noopAuditWriter() *audit.Writer {
	return audit.New(audit.Config{Enabled: false})
}

// seedPasteWithToken creates a paste and an owner token pointing at it,
// returning the token prefix used to scope data export/delete requests.
func seedPasteWithToken(t *testing.T, db database.DB, pasteID string) string {
	t.Helper()
	if err := db.CreatePaste(&model.Paste{
		ID:              pasteID,
		Title:           "test paste",
		Content:         "hello world",
		Language:        "text",
		Visibility:      0,
		DeleteTokenHash: "deadbeef",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("CreatePaste: %v", err)
	}

	prefix := "tok_" + pasteID[:8]
	hash := prefix + "hashhashhashhashhashhashhashhash"
	if err := db.CreateAPIToken(hash, prefix, "paste", pasteID, nil); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return prefix
}

func TestRunDataExport_Found(t *testing.T) {
	db := newDataTestDB(t)
	prefix := seedPasteWithToken(t, db, "paste-export-1")

	if err := runDataExport(db, noopAuditWriter(), prefix); err != nil {
		t.Fatalf("runDataExport: %v", err)
	}
}

func TestRunDataExport_UnknownPrefix(t *testing.T) {
	db := newDataTestDB(t)
	if err := runDataExport(db, noopAuditWriter(), "tok_doesnotexist"); err == nil {
		t.Error("expected error for unknown token prefix")
	}
}

func TestRunDataDelete_RemovesPasteAndRevokesToken(t *testing.T) {
	db := newDataTestDB(t)
	prefix := seedPasteWithToken(t, db, "paste-delete-1")

	if err := runDataDelete(db, noopAuditWriter(), prefix); err != nil {
		t.Fatalf("runDataDelete: %v", err)
	}

	if p, err := db.GetPasteByID("paste-delete-1"); err != nil || p != nil {
		t.Errorf("expected paste to be deleted (nil, nil), got paste=%v err=%v", p, err)
	}
	if _, err := db.GetAPITokenByPrefix(prefix); err == nil {
		t.Error("expected token to be revoked (not returned by GetAPITokenByPrefix)")
	}

	// A second delete against the now-revoked prefix must fail, not silently
	// no-op — the token lookup itself is the erasure gate.
	if err := runDataDelete(db, noopAuditWriter(), prefix); err == nil {
		t.Error("expected second delete against a revoked prefix to fail")
	}
}

func TestRunDataDelete_UnknownPrefix(t *testing.T) {
	db := newDataTestDB(t)
	if err := runDataDelete(db, noopAuditWriter(), "tok_doesnotexist"); err == nil {
		t.Error("expected error for unknown token prefix")
	}
}

// ─── RunData (exported entrypoint) ──────────────────────────────────────────

func TestRunData_EmptyPrefixRejected(t *testing.T) {
	dir := t.TempDir()
	if err := RunData("export", "", DataOptions{ConfigDir: dir, DBPath: filepath.Join(dir, "test.db")}); err == nil {
		t.Error("expected error for empty prefix")
	}
}

func TestRunData_UnknownActionRejected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := database.NewDatabase("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected database file to exist: %v", err)
	}

	if err := RunData("purge", "tok_abc12345", DataOptions{ConfigDir: dir, DBPath: dbPath}); err == nil {
		t.Error("expected error for an unknown action")
	}
}
