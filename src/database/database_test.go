package database_test

// Tests for the SQLiteDB implementation.
// Each test spins up a fresh in-process SQLite database so there are no
// external dependencies and tests are safe to run in parallel.

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/model"
)

// newTestDB creates an isolated SQLite database in a temp directory and
// registers cleanup to close and remove it when the test finishes.
func newTestDB(t *testing.T) database.DB {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// samplePaste builds a minimal valid Paste for insertion.
func samplePaste(id string) *model.Paste {
	return &model.Paste{
		ID:              id,
		Title:           "Test Paste",
		Content:         "hello world",
		Language:        "text",
		Visibility:      model.VisibilityPublic,
		BurnAfter:       0,
		DeleteTokenHash: "",
		Views:           0,
	}
}

// ─── Paste CRUD ───────────────────────────────────────────────────────────────

// TestCreateAndGetPaste verifies a round-trip: insert, retrieve, check every field.
func TestCreateAndGetPaste(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("abc12345")
	p.Title = "Round-trip"
	p.Content = "some content"
	p.Language = "go"
	p.Visibility = model.VisibilityUnlisted

	if err := db.CreatePaste(p); err != nil {
		t.Fatalf("CreatePaste: %v", err)
	}

	got, err := db.GetPasteByID("abc12345")
	if err != nil {
		t.Fatalf("GetPasteByID: %v", err)
	}
	if got == nil {
		t.Fatal("GetPasteByID: got nil, want paste")
	}
	if got.ID != p.ID {
		t.Errorf("ID: got %q, want %q", got.ID, p.ID)
	}
	if got.Title != p.Title {
		t.Errorf("Title: got %q, want %q", got.Title, p.Title)
	}
	if got.Content != p.Content {
		t.Errorf("Content: got %q, want %q", got.Content, p.Content)
	}
	if got.Language != p.Language {
		t.Errorf("Language: got %q, want %q", got.Language, p.Language)
	}
	if got.Visibility != p.Visibility {
		t.Errorf("Visibility: got %d, want %d", got.Visibility, p.Visibility)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

// TestGetPaste_NotFound expects nil,nil for an ID that was never inserted.
func TestGetPaste_NotFound(t *testing.T) {
	db := newTestDB(t)

	got, err := db.GetPasteByID("doesnotexist")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil paste, got %+v", got)
	}
}

// TestDeletePaste inserts a paste, deletes it, and confirms it is gone.
func TestDeletePaste(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("del00001")
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePaste("del00001"); err != nil {
		t.Fatalf("DeletePaste: %v", err)
	}
	got, err := db.GetPasteByID("del00001")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil after delete, paste still exists")
	}
}

// TestDeletePasteByToken_WrongToken confirms that a mismatched token returns an error.
func TestDeletePasteByToken_WrongToken(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("tok00001")
	p.DeleteTokenHash = "abc"
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	err := db.DeletePasteByToken("tok00001", "def")
	if err == nil {
		t.Fatal("expected error with wrong token, got nil")
	}
}

// TestDeletePasteByToken_Correct confirms deletion succeeds with the correct hash.
func TestDeletePasteByToken_Correct(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("tok00002")
	p.DeleteTokenHash = "correcthash"
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	if err := db.DeletePasteByToken("tok00002", "correcthash"); err != nil {
		t.Fatalf("DeletePasteByToken with correct token: %v", err)
	}

	got, _ := db.GetPasteByID("tok00002")
	if got != nil {
		t.Error("paste should be gone after token deletion")
	}
}

// TestDeleteExpiredPastes inserts a paste with an expiry in the past and verifies
// DeleteExpiredPastes removes it.
func TestDeleteExpiredPastes(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("exp00001")
	past := time.Now().Add(-time.Hour)
	p.ExpiresAt = &past
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteExpiredPastes()
	if err != nil {
		t.Fatalf("DeleteExpiredPastes: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deleted, got %d", n)
	}

	got, _ := db.GetPasteByID("exp00001")
	if got != nil {
		t.Error("expired paste should be gone")
	}
}

// TestDeleteBurnedPastes inserts a paste that has reached its burn limit and verifies
// DeleteBurnedPastes removes it.
func TestDeleteBurnedPastes(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("burn0001")
	p.BurnAfter = 2
	p.Views = 2
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteBurnedPastes()
	if err != nil {
		t.Fatalf("DeleteBurnedPastes: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deleted, got %d", n)
	}

	got, _ := db.GetPasteByID("burn0001")
	if got != nil {
		t.Error("burned paste should be gone")
	}
}

// TestGetPublicPastes inserts 3 public pastes and confirms all are returned on page 1.
func TestGetPublicPastes(t *testing.T) {
	db := newTestDB(t)

	ids := []string{"pub00001", "pub00002", "pub00003"}
	for _, id := range ids {
		p := samplePaste(id)
		p.Visibility = model.VisibilityPublic
		if err := db.CreatePaste(p); err != nil {
			t.Fatalf("CreatePaste %s: %v", id, err)
		}
	}

	items, total, err := db.GetPublicPastes(1, 10)
	if err != nil {
		t.Fatalf("GetPublicPastes: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d, want 3", total)
	}
	if len(items) != 3 {
		t.Errorf("len(items): got %d, want 3", len(items))
	}
}

// TestSearchPublicPastes verifies search-by-id/title/content matching and
// sort-by-name/date/views ordering in both directions (TODO.md items 1-3).
func TestSearchPublicPastes(t *testing.T) {
	db := newTestDB(t)

	specs := []struct {
		id, title, content string
		views              int
	}{
		{"aaa00001", "Zebra notes", "unique-alpha-content", 5},
		{"bbb00002", "Apple pie", "unique-beta-content", 20},
		{"ccc00003", "Mango salad", "search-target-here", 10},
	}
	for _, s := range specs {
		p := samplePaste(s.id)
		p.Title = s.title
		p.Content = s.content
		p.Visibility = model.VisibilityPublic
		if err := db.CreatePaste(p); err != nil {
			t.Fatalf("CreatePaste %s: %v", s.id, err)
		}
		for n := 0; n < s.views; n++ {
			if _, _, err := db.IncrementViewsAndCheckBurn(s.id); err != nil {
				t.Fatalf("IncrementViewsAndCheckBurn %s: %v", s.id, err)
			}
		}
	}

	t.Run("search by id", func(t *testing.T) {
		items, total, err := db.SearchPublicPastes(1, 10, "aaa00001", "", "")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != "aaa00001" {
			t.Fatalf("got total=%d items=%v, want 1 match for aaa00001", total, items)
		}
	})

	t.Run("search by title", func(t *testing.T) {
		items, total, err := db.SearchPublicPastes(1, 10, "Mango", "", "")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != "ccc00003" {
			t.Fatalf("got total=%d items=%v, want 1 match for Mango title", total, items)
		}
	})

	t.Run("search by content", func(t *testing.T) {
		items, total, err := db.SearchPublicPastes(1, 10, "search-target-here", "", "")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 1 || len(items) != 1 || items[0].ID != "ccc00003" {
			t.Fatalf("got total=%d items=%v, want 1 match for content search", total, items)
		}
	})

	t.Run("no match", func(t *testing.T) {
		items, total, err := db.SearchPublicPastes(1, 10, "nonexistent-term-xyz", "", "")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 0 || len(items) != 0 {
			t.Fatalf("got total=%d items=%v, want no matches", total, items)
		}
	})

	t.Run("sort by name asc", func(t *testing.T) {
		items, total, err := db.SearchPublicPastes(1, 10, "", "name", "asc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 3 || len(items) != 3 {
			t.Fatalf("got total=%d len=%d, want 3", total, len(items))
		}
		want := []string{"Apple pie", "Mango salad", "Zebra notes"}
		for i, w := range want {
			if items[i].Title != w {
				t.Errorf("items[%d].Title: got %q, want %q", i, items[i].Title, w)
			}
		}
	})

	t.Run("sort by name desc", func(t *testing.T) {
		items, _, err := db.SearchPublicPastes(1, 10, "", "name", "desc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		want := []string{"Zebra notes", "Mango salad", "Apple pie"}
		for i, w := range want {
			if items[i].Title != w {
				t.Errorf("items[%d].Title: got %q, want %q", i, items[i].Title, w)
			}
		}
	})

	t.Run("sort by views desc", func(t *testing.T) {
		items, _, err := db.SearchPublicPastes(1, 10, "", "views", "desc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		want := []string{"bbb00002", "ccc00003", "aaa00001"}
		for i, w := range want {
			if items[i].ID != w {
				t.Errorf("items[%d].ID: got %q, want %q", i, items[i].ID, w)
			}
		}
	})

	t.Run("sort by date desc is default", func(t *testing.T) {
		defaultItems, _, err := db.SearchPublicPastes(1, 10, "", "", "")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		dateItems, _, err := db.SearchPublicPastes(1, 10, "", "date", "desc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if len(defaultItems) != len(dateItems) {
			t.Fatalf("len mismatch: %d vs %d", len(defaultItems), len(dateItems))
		}
		for i := range defaultItems {
			if defaultItems[i].ID != dateItems[i].ID {
				t.Errorf("order mismatch at %d: %q vs %q", i, defaultItems[i].ID, dateItems[i].ID)
			}
		}
	})

	t.Run("pagination", func(t *testing.T) {
		page1, total, err := db.SearchPublicPastes(1, 2, "", "name", "asc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 3 || len(page1) != 2 {
			t.Fatalf("page1: got total=%d len=%d, want total=3 len=2", total, len(page1))
		}
		page2, total, err := db.SearchPublicPastes(2, 2, "", "name", "asc")
		if err != nil {
			t.Fatalf("SearchPublicPastes: %v", err)
		}
		if total != 3 || len(page2) != 1 {
			t.Fatalf("page2: got total=%d len=%d, want total=3 len=1", total, len(page2))
		}
	})
}

// TestIncrementViews verifies the view counter increments correctly.
func TestIncrementViews(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("views001")
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	views, burned, err := db.IncrementViewsAndCheckBurn("views001")
	if err != nil {
		t.Fatalf("IncrementViewsAndCheckBurn: %v", err)
	}
	if views != 1 {
		t.Errorf("views: got %d, want 1", views)
	}
	if burned {
		t.Errorf("burned: got true, want false (BurnAfter=0 means no burn)")
	}

	got, err := db.GetPasteByID("views001")
	if err != nil || got == nil {
		t.Fatalf("GetPasteByID after increment: %v / nil=%v", err, got == nil)
	}
	if got.Views != 1 {
		t.Errorf("Views: got %d, want 1", got.Views)
	}
}

// TestIncrementViewsBurnsAtThreshold verifies the paste is deleted once its
// view count reaches BurnAfter, in the same call that pushed it over.
func TestIncrementViewsBurnsAtThreshold(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("burn0001")
	p.BurnAfter = 2
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	views, burned, err := db.IncrementViewsAndCheckBurn("burn0001")
	if err != nil {
		t.Fatalf("IncrementViewsAndCheckBurn (1st): %v", err)
	}
	if views != 1 || burned {
		t.Fatalf("1st call: got views=%d burned=%v, want views=1 burned=false", views, burned)
	}

	views, burned, err = db.IncrementViewsAndCheckBurn("burn0001")
	if err != nil {
		t.Fatalf("IncrementViewsAndCheckBurn (2nd): %v", err)
	}
	if views != 2 || !burned {
		t.Fatalf("2nd call: got views=%d burned=%v, want views=2 burned=true", views, burned)
	}

	got, err := db.GetPasteByID("burn0001")
	if err != nil {
		t.Fatalf("GetPasteByID after burn: %v", err)
	}
	if got != nil {
		t.Errorf("paste still exists after burn threshold reached")
	}
}

// TestIncrementViewsBurnConcurrent proves the check-then-act race is closed:
// with BurnAfter=1, N concurrent callers must see exactly one "burned=true"
// result, never more (which would mean the paste was double-served past its
// configured limit) and never zero (which would mean it was never burned).
// Every other caller must see a "paste not found" error — once the winning
// transaction commits the delete, no later transaction can observe the row,
// so a concurrent caller can never serve a stale read past the threshold.
func TestIncrementViewsBurnConcurrent(t *testing.T) {
	db := newTestDB(t)

	p := samplePaste("burnrace")
	p.BurnAfter = 1
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	var burnedCount, notFoundCount int32
	unexpected := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, burned, err := db.IncrementViewsAndCheckBurn("burnrace")
			switch {
			case err != nil && err.Error() == "paste not found":
				atomic.AddInt32(&notFoundCount, 1)
			case err != nil:
				unexpected <- err
			case burned:
				atomic.AddInt32(&burnedCount, 1)
			default:
				unexpected <- fmt.Errorf("call succeeded with burned=false, want burned=true or \"paste not found\"")
			}
		}()
	}
	wg.Wait()
	close(unexpected)

	for err := range unexpected {
		t.Errorf("IncrementViewsAndCheckBurn concurrent call: %v", err)
	}
	if burnedCount != 1 {
		t.Errorf("burnedCount: got %d, want exactly 1", burnedCount)
	}
	if notFoundCount != n-1 {
		t.Errorf("notFoundCount: got %d, want %d", notFoundCount, n-1)
	}
}

// ─── Scheduler ────────────────────────────────────────────────────────────────

// TestSchedulerUpsertAndGet upserts a TaskState and retrieves it, verifying fields.
func TestSchedulerUpsertAndGet(t *testing.T) {
	db := newTestDB(t)

	now := time.Now().Truncate(time.Second)
	ts := &database.TaskState{
		TaskID:     "task-001",
		TaskName:   "ssl_renewal",
		Schedule:   "0 3 * * *",
		LastRun:    now,
		LastStatus: "success",
		LastError:  "",
		NextRun:    now.Add(24 * time.Hour),
		RunCount:   5,
		FailCount:  1,
		Enabled:    true,
	}

	if err := db.UpsertSchedulerTask(ts); err != nil {
		t.Fatalf("UpsertSchedulerTask: %v", err)
	}

	got, err := db.GetSchedulerTask("task-001")
	if err != nil {
		t.Fatalf("GetSchedulerTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetSchedulerTask: got nil")
	}
	if got.TaskID != ts.TaskID {
		t.Errorf("TaskID: got %q, want %q", got.TaskID, ts.TaskID)
	}
	if got.TaskName != ts.TaskName {
		t.Errorf("TaskName: got %q, want %q", got.TaskName, ts.TaskName)
	}
	if got.Schedule != ts.Schedule {
		t.Errorf("Schedule: got %q, want %q", got.Schedule, ts.Schedule)
	}
	if got.LastStatus != ts.LastStatus {
		t.Errorf("LastStatus: got %q, want %q", got.LastStatus, ts.LastStatus)
	}
	if got.Enabled != ts.Enabled {
		t.Errorf("Enabled: got %v, want %v", got.Enabled, ts.Enabled)
	}
}

// TestSchedulerListTasks upserts 2 tasks and verifies ListSchedulerTasks returns both.
func TestSchedulerListTasks(t *testing.T) {
	db := newTestDB(t)

	for _, id := range []string{"task-a", "task-b"} {
		ts := &database.TaskState{
			TaskID:     id,
			TaskName:   id,
			Schedule:   "* * * * *",
			LastStatus: "pending",
			Enabled:    true,
		}
		if err := db.UpsertSchedulerTask(ts); err != nil {
			t.Fatalf("UpsertSchedulerTask %s: %v", id, err)
		}
	}

	list, err := db.ListSchedulerTasks()
	if err != nil {
		t.Fatalf("ListSchedulerTasks: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len: got %d, want 2", len(list))
	}
}

// TestUpdateTaskRun upserts a task then updates its run metadata, verifying retrieval.
func TestUpdateTaskRun(t *testing.T) {
	db := newTestDB(t)

	ts := &database.TaskState{
		TaskID:     "task-upd",
		TaskName:   "token_cleanup",
		Schedule:   "0 * * * *",
		LastStatus: "pending",
		Enabled:    true,
	}
	if err := db.UpsertSchedulerTask(ts); err != nil {
		t.Fatal(err)
	}

	ran := time.Now().Truncate(time.Second)
	next := ran.Add(time.Hour)
	if err := db.UpdateTaskRun("task-upd", ran, "success", "", 1, 0, next); err != nil {
		t.Fatalf("UpdateTaskRun: %v", err)
	}

	got, err := db.GetSchedulerTask("task-upd")
	if err != nil || got == nil {
		t.Fatalf("GetSchedulerTask: %v / nil=%v", err, got == nil)
	}
	if got.LastStatus != "success" {
		t.Errorf("LastStatus: got %q, want %q", got.LastStatus, "success")
	}
	if got.RunCount != 1 {
		t.Errorf("RunCount: got %d, want 1", got.RunCount)
	}
	if got.FailCount != 0 {
		t.Errorf("FailCount: got %d, want 0", got.FailCount)
	}
}

// TestRecordTaskHistory verifies that RecordTaskHistory does not return an error.
// We trust the underlying INSERT; a separate query would just re-test the driver.
func TestRecordTaskHistory(t *testing.T) {
	db := newTestDB(t)

	// Upsert the parent task first to satisfy any FK constraints.
	ts := &database.TaskState{
		TaskID:     "task-hist",
		TaskName:   "healthcheck_self",
		Schedule:   "*/5 * * * *",
		LastStatus: "pending",
		Enabled:    true,
	}
	if err := db.UpsertSchedulerTask(ts); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	h := &database.TaskHistory{
		TaskID:     "task-hist",
		StartedAt:  now,
		FinishedAt: now.Add(200 * time.Millisecond),
		Status:     "success",
		ErrorMsg:   "",
		DurationMS: 200,
	}
	if err := db.RecordTaskHistory(h); err != nil {
		t.Fatalf("RecordTaskHistory: %v", err)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// makeToken generates a random 32-byte token, returning the base64url-encoded
// raw string, its SHA-256 hash as [32]byte, the hex digest, and the 12-char prefix.
func makeToken(t *testing.T) (raw string, hashBytes [32]byte, hashHex string, prefix string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		t.Fatal(err)
	}
	raw = base64.URLEncoding.EncodeToString(b)
	hashBytes = sha256.Sum256([]byte(raw))
	hashHex = hex.EncodeToString(hashBytes[:])
	prefix = raw[:12]
	return raw, hashBytes, hashHex, prefix
}

// minimalTask returns a TaskState suitable for prerequisite upserts.
func minimalTask(id string) *database.TaskState {
	return &database.TaskState{
		TaskID:     id,
		TaskName:   id,
		Schedule:   "* * * * *",
		LastStatus: "pending",
		Enabled:    true,
	}
}

// ─── Type and Ping ────────────────────────────────────────────────────────────

// TestDB_TypeAndPing verifies Type returns "sqlite" and Ping returns nil.
func TestDB_TypeAndPing(t *testing.T) {
	db := newTestDB(t)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"Type", func() error {
			if got := db.Type(); got != "sqlite" {
				t.Errorf("Type: got %q, want %q", got, "sqlite")
			}
			return nil
		}},
		{"Ping", func() error { return db.Ping() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
		})
	}
}

