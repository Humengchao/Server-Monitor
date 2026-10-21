package models

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrAITaskConflict = errors.New("task state changed; refresh before continuing")
var ErrAITaskLimit = errors.New("finish or cancel the active task on this server first; at most two active tasks per account")
var ErrAIRequestConflict = errors.New("this request ID was already used with different content; use a new request ID")
var ErrAITaskStepLimit = errors.New("Task reached its 24-step limit. Review the results and start a new task for remaining work.")
var ErrAIConversationLimit = errors.New("Conversation size limit reached; start a new task with the remaining work.")

type AITask struct {
	Scope               string               `json:"scope"`
	ID                  uuid.UUID            `json:"id"`
	UserID              uuid.UUID            `json:"-"`
	ServerID            uuid.UUID            `json:"server_id"`
	ServerName          string               `json:"server_name"`
	ServerType          string               `json:"server_type"`
	ProviderID          uuid.UUID            `json:"provider_id"`
	ProviderName        string               `json:"provider_name"`
	Model               string               `json:"model"`
	RequestID           uuid.UUID            `json:"-"`
	Prompt              string               `json:"prompt"`
	Status              string               `json:"status"`
	Error               string               `json:"error"`
	Messages            json.RawMessage      `json:"-"`
	TargetFingerprint   string               `json:"-"`
	ProviderFingerprint string               `json:"-"`
	WorkerID            string               `json:"-"`
	Steps               int                  `json:"steps"`
	PromptTokens        int64                `json:"prompt_tokens"`
	CompletionTokens    int64                `json:"completion_tokens"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	ApprovalMode        string               `json:"approval_mode"`
	Options             json.RawMessage      `json:"-"`
	Skills              []AITaskExtensionRef `json:"skills"`
	Plugins             []AITaskExtensionRef `json:"plugins"`
}
type AIEvent struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	Phase     string     `json:"phase,omitempty"`
	Content   string     `json:"content"`
	ActionID  *uuid.UUID `json:"action_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}
type AIAction struct {
	ID               uuid.UUID       `json:"id"`
	TaskID           uuid.UUID       `json:"-"`
	CallID           string          `json:"-"`
	ToolName         string          `json:"tool_name"`
	Arguments        json.RawMessage `json:"arguments"`
	Summary          string          `json:"summary"`
	Command          string          `json:"command"`
	RequiresApproval bool            `json:"requires_approval"`
	RiskReason       string          `json:"risk_reason"`
	Mutating         bool            `json:"mutating"`
	Status           string          `json:"status"`
	Output           string          `json:"output"`
	Error            string          `json:"error"`
	Truncated        bool            `json:"truncated"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Unknown          bool            `json:"unknown"`
	DurationMS       int64           `json:"duration_ms"`
	BackupPath       string          `json:"backup_path,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
}

const aiTaskColumns = `id,user_id,server_id,server_name,server_type,provider_id,provider_name,model,request_id,prompt,status,error,messages,target_fingerprint,provider_fingerprint,worker_id,steps,prompt_tokens,completion_tokens,created_at,updated_at,approval_mode,options,scope`

func scanAITask(row interface{ Scan(...any) error }) (AITask, error) {
	var t AITask
	err := row.Scan(&t.ID, &t.UserID, &t.ServerID, &t.ServerName, &t.ServerType, &t.ProviderID, &t.ProviderName, &t.Model, &t.RequestID, &t.Prompt, &t.Status, &t.Error, &t.Messages, &t.TargetFingerprint, &t.ProviderFingerprint, &t.WorkerID, &t.Steps, &t.PromptTokens, &t.CompletionTokens, &t.CreatedAt, &t.UpdatedAt, &t.ApprovalMode, &t.Options, &t.Scope)
	if err == nil {
		_, err = t.DecodeOptions()
	}
	return t, err
}
func GetAITask(ctx context.Context, db *sql.DB, id, user uuid.UUID) (AITask, error) {
	return scanAITask(db.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE id=$1 AND user_id=$2`, id, user))
}
func FindAITaskRequest(ctx context.Context, db *sql.DB, user, request uuid.UUID) (AITask, error) {
	return scanAITask(db.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE user_id=$1 AND request_id=$2`, user, request))
}

