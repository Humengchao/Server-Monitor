package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func IsAIPanelAlertTool(name string) bool {
	return name == "monitor_create_alert" || name == "monitor_update_alert" || name == "monitor_delete_alert"
}

func AIPanelAlertTools() []AIToolDefinition {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	definition := func(name, description string, properties map[string]any, required ...string) AIToolDefinition {
		return AIToolDefinition{Type: "function", Function: AIFunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
	}
	properties := func() map[string]any {
		return map[string]any{
			"name":             text("Rule name, at most 128 UTF-8 bytes. Reusing the exact same configuration will not create another rule."),
			"scope":            map[string]any{"type": "string", "enum": []string{"server", "all_owned"}, "description": "Explicit server or all servers owned by the user. all_owned requires a fleet task and also applies to future owned servers."},
			"server_id":        text("Exact owned server UUID, required only for scope=server. A single-server task may only target its selected server."),
			"metric":           map[string]any{"type": "string", "enum": []string{"cpu", "memory", "disk", "load1", "latency", "offline"}},
			"comparator":       map[string]any{"type": "string", "enum": []string{">", "<"}},
			"threshold":        map[string]any{"type": "number", "minimum": 0, "description": "cpu/memory/disk use 0-100 percent, load1 is load average, latency is milliseconds; offline ignores threshold."},
			"duration_seconds": map[string]any{"type": "integer", "minimum": 30, "maximum": 86400, "description": "Continuous breach duration before firing, 30 to 86400 seconds."},
			"enabled":          map[string]any{"type": "boolean"},
			"channel_ids":      map[string]any{"type": "array", "maxItems": 32, "uniqueItems": true, "items": text("Existing owned notification channel UUID, obtained from monitor_channels."), "description": "Optional: new rules without channels are visible in the panel only. Updates preserve associations when omitted; [] clears them. Never provide notification secrets or external URLs."},
		}
	}
	required := []string{"name", "scope", "metric", "threshold", "duration_seconds", "enabled"}
	create := definition("monitor_create_alert", "Create a Server Monitor alert rule using dashboard metrics and existing notification channels. This changes the panel, not the remote host. Read existing rules first. Standard mode creates enabled rules immediately; read-only mode forbids writes. Do not create duplicate rules or invent external destinations.", properties(), required...)
	updateProperties := properties()
	updateProperties["rule_id"] = text("Existing owned rule UUID returned by monitor_alerts(kind=rules). Read that rule first and copy unchanged fields.")
	update := definition("monitor_update_alert", "Update a Server Monitor alert rule with the complete desired configuration. Existing webhook credentials are preserved and never exposed. All updates require approval in standard mode; fully automatic mode executes directly. The exact rule/channel revisions are checked again at execution.", updateProperties, append([]string{"rule_id"}, required...)...)
	del := definition("monitor_delete_alert", "Delete one existing owned Server Monitor alert rule. Requires exact-action approval in standard mode, runs directly in fully automatic mode, and is forbidden in read-only mode. A single-server task cannot delete global rules or another server's rules.", map[string]any{"rule_id": text("Existing owned rule UUID")}, "rule_id")
	return []AIToolDefinition{create, update, del}
}

func PrepareAIPanelAlertAction(ctx context.Context, db *models.DB, task models.AITask, call AIToolCall) (AIAction, error) {
	action := AIAction{ToolName: call.Function.Name, Mutating: true}
	if !IsAIPanelAlertTool(action.ToolName) || call.Type != "" && call.Type != "function" {
		return action, errors.New("unsupported panel alert tool")
	}
	if task.ApprovalMode == "readonly" {
		return action, errors.New("read-only tasks cannot change alert rules")
	}
	var args models.AIPanelAlertArguments
	raw := []byte(call.Function.Arguments)
	if err := validateAIJSON(raw); err != nil {
		return action, err
	}
	if err := decodeAIArguments(raw, &args); err != nil {
		return action, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return action, err
	}
	if action.ToolName == "monitor_delete_alert" {
		for key := range fields {
			if key != "rule_id" && key != "expected_revision" && key != "channel_ids" {
				return action, errors.New("delete accepts only rule_id")
			}
		}
	} else {
		for _, key := range []string{"name", "scope", "metric", "threshold", "duration_seconds", "enabled"} {
			if value, exists := fields[key]; !exists || string(value) == "null" {
				return action, fmt.Errorf("%s is required", key)
			}
		}
	}
	if action.ToolName == "monitor_create_alert" && args.RuleID != uuid.Nil || action.ToolName != "monitor_create_alert" && args.RuleID == uuid.Nil {
		return action, errors.New("rule_id is required only for existing rules")
	}
	if action.ToolName != "monitor_delete_alert" {
		if err := models.NormalizeAIPanelAlert(&args); err != nil {
			return action, err
		}
	}
	if len(args.ChannelIDs) > 32 {
		return action, errors.New("at most 32 notification channels per rule")
	}
	if err := models.PrepareAIPanelAlert(ctx, db.Raw, task.UserID, task.ServerID, action.ToolName, &args); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return action, errors.New("alert rule or target server was not found in this account")
		case errors.Is(err, models.ErrAIPanelAlertRevision), errors.Is(err, models.ErrAIPanelAlertScope), errors.Is(err, models.ErrNotificationChannels):
			return action, err
		default:
			if ctx.Err() != nil {
				return action, ctx.Err()
			}
			return action, errors.New("could not read the current alert configuration; retry after checking panel availability")
		}
	}
	var normalized any = args
	if action.ToolName == "monitor_delete_alert" {
		normalized = map[string]any{"rule_id": args.RuleID, "expected_revision": args.ExpectedRevision, "channel_ids": args.ChannelIDs}
		action.Summary = "Delete Server Monitor alert " + args.RuleID.String()
		action.RequiresApproval = true
		action.RiskReason = "Deleting this rule removes monitoring coverage and cancels its queued notifications."
	} else {
		target := "all owned servers"
		if args.ServerID != nil {
			target = args.ServerID.String()
		}
		action.Summary = fmt.Sprintf("%s Server Monitor alert %q for %s: %s %s %g for %d seconds (enabled=%t)", map[string]string{"monitor_create_alert": "Create", "monitor_update_alert": "Update"}[action.ToolName], args.Name, target, args.Metric, args.Comparator, args.Threshold, args.Duration, args.Enabled)
		if action.ToolName == "monitor_update_alert" || !args.Enabled {
			action.RequiresApproval = true
			action.RiskReason = "Changing thresholds, targets, notification channels or enablement can reduce monitoring coverage or change when notifications are sent."
		}
	}
	var err error
	action.Arguments, err = json.Marshal(normalized)
	return action, err
}

func ExecuteAIPanelAlertAction(ctx context.Context, db *models.DB, task models.AITask, stored models.AIAction) (AIActionResult, error) {
	started := time.Now()
	result, err := models.ApplyAIPanelAlert(ctx, db.Raw, task.ID, task.UserID, stored.ID, task.WorkerID, stored.ToolName, stored.Arguments)
	out := AIActionResult{DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		switch {
		case errors.Is(err, models.ErrAIPanelAlertCommit):
			out.Unknown = true
			out.Error = "The panel update may have committed but its acknowledgment was lost. Inspect the current alert rules; do not automatically repeat the operation."
		case errors.Is(err, models.ErrAIPanelAlertRevision), errors.Is(err, models.ErrAIPanelAlertScope), errors.Is(err, models.ErrAIPanelAlertExists), errors.Is(err, models.ErrNotificationChannels):
			out.Error = err.Error()
		default:
			out.Error = "Panel alert operation was not completed; its task authority, ownership or configuration changed. Read the current rules before retrying."
		}
		return out, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return out, err
	}
	out.Output = string(raw)
	return out, nil
}