// ─── CountPastes ──────────────────────────────────────────────────────────────

// TestCountPastes verifies that CountPastes returns 0 on an empty DB and 1 after insert.
func TestCountPastes(t *testing.T) {
	db := newTestDB(t)

	cases := []struct {
		name  string
		setup func()
		want  int64
	}{
		{"empty", func() {}, 0},
		{"after_insert", func() {
			if err := db.CreatePaste(samplePaste("cnt00001")); err != nil {
				t.Fatal(err)
			}
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			got, err := db.CountPastes()
			if err != nil {
				t.Fatalf("CountPastes: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// ─── SetTaskEnabled ───────────────────────────────────────────────────────────

// TestSetTaskEnabled upserts a task, disables it, confirms Enabled==false,
// then re-enables it and confirms Enabled==true.
func TestSetTaskEnabled(t *testing.T) {
	db := newTestDB(t)

	if err := db.UpsertSchedulerTask(minimalTask("task-en")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		enabled bool
	}{
		{"disable", false},
		{"re_enable", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.SetTaskEnabled("task-en", tc.enabled); err != nil {
				t.Fatalf("SetTaskEnabled(%v): %v", tc.enabled, err)
			}
			got, err := db.GetSchedulerTask("task-en")
			if err != nil || got == nil {
				t.Fatalf("GetSchedulerTask: %v / nil=%v", err, got == nil)
			}
			if got.Enabled != tc.enabled {
				t.Errorf("Enabled: got %v, want %v", got.Enabled, tc.enabled)
			}
		})
	}
}

// ─── ListTaskHistory ──────────────────────────────────────────────────────────

// TestListTaskHistory_DefaultLimit inserts 25 history entries, then verifies that
// a limit of 0 returns 20 (the default) and a limit of 5 returns exactly 5.
func TestListTaskHistory_DefaultLimit(t *testing.T) {
	db := newTestDB(t)

	if err := db.UpsertSchedulerTask(minimalTask("task-lh")); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	for i := 0; i < 25; i++ {
		h := &database.TaskHistory{
			TaskID:     "task-lh",
			StartedAt:  now.Add(time.Duration(i) * time.Second),
			FinishedAt: now.Add(time.Duration(i)*time.Second + 100*time.Millisecond),
			Status:     "success",
			DurationMS: 100,
		}
		if err := db.RecordTaskHistory(h); err != nil {
			t.Fatalf("RecordTaskHistory #%d: %v", i, err)
		}
	}

	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"default_zero_gives_20", 0, 20},
		{"explicit_5", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.ListTaskHistory("task-lh", tc.limit)
			if err != nil {
				t.Fatalf("ListTaskHistory(limit=%d): %v", tc.limit, err)
			}
			if len(got) != tc.want {
				t.Errorf("len: got %d, want %d", len(got), tc.want)
			}
		})
	}
}

// ─── EnsureAppSecret ──────────────────────────────────────────────────────────

// TestEnsureAppSecret_Idempotent verifies that EnsureAppSecret returns 32 bytes
// on first call and the identical bytes on a second call for the same key.
func TestEnsureAppSecret_Idempotent(t *testing.T) {
	db := newTestDB(t)

	first, err := db.EnsureAppSecret("test-secret")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(first) != 32 {
		t.Errorf("first call: got %d bytes, want 32", len(first))
	}

	second, err := db.EnsureAppSecret("test-secret")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(first) != string(second) {
		t.Error("second call returned different bytes than the first")
	}
}

// TestEnsureAppSecret_DifferentKeys verifies that two different keys each return
// their own independent 32-byte secret.
func TestEnsureAppSecret_DifferentKeys(t *testing.T) {
	db := newTestDB(t)

	a, err := db.EnsureAppSecret("key-alpha")
	if err != nil {
		t.Fatalf("key-alpha: %v", err)
	}
	b, err := db.EnsureAppSecret("key-beta")
	if err != nil {
		t.Fatalf("key-beta: %v", err)
	}
	if string(a) == string(b) {
		t.Error("different keys unexpectedly returned identical secrets")
	}
}

// ─── API Tokens ───────────────────────────────────────────────────────────────

// TestCreateAndVerifyAPIToken creates a token then verifies it succeeds.
func TestCreateAndVerifyAPIToken(t *testing.T) {
	db := newTestDB(t)

	_, hashBytes, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", nil); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := db.VerifyAPIToken(hashBytes, "paste", "paste-001"); err != nil {
		t.Errorf("VerifyAPIToken: unexpected error: %v", err)
	}
}

// TestVerifyAPIToken_WrongResource verifies that VerifyAPIToken returns an error
// when the resource ID does not match the stored row.
func TestVerifyAPIToken_WrongResource(t *testing.T) {
	db := newTestDB(t)

	_, hashBytes, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", nil); err != nil {
		t.Fatal(err)
	}
	err := db.VerifyAPIToken(hashBytes, "paste", "paste-WRONG")
	if err == nil {
		t.Error("expected error for wrong resourceID, got nil")
	}
}

