package models

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// UpdateServerNotes changes only notes, so an older detail page cannot undo
// connection or billing changes made by another editor.
func UpdateServerNotes(ctx context.Context, db *sql.DB, id, userID uuid.UUID, notes string) error {
	result, err := db.ExecContext(ctx, `UPDATE servers SET notes=$1 WHERE id=$2 AND user_id=$3`, notes, id, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}