func CreateAITask(ctx context.Context, db *sql.DB, t *AITask) error {
	if t.Scope == "" {
		t.Scope = "server"
	}
	if t.Scope == "fleet" && t.ServerID != uuid.Nil || t.Scope == "server" && t.ServerID == uuid.Nil {
		return ErrAITaskScope
	}
	if t.ApprovalMode == "" {
		t.ApprovalMode = "standard"
	}
	if len(t.Options) == 0 {
		t.Options = json.RawMessage(`{}`)
	}
	selection, err := t.Selection()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, t.UserID).Scan(&owned); err != nil {
		return err
	}
	previous, err := scanAITask(tx.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE user_id=$1 AND request_id=$2`, t.UserID, t.RequestID))
	if err == nil {
		if previous.ServerID != t.ServerID || previous.ProviderID != t.ProviderID || previous.Prompt != t.Prompt || !previous.MatchesSelection(selection) {
			return ErrAIRequestConflict
		}
		*t = previous
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Row lock also serializes task creation on the same server across callers.
	if t.Scope == "server" {
		if err = tx.QueryRowContext(ctx, `SELECT id FROM servers WHERE id=$1 AND user_id=$2 FOR UPDATE`, t.ServerID, t.UserID).Scan(&owned); err != nil {
			return err
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_tasks WHERE user_id=$1 AND status IN ('queued','running','awaiting_approval','awaiting_input','cancel_requested')`, t.UserID).Scan(&count); err != nil {
		return err
	}
	if count >= 2 {
		return ErrAITaskLimit
	}
	var active bool
	if t.Scope == "server" {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_tasks WHERE scope='server' AND server_id=$1 AND status IN ('queued','running','awaiting_approval','awaiting_input','cancel_requested'))`, t.ServerID).Scan(&active); err != nil {
			return err
		}
	}
	if active {
		return ErrAITaskLimit
	}
	t.ID = uuid.New()
	t.Status = "queued"
	err = tx.QueryRowContext(ctx, `INSERT INTO ai_tasks(id,user_id,server_id,server_name,server_type,provider_id,provider_name,model,request_id,prompt,messages,target_fingerprint,provider_fingerprint,approval_mode,options,scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING created_at,updated_at`, t.ID, t.UserID, t.ServerID, t.ServerName, t.ServerType, t.ProviderID, t.ProviderName, t.Model, t.RequestID, t.Prompt, []byte(t.Messages), t.TargetFingerprint, t.ProviderFingerprint, t.ApprovalMode, []byte(t.Options), t.Scope).Scan(&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content) VALUES($1,'user',$2)`, t.ID, t.Prompt); err != nil {
		return err
	}
	return tx.Commit()
}

func ListAITasks(ctx context.Context, db *sql.DB, user uuid.UUID, limit int, before string) ([]AITask, string, error) {
	query := `SELECT ` + aiTaskColumns + ` FROM ai_tasks WHERE user_id=$1`
	args := []any{user}
	if before != "" {
		raw, e := base64.RawURLEncoding.DecodeString(before)
		if e != nil {
			return nil, "", ErrAITaskConflict
		}
		parts := strings.SplitN(string(raw), "|", 2)
		if len(parts) != 2 {
			return nil, "", ErrAITaskConflict
		}
		at, e := time.Parse(time.RFC3339Nano, parts[0])
		if e != nil {
			return nil, "", ErrAITaskConflict
		}
		id, e := uuid.Parse(parts[1])
		if e != nil {
			return nil, "", ErrAITaskConflict
		}
		query += ` AND (created_at,id)<($2,$3)`
		args = append(args, at, id)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT `
	if before == "" {
		query += `$2`
	} else {
		query += `$4`
	}
	args = append(args, limit+1)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	tasks := []AITask{}
	for rows.Next() {
		t, e := scanAITask(rows)
		if e != nil {
			return nil, "", e
		}
		tasks = append(tasks, t)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(tasks) > limit {
		tasks = tasks[:limit]
		last := tasks[len(tasks)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID.String()))
	}
	return tasks, next, nil
}

func AITaskDetails(ctx context.Context, db *sql.DB, id, user uuid.UUID) (AITask, []AIEvent, []AIAction, error) {
	t, err := GetAITask(ctx, db, id, user)
	if err != nil {
		return t, nil, nil, err
	}
	events := []AIEvent{}
	actions := []AIAction{}
	rows, err := db.QueryContext(ctx, `SELECT id,kind,phase,content,action_id,created_at FROM ai_events WHERE task_id=$1 ORDER BY id LIMIT 500`, id)
	if err != nil {
		return t, nil, nil, err
	}
	for rows.Next() {
		var e AIEvent
		if err = rows.Scan(&e.ID, &e.Kind, &e.Phase, &e.Content, &e.ActionID, &e.CreatedAt); err != nil {
			rows.Close()
			return t, nil, nil, err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return t, nil, nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT id,task_id,call_id,tool_name,arguments,summary,command,requires_approval,risk_reason,mutating,status,output,error,truncated,exit_code,unknown,duration_ms,backup_path,created_at,completed_at FROM ai_actions WHERE task_id=$1 ORDER BY created_at,id LIMIT 100`, id)
	if err != nil {
		return t, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a AIAction
		if err = rows.Scan(&a.ID, &a.TaskID, &a.CallID, &a.ToolName, &a.Arguments, &a.Summary, &a.Command, &a.RequiresApproval, &a.RiskReason, &a.Mutating, &a.Status, &a.Output, &a.Error, &a.Truncated, &a.ExitCode, &a.Unknown, &a.DurationMS, &a.BackupPath, &a.CreatedAt, &a.CompletedAt); err != nil {
			return t, nil, nil, err
		}
		actions = append(actions, a)
	}
	return t, events, actions, rows.Err()
}

