package database

import (
	"path/filepath"
	"testing"
)

func TestSystemOwnerIsCreatedAndNeverPurged(t *testing.T) {
	db, err := newSQLiteDB(filepath.Join(t.TempDir(), "system-owner.db"), DefaultPool())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var ownerID string
	if err := db.db.QueryRow(`SELECT id FROM owners WHERE id = 'system'`).Scan(&ownerID); err != nil {
		t.Fatalf("system owner missing after schema initialization: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE owners SET last_seen_at = 0 WHERE id = 'system'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurgeIdleOwners(1); err != nil {
		t.Fatalf("PurgeIdleOwners: %v", err)
	}
	if err := db.db.QueryRow(`SELECT id FROM owners WHERE id = 'system'`).Scan(&ownerID); err != nil {
		t.Fatalf("system owner was purged: %v", err)
	}
}
