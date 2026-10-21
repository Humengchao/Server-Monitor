package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"server-monitor/internal/database"
	"server-monitor/internal/testdb"
)

func TestAIConversationEventPhase(t *testing.T) {
	for _, test := range []struct {
		name, messages, content, phase string
		invalid                        bool
	}{
		{"explanation", `[{"role":"assistant","content":"Inspecting","tool_calls":[{"id":"one"}]}]`, "Inspecting", "progress", false},
		{"final", `[{"role":"assistant","content":"Healthy","reasoning_content":"private"}]`, "Healthy", "final", false},
		{"question", `[{"role":"assistant","content":"Which service?","tool_calls":[]}]`, "Which service?", "final", false},
		{"null_calls", `[{"role":"assistant","content":"Healthy","tool_calls":null}]`, "Healthy", "final", false},
		{"rejected_multiple_calls", `[{"role":"assistant","content":"Inspecting","tool_calls":[{"id":"one"},{"id":"two"}]},{"role":"tool","content":"Rejected"},{"role":"tool","content":"Rejected"}]`, "Inspecting", "progress", false},
		{"empty_content", `[{"role":"assistant","content":null,"tool_calls":[{"id":"one"}]}]`, "", "", false},
		{"mismatched_content", `[{"role":"assistant","content":"Inspecting","tool_calls":[{"id":"one"}]}]`, "Healthy", "", true},
		{"stale_response", `[{"role":"assistant","content":"Healthy"},{"role":"user","content":"Check again"}]`, "Healthy", "", true},
		{"missing_response", `[{"role":"user","content":"Healthy"}]`, "Healthy", "", true},
		{"invalid_json", `{`, "Healthy", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			phase, err := aiConversationEventPhase(json.RawMessage(test.messages), test.content)
			if phase != test.phase || (err != nil) != test.invalid {
				t.Fatalf("phase=%q err=%v", phase, err)
			}
		})
	}
}

func TestAITaskEventPhasesPreserveModelStepsAndPrivateContext(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	foreign, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	claimed, err := ClaimAITask(ctx, db, "phase-worker")
	if err != nil || claimed.ID != task.ID {
		t.Fatal(err)
	}
	progress := json.RawMessage(`[{"role":"user","content":"inspect"},{"role":"assistant","content":"Checking the service","reasoning_content":"private reasoning fixture","tool_calls":[{"id":"one","type":"function","function":{"name":"inspect_server","arguments":"{}"}}]}]`)
	if err = SaveAIConversation(ctx, db, task.ID, claimed.WorkerID, progress, "Checking the service", 10, 4); err != nil {
		t.Fatal(err)
	}
	validation := json.RawMessage(strings.TrimSuffix(string(progress), "]") + `,{"role":"tool","tool_call_id":"one","content":"invalid arguments"}]`)
	if err = SaveAIValidationReply(ctx, db, task.ID, claimed.WorkerID, validation); err != nil {
		t.Fatal(err)
	}
	got, events, actions, err := AITaskDetails(ctx, db, task.ID, user)
	if err != nil || got.Status != "running" || got.Steps != 1 || got.PromptTokens != 10 || got.CompletionTokens != 4 || len(events) != 2 || len(actions) != 0 {
		t.Fatalf("after validation: status=%s steps=%d tokens=%d/%d events=%d actions=%d err=%v", got.Status, got.Steps, got.PromptTokens, got.CompletionTokens, len(events), len(actions), err)
	}
	if events[0].Phase != "" || events[1].Kind != "assistant" || events[1].Phase != "progress" {
		t.Fatalf("validation changed phases: %+v", events)
	}
	final := json.RawMessage(strings.TrimSuffix(string(validation), "]") + `,{"role":"assistant","content":"The service is healthy","reasoning_content":"private final reasoning fixture"}]`)
	if err = SaveAIConversation(ctx, db, task.ID, claimed.WorkerID, final, "The service is healthy", 15, 7); err != nil {
		t.Fatal(err)
	}
	// The phase is reliable even before the worker separately marks completion.
	got, events, _, err = AITaskDetails(ctx, db, task.ID, user)
	if err != nil || got.Status != "running" || got.Steps != 2 || got.PromptTokens != 25 || got.CompletionTokens != 11 || len(events) != 3 || events[2].Phase != "final" {
		t.Fatalf("final response was not recorded independently of status: events=%+v err=%v", events, err)
	}
	raw, err := json.Marshal(struct {
		Task   AITask    `json:"task"`
		Events []AIEvent `json:"events"`
	}{got, events})
	if err != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "reasoning_content") || strings.Contains(string(raw), "tool_calls") || strings.Contains(string(raw), `"messages"`) {
		t.Fatalf("private conversation exposed: %v", err)
	}
	if _, _, _, err = AITaskDetails(ctx, db, task.ID, foreign); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign task events were exposed: %v", err)
	}
	if err = SaveAIConversation(ctx, db, task.ID, "different-worker", final, "The service is healthy", 15, 7); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("another worker saved an event: %v", err)
	}
	if err = SaveAIConversation(ctx, db, task.ID, claimed.WorkerID, final, "Not the saved response", 15, 7); err == nil {
		t.Fatal("mismatched event was saved")
	}
	got, events, _, err = AITaskDetails(ctx, db, task.ID, user)
	if err != nil || got.Steps != 2 || len(events) != 3 {
		t.Fatal("rejected saves changed the conversation", got.Steps, len(events), err)
	}
}

func TestAITaskEventPhaseMigrationPreservesLegacyEvents(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	task := seedAITask(t, db, user, server)
	// Simulate the previous schema in this test's isolated PostgreSQL schema.
	if _, err := db.Exec(`ALTER TABLE ai_events DROP COLUMN phase`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_events(task_id,kind,content) VALUES($1,'assistant','Legacy response')`, task.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.RunMigrations(db); err != nil {
			t.Fatal(err)
		}
	}
	_, events, _, err := AITaskDetails(ctx, db, task.ID, user)
	if err != nil || len(events) != 2 || events[1].Content != "Legacy response" {
		t.Fatal("legacy event changed", events, err)
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil || event.Phase != "" || strings.Contains(string(raw), `"phase"`) {
			t.Fatal("legacy phase was inferred or exposed", event, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ai_events(task_id,kind,content,phase) VALUES($1,'assistant','Invalid phase','complete')`, task.ID); err == nil {
		t.Fatal("unsupported event phase accepted")
	}
}
