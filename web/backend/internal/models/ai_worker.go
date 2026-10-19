package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// A lost lease never retries a command: its remote outcome may be unknown.
func ExpireAILeases(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `WITH expired AS (
 UPDATE ai_tasks SET status='interrupted',error='Worker disconnected; remote completion is unknown. Inspect the server before starting another task.',worker_id='',lease_until=NULL,updated_at=NOW()
 WHERE status IN ('running','cancel_requested') AND lease_until<NOW() RETURNING id
 ), stopped AS (
 UPDATE ai_actions SET status='interrupted',unknown=TRUE,error='Worker disconnected; remote completion is unknown.',completed_at=NOW()
 WHERE task_id IN (SELECT id FROM expired) AND status='running'
 ), unstarted AS (
 UPDATE ai_actions SET status='rejected',error='Task ended before this operation started.',completed_at=NOW()
 WHERE task_id IN (SELECT id FROM expired) AND status IN ('approved','pending')
 ) INSERT INTO ai_events(task_id,kind,content) SELECT id,'system','Worker disconnected. Commands were not replayed automatically.' FROM expired`)
	return err
}
func ClaimAITask(ctx context.Context, db *sql.DB, worker string) (AITask, error) {
	return scanAITask(db.QueryRowContext(ctx, `UPDATE ai_tasks SET status='running',worker_id=$1,lease_until=NOW()+INTERVAL '45 seconds',updated_at=NOW()
 WHERE id=(SELECT id FROM ai_tasks WHERE status='queued' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+aiTaskColumns, worker))
}
func HeartbeatAITask(ctx context.Context, db *sql.DB, id uuid.UUID, worker string) (string, error) {
	var status string
	err := db.QueryRowContext(ctx, `UPDATE ai_tasks SET lease_until=NOW()+INTERVAL '45 seconds' WHERE id=$1 AND worker_id=$2 AND status IN ('running','cancel_requested') AND lease_until>NOW() RETURNING status`, id, worker).Scan(&status)
	return status, err
}

func lockAIWorker(ctx context.Context, tx *sql.Tx, id uuid.UUID, worker string) error {
	var owned uuid.UUID
	return tx.QueryRowContext(ctx, `SELECT id FROM ai_tasks WHERE id=$1 AND worker_id=$2 AND status='running' AND lease_until>NOW() FOR UPDATE`, id, worker).Scan(&owned)
}

// A remote initialization can outlive a cancellation or lease change. This is
// a final read before a recorded action issues its first mutating request.
func CheckAIActionAuthority(ctx context.Context, db *sql.DB, task, action uuid.UUID, worker string) error {
	var owned uuid.UUID
	return db.QueryRowContext(ctx, `SELECT a.id FROM ai_actions a JOIN ai_tasks t ON t.id=a.task_id
 WHERE t.id=$1 AND t.worker_id=$2 AND t.status='running' AND t.lease_until>NOW()
 AND a.id=$3 AND a.status='running'`, task, worker, action).Scan(&owned)
}

func CheckAITaskAuthority(ctx context.Context, db *sql.DB, task uuid.UUID, worker string) error {
	var owned uuid.UUID
	return db.QueryRowContext(ctx, `SELECT id FROM ai_tasks WHERE id=$1 AND worker_id=$2 AND status='running' AND lease_until>NOW()`, task, worker).Scan(&owned)
}
func SaveAIConversation(ctx context.Context, db *sql.DB, id uuid.UUID, worker string, messages json.RawMessage, content string, promptTokens, completionTokens int64) error {
	return saveAIConversation(ctx, db, id, worker, messages, content, promptTokens, completionTokens, 1)
}

// A validation reply belongs to the model step already recorded. Saving it
// must not consume another step or charge the same provider usage twice.
func SaveAIValidationReply(ctx context.Context, db *sql.DB, id uuid.UUID, worker string, messages json.RawMessage) error {
	return saveAIConversation(ctx, db, id, worker, messages, "", 0, 0, 0)
}

func saveAIConversation(ctx context.Context, db *sql.DB, id uuid.UUID, worker string, messages json.RawMessage, content string, promptTokens, completionTokens int64, steps int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAIWorker(ctx, tx, id, worker); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET messages=$3,steps=steps+$6,prompt_tokens=prompt_tokens+$4,completion_tokens=completion_tokens+$5,updated_at=NOW() WHERE id=$1 AND worker_id=$2`, id, worker, []byte(messages), promptTokens, completionTokens, steps)
	if err != nil {
		return err
	}
	if content != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content) VALUES($1,'assistant',$2)`, id, content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func InsertAIAction(ctx context.Context, db *sql.DB, id uuid.UUID, worker string, a *AIAction) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAIWorker(ctx, tx, id, worker); err != nil {
		return err
	}
	a.ID = uuid.New()
	a.TaskID = id
	a.Status = "approved"
	if a.RequiresApproval {
		a.Status = "pending"
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO ai_actions(id,task_id,call_id,tool_name,arguments,summary,command,requires_approval,risk_reason,mutating,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING created_at`, a.ID, id, a.CallID, a.ToolName, []byte(a.Arguments), a.Summary, a.Command, a.RequiresApproval, a.RiskReason, a.Mutating, a.Status).Scan(&a.CreatedAt)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content,action_id) VALUES($1,'tool',$2,$3)`, id, a.Summary, a.ID); err != nil {
		return err
	}
	if a.RequiresApproval {
		if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status='awaiting_approval',worker_id='',lease_until=NULL,updated_at=NOW() WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func StartAIAction(ctx context.Context, db *sql.DB, id, action uuid.UUID, worker string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAIWorker(ctx, tx, id, worker); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_actions SET status='running' WHERE id=$1 AND task_id=$2 AND status='approved'`, action, id)
	if err != nil {
		return err
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrAITaskConflict
	}
	return tx.Commit()
}
func FinishAIAction(ctx context.Context, db *sql.DB, id uuid.UUID, worker string, a AIAction, messages json.RawMessage) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM ai_tasks WHERE id=$1 AND worker_id=$2 AND status IN ('running','cancel_requested') AND lease_until>NOW() FOR UPDATE`, id, worker).Scan(&status); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_actions SET status=$3,output=$4,error=$5,truncated=$6,exit_code=$7,unknown=$8,duration_ms=$9,backup_path=$10,completed_at=NOW() WHERE id=$1 AND task_id=$2 AND status='running'`, a.ID, id, a.Status, a.Output, a.Error, a.Truncated, a.ExitCode, a.Unknown, a.DurationMS, a.BackupPath)
	if err != nil {
		return err
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrAITaskConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET messages=$2,updated_at=NOW() WHERE id=$1`, id, []byte(messages)); err != nil {
		return err
	}
	return tx.Commit()
}
func FinishAITask(ctx context.Context, db *sql.DB, id uuid.UUID, worker, status, detail string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM ai_tasks WHERE id=$1 AND worker_id=$2 AND status IN ('running','cancel_requested') AND lease_until>NOW() FOR UPDATE`, id, worker).Scan(&current); err != nil {
		return err
	}
	if current == "cancel_requested" {
		status = "canceled"
		detail = "Task stopped. Previous operations are not rolled back; inspect any interrupted command before retrying."
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status=$3,error=$4,worker_id='',lease_until=NULL,updated_at=NOW() WHERE id=$1 AND worker_id=$2`, id, worker, status, detail); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_actions SET status='interrupted',unknown=TRUE,error='Execution stopped; remote completion is unknown.',completed_at=NOW() WHERE task_id=$1 AND status='running'`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_actions SET status='rejected',error='Task ended before this operation started.',completed_at=NOW() WHERE task_id=$1 AND status IN ('approved','pending')`, id); err != nil {
		return err
	}
	if detail != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content) VALUES($1,'system',$2)`, id, detail); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func PendingApprovedAIAction(ctx context.Context, db *sql.DB, id, user uuid.UUID) (*AIAction, error) {
	_, _, actions, err := AITaskDetails(ctx, db, id, user)
	if err != nil {
		return nil, err
	}
	var pending *AIAction
	for _, a := range actions {
		if a.Status == "approved" {
			if pending != nil {
				return nil, errors.New("multiple pending operations")
			}
			copy := a
			pending = &copy
		} else if a.Status == "running" || a.Status == "pending" {
			return nil, ErrAITaskConflict
		}
	}
	return pending, nil
}
