package services

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/models"
)

// Leave room for the worker's tool envelope without producing truncated JSON.
const aiPanelJSONLimit = 22 << 10

type aiPanelPageArgs struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}
type aiPanelServerArgs struct {
	ServerID string `json:"server_id"`
}
type aiPanelHistoryArgs struct {
	ServerID string `json:"server_id"`
	Range    string `json:"range"`
	Limit    int    `json:"limit"`
}
type aiPanelAlertsArgs struct {
	Kind     string `json:"kind"`
	ServerID string `json:"server_id,omitempty"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func IsAIPanelReadTool(name string) bool {
	switch name {
	case "monitor_fleet", "monitor_server", "monitor_history", "monitor_alerts", "monitor_channels":
		return true
	}
	return false
}

func AIPanelReadTools() []AIToolDefinition {
	def := func(name, description string, props map[string]any, required ...string) AIToolDefinition {
		if required == nil {
			required = []string{}
		}
		return AIToolDefinition{Type: "function", Function: AIFunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}}
	}
	integer := func(maximum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": maximum}
	}
	server := map[string]any{"type": "string", "format": "uuid", "description": "Server ID returned by monitor_fleet, owned by this account"}
	offset := map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000, "description": "Use next_offset from the previous result; default 0"}
	return []AIToolDefinition{
		def("monitor_fleet", "Read Server Monitor's saved monitoring data for every server in the account. The summary, top 10 issues, and highest fresh reading per resource (top_resources) cover ALL servers regardless of page. items are paginated (default 20, max 50); use next_offset if needed. Values with stale samples are historical; null means unknown. Does not connect to servers. Do not infer uptime or packet loss from missing samples.", map[string]any{"offset": offset, "limit": integer(50)}),
		def("monitor_server", "Read one owned server's monitor snapshot, collection freshness, capacity, expiry and tags, without connecting to it. No credentials or connection addresses are returned.", map[string]any{"server_id": server}, "server_id"),
		def("monitor_history", "Read CPU, memory, disk and monitor latency history from stored metrics. range is 1h, 24h or 7d. Up to 120 observed aggregate buckets (default 60, reduced if needed for output size). Missing buckets are unknown, not zero usage or proven downtime. Samples are weighted; minute rollups are used beyond 24 hours.", map[string]any{"server_id": server, "range": map[string]any{"type": "string", "enum": []string{"1h", "24h", "7d"}}, "limit": integer(120)}, "server_id", "range"),
		def("monitor_alerts", "Read existing alert rules (kind=rules) or currently firing alert events (kind=active, default) for this account, paginated. Optional server_id filters events; rules include global rules applying to that server. Rule notification channels expose IDs/names/enabled only. Read existing rules before adding a new rule to avoid duplicates.", map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"rules", "active"}}, "server_id": server, "offset": offset, "limit": integer(50)}),
		def("monitor_channels", "Read configured notification channel IDs, names, types and enabled flags for selecting channels when adding an alert. No tokens, endpoints, recipients, or configuration are exposed. Paginated with default 20, max 50; only enabled channels can receive notifications.", map[string]any{"offset": offset, "limit": integer(50)}),
	}
}

func aiPanelNormalizeServerID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return "", errors.New("server_id must be a nonempty UUID returned by monitor_fleet")
	}
	return id.String(), nil
}
func aiPanelNormalizePage(offset int, limit *int) error {
	if *limit == 0 {
		*limit = 20
	}
	if offset < 0 || offset > 1000000 || *limit < 1 || *limit > 50 {
		return errors.New("offset must be between 0 and 1000000; limit must be between 1 and 50")
	}
	return nil
}

func PrepareAIPanelReadAction(call AIToolCall) (AIAction, error) {
	action := AIAction{ToolName: call.Function.Name}
	if call.Type != "" && call.Type != "function" {
		return action, errors.New("unsupported tool-call type")
	}
	if !IsAIPanelReadTool(action.ToolName) {
		return action, errors.New("unknown panel read tool")
	}
	raw := []byte(call.Function.Arguments)
	if err := validateAIJSON(raw); err != nil {
		return action, err
	}
	var normalized any
	switch action.ToolName {
	case "monitor_fleet", "monitor_channels":
		var args aiPanelPageArgs
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		if err := aiPanelNormalizePage(args.Offset, &args.Limit); err != nil {
			return action, err
		}
		normalized = args
	case "monitor_server":
		var args aiPanelServerArgs
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		var err error
		args.ServerID, err = aiPanelNormalizeServerID(args.ServerID)
		if err != nil {
			return action, err
		}
		normalized = args
	case "monitor_history":
		var args aiPanelHistoryArgs
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		var err error
		args.ServerID, err = aiPanelNormalizeServerID(args.ServerID)
		if err != nil {
			return action, err
		}
		if args.Range != "1h" && args.Range != "24h" && args.Range != "7d" {
			return action, errors.New("range must be 1h, 24h or 7d")
		}
		if args.Limit == 0 {
			args.Limit = 60
		}
		if args.Limit < 1 || args.Limit > 120 {
			return action, errors.New("limit must be between 1 and 120")
		}
		normalized = args
	case "monitor_alerts":
		var args aiPanelAlertsArgs
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		if args.Kind == "" {
			args.Kind = "active"
		}
		if args.Kind != "active" && args.Kind != "rules" {
			return action, errors.New("kind must be rules or active")
		}
		if args.ServerID != "" {
			var err error
			args.ServerID, err = aiPanelNormalizeServerID(args.ServerID)
			if err != nil {
				return action, err
			}
		}
		if err := aiPanelNormalizePage(args.Offset, &args.Limit); err != nil {
			return action, err
		}
		normalized = args
	}
	action.Summary = map[string]string{"monitor_fleet": "Read account-wide Server Monitor fleet health", "monitor_server": "Read saved monitoring snapshot", "monitor_history": "Read saved monitoring history", "monitor_alerts": "Read Server Monitor alert rules or active alerts", "monitor_channels": "List configured notification channel names"}[action.ToolName]
	var err error
	action.Arguments, err = json.Marshal(normalized)
	return action, err
}

// ExecuteAIPanelReadAction is database-only and always rebuilds the action from
// the persisted arguments. Ownership comes exclusively from the worker's task.
func ExecuteAIPanelReadAction(ctx context.Context, db *models.DB, userID uuid.UUID, action AIAction) (result AIActionResult, err error) {
	started := time.Now()
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = err.Error()
		}
	}()
	prepared, err := PrepareAIPanelReadAction(AIToolCall{Type: "function", Function: AIFunctionCall{Name: action.ToolName, Arguments: string(action.Arguments)}})
	if err != nil {
		return result, err
	}
	if !bytes.Equal(action.Arguments, prepared.Arguments) || action.Command != "" || action.Mutating || action.RequiresApproval || action.RiskReason != "" {
		return result, errors.New("panel action changed; prepare it again")
	}
	if _, err = aiActionApproval(action.ApprovalMode, prepared); err != nil {
		return result, err
	}
	if db == nil || db.Raw == nil || userID == uuid.Nil {
		return result, errors.New("panel account unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	now := time.Now().UTC()
	var encoded []byte
	switch action.ToolName {
	case "monitor_fleet":
		var args aiPanelPageArgs
		_ = json.Unmarshal(action.Arguments, &args)
		var data models.AIPanelFleet
		data, err = models.GetAIPanelFleet(ctx, db.Raw, userID, now, args.Offset, args.Limit)
		if err == nil {
			encoded, err = aiPanelMarshalPage(&data.AIPanelPage, func() ([]byte, error) { return json.Marshal(data) })
		}
	case "monitor_server":
		var args aiPanelServerArgs
		_ = json.Unmarshal(action.Arguments, &args)
		encoded, err = models.GetAIPanelServer(ctx, db.Raw, userID, uuid.MustParse(args.ServerID), now)
	case "monitor_history":
		var args aiPanelHistoryArgs
		_ = json.Unmarshal(action.Arguments, &args)
		// Reaggregate the entire range, rather than dropping later buckets when
		// the requested point count would exceed the model output budget.
		for {
			var data models.AIPanelHistory
			data, err = models.GetAIPanelHistory(ctx, db.Raw, userID, uuid.MustParse(args.ServerID), now, args.Range, args.Limit)
			if err != nil {
				break
			}
			encoded, err = json.Marshal(data)
			if err != nil || len(encoded) <= aiPanelJSONLimit || args.Limit == 1 {
				break
			}
			args.Limit = max(1, args.Limit*aiPanelJSONLimit/len(encoded)-1)
		}
	case "monitor_alerts", "monitor_channels":
		var data models.AIPanelPage
		if action.ToolName == "monitor_channels" {
			var args aiPanelPageArgs
			_ = json.Unmarshal(action.Arguments, &args)
			data, err = models.GetAIPanelChannels(ctx, db.Raw, userID, now, args.Offset, args.Limit)
		} else {
			var args aiPanelAlertsArgs
			_ = json.Unmarshal(action.Arguments, &args)
			var serverID *uuid.UUID
			if args.ServerID != "" {
				id := uuid.MustParse(args.ServerID)
				serverID = &id
			}
			data, err = models.GetAIPanelAlerts(ctx, db.Raw, userID, serverID, now, args.Kind, args.Offset, args.Limit)
		}
		if err == nil {
			encoded, err = aiPanelMarshalPage(&data, func() ([]byte, error) { return json.Marshal(data) })
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return result, errors.New("server not found in this account")
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("could not read Server Monitor data; retry after checking panel availability")
	}
	if len(encoded) > aiPanelJSONLimit {
		return result, errors.New("panel result exceeds the safe output size")
	}
	result.Output = string(encoded)
	code := 0
	result.ExitCode = &code
	return result, nil
}

func aiPanelMarshalPage(page *models.AIPanelPage, marshal func() ([]byte, error)) ([]byte, error) {
	for {
		next := page.Offset + len(page.Items)
		page.NextOffset = nil
		if next < page.Total {
			page.NextOffset = &next
		}
		encoded, err := marshal()
		if err != nil || len(encoded) <= aiPanelJSONLimit {
			return encoded, err
		}
		if len(page.Items) <= 1 {
			return nil, errors.New("a panel item exceeds the output limit")
		}
		page.Items = page.Items[:len(page.Items)-1]
	}
}
