package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func aiWorkerFixture(t *testing.T) (*AITaskWorker, models.AITask) {
	t.Helper()
	raw := testdb.Open(t)
	user, server, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	provider := models.AIProvider{UserID: user, Name: "fixture", Kind: "compatible", BaseURL: "https://example.invalid/v1", Model: "test", APIKey: "secret-fixture-key", Enabled: true}
	if err := models.SaveAIProvider(context.Background(), db, &provider, true); err != nil {
		t.Fatal(err)
	}
	w := NewAITaskWorker(db, NewAIClient(false))
	t.Cleanup(w.cancel)
	task, err := w.Create(context.Background(), user, server, provider.ID, uuid.New(), "Install nginx and report the result")
	if err != nil {
		t.Fatal(err)
	}
	return w, task
}

func TestAIWorkerAcceptedFollowupRetrySurvivesBindingChanges(t *testing.T) {
	for _, mutation := range []string{"disable provider", "delete provider", "change target"} {
		t.Run(mutation, func(t *testing.T) {
			w, task := aiWorkerFixture(t)
			ctx := context.Background()
			w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
				return AICompletion{Message: AIMessage{Role: "assistant", Content: "Done"}}, nil
			}
			claimAndRunAI(t, w)
			request := uuid.New()
			if _, err := w.Continue(ctx, task.UserID, task.ID, request, "Check version"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "disable provider":
				_, err = w.db.Raw.Exec(`UPDATE ai_providers SET enabled=FALSE WHERE id=$1`, task.ProviderID)
			case "delete provider":
				_, err = w.db.Raw.Exec(`DELETE FROM ai_providers WHERE id=$1`, task.ProviderID)
			case "change target":
				_, err = w.db.Raw.Exec(`UPDATE servers SET host='192.0.2.99' WHERE id=$1`, task.ServerID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := w.Continue(ctx, task.UserID, task.ID, request, "Check version"); err != nil || got.ID != task.ID || got.Status != "queued" {
				t.Fatal("accepted retry failed new binding checks", got.Status, err)
			}
			if _, err := w.Continue(ctx, task.UserID, task.ID, request, "Different request"); !errors.Is(err, models.ErrAIRequestConflict) {
				t.Fatal("same ID accepted different content", err)
			}
			if _, err := w.Continue(ctx, task.UserID, task.ID, uuid.New(), "New request"); !errors.Is(err, models.ErrAITaskConflict) {
				t.Fatal("new followup skipped binding checks", err)
			}
			var count int
			if err := w.db.Raw.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, task.ID).Scan(&count); err != nil || count != 2 {
				t.Fatal("retry changed conversation", count, err)
			}
		})
	}
}

