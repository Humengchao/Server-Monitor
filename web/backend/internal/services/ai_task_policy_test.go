package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func selectedAITask(t *testing.T, w *AITaskWorker, first models.AITask, selection models.AITaskSelection) models.AITask {
	t.Helper()
	ctx := context.Background()
	if _, err := models.CancelAITask(ctx, w.db.Raw, first.ID, first.UserID); err != nil {
		t.Fatal(err)
	}
	task, err := w.Create(ctx, first.UserID, first.ServerID, first.ProviderID, uuid.New(), first.Prompt, selection)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestAITaskApprovalModesEnforceSavedAuthority(t *testing.T) {
	for _, test := range []struct {
		mode, name, args, want string
		executes               int
	}{
		{"standard", "run_command", `{"command":"rm -rf /tmp/fixture"}`, "awaiting_approval", 0},
		{"automatic", "run_command", `{"command":"rm -rf /tmp/fixture"}`, "completed", 1},
		{"readonly", "install_packages", `{"manager":"apt","packages":["nginx"]}`, "completed", 0},
		{"readonly", "run_command", `{"command":"rm -rf /tmp/fixture"}`, "completed", 0},
		{"readonly", "inspect_server", `{}`, "completed", 1},
	} {
		t.Run(test.mode+"/"+test.name, func(t *testing.T) {
			w, first := aiWorkerFixture(t)
			task := selectedAITask(t, w, first, models.AITaskSelection{ApprovalMode: test.mode})
			calls, executions := 0, 0
			w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, tools []AIToolDefinition) (AICompletion, error) {
				calls++
				if !strings.Contains(messages[0].Content, "Execution policy:") {
					t.Error("missing execution policy")
				}
				if calls == 1 {
					return toolCompletion("one", test.name, test.args), nil
				}
				return AICompletion{Message: AIMessage{Role: "assistant", Content: "Finished"}}, nil
			}
			w.execute = func(_ context.Context, _ *models.Server, action AIAction) (AIActionResult, error) {
				executions++
				if action.ApprovalMode != test.mode {
					t.Error("policy lost before execution")
				}
				return AIActionResult{Output: "done", ExitCode: new(int)}, nil
			}
			claimAndRunAI(t, w)
			got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
			if err != nil || got.Status != test.want || executions != test.executes {
				t.Fatalf("status=%s exec=%d err=%v", got.Status, executions, err)
			}
			if test.mode == "automatic" && len(actions) == 1 && actions[0].RequiresApproval {
				t.Error("automatic task paused for approval")
			}
			changed := models.AITaskSelection{ApprovalMode: "automatic"}
			if test.mode == "automatic" {
				changed.ApprovalMode = "standard"
			}
			if _, err = w.Create(context.Background(), task.UserID, task.ServerID, task.ProviderID, task.RequestID, task.Prompt, changed); !errors.Is(err, models.ErrAIRequestConflict) {
				t.Fatalf("idempotency allowed changed authority: %v", err)
			}
		})
	}
}

func TestAITaskSkillSnapshotKeepsOriginalContentAndScopesOwnership(t *testing.T) {
	w, first := aiWorkerFixture(t)
	ctx := context.Background()
	skill := models.AISkill{UserID: first.UserID, Name: "Inspect carefully", Content: "Check the installed version before making changes."}
	if err := models.SaveAISkill(ctx, w.db, &skill, true); err != nil {
		t.Fatal(err)
	}
	task := selectedAITask(t, w, first, models.AITaskSelection{SkillIDs: []uuid.UUID{skill.ID}})
	skill.Content = "replacement instructions"
	if err := models.SaveAISkill(ctx, w.db, &skill, false); err != nil {
		t.Fatal(err)
	}
	w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, _ []AIToolDefinition) (AICompletion, error) {
		if !strings.Contains(messages[0].Content, "Check the installed version") || strings.Contains(messages[0].Content, "replacement instructions") {
			t.Error("skill snapshot changed")
		}
		return AICompletion{Message: AIMessage{Role: "assistant", Content: "Done"}}, nil
	}
	claimAndRunAI(t, w)
	got, err := models.GetAITask(ctx, w.db.Raw, task.ID, task.UserID)
	if err != nil || len(got.Skills) != 1 || got.Skills[0].ID != skill.ID {
		t.Fatal(got.Skills, err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "Check the installed version") || strings.Contains(string(raw), "fingerprint") {
		t.Error("private snapshot exposed")
	}
	if _, err = w.Create(ctx, task.UserID, task.ServerID, task.ProviderID, uuid.New(), "inspect", models.AITaskSelection{SkillIDs: []uuid.UUID{uuid.New()}}); err == nil {
		t.Error("unknown skill accepted")
	}
}

