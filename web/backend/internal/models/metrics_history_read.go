package models

import (
	"context"
	"database/sql"
)

// The tiered history plan can exceed PostgreSQL's JIT cost threshold even
// though its indexed reads are short. Keep compilation disabled only for this
// read transaction; SET LOCAL restores the pooled connection on every exit.
func readMetricHistory(ctx context.Context, db *sql.DB, read func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL jit=off`); err != nil {
		return err
	}
	if err := read(tx); err != nil {
		return err
	}
	return tx.Commit()
}