func TestAIWorkerLegacyFollowupRetryNeverResumesTask(t *testing.T) {
	w, task := aiWorkerFixture(t)
	ctx := context.Background()
	if _, err := models.CancelAITask(ctx, w.db.Raw, task.ID, task.UserID); err != nil {
		t.Fatal(err)
	}
	request := uuid.New()
	if _, err := w.db.Raw.Exec(`INSERT INTO ai_task_messages(task_id,request_id) VALUES($1,$2)`, task.ID, request); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Raw.Exec(`DELETE FROM ai_providers WHERE id=$1`, task.ProviderID); err != nil {
		t.Fatal(err)
	}
	got, err := w.Continue(ctx, task.UserID, task.ID, request, "Legacy retry has no stored digest")
	if err != nil || got.Status != "canceled" {
		t.Fatal("legacy retry resumed or failed after provider removal", got.Status, err)
	}
	var count int
	if err := w.db.Raw.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, task.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy retry appended content", count, err)
	}
}
func claimAndRunAI(t *testing.T, w *AITaskWorker) {
	t.Helper()
	task, err := models.ClaimAITask(context.Background(), w.db.Raw, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	w.run(task)
}
func toolCompletion(id, name, args string) AICompletion {
	return AICompletion{Message: AIMessage{Role: "assistant", Content: "Next step", ToolCalls: []AIToolCall{{ID: id, Type: "function", Function: AIFunctionCall{Name: name, Arguments: args}}}}, PromptTokens: 10, CompletionTokens: 5, FinishReason: "tool_calls"}
}

func TestAIWorkerApprovalResumesExactCommandOnceAndRedactsCredentials(t *testing.T) {
	w, task := aiWorkerFixture(t)
	calls := 0
	var executed atomic.Int32
	w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, _ []AIToolDefinition) (AICompletion, error) {
		calls++
		switch calls {
		case 1:
			return toolCompletion("call-install", "install_packages", `{"manager":"apt","packages":["nginx"]}`), nil
		case 2:
			return toolCompletion("call-delete", "run_command", `{"command":"rm -rf /tmp/example"}`), nil
		default:
			last := messages[len(messages)-1]
			if last.Role != "tool" || last.ToolCallID != "call-delete" || strings.Contains(last.Content, "secret-fixture-key") {
				t.Fatal("tool result/secret handling", last)
			}
			return AICompletion{Message: AIMessage{Role: "assistant", Content: "Verified result", ReasoningContent: "internal fixture reasoning"}, PromptTokens: 11, CompletionTokens: 6}, nil
		}
	}
	w.execute = func(_ context.Context, _ *models.Server, a AIAction) (AIActionResult, error) {
		executed.Add(1)
		return AIActionResult{Output: "completed secret-fixture-key", ExitCode: new(int)}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "awaiting_approval" || executed.Load() != 1 || len(actions) != 2 {
		t.Fatal(got.Status, executed.Load(), actions, err)
	}
	if actions[0].Status != "succeeded" || !strings.Contains(actions[0].Output, "[redacted credential]") {
		t.Fatal(actions[0])
	}
	if _, err = models.DecideAIAction(context.Background(), w.db.Raw, task.ID, actions[1].ID, task.UserID, true); err != nil {
		t.Fatal(err)
	}
	claimAndRunAI(t, w)
	got, events, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "completed" || executed.Load() != 2 || got.PromptTokens != 31 || got.CompletionTokens != 16 {
		t.Fatal(got, executed.Load(), err)
	}
	raw, _ := json.Marshal(map[string]any{"task": got, "events": events, "actions": actions})
	if strings.Contains(string(raw), "secret-fixture-key") || strings.Contains(string(raw), "internal fixture reasoning") {
		t.Fatal("internal secrets/reasoning leaked")
	}
}

func TestAIWorkerUnknownOutcomeStopsWithoutModelOrCommandRetry(t *testing.T) {
	w, task := aiWorkerFixture(t)
	modelCalls := 0
	execCalls := 0
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		modelCalls++
		return toolCompletion("install", "install_packages", `{"manager":"apt","packages":["nginx"]}`), nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		execCalls++
		return AIActionResult{Unknown: true, Error: "connection dropped"}, errors.New("lost")
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "failed" || len(actions) != 1 || !actions[0].Unknown || modelCalls != 1 || execCalls != 1 {
		t.Fatal(got, actions, modelCalls, execCalls, err)
	}
}

func TestAIWorkerRejectedArgumentsCountOnlyOneModelStep(t *testing.T) {
	w, task := aiWorkerFixture(t)
	calls := 0
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		calls++
		if calls == 1 {
			return toolCompletion("bad-call", "install_packages", `{"manager":"invalid","packages":["nginx"]}`), nil
		}
		return AICompletion{Message: AIMessage{Role: "assistant", Content: "Need a supported package manager."}}, nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		t.Error("invalid action executed")
		return AIActionResult{}, nil
	}
	claimAndRunAI(t, w)
	got, err := models.GetAITask(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "completed" || got.Steps != calls || calls != 2 {
		t.Fatalf("status=%s steps=%d model calls=%d error=%v", got.Status, got.Steps, calls, err)
	}
}

func TestAIWorkerRequestIDCannotChangeTargetOrPrompt(t *testing.T) {
	w, task := aiWorkerFixture(t)
	ctx := context.Background()
	for _, change := range []struct {
		server, provider uuid.UUID
		prompt           string
	}{
		{uuid.New(), task.ProviderID, task.Prompt},
		{task.ServerID, uuid.New(), task.Prompt},
		{task.ServerID, task.ProviderID, "a different operation"},
	} {
		if _, err := w.Create(ctx, task.UserID, change.server, change.provider, task.RequestID, change.prompt); !errors.Is(err, models.ErrAIRequestConflict) {
			t.Fatalf("changed request was accepted: %v", err)
		}
	}
	if got, err := w.Create(ctx, task.UserID, task.ServerID, task.ProviderID, task.RequestID, task.Prompt); err != nil || got.ID != task.ID {
		t.Fatalf("identical retry: id=%s error=%v", got.ID, err)
	}
}

func TestAIWorkerRejectsUserSecretsBeforePersistence(t *testing.T) {
	w, task := aiWorkerFixture(t)
	ctx := context.Background()
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		return AICompletion{Message: AIMessage{Role: "assistant", Content: "Done"}}, nil
	}
	claimAndRunAI(t, w)
	for _, prompt := range []string{
		"Use secret-fixture-key to install this service",
		"Set password=private-test-password",
		"-----BEGIN PRIVATE KEY-----\nprivate-test-data\n-----END PRIVATE KEY-----",
	} {
		if _, err := w.Create(ctx, task.UserID, task.ServerID, task.ProviderID, uuid.New(), prompt); !errors.Is(err, ErrAISecretInput) {
			t.Fatalf("secret creation accepted: %v", err)
		}
		if _, err := w.Continue(ctx, task.UserID, task.ID, uuid.New(), prompt); !errors.Is(err, ErrAISecretInput) {
			t.Fatalf("secret follow-up accepted: %v", err)
		}
	}
	var tasks, userMessages int
	if err := w.db.Raw.QueryRow(`SELECT COUNT(*) FROM ai_tasks WHERE user_id=$1`, task.UserID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Raw.QueryRow(`SELECT COUNT(*) FROM ai_events WHERE task_id=$1 AND kind='user'`, task.ID).Scan(&userMessages); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || userMessages != 1 {
		t.Fatalf("secret submissions persisted: tasks=%d messages=%d", tasks, userMessages)
	}
	request := uuid.New()
	for i := 0; i < 2; i++ {
		if _, err := w.Continue(ctx, task.UserID, task.ID, request, "Check nginx version"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAIWorkerPersistsKnownResultWhenConversationFills(t *testing.T) {
	w, task := aiWorkerFixture(t)
	messages, err := json.Marshal([]AIMessage{{Role: "user", Content: strings.Repeat("a", aiConversationLimit-500)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.db.Raw.Exec(`UPDATE ai_tasks SET messages=$2 WHERE id=$1`, task.ID, messages); err != nil {
		t.Fatal(err)
	}
	calls, executions := 0, 0
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		calls++
		return toolCompletion("inspect", "inspect_server", `{}`), nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		executions++
		return AIActionResult{Output: strings.Repeat("known output ", 200), ExitCode: new(int)}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "failed" || !strings.Contains(got.Error, "Conversation size limit") || calls != 1 || executions != 1 || len(actions) != 1 {
		t.Fatalf("status=%s calls=%d executions=%d actions=%d error=%v detail=%s", got.Status, calls, executions, len(actions), err, got.Error)
	}
	if actions[0].Status != "succeeded" || actions[0].Unknown || !strings.Contains(actions[0].Output, "known output") || actions[0].ExitCode == nil || *actions[0].ExitCode != 0 {
		t.Fatalf("known result was lost: %+v", actions[0])
	}
}

func TestAIWorkerCancellationStopsActiveExecution(t *testing.T) {
	w, task := aiWorkerFixture(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		return toolCompletion("install", "install_packages", `{"manager":"apt","packages":["nginx"]}`), nil
	}
	w.execute = func(ctx context.Context, _ *models.Server, _ AIAction) (AIActionResult, error) {
		close(entered)
		<-ctx.Done()
		return AIActionResult{Unknown: true}, ctx.Err()
	}
	claimed, err := models.ClaimAITask(context.Background(), w.db.Raw, "cancel-worker")
	if err != nil {
		t.Fatal(err)
	}
	go func() { defer close(exited); w.run(claimed) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not executing")
	}
	if _, err = models.CancelAITask(context.Background(), w.db.Raw, task.ID, task.UserID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(6 * time.Second):
		t.Fatal("cancellation did not stop task")
	}
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "canceled" || len(actions) != 1 || !actions[0].Unknown {
		t.Fatal(got, actions, err)
	}
}

func TestAIWorkerRevalidatesTargetBeforeApprovedExecution(t *testing.T) {
	w, task := aiWorkerFixture(t)
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		return toolCompletion("delete", "run_command", `{"command":"rm -rf /tmp/example"}`), nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		t.Error("changed target executed")
		return AIActionResult{}, nil
	}
	claimAndRunAI(t, w)
	_, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.db.Raw.Exec(`UPDATE servers SET host='192.0.2.99' WHERE id=$1`, task.ServerID); err != nil {
		t.Fatal(err)
	}
	if _, err = models.DecideAIAction(context.Background(), w.db.Raw, task.ID, actions[0].ID, task.UserID, true); err != nil {
		t.Fatal(err)
	}
	claimAndRunAI(t, w)
	got, err := models.GetAITask(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "failed" || !strings.Contains(got.Error, "connection settings changed") {
		t.Fatal(got, err)
	}
}

func TestAIRedactionFindsPrivateKeyAfterCertificateAndRejectsSecretArguments(t *testing.T) {
	server := &models.Server{SSHPassword: "ssh-password-fixture"}
	provider := models.AIProvider{APIKey: "api-key-fixture"}
	text := "-----BEGIN CERTIFICATE-----\npublic\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nhidden\n-----END PRIVATE KEY----- tail api-key-fixture"
	got := redactAIText(text, server, provider)
	if strings.Contains(got, "hidden") || strings.Contains(got, provider.APIKey) || !strings.Contains(got, "public") || !strings.Contains(got, "tail") {
		t.Fatal(got)
	}
	for _, args := range []string{`{"command":"echo api-key-fixture"}`, `{"command":"echo ssh-password-fixture"}`, `{"content":"password=super-secret"}`} {
		if !aiKnownSecret(args, server, provider) {
			t.Fatal("secret accepted", args)
		}
	}
	server.SSHPassword = "pa\"ss\\word"
	args, _ := json.Marshal(map[string]any{"command": "echo " + server.SSHPassword})
	if !aiKnownSecret(string(args), server, provider) {
		t.Fatal("JSON escaping hid a credential")
	}
	if got := redactAIText(string(args), server, provider); aiKnownSecret(got, server, provider) || strings.Contains(got, `pa\"ss`) {
		t.Fatal("JSON-escaped credential survived redaction", got)
	}
}

func TestAIWorkerRedactsJSONEncodedOutputBeforeSavingAndNextModelCall(t *testing.T) {
	w, task := aiWorkerFixture(t)
	calls := 0
	w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, _ []AIToolDefinition) (AICompletion, error) {
		calls++
		if calls == 1 {
			return toolCompletion("inspect", "inspect_server", `{}`), nil
		}
		if strings.Contains(messages[len(messages)-1].Content, "fixture-key") {
			t.Error("encoded secret reached the model")
		}
		return AICompletion{Message: AIMessage{Role: "assistant", Content: "Checked"}}, nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		return AIActionResult{Output: `{"value":"\u0073ecret-fixture-key"}`, ExitCode: new(int)}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "completed" || calls != 2 || len(actions) != 1 {
		t.Fatal(got.Status, calls, len(actions), err)
	}
	if strings.Contains(string(got.Messages), "fixture-key") || strings.Contains(actions[0].Output, "fixture-key") || !strings.Contains(actions[0].Output, "redacted") {
		t.Fatal("encoded secret persisted")
	}
}

func TestAIRedactionScansNestedFileJSONAndDuplicateValues(t *testing.T) {
	server := &models.Server{SSHPassword: "pa\"ss\\word"}
	provider := models.AIProvider{APIKey: "api-key-fixture"}
	encoded := `{"value":"\u0061pi-key-fixture"}`
	wrapped, err := json.Marshal(map[string]string{"content": encoded, "revision": "fixture-revision"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		string(wrapped),
		`{"value":"\u0061pi-key-fixture","value":"harmless"}`,
		`{"command":"pa\"ss\\word","command":"harmless"}`,
	} {
		if !aiKnownSecret(text, server, provider) {
			t.Error("encoded credential escaped detection")
		}
		if got := redactAIText(text, server, provider); strings.Contains(got, "fixture") || strings.Contains(got, `pa\"ss`) || !strings.Contains(got, "redacted") {
			t.Error("encoded credential escaped redaction")
		}
	}
	for _, text := range []string{`{"content":"server { listen 80; }","revision":"fixture"}`, `{"value":"ordinary","value":"also ordinary"}`, "service status: active"} {
		if aiKnownSecret(text, server, provider) || redactAIText(text, server, provider) != text {
			t.Error("ordinary diagnostic output was removed", text)
		}
	}
}

func TestAIWorkerRefusesSecretArgumentsBeforeRecordingOrExecuting(t *testing.T) {
	w, task := aiWorkerFixture(t)
	calls := 0
	w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, _ []AIToolDefinition) (AICompletion, error) {
		calls++
		if calls == 1 {
			return toolCompletion("secret-call", "run_command", `{"command":"echo secret-fixture-key"}`), nil
		}
		raw, _ := json.Marshal(messages)
		if strings.Contains(string(raw), "secret-fixture-key") {
			t.Error("secret remained in context")
		}
		return AICompletion{Message: AIMessage{Role: "assistant", Content: "Cannot use credentials"}}, nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		t.Error("secret command executed")
		return AIActionResult{}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || len(actions) != 0 || strings.Contains(string(got.Messages), "secret-fixture-key") {
		t.Fatal(got, actions, err)
	}
}

func TestAIWorkerAgentIdentityChangesInvalidatePendingApproval(t *testing.T) {
	for _, change := range []string{"reenrollment", "runtime user", "runtime OS"} {
		t.Run(change, func(t *testing.T) {
			w, first := aiWorkerFixture(t)
			ctx := context.Background()
			if _, err := models.CancelAITask(ctx, w.db.Raw, first.ID, first.UserID); err != nil {
				t.Fatal(err)
			}
			if _, err := w.db.Raw.Exec(`UPDATE servers SET connection_mode='agent',host='agent',ssh_username='root',server_type='linux' WHERE id=$1`, first.ServerID); err != nil {
				t.Fatal(err)
			}
			var hello agenttransport.Hello
			enroll := func() {
				t.Helper()
				var err error
				hello, _, err = agenttransport.NewIdentity("fixture")
				if err != nil {
					t.Fatal(err)
				}
				hello.OS = "linux"
				hello.Arch = "amd64"
				hello.Username = "root"
				ticket, _, err := models.CreateAgentTicket(ctx, w.db.Raw, first.ServerID, first.UserID)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = models.RegisterAgent(ctx, w.db.Raw, ticket, hello); err != nil {
					t.Fatal(err)
				}
			}
			enroll()
			task, err := w.Create(ctx, first.UserID, first.ServerID, first.ProviderID, uuid.New(), "Inspect this server")
			if err != nil {
				t.Fatal(err)
			}
			w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
				return toolCompletion("review", "run_command", `{"command":"echo approved-action"}`), nil
			}
			w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
				t.Error("old approval reached replacement Agent")
				return AIActionResult{}, nil
			}
			claimAndRunAI(t, w)
			_, _, actions, err := models.AITaskDetails(ctx, w.db.Raw, task.ID, task.UserID)
			if err != nil || len(actions) != 1 {
				t.Fatal(actions, err)
			}
			switch change {
			case "reenrollment":
				enroll()
			case "runtime user":
				hello.Username = "monitor-service"
			case "runtime OS":
				hello.OS = "windows"
			}
			if change != "reenrollment" {
				if err := models.TouchAgent(ctx, w.db.Raw, first.ServerID, hello); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = models.DecideAIAction(ctx, w.db.Raw, task.ID, actions[0].ID, task.UserID, true); err != nil {
				t.Fatal(err)
			}
			claimAndRunAI(t, w)
			got, err := models.GetAITask(ctx, w.db.Raw, task.ID, task.UserID)
			if err != nil || got.Status != "failed" || !strings.Contains(got.Error, "connection settings changed") {
				t.Fatal(got, err)
			}
		})
	}
}
