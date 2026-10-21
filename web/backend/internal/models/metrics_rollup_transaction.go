package models

import (
	"context"
	"database/sql"
)

// Invalid-bucket cleanup can cross PostgreSQL's JIT cost threshold despite
// examining only a few indexed rows. Its compilation costs more than both
// maintenance queries together. Scope this setting to the existing transaction
// so successful, failed and cancelled batches all restore pooled connections.
func withMetricRollupTransaction(ctx context.Context, db *sql.DB, rollup func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL jit=off`); err != nil {
		return err
	}
	if err := rollup(tx); err != nil {
		return err
	}
	return tx.Commit()
}
