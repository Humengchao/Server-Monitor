package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func seedAITask(t *testing.T, db *sql.DB, user, server uuid.UUID) AITask {
	t.Helper()
	task := AITask{UserID: user, ServerID: server, ServerName: "test", ServerType: "linux", ProviderID: uuid.New(), ProviderName: "test", Model: "mock", RequestID: uuid.New(), Prompt: "install nginx", Messages: json.RawMessage(`[{"role":"user","content":"install nginx"}]`), TargetFingerprint: "target", ProviderFingerprint: "provider"}
	if err := CreateAITask(context.Background(), db, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestAITaskIdempotencyOwnershipAndSingleActiveTarget(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	foreign, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copy := task
			copy.ID = uuid.Nil
			if err := CreateAITask(ctx, db, &copy); err != nil || copy.ID != task.ID {
				t.Errorf("replay: %s %v", copy.ID, err)
			}
		}()
	}
	wg.Wait()
	for _, change := range []func(*AITask){
		func(task *AITask) { task.ServerID = uuid.New() },
		func(task *AITask) { task.ProviderID = uuid.New() },
		func(task *AITask) { task.Prompt = "changed task" },
	} {
		changed := task
		change(&changed)
		if err := CreateAITask(ctx, db, &changed); !errors.Is(err, ErrAIRequestConflict) {
			t.Fatalf("request ID reused with different content: %v", err)
		}
	}
	other := task
	other.RequestID = uuid.New()
	if err := CreateAITask(ctx, db, &other); !errors.Is(err, ErrAITaskLimit) {
		t.Fatal("concurrent task accepted", err)
	}
	if _, err := GetAITask(ctx, db, task.ID, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign task read", err)
	}
	if _, err := CancelAITask(ctx, db, task.ID, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign cancellation", err)
	}
	tasks, _, err := ListAITasks(ctx, db, foreign, 30, "")
	if err != nil || len(tasks) != 0 {
		t.Fatal("foreign task listed", err)
	}
	got, err := CancelAITask(ctx, db, task.ID, user)
	if err != nil || got.Status != "canceled" {
		t.Fatal(got.Status, err)
	}
	other = task
	other.RequestID = uuid.New()
	if err = CreateAITask(ctx, db, &other); err != nil {
		t.Fatal(err)
	}
}

func TestAITaskApprovalIsBoundToPersistedActionAndConsumedOnce(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	foreign, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "worker-a")
	if err != nil || claimed.ID != task.ID {
		t.Fatal(err)
	}
	action := AIAction{CallID: "call-one", ToolName: "run_command", Arguments: json.RawMessage(`{"command":"rm -rf /tmp/example"}`), Command: "rm -rf /tmp/example", Summary: "remove example", RequiresApproval: true, RiskReason: "deletion"}
	if err = InsertAIAction(ctx, db, task.ID, claimed.WorkerID, &action); err != nil {
		t.Fatal(err)
	}
	if err = StartAIAction(ctx, db, task.ID, action.ID, claimed.WorkerID); err == nil {
		t.Fatal("pending action executed")
	}
	if _, err = DecideAIAction(ctx, db, task.ID, action.ID, foreign, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign approval", err)
	}
	if _, err = DecideAIAction(ctx, db, task.ID, uuid.New(), user, true); !errors.Is(err, ErrAITaskConflict) {
		t.Fatal("wrong approval accepted", err)
	}
	got, err := DecideAIAction(ctx, db, task.ID, action.ID, user, true)
	if err != nil || got.Status != "queued" {
		t.Fatal(got.Status, err)
	}
	if _, err = DecideAIAction(ctx, db, task.ID, action.ID, user, true); !errors.Is(err, ErrAITaskConflict) {
		t.Fatal("duplicate approval accepted", err)
	}
	claimed, err = ClaimAITask(ctx, db, "worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if err = StartAIAction(ctx, db, task.ID, action.ID, "worker-a"); err == nil {
		t.Fatal("old worker authorized")
	}
	if err = StartAIAction(ctx, db, task.ID, action.ID, claimed.WorkerID); err != nil {
		t.Fatal(err)
	}
	if err = StartAIAction(ctx, db, task.ID, action.ID, claimed.WorkerID); !errors.Is(err, ErrAITaskConflict) {
		t.Fatal("operation replayed", err)
	}
	_, _, actions, err := AITaskDetails(ctx, db, task.ID, user)
	if err != nil || len(actions) != 1 || actions[0].Command != action.Command || actions[0].Status != "running" {
		t.Fatal(actions, err)
	}
}

