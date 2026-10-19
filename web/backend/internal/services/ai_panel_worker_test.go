package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestAIFleetTaskReadsMonitoringCreatesRuleAndVerifiesItWithoutSSH(t *testing.T) {
	w, initial := aiWorkerFixture(t)
	ctx := context.Background()
	if _, err := models.CancelAITask(ctx, w.db.Raw, initial.ID, initial.UserID); err != nil {
		t.Fatal(err)
	}
	task, err := w.Create(ctx, initial.UserID, uuid.Nil, initial.ProviderID, uuid.New(), "Analyze all servers and add a CPU > 90% for five minutes alert", models.AITaskSelection{Scope: "fleet"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	arguments := `{"name":"AI fleet CPU","scope":"all_owned","metric":"cpu","comparator":">","threshold":90,"duration_seconds":300,"enabled":true,"channel_ids":[]}`
	w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, tools []AIToolDefinition) (AICompletion, error) {
		calls++
		found := map[string]bool{}
		for _, tool := range tools {
			found[tool.Function.Name] = true
		}
		for _, name := range []string{"monitor_fleet", "monitor_history", "monitor_alerts", "monitor_create_alert"} {
			if !found[name] {
				t.Error("missing panel tool", name)
			}
		}
		for _, name := range []string{"run_command", "read_file", "write_file", "inspect_server", "install_packages", "manage_service"} {
			if found[name] {
				t.Error("fleet exposed remote tool", name)
			}
		}
		if !strings.Contains(messages[0].Content, "account-level Server Monitor task") {
			t.Error("fleet prompt missing scope")
		}
		switch calls {
		case 1:
			return toolCompletion("fleet-read", "monitor_fleet", `{}`), nil
		case 2:
			if !strings.Contains(messages[len(messages)-1].Content, "total_servers") {
				t.Error("no monitoring evidence")
			}
			return toolCompletion("create-rule", "monitor_create_alert", arguments), nil
		case 3:
			if !strings.Contains(messages[len(messages)-1].Content, "rule_id") {
				t.Error("created rule result missing")
			}
			return toolCompletion("duplicate-rule", "monitor_create_alert", arguments), nil
		case 4:
			if !strings.Contains(messages[len(messages)-1].Content, "already_exists") {
				t.Error("duplicate was not reported")
			}
			return toolCompletion("verify-rule", "monitor_alerts", `{"kind":"rules"}`), nil
		default:
			if !strings.Contains(messages[len(messages)-1].Content, "AI fleet CPU") {
				t.Error("rule not visible in panel read")
			}
			return AICompletion{Message: AIMessage{Role: "assistant", Content: "Verified the fleet alert in Server Monitor."}}, nil
		}
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		t.Error("panel task opened SSH execution")
		return AIActionResult{}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(ctx, w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "completed" || got.Scope != "fleet" || got.ServerID != uuid.Nil || len(actions) != 4 {
		t.Fatalf("status=%s actions=%d detail=%s err=%v", got.Status, len(actions), got.Error, err)
	}
	var count int
	if err = w.db.Raw.QueryRow(`SELECT COUNT(*) FROM alert_rules WHERE user_id=$1 AND name='AI fleet CPU' AND server_id IS NULL AND threshold=90 AND duration_seconds=300`, task.UserID).Scan(&count); err != nil || count != 1 {
		t.Fatal("wrong rule count", count, err)
	}
	for _, action := range actions {
		if action.Status != "succeeded" {
			t.Fatal(action.Status, action.Error)
		}
	}
}

func TestAIFleetTasksDoNotRequireServersOrShareCrossAccountTargetLocks(t *testing.T) {
	w, initial := aiWorkerFixture(t)
	ctx := context.Background()
	if _, err := models.CancelAITask(ctx, w.db.Raw, initial.ID, initial.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Raw.Exec(`DELETE FROM servers WHERE id=$1`, initial.ServerID); err != nil {
		t.Fatal(err)
	}
	first, err := w.Create(ctx, initial.UserID, uuid.Nil, initial.ProviderID, uuid.New(), "Analyze the empty panel", models.AITaskSelection{Scope: "fleet", ApprovalMode: "readonly"})
	if err != nil {
		t.Fatal("fleet required a server", err)
	}
	foreign, _, _ := testdb.Seed(t, w.db.Raw)
	provider := models.AIProvider{UserID: foreign, Name: "other", Kind: "compatible", BaseURL: "https://example.invalid/v1", Model: "mock", APIKey: "other-key", Enabled: true}
	if err = models.SaveAIProvider(ctx, w.db, &provider, true); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Create(ctx, foreign, uuid.Nil, provider.ID, uuid.New(), "Analyze my panel", models.AITaskSelection{Scope: "fleet"}); err != nil {
		t.Fatal("fleet tasks shared a global zero target lock", err)
	}
	if _, err = w.Create(ctx, first.UserID, uuid.Nil, first.ProviderID, uuid.New(), "Second panel task", models.AITaskSelection{Scope: "fleet"}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Create(ctx, first.UserID, uuid.Nil, first.ProviderID, uuid.New(), "Third panel task", models.AITaskSelection{Scope: "fleet"}); !errors.Is(err, models.ErrAITaskLimit) {
		t.Fatal("account limit bypass", err)
	}
	if _, err = w.Create(ctx, first.UserID, initial.ServerID, first.ProviderID, first.RequestID, first.Prompt, models.AITaskSelection{Scope: "server", ApprovalMode: "readonly"}); !errors.Is(err, models.ErrAIRequestConflict) {
		t.Fatal("request changed scope", err)
	}
}

func TestAIFleetReadonlyRejectsRemoteAndPanelMutations(t *testing.T) {
	for _, name := range []string{"monitor_create_alert", "run_command"} {
		t.Run(name, func(t *testing.T) {
			w, initial := aiWorkerFixture(t)
			ctx := context.Background()
			if _, err := models.CancelAITask(ctx, w.db.Raw, initial.ID, initial.UserID); err != nil {
				t.Fatal(err)
			}
			task, err := w.Create(ctx, initial.UserID, uuid.Nil, initial.ProviderID, uuid.New(), "Inspect the panel", models.AITaskSelection{Scope: "fleet", ApprovalMode: "readonly"})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, _ []AIToolDefinition) (AICompletion, error) {
				calls++
				if calls == 1 {
					args := `{"command":"whoami"}`
					if name == "monitor_create_alert" {
						args = `{"name":"forbidden","scope":"all_owned","metric":"cpu","threshold":90,"duration_seconds":300,"enabled":true}`
					}
					return toolCompletion("refused", name, args), nil
				}
				return AICompletion{Message: AIMessage{Role: "assistant", Content: "That operation is unavailable in this task."}}, nil
			}
			w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
				t.Error("remote action executed")
				return AIActionResult{}, nil
			}
			claimAndRunAI(t, w)
			got, _, actions, err := models.AITaskDetails(ctx, w.db.Raw, task.ID, task.UserID)
			if err != nil || got.Status != "completed" || len(actions) != 0 {
				t.Fatal(got.Status, len(actions), err)
			}
			var messages []AIMessage
			if json.Unmarshal(got.Messages, &messages) != nil {
				t.Fatal("invalid stored messages")
			}
		})
	}
}
