package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

func cacheTestDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-cache-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func cacheTestPaste(id string) *model.Paste {
	return &model.Paste{ID: id, Title: id, Content: "cache body", Language: "text", Visibility: model.VisibilityPublic}
}

func newTestCachedDB(t *testing.T) (*CachedDB, *SQLiteDB) {
	t.Helper()
	dir := cacheTestDir(t)
	remote, err := newSQLiteDB(filepath.Join(dir, "remote.db"), DefaultPool())
	if err != nil {
		t.Fatal(err)
	}
	cached, err := newCachedDB(remote, LocalCacheConfig{Enabled: true, Path: filepath.Join(dir, "cache.db"), OfflineMode: true, MaxAge: time.Hour}, false)
	if err != nil {
		_ = remote.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cached.Close() })
	return cached, remote
}

func TestLocalCacheReconcilesUpdatesAndDeletesAtomically(t *testing.T) {
	cached, remote := newTestCachedDB(t)
	if err := remote.CreatePaste(cacheTestPaste("first")); err != nil {
		t.Fatal(err)
	}
	if err := cached.syncOnce(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if got, err := cached.cache.GetPasteByID("first"); err != nil || got == nil {
		t.Fatalf("initial snapshot paste=%v err=%v", got, err)
	}
	if err := remote.CreatePaste(cacheTestPaste("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.cache.db.ExecContext(t.Context(), `CREATE TRIGGER reject_second BEFORE INSERT ON pastes WHEN NEW.id = 'second' BEGIN SELECT RAISE(ABORT, 'injected cache failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := cached.syncOnce(); err == nil {
		t.Fatal("sync with an injected row failure succeeded")
	}
	if got, err := cached.cache.GetPasteByID("first"); err != nil || got == nil {
		t.Fatalf("failed batch changed prior snapshot: paste=%v err=%v", got, err)
	}
	if got, err := cached.cache.GetPasteByID("second"); err != nil || got != nil {
		t.Fatalf("failed batch partially applied second row: paste=%v err=%v", got, err)
	}
	if cached.dirty.Load() || !cached.offlineReadAllowed() {
		t.Fatal("failed atomic sync invalidated the still-fresh offline snapshot")
	}
	if _, err := cached.cache.db.ExecContext(t.Context(), `DROP TRIGGER reject_second`); err != nil {
		t.Fatal(err)
	}
	if err := remote.DeletePaste("first"); err != nil {
		t.Fatal(err)
	}
	if err := cached.syncOnce(); err != nil {
		t.Fatalf("reconciliation sync: %v", err)
	}
	if got, err := cached.cache.GetPasteByID("first"); err != nil || got != nil {
		t.Fatalf("reconciliation did not remove deleted row: paste=%v err=%v", got, err)
	}
	if got, err := cached.cache.GetPasteByID("second"); err != nil || got == nil {
		t.Fatalf("reconciliation did not add remote row: paste=%v err=%v", got, err)
	}
	if cached.dirty.Load() {
		t.Fatal("successful synchronization did not clear dirty state")
	}
}

func TestFailedRemoteSyncKeepsFreshSnapshotAvailableOffline(t *testing.T) {
	cached, _ := newTestCachedDB(t)
	if err := cached.remote.CreatePaste(cacheTestPaste("cached-paste")); err != nil {
		t.Fatal(err)
	}
	if err := cached.syncOnce(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if err := cached.remote.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cached.syncOnce(); err == nil {
		t.Fatal("sync against a closed remote database succeeded")
	}
	if cached.dirty.Load() {
		t.Fatal("remote outage marked the committed snapshot dirty")
	}
	got, err := cached.GetPasteByID("cached-paste")
	if err != nil || got == nil {
		t.Fatalf("offline read paste=%v err=%v, want cached paste", got, err)
	}
}

func TestSQLiteSnapshotIncludesCommittedWALPages(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	source, err := NewDatabase("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err := source.CreatePaste(cacheTestPaste("snapshot-paste")); err != nil {
		t.Fatal(err)
	}
	snapshotter, ok := source.(interface{ SnapshotTo(string) error })
	if !ok {
		t.Fatal("SQLite database does not support consistent snapshots")
	}
	if err := snapshotter.SnapshotTo(snapshotPath); err != nil {
		t.Fatalf("snapshot SQLite database: %v", err)
	}
	snapshot, err := NewDatabase("sqlite", snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	paste, err := snapshot.GetPasteByID("snapshot-paste")
	if err != nil {
		t.Fatalf("read committed paste from snapshot: %v", err)
	}
	if paste == nil || paste.Content != "cache body" {
		t.Fatalf("snapshot paste = %#v, want committed paste", paste)
	}
}

func TestSuccessfulRemoteWriteInvalidatesFailedCacheRowUpdate(t *testing.T) {
	cached, remote := newTestCachedDB(t)
	if err := cached.syncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := cached.cache.CreatePaste(cacheTestPaste("new-paste")); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.cache.db.ExecContext(t.Context(), `CREATE TRIGGER reject_insert BEFORE INSERT ON pastes WHEN NEW.id = 'new-paste' BEGIN SELECT RAISE(ABORT, 'injected cache failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := cached.cache.db.ExecContext(t.Context(), `CREATE TRIGGER reject_delete BEFORE DELETE ON pastes WHEN OLD.id = 'new-paste' BEGIN SELECT RAISE(ABORT, 'injected invalidation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := cached.CreatePaste(cacheTestPaste("new-paste")); err != nil {
		t.Fatalf("remote write should succeed when cache update fails: %v", err)
	}
	if got, err := remote.GetPasteByID("new-paste"); err != nil || got == nil {
		t.Fatalf("remote write was not retained: paste=%v err=%v", got, err)
	}
	if !cached.dirty.Load() {
		t.Fatal("failed cache invalidation did not mark the cache dirty")
	}
}

func TestSuccessfulRemoteWriteCacheRefresh(t *testing.T) {
	cached, _ := newTestCachedDB(t)
	if err := cached.syncOnce(); err != nil {
		t.Fatal(err)
	}
	if err := cached.CreatePaste(cacheTestPaste("new-paste")); err != nil {
		t.Fatal(err)
	}
	if got, err := cached.cache.GetPasteByID("new-paste"); err != nil || got == nil {
		t.Fatalf("successful write was not mirrored to cache: paste=%v err=%v", got, err)
	}
}

func TestRestoreSeedOnlyCopiesIntoEmptyRemote(t *testing.T) {
	dir := cacheTestDir(t)
	seed, err := newSQLiteDB(filepath.Join(dir, "restored.db"), DefaultPool())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seed.Close() }()
	if err := seed.CreatePaste(cacheTestPaste("restored-paste")); err != nil {
		t.Fatal(err)
	}
	target, err := newSQLiteDB(filepath.Join(dir, "target.db"), DefaultPool())
	if err != nil {
		t.Fatal(err)
	}
	target.driver = "libsql"
	defer func() { _ = target.Close() }()
	seeded, err := SeedRemoteFromSQLite(target, filepath.Join(dir, "restored.db"))
	if err != nil || !seeded {
		t.Fatalf("empty remote seed seeded=%t err=%v", seeded, err)
	}
	if got, err := target.GetPasteByID("restored-paste"); err != nil || got == nil {
		t.Fatalf("restored paste missing after seed: paste=%v err=%v", got, err)
	}
	if err := target.CreatePaste(cacheTestPaste("existing-paste")); err != nil {
		t.Fatal(err)
	}
	seeded, err = SeedRemoteFromSQLite(target, filepath.Join(dir, "restored.db"))
	if err != nil || seeded {
		t.Fatalf("non-empty remote was modified seeded=%t err=%v", seeded, err)
	}
	if got, err := target.GetPasteByID("existing-paste"); err != nil || got == nil {
		t.Fatalf("existing remote row was changed: paste=%v err=%v", got, err)
	}
}