// TestValidateAPIToken_Valid creates a token, then validates it by resource type only.
func TestValidateAPIToken_Valid(t *testing.T) {
	db := newTestDB(t)

	_, hashBytes, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateAPIToken(hashBytes, "paste"); err != nil {
		t.Errorf("ValidateAPIToken: unexpected error: %v", err)
	}
}

// TestValidateAPIToken_Unknown confirms ValidateAPIToken returns an error when no
// matching token exists.
func TestValidateAPIToken_Unknown(t *testing.T) {
	db := newTestDB(t)

	_, hashBytes, _, _ := makeToken(t)
	if err := db.ValidateAPIToken(hashBytes, "paste"); err == nil {
		t.Error("expected error for unknown token, got nil")
	}
}

// TestRevokeAPIToken verifies that a token can be revoked once, and that a second
// revocation attempt on the same prefix returns an error.
func TestRevokeAPIToken(t *testing.T) {
	db := newTestDB(t)

	_, _, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", nil); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		wantErr bool
	}{
		{"first_revoke", false},
		{"second_revoke_already_revoked", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := db.RevokeAPIToken(prefix, "test reason")
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestListAPITokens creates 2 tokens, revokes one, and asserts only 1 is returned.
func TestListAPITokens(t *testing.T) {
	db := newTestDB(t)

	_, _, hashHex1, prefix1 := makeToken(t)
	_, _, hashHex2, prefix2 := makeToken(t)

	if err := db.CreateAPIToken(hashHex1, prefix1, "paste", "paste-001", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAPIToken(hashHex2, prefix2, "paste", "paste-002", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeAPIToken(prefix1, "cleanup"); err != nil {
		t.Fatal(err)
	}

	tokens, err := db.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("len: got %d, want 1", len(tokens))
	}
	if len(tokens) == 1 && tokens[0].TokenPrefix != prefix2 {
		t.Errorf("remaining token prefix: got %q, want %q", tokens[0].TokenPrefix, prefix2)
	}
}

// TestGetAPITokenByPrefix creates a token and confirms it can be fetched by
// prefix, that revoked tokens are not returned, and that an unknown prefix
// errors (AI.md Compliance: data export/delete scope by owner-token prefix).
func TestGetAPITokenByPrefix(t *testing.T) {
	db := newTestDB(t)

	_, _, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", nil); err != nil {
		t.Fatal(err)
	}

	t.Run("found", func(t *testing.T) {
		rec, err := db.GetAPITokenByPrefix(prefix)
		if err != nil {
			t.Fatalf("GetAPITokenByPrefix: %v", err)
		}
		if rec.TokenPrefix != prefix {
			t.Errorf("TokenPrefix: got %q, want %q", rec.TokenPrefix, prefix)
		}
		if rec.ResourceType != "paste" || rec.ResourceID != "paste-001" {
			t.Errorf("resource: got %q/%q, want paste/paste-001", rec.ResourceType, rec.ResourceID)
		}
	})

	t.Run("unknown_prefix", func(t *testing.T) {
		if _, err := db.GetAPITokenByPrefix("tok_doesnotexist"); err == nil {
			t.Error("expected error for unknown prefix, got nil")
		}
	})

	t.Run("revoked_not_returned", func(t *testing.T) {
		if err := db.RevokeAPIToken(prefix, "test cleanup"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetAPITokenByPrefix(prefix); err == nil {
			t.Error("expected error for revoked token, got nil")
		}
	})
}

// TestDeleteExpiredAPITokens creates a token with an expiry in the past and
// verifies DeleteExpiredAPITokens removes it and returns a count of 1.
func TestDeleteExpiredAPITokens(t *testing.T) {
	db := newTestDB(t)

	_, _, hashHex, prefix := makeToken(t)
	past := time.Now().Add(-time.Hour)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", "paste-001", &past); err != nil {
		t.Fatal(err)
	}

	n, err := db.DeleteExpiredAPITokens()
	if err != nil {
		t.Fatalf("DeleteExpiredAPITokens: %v", err)
	}
	if n != 1 {
		t.Errorf("rows deleted: got %d, want 1", n)
	}
}

// ─── NewDatabase default type ─────────────────────────────────────────────────

// TestNewDatabase_DefaultType verifies that passing an empty type string to
// NewDatabase falls back to SQLite and returns a working DB.
func TestNewDatabase_DefaultType(t *testing.T) {
	dir := t.TempDir()
	db, err := database.NewDatabase("", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewDatabase(\"\", ...): %v", err)
	}
	if db == nil {
		t.Fatal("NewDatabase returned nil")
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Type(); got != "sqlite" {
		t.Errorf("Type: got %q, want %q", got, "sqlite")
	}
	if err := db.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOwnerTokenLifecycle(t *testing.T) {
	db := newTestDB(t)
	ownerDB, ok := db.(database.OwnerDB)
	if !ok {
		t.Fatal("SQLite database does not implement OwnerDB")
	}
	_, primaryHash, primaryHashHex, primaryPrefix := makeToken(t)
	if err := ownerDB.CreateOwnerWithPrimaryToken("owner-one", primaryHashHex, primaryPrefix, "test", "encrypted-primary"); err != nil {
		t.Fatal(err)
	}
	primary, err := ownerDB.GetOwnerTokenByHash(primaryHash)
	if err != nil || primary.Kind != "primary" || primary.OwnerID != "owner-one" {
		t.Fatalf("primary token lookup = %#v, %v", primary, err)
	}
	expiry := time.Now().Add(24 * time.Hour)
	_, linkedHash, linkedHashHex, linkedPrefix := makeToken(t)
	linkedID, err := ownerDB.CreateLinkedToken("owner-one", linkedHashHex, linkedPrefix, "test", "automation", "encrypted-linked", &expiry, 25)
	if err != nil {
		t.Fatal(err)
	}
	_, _, overLimitHashHex, overLimitPrefix := makeToken(t)
	if _, err := ownerDB.CreateLinkedToken("owner-one", overLimitHashHex, overLimitPrefix, "more", "over-limit", "encrypted", nil, 1); err == nil {
		t.Fatal("linked token limit was not enforced")
	}
	listed, err := ownerDB.ListOwnerTokens("owner-one")
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListOwnerTokens = %d records, %v", len(listed), err)
	}
	for _, token := range listed {
		if token.TokenEnc != "" {
			t.Fatal("encrypted token copy leaked from owner token listing")
		}
		if token.TokenSuffix == "" {
			t.Fatal("token suffix missing from owner listing")
		}
	}
	detailed, err := ownerDB.GetOwnerTokenByID("owner-one", linkedID)
	if err != nil || detailed.TokenEnc != "encrypted-linked" {
		t.Fatalf("scoped token detail lookup = %#v, %v", detailed, err)
	}
	if _, err := ownerDB.GetOwnerTokenByHash(linkedHash); err != nil {
		t.Fatalf("linked token lookup: %v", err)
	}
	_, rotatedHash, rotatedHashHex, rotatedPrefix := makeToken(t)
	if err := ownerDB.RotateOwnerToken("owner-one", linkedID, rotatedHashHex, rotatedPrefix, "test", "rotated-linked"); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerDB.GetOwnerTokenByHash(linkedHash); err == nil {
		t.Fatal("old linked token remained valid after rotation")
	}
	if _, err := ownerDB.GetOwnerTokenByHash(rotatedHash); err != nil {
		t.Fatalf("rotated token lookup: %v", err)
	}
	if err := ownerDB.UpdateOwnerToken("owner-one", linkedID, "renamed", nil, 25); err != nil {
		t.Fatal(err)
	}
	if err := ownerDB.RevokeOwnerToken("owner-one", linkedID); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerDB.GetOwnerTokenByHash(rotatedHash); err == nil {
		t.Fatal("revoked linked token remained valid")
	}
}

func TestCreatePasteWithPrimaryTokenIsAtomic(t *testing.T) {
	db := newTestDB(t)
	ownerDB := db.(database.OwnerDB)
	_, _, hashHex, prefix := makeToken(t)
	paste := samplePaste("owned-paste")
	paste.OwnerID = "owner-atomic"
	if err := ownerDB.CreatePasteWithPrimaryToken(paste, hashHex, prefix, "test", "encrypted"); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetPasteByID(paste.ID)
	if err != nil || stored == nil || stored.OwnerID != "owner-atomic" {
		t.Fatalf("stored paste = %#v, %v", stored, err)
	}
	_, _, secondHashHex, secondPrefix := makeToken(t)
	second := samplePaste("rolled-back-paste")
	second.OwnerID = "owner-atomic"
	if err := ownerDB.CreatePasteWithPrimaryToken(second, secondHashHex, secondPrefix, "test", "encrypted"); err == nil {
		t.Fatal("duplicate owner create unexpectedly succeeded")
	}
	rolledBack, err := db.GetPasteByID(second.ID)
	if err != nil || rolledBack != nil {
		t.Fatalf("failed owner transaction persisted paste: %#v, %v", rolledBack, err)
	}
}

func TestLegacyTokenMigrationAssignsPasteOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := database.NewDatabase("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	p := samplePaste("legacy-paste")
	if err := db.CreatePaste(p); err != nil {
		t.Fatal(err)
	}
	_, hash, hashHex, prefix := makeToken(t)
	if err := db.CreateAPIToken(hashHex, prefix, "paste", p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.NewDatabase("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	upgraded, err := db.GetPasteByID(p.ID)
	if err != nil || upgraded == nil || upgraded.OwnerID == "" {
		t.Fatalf("paste owner migration = %#v, %v", upgraded, err)
	}
	ownerDB := db.(database.OwnerDB)
	credential, err := ownerDB.GetOwnerTokenByHash(hash)
	if err != nil || credential.OwnerID != upgraded.OwnerID || credential.Kind != "primary" {
		t.Fatalf("legacy credential migration = %#v, %v", credential, err)
	}
}
