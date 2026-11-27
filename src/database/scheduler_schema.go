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
	for _, column := range []struct{ name, declaration string }{
		{"schedule", "TEXT NOT NULL DEFAULT ''"},
		{"last_run", "INTEGER"},
		{"last_status", "TEXT"},
		{"last_error", "TEXT"},
		{"next_run", "INTEGER"},
		{"run_count", "INTEGER NOT NULL DEFAULT 0"},
		{"fail_count", "INTEGER NOT NULL DEFAULT 0"},
		{"enabled", "INTEGER NOT NULL DEFAULT 1"},
	} {
		if err := ensureSchedulerColumn(ctx, db, "scheduler_tasks", column.name, column.declaration); err != nil {
			return err
		}
	}
	for _, column := range []string{"last_run", "next_run"} {
		if err := migrateSchedulerTimestamp(ctx, db, "scheduler_tasks", column); err != nil {
			return err
		}
	}
	historyColumns, err := sqliteTableColumns(ctx, db, "scheduler_history")
	if err != nil {
		return err
	}
	if err := renameSQLiteColumn(ctx, db, "scheduler_history", historyColumns, "ran_at", "started_at"); err != nil {
		return err
	}
	for _, column := range []struct{ name, declaration string }{
		{"task_id", "TEXT NOT NULL DEFAULT ''"},
		{"started_at", "INTEGER NOT NULL DEFAULT 0"},
		{"finished_at", "INTEGER"},
		{"status", "TEXT NOT NULL DEFAULT ''"},
		{"error", "TEXT"},
		{"duration_ms", "INTEGER"},
	} {
		if !historyColumns[column.name] {
			if err := ensureSchedulerColumn(ctx, db, "scheduler_history", column.name, column.declaration); err != nil {
				return err
			}
			historyColumns[column.name] = true
		}
	}
	if historyColumns["error_msg"] && historyColumns["error"] {
		statement := `UPDATE scheduler_history SET error = error_msg WHERE error IS NULL AND error_msg IS NOT NULL`
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("schema migration table scheduler_history statement %q: %w", statement, err)
		}
	}
	for _, column := range []string{"started_at", "finished_at"} {
		if err := migrateSchedulerTimestamp(ctx, db, "scheduler_history", column); err != nil {
			return err
		}
	}

	return nil
}

func ensureSchedulerColumn(ctx context.Context, db *sql.DB, table, column, declaration string) error {
	columns, err := sqliteTableColumns(ctx, db, table)
	if err != nil {
		return err
	}
	if columns[column] {
		return nil
	}
	statement := `ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + declaration
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
	}
	return nil
}

func migrateSchedulerTimestamp(ctx context.Context, db *sql.DB, table, column string) error {
	columns, err := sqliteTableColumns(ctx, db, table)
	if err != nil {
		return err
	}
	if !columns[column] {
		return nil
	}
	statement := `UPDATE ` + table + ` SET ` + column + ` = CAST(strftime('%s', ` + column + `) AS INTEGER) WHERE typeof(` + column + `) = 'text' AND strftime('%s', ` + column + `) IS NOT NULL`
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
	}
	return nil
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	statement := `PRAGMA table_info(` + table + `)`
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
	}
	return columns, nil
}

func renameSQLiteColumn(ctx context.Context, db *sql.DB, table string, columns map[string]bool, legacy, canonical string) error {
	if columns[legacy] && columns[canonical] {
		statement := `ALTER TABLE ` + table + ` RENAME COLUMN ` + legacy + ` TO ` + canonical
		return fmt.Errorf("schema migration table %s statement %q: both columns already exist", table, statement)
	}
	if columns[canonical] {
		return nil
	}
	if !columns[legacy] {
		statement := `ALTER TABLE ` + table + ` RENAME COLUMN ` + legacy + ` TO ` + canonical
		return fmt.Errorf("schema migration table %s statement %q: table has neither column", table, statement)
	}
	statement := `ALTER TABLE ` + table + ` RENAME COLUMN ` + legacy + ` TO ` + canonical
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("schema migration table %s statement %q: %w", table, statement, err)
	}
	return nil
}
