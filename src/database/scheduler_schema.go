package database

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateSchedulerSchema(ctx context.Context, db *sql.DB) error {
	columns, err := sqliteTableColumns(ctx, db, "scheduler_tasks")
	if err != nil {
		return err
	}
	if err := renameSQLiteColumn(ctx, db, "scheduler_tasks", columns, "task_id", "id"); err != nil {
		return err
	}
	columns, err = sqliteTableColumns(ctx, db, "scheduler_tasks")
	if err != nil {
		return err
	}
	if err := renameSQLiteColumn(ctx, db, "scheduler_tasks", columns, "task_name", "name"); err != nil {
		return err
	}
	historyColumns, err := sqliteTableColumns(ctx, db, "scheduler_history")
	if err != nil {
		return err
	}
	if err := renameSQLiteColumn(ctx, db, "scheduler_history", historyColumns, "ran_at", "started_at"); err != nil {
		return err
	}
	for _, column := range []struct{ name, declaration string }{
		{"finished_at", "DATETIME"},
		{"error_msg", "TEXT NOT NULL DEFAULT ''"},
		{"duration_ms", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if !historyColumns[column.name] {
			if _, err := db.ExecContext(ctx, `ALTER TABLE scheduler_history ADD COLUMN `+column.name+` `+column.declaration); err != nil {
				return fmt.Errorf("add scheduler_history.%s: %w", column.name, err)
			}
			historyColumns[column.name] = true
		}
	}

	fk, err := hasSchedulerHistoryForeignKey(ctx, db)
	if err != nil || fk {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
		return err
	}
	statements := []string{
		`CREATE TABLE scheduler_history_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL REFERENCES scheduler_tasks(id),
			started_at DATETIME NOT NULL,
			finished_at DATETIME,
			status TEXT NOT NULL,
			error_msg TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO scheduler_history_new (id, task_id, started_at, finished_at, status, error_msg, duration_ms)
			SELECT id, task_id, started_at, finished_at, status, error_msg, duration_ms FROM scheduler_history`,
		`DROP TABLE scheduler_history`,
		`ALTER TABLE scheduler_history_new RENAME TO scheduler_history`,
		`CREATE INDEX IF NOT EXISTS idx_sched_history_task ON scheduler_history(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sched_history_started ON scheduler_history(started_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			return fmt.Errorf("migrate scheduler history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
		return fmt.Errorf("commit scheduler history migration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable scheduler foreign keys: %w", err)
	}
	return nil
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func renameSQLiteColumn(ctx context.Context, db *sql.DB, table string, columns map[string]bool, legacy, canonical string) error {
	if columns[legacy] && columns[canonical] {
		return fmt.Errorf("%s contains both %q and %q", table, legacy, canonical)
	}
	if columns[canonical] {
		return nil
	}
	if !columns[legacy] {
		return fmt.Errorf("%s is missing both %q and %q", table, legacy, canonical)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` RENAME COLUMN `+legacy+` TO `+canonical); err != nil {
		return fmt.Errorf("rename %s.%s to %s: %w", table, legacy, canonical, err)
	}
	return nil
}

func hasSchedulerHistoryForeignKey(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(scheduler_history)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, seq int
		var tableName, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &tableName, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, err
		}
		if tableName == "scheduler_tasks" && from == "task_id" && to == "id" {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}
