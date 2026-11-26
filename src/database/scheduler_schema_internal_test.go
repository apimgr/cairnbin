package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSchedulerSchemaMigrationRenamesTaskColumnsAndPreservesHistory(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scheduler-legacy.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	legacySchema := []string{
		`CREATE TABLE scheduler_tasks (task_id TEXT PRIMARY KEY, task_name TEXT NOT NULL, schedule TEXT NOT NULL, last_run DATETIME, last_status TEXT NOT NULL DEFAULT 'pending', last_error TEXT NOT NULL DEFAULT '', next_run DATETIME, run_count INTEGER NOT NULL DEFAULT 0, fail_count INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE scheduler_history (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT NOT NULL, ran_at DATETIME NOT NULL, status TEXT NOT NULL)`,
		`INSERT INTO scheduler_tasks (task_id, task_name, schedule) VALUES ('daily', 'Daily task', '@daily')`,
		`INSERT INTO scheduler_history (task_id, ran_at, status) VALUES ('daily', '2026-01-01T00:00:00Z', 'success')`,
	}
	for _, statement := range legacySchema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureSchema(db); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}
	columns, err := sqliteTableColumns(context.Background(), db, "scheduler_tasks")
	if err != nil {
		t.Fatal(err)
	}
	if !columns["id"] || !columns["name"] || columns["task_id"] || columns["task_name"] {
		t.Fatalf("scheduler_tasks columns = %#v", columns)
	}
	var id, name string
	if err := db.QueryRow(`SELECT id, name FROM scheduler_tasks`).Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	if id != "daily" || name != "Daily task" {
		t.Fatalf("migrated task = (%q, %q), want (daily, Daily task)", id, name)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM scheduler_history WHERE task_id = 'daily'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("history count = %d, %v; want 1", count, err)
	}
	historyColumns, err := sqliteTableColumns(context.Background(), db, "scheduler_history")
	if err != nil {
		t.Fatal(err)
	}
	if !historyColumns["started_at"] || historyColumns["ran_at"] || !historyColumns["finished_at"] {
		t.Fatalf("scheduler_history columns = %#v", historyColumns)
	}
	if _, err := db.Exec(`INSERT INTO scheduler_history (task_id, started_at, status) VALUES ('missing', '2026-01-02T00:00:00Z', 'success')`); err == nil {
		t.Fatal("history accepted a task_id without a scheduler_tasks.id")
	}
}
