package database

import (
	"context"
	"fmt"
	"time"
)

type ConnectionProbe interface {
	CheckConnection(context.Context) error
}

func (s *SQLiteDB) CheckConnection(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("read probe: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write probe begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE pastes SET id = id WHERE 0"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("write probe: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write probe commit: %w", err)
	}
	return nil
}