func TestAIWorkerRejectsCredentialInToolCallIdentity(t *testing.T) {
	w, task := aiWorkerFixture(t)
	w.complete = func(context.Context, models.AIProvider, []AIMessage, []AIToolDefinition) (AICompletion, error) {
		return toolCompletion("secret-fixture-key", "inspect_server", `{}`), nil
	}
	w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
		t.Error("credential-bearing call executed")
		return AIActionResult{}, nil
	}
	claimAndRunAI(t, w)
	got, _, actions, err := models.AITaskDetails(context.Background(), w.db.Raw, task.ID, task.UserID)
	if err != nil || got.Status != "failed" || len(actions) != 0 || strings.Contains(string(got.Messages), "secret-fixture-key") || strings.Contains(got.Error, "secret-fixture-key") {
		t.Fatal("credential-bearing identity was retained", got.Status, len(actions), err)
	}
}

func TestAITaskMCPApprovalSchemaChangesAndAutomaticExecution(t *testing.T) {
	for _, mode := range []string{"standard", "automatic", "readonly", "changed-schema", "changed-config", "reordered-schema"} {
		t.Run(mode, func(t *testing.T) {
			w, first := aiWorkerFixture(t)
			ctx := context.Background()
			plugin := models.AIPlugin{UserID: first.UserID, Name: "Fixture MCP", URL: "https://mcp.example.invalid/mcp", BearerToken: "plugin-private-fixture", Enabled: true}
			if err := models.SaveAIPlugin(ctx, w.db, &plugin, true, false); err != nil {
				t.Fatal(err)
			}
			level := mode
			if strings.HasPrefix(mode, "changed-") || mode == "reordered-schema" {
				level = "standard"
			}
			task := selectedAITask(t, w, first, models.AITaskSelection{ApprovalMode: level, PluginIDs: []uuid.UUID{plugin.ID}})
			description := "Test operation"
			schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
			modelCalls, executions := 0, 0
			w.mcpList = func(context.Context, models.AIPlugin) ([]AIMCPTool, error) {
				return []AIMCPTool{{Name: "service.inspect", Description: description, InputSchema: schema}}, nil
			}
			w.complete = func(_ context.Context, _ models.AIProvider, messages []AIMessage, tools []AIToolDefinition) (AICompletion, error) {
				modelCalls++
				alias := aiPluginToolName(plugin.ID, "service.inspect")
				if level == "readonly" {
					for _, tool := range tools {
						if tool.Function.Name == alias {
							t.Error("MCP exposed in readonly mode")
						}
					}
				}
				if modelCalls == 1 {
					return toolCompletion("mcp-call", alias, `{"name":"nginx"}`), nil
				}
				if strings.Contains(messages[len(messages)-1].Content, plugin.BearerToken) {
					t.Error("plugin secret sent to model")
				}
				return AICompletion{Message: AIMessage{Role: "assistant", Content: "Done"}}, nil
			}
			w.mcpCall = func(_ context.Context, p models.AIPlugin, name string, args json.RawMessage, approved, readonly bool) (AIActionResult, error) {
				executions++
				if p.ID != plugin.ID || name != "service.inspect" || !approved || readonly {
					t.Error("MCP scope mismatch")
				}
				return AIActionResult{Output: "result " + plugin.BearerToken, ExitCode: new(int)}, nil
			}
			w.execute = func(context.Context, *models.Server, AIAction) (AIActionResult, error) {
				t.Error("plugin sent to SSH")
				return AIActionResult{}, nil
			}
			claimAndRunAI(t, w)
			got, _, actions, err := models.AITaskDetails(ctx, w.db.Raw, task.ID, task.UserID)
			if err != nil {
				t.Fatal(err)
			}
			if level == "standard" {
				if got.Status != "awaiting_approval" || executions != 0 || len(actions) != 1 {
					t.Fatalf("approval missing: %s %d", got.Status, executions)
				}
				if mode == "changed-schema" {
					description = "changed operation"
				}
				if mode == "reordered-schema" {
					schema = json.RawMessage(`{"properties":{"name":{"type":"string"}},"type":"object"}`)
				}
				if mode == "changed-config" {
					plugin.URL = "https://another.example.invalid/mcp"
					if err := models.SaveAIPlugin(ctx, w.db, &plugin, false, false); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := models.DecideAIAction(ctx, w.db.Raw, task.ID, actions[0].ID, task.UserID, true); err != nil {
					t.Fatal(err)
				}
				claimAndRunAI(t, w)
				got, _, actions, err = models.AITaskDetails(ctx, w.db.Raw, task.ID, task.UserID)
				if err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if level == "readonly" || strings.HasPrefix(mode, "changed-") {
				want = 0
			}
			if executions != want {
				t.Fatalf("exec=%d want=%d status=%s", executions, want, got.Status)
			}
			if strings.HasPrefix(mode, "changed-") && got.Status != "failed" {
				t.Error("changed plugin action did not stop")
			}
			for _, action := range actions {
				if strings.Contains(action.Output, plugin.BearerToken) {
					t.Error("plugin credential persisted")
				}
			}
		})
	}
}
