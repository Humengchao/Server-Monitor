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

func TestAIPanelAlertToolSchemasAndPolicy(t *testing.T) {
	tools := AIPanelAlertTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools: %d", len(tools))
	}
	for _, tool := range tools {
		if !IsAIPanelAlertTool(tool.Function.Name) {
			t.Fatal(tool.Function.Name)
		}
		raw, err := json.Marshal(tool)
		if err != nil || strings.Contains(string(raw), `"user_id"`) || strings.Contains(string(raw), `"webhook_url"`) || strings.Contains(string(raw), `"expected_revision"`) {
			t.Fatal("tool permits internal or secret arguments", string(raw), err)
		}
	}
	if IsAIPanelAlertTool("monitor_delete_all") {
		t.Fatal("unrecognized tool accepted")
	}
	for _, operation := range []string{"monitor_create_alert", "monitor_update_alert", "monitor_delete_alert"} {
		action := AIAction{Mutating: true, RequiresApproval: operation != "monitor_create_alert"}
		if _, err := aiActionApproval("readonly", action); err == nil {
			t.Fatal("readonly mutation accepted")
		}
		if approval, err := aiActionApproval("standard", action); err != nil || approval != (operation != "monitor_create_alert") {
			t.Fatal(approval, err)
		}
		if approval, err := aiActionApproval("automatic", action); err != nil || approval {
			t.Fatal(approval, err)
		}
	}
}

func TestAIPanelAlertPrepareRejectsExtraAuthorityAndKeepsSavedRevision(t *testing.T) {
	raw := testdb.Open(t)
	user, server, rule := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw}
	task := models.AITask{UserID: user, ServerID: server, ApprovalMode: "standard"}
	ctx := context.Background()
	args := map[string]any{"name": "AI CPU", "scope": "server", "server_id": server, "metric": "cpu", "threshold": 85, "duration_seconds": 300, "enabled": true}
	call := func(operation string, args any) AIToolCall {
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return AIToolCall{Type: "function", Function: AIFunctionCall{Name: operation, Arguments: string(data)}}
	}
	create, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_create_alert", args))
	if err != nil || create.RequiresApproval || !create.Mutating || create.Command != "" {
		t.Fatal(create, err)
	}
	if strings.Contains(string(create.Arguments), "https://example.invalid") {
		t.Fatal("webhook leaked")
	}
	for _, key := range []string{"user_id", "webhook_url", "command", "requires_approval"} {
		args[key] = "spoofed"
		if _, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_create_alert", args)); err == nil {
			t.Fatal("forbidden argument accepted", key)
		}
		delete(args, key)
	}
	for _, key := range []string{"name", "scope", "metric", "threshold", "duration_seconds", "enabled"} {
		value := args[key]
		delete(args, key)
		if _, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_create_alert", args)); err == nil {
			t.Fatal("missing field accepted", key)
		}
		args[key] = value
	}
	args["rule_id"] = rule
	args["channel_ids"] = []uuid.UUID{}
	update, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_update_alert", args))
	if err != nil || !update.RequiresApproval {
		t.Fatal(update, err)
	}
	preparedAgain, err := PrepareAIPanelAlertAction(ctx, db, task, AIToolCall{Function: AIFunctionCall{Name: update.ToolName, Arguments: string(update.Arguments)}})
	if err != nil || string(preparedAgain.Arguments) != string(update.Arguments) {
		t.Fatal("reprepare changed exact arguments", preparedAgain, err)
	}
	if _, err := raw.Exec(`UPDATE alert_rules SET threshold=50 WHERE id=$1`, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAIPanelAlertAction(ctx, db, task, AIToolCall{Function: AIFunctionCall{Name: update.ToolName, Arguments: string(update.Arguments)}}); !errors.Is(err, models.ErrAIPanelAlertRevision) {
		t.Fatal("stale rule approval silently refreshed", err)
	}
	del, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_delete_alert", map[string]any{"rule_id": rule}))
	if err != nil || !del.RequiresApproval {
		t.Fatal(del, err)
	}
	preparedAgain, err = PrepareAIPanelAlertAction(ctx, db, task, AIToolCall{Function: AIFunctionCall{Name: del.ToolName, Arguments: string(del.Arguments)}})
	if err != nil || string(preparedAgain.Arguments) != string(del.Arguments) {
		t.Fatal("delete cannot resume", preparedAgain, err)
	}
	task.ApprovalMode = "readonly"
	if _, err := PrepareAIPanelAlertAction(ctx, db, task, call("monitor_delete_alert", map[string]any{"rule_id": rule})); err == nil {
		t.Fatal("readonly delete accepted")
	}
}

func TestAIPanelAlertUpdatePreservesOmittedChannelsButAllowsExplicitClear(t *testing.T) {
	raw := testdb.Open(t)
	user, server, rule := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw}
	channel := uuid.New()
	if _, err := raw.Exec(`INSERT INTO notification_channels(id,user_id,name,type,config_encrypted) VALUES($1,$2,'test','telegram','opaque')`, channel, user); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2)`, rule, channel); err != nil {
		t.Fatal(err)
	}
	task := models.AITask{UserID: user, ServerID: server, ApprovalMode: "standard"}
	args := map[string]any{"rule_id": rule, "name": "CPU", "scope": "server", "server_id": server, "metric": "cpu", "threshold": 85, "duration_seconds": 300, "enabled": true}
	for _, clear := range []bool{false, true} {
		if clear {
			args["channel_ids"] = []uuid.UUID{}
		}
		data, _ := json.Marshal(args)
		prepared, err := PrepareAIPanelAlertAction(context.Background(), db, task, AIToolCall{Function: AIFunctionCall{Name: "monitor_update_alert", Arguments: string(data)}})
		if err != nil {
			t.Fatal(err)
		}
		var got models.AIPanelAlertArguments
		if err := json.Unmarshal(prepared.Arguments, &got); err != nil {
			t.Fatal(err)
		}
		if clear && len(got.ChannelIDs) != 0 || !clear && (len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != channel) {
			t.Fatal(clear, got.ChannelIDs)
		}
		again, err := PrepareAIPanelAlertAction(context.Background(), db, task, AIToolCall{Function: AIFunctionCall{Name: "monitor_update_alert", Arguments: string(prepared.Arguments)}})
		if err != nil || string(again.Arguments) != string(prepared.Arguments) {
			t.Fatal("empty channel selection changed on resume", clear, err)
		}
	}
}