func TestAITaskExpiredLeaseNeverReplaysRunningCommand(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "gone-worker")
	if err != nil {
		t.Fatal(err)
	}
	a := AIAction{CallID: "call", ToolName: "install_packages", Arguments: json.RawMessage(`{"packages":["nginx"]}`), Summary: "install"}
	if err = InsertAIAction(ctx, db, task.ID, claimed.WorkerID, &a); err != nil {
		t.Fatal(err)
	}
	if err = StartAIAction(ctx, db, task.ID, a.ID, claimed.WorkerID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE ai_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = HeartbeatAITask(ctx, db, task.ID, claimed.WorkerID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired worker revived its lease", err)
	}
	if err = FinishAITask(ctx, db, task.ID, claimed.WorkerID, "completed", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired worker completed before recovery", err)
	}
	if err = ExpireAILeases(ctx, db); err != nil {
		t.Fatal(err)
	}
	got, _, actions, err := AITaskDetails(ctx, db, task.ID, user)
	if err != nil || got.Status != "interrupted" || !actions[0].Unknown || actions[0].Status != "interrupted" {
		t.Fatal(got, actions, err)
	}
	if _, err = ClaimAITask(ctx, db, "new-worker"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("interrupted task replayed", err)
	}
	if _, err = ContinueAITask(ctx, db, task.ID, user, uuid.New(), "retry"); !errors.Is(err, ErrAITaskConflict) {
		t.Fatal("unknown command silently resumed", err)
	}
}

func TestAITaskCancelWinsFinishAndFollowupIsIdempotent(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "worker")
	if err != nil {
		t.Fatal(err)
	}
	got, err := CancelAITask(ctx, db, task.ID, user)
	if err != nil || got.Status != "cancel_requested" {
		t.Fatal(got, err)
	}
	if err = FinishAITask(ctx, db, task.ID, claimed.WorkerID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	got, err = GetAITask(ctx, db, task.ID, user)
	if err != nil || got.Status != "canceled" {
		t.Fatal(got, err)
	}
	second := seedAITask(t, db, user, server)
	claimed, err = ClaimAITask(ctx, db, "worker2")
	if err != nil {
		t.Fatal(err)
	}
	if err = FinishAITask(ctx, db, second.ID, claimed.WorkerID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	request := uuid.New()
	if _, err = ContinueAITask(ctx, db, second.ID, user, request, "check version"); err != nil {
		t.Fatal(err)
	}
	if _, err = ContinueAITask(ctx, db, second.ID, user, request, "check version"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, second.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestAITaskFollowupDigestRejectsDifferentContentAndKeepsLegacyRetriesReadOnly(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	foreign, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "followup-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = FinishAITask(ctx, db, task.ID, claimed.WorkerID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	request := uuid.New()
	if _, err = ContinueAITask(ctx, db, task.ID, user, request, "inspect version"); err != nil {
		t.Fatal(err)
	}
	if _, err = ContinueAITask(ctx, db, task.ID, user, request, "restart instead"); !errors.Is(err, ErrAIRequestConflict) {
		t.Fatal("changed content reused accepted ID", err)
	}
	if _, accepted, err := FindAITaskMessageRequest(ctx, db, task.ID, user, request, "restart instead"); !accepted || !errors.Is(err, ErrAIRequestConflict) {
		t.Fatal("preflight missed conflicting body", accepted, err)
	}
	if got, accepted, err := FindAITaskMessageRequest(ctx, db, task.ID, user, request, "inspect version"); err != nil || !accepted || got.ID != task.ID {
		t.Fatal(got.ID, accepted, err)
	}
	if _, _, err := FindAITaskMessageRequest(ctx, db, task.ID, foreign, request, "inspect version"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign retry disclosed task", err)
	}
	var digest string
	if err := db.QueryRow(`SELECT message_hash FROM ai_task_messages WHERE task_id=$1 AND request_id=$2`, task.ID, request).Scan(&digest); err != nil || len(digest) != 64 || digest == "inspect version" {
		t.Fatal("message digest missing", digest, err)
	}
	if _, err := CancelAITask(ctx, db, task.ID, user); err != nil {
		t.Fatal(err)
	}
	legacy := uuid.New()
	if _, err := db.Exec(`INSERT INTO ai_task_messages(task_id,request_id) VALUES($1,$2)`, task.ID, legacy); err != nil {
		t.Fatal(err)
	}
	if got, accepted, err := FindAITaskMessageRequest(ctx, db, task.ID, user, legacy, "unverifiable old message"); err != nil || !accepted || got.Status != "canceled" {
		t.Fatal("legacy accepted retry was not read-only", got.Status, accepted, err)
	}
	if got, err := ContinueAITask(ctx, db, task.ID, user, legacy, "another old message"); err != nil || got.Status != "canceled" {
		t.Fatal("legacy retry requeued canceled task", got.Status, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, task.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("retry appended a user message", count, err)
	}
}

func TestAITaskConcurrentFollowupIDCannotAcceptTwoBodies(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "followup-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = FinishAITask(ctx, db, task.ID, claimed.WorkerID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	request := uuid.New()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, message := range []string{"first body", "different body"} {
		go func(message string) {
			<-start
			_, err := ContinueAITask(ctx, db, task.ID, user, request, message)
			results <- err
		}(message)
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrAIRequestConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("both bodies were accepted", success, conflict)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, task.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("concurrent retry appended more than one message", count, err)
	}
}