func CancelAITask(ctx context.Context, db *sql.DB, id, user uuid.UUID) (AITask, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return AITask{}, err
	}
	defer tx.Rollback()
	t, err := scanAITask(tx.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user))
	if err != nil {
		return t, err
	}
	status := "canceled"
	if t.Status == "running" || t.Status == "cancel_requested" {
		status = "cancel_requested"
	} else if t.Status != "queued" && t.Status != "awaiting_input" && t.Status != "awaiting_approval" {
		return t, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status=$2,updated_at=NOW() WHERE id=$1`, id, status); err != nil {
		return t, err
	}
	if status == "canceled" {
		if _, err = tx.ExecContext(ctx, `UPDATE ai_actions SET status='rejected',completed_at=NOW() WHERE task_id=$1 AND status IN ('pending','approved')`, id); err != nil {
			return t, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content) VALUES($1,'system','Cancellation requested. Previously executed operations are not rolled back.')`, id); err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	return GetAITask(ctx, db, id, user)
}

func DecideAIAction(ctx context.Context, db *sql.DB, id, action, user uuid.UUID, approve bool) (AITask, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return AITask{}, err
	}
	defer tx.Rollback()
	t, err := scanAITask(tx.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user))
	if err != nil {
		return t, err
	}
	if t.Status != "awaiting_approval" {
		return t, ErrAITaskConflict
	}
	status, next, content := "rejected", "canceled", "Operation rejected. Task stopped; earlier operations were not rolled back."
	if approve {
		status, next, content = "approved", "queued", "The displayed operation was explicitly approved."
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_actions SET status=$3,approved_at=CASE WHEN $3='approved' THEN NOW() ELSE NULL END WHERE id=$1 AND task_id=$2 AND status='pending'`, action, id, status)
	if err != nil {
		return t, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return t, err
	}
	if n != 1 {
		return t, ErrAITaskConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status=$2,updated_at=NOW() WHERE id=$1`, id, next); err != nil {
		return t, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content,action_id) VALUES($1,'system',$2,$3)`, id, content, action); err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	return GetAITask(ctx, db, id, user)
}

func aiFollowupHash(message string) string {
	digest := sha256.Sum256([]byte(message))
	return hex.EncodeToString(digest[:])
}

// FindAITaskMessageRequest verifies ownership first. Already accepted retries
// must succeed even if a provider was since disabled/deleted or a host changed:
// they only return existing state, never resume or start another action.
func FindAITaskMessageRequest(ctx context.Context, db *sql.DB, id, user, request uuid.UUID, message string) (AITask, bool, error) {
	task, err := GetAITask(ctx, db, id, user)
	if err != nil {
		return AITask{}, false, err
	}
	var digest string
	err = db.QueryRowContext(ctx, `SELECT message_hash FROM ai_task_messages WHERE task_id=$1 AND request_id=$2`, id, request).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return task, false, nil
	}
	if err != nil {
		return AITask{}, false, err
	}
	if digest != "" && digest != aiFollowupHash(message) {
		return AITask{}, true, ErrAIRequestConflict
	}
	return task, true, nil
}

// Follow-ups carry a separate idempotency key and cannot alter a pending action.
func ContinueAITask(ctx context.Context, db *sql.DB, id, user, request uuid.UUID, message string) (AITask, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return AITask{}, err
	}
	defer tx.Rollback()
	var owner uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&owner); err != nil {
		return AITask{}, err
	}
	t, err := scanAITask(tx.QueryRowContext(ctx, `SELECT `+aiTaskColumns+` FROM ai_tasks WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user))
	if err != nil {
		return t, err
	}
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT message_hash FROM ai_task_messages WHERE task_id=$1 AND request_id=$2`, id, request).Scan(&digest)
	if err == nil {
		if digest != "" && digest != aiFollowupHash(message) {
			return t, ErrAIRequestConflict
		}
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return t, err
	}
	if t.Status != "completed" && t.Status != "awaiting_input" {
		return t, ErrAITaskConflict
	}
	if t.Steps >= 24 {
		return t, ErrAITaskStepLimit
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_tasks WHERE id<>$1 AND user_id=$2 AND status IN ('queued','running','awaiting_approval','awaiting_input','cancel_requested')`, id, user).Scan(&active); err != nil {
		return t, err
	}
	if active >= 2 {
		return t, ErrAITaskLimit
	}
	var exists bool
	if t.Scope == "server" {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_tasks WHERE scope='server' AND id<>$1 AND server_id=$2 AND status IN ('queued','running','awaiting_approval','awaiting_input','cancel_requested'))`, id, t.ServerID).Scan(&exists); err != nil {
			return t, err
		}
	}
	if exists {
		return t, ErrAITaskLimit
	}
	msg, _ := json.Marshal(map[string]string{"role": "user", "content": message})
	if len(t.Messages)+len(msg) > 256*1024 {
		return t, ErrAIConversationLimit
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET messages=messages||jsonb_build_array($2::jsonb),status='queued',error='',updated_at=NOW() WHERE id=$1`, id, string(msg)); err != nil {
		return t, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_task_messages(task_id,request_id,message_hash) VALUES($1,$2,$3)`, id, request, aiFollowupHash(message)); err != nil {
		return t, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_events(task_id,kind,content) VALUES($1,'user',$2)`, id, message); err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	return GetAITask(ctx, db, id, user)
}
