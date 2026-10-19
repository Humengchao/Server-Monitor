package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func panelReadAction(t *testing.T, name, arguments string) AIAction {
	t.Helper()
	action, err := PrepareAIPanelReadAction(AIToolCall{Type: "function", Function: AIFunctionCall{Name: name, Arguments: arguments}})
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func TestAIPanelReadStrictArgumentsAndReadOnlyPolicy(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct{ name, args string }{
		{"monitor_fleet", `{"user_id":"` + id + `"}`},
		{"monitor_fleet", `{"limit":51}`},
		{"monitor_fleet", `{"offset":-1}`},
		{"monitor_fleet", `{"offset":1000001}`},
		{"monitor_fleet", `{"limit":1,"limit":2}`},
		{"monitor_fleet", `null`},
		{"monitor_server", `{}`},
		{"monitor_server", `{"server_id":"00000000-0000-0000-0000-000000000000"}`},
		{"monitor_server", `{"server_id":"` + id + `","host":"192.0.2.1"}`},
		{"monitor_history", `{"server_id":"` + id + `","range":"30d"}`},
		{"monitor_history", `{"server_id":"` + id + `","range":"1h","limit":121}`},
		{"monitor_alerts", `{"kind":"delete"}`},
		{"monitor_alerts", `{"kind":"rules","server_id":"bad"}`},
		{"monitor_channels", `{"config":true}`},
		{"run_command", `{"command":"hostname"}`},
	} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			if _, err := PrepareAIPanelReadAction(AIToolCall{Function: AIFunctionCall{Name: tc.name, Arguments: tc.args}}); err == nil {
				t.Fatal("accepted invalid panel arguments")
			}
		})
	}
	for _, definition := range AIPanelReadTools() {
		args := `{}`
		if definition.Function.Name == "monitor_server" {
			args = `{"server_id":"` + id + `"}`
		}
		if definition.Function.Name == "monitor_history" {
			args = `{"server_id":"` + id + `","range":"24h"}`
		}
		action := panelReadAction(t, definition.Function.Name, args)
		if action.Mutating || action.RequiresApproval || action.Command != "" {
			t.Fatal("database reader became a remote/mutating action", action)
		}
		if approval, err := aiActionApproval("readonly", action); err != nil || approval {
			t.Fatal("read-only mode blocked panel reader", approval, err)
		}
	}
}

func TestAIPanelReadRejectsAlteredPersistedActions(t *testing.T) {
	for _, change := range []func(*AIAction){
		func(a *AIAction) { a.Command = "hostname" }, func(a *AIAction) { a.Mutating = true }, func(a *AIAction) { a.RequiresApproval = true },
		func(a *AIAction) { a.RiskReason = "skip policy" }, func(a *AIAction) { a.Arguments = json.RawMessage(`{"limit":20}`) }, func(a *AIAction) { a.ApprovalMode = "invented" },
	} {
		action := panelReadAction(t, "monitor_fleet", `{}`)
		change(&action)
		if _, err := ExecuteAIPanelReadAction(context.Background(), nil, uuid.New(), action); err == nil {
			t.Fatal("accepted altered action")
		}
	}
}

func TestAIPanelReadFleetOutputHasValidJSONAndTruePagination(t *testing.T) {
	raw := testdb.Open(t)
	user, first, _ := testdb.Seed(t, raw)
	_, foreign, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "intentionally-not-an-encryption-key"}
	if _, err := raw.Exec(`UPDATE servers SET ssh_password='not decryptable',ssh_key='not decryptable' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 54; i++ {
		if _, err := raw.Exec(`INSERT INTO servers(id,user_id,name,host) VALUES($1,$2,$3,'192.0.2.1')`, uuid.New(), user, strings.Repeat("服", 120)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	offset := 0
	pageCount := 0
	for {
		args, _ := json.Marshal(map[string]any{"offset": offset, "limit": 50})
		action := panelReadAction(t, "monitor_fleet", string(args))
		action.ApprovalMode = "readonly"
		result, err := ExecuteAIPanelReadAction(context.Background(), db, user, action)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Output) > aiPanelJSONLimit || !json.Valid([]byte(result.Output)) || result.Truncated {
			t.Fatal("invalid bounded tool output", len(result.Output), result)
		}
		var page struct {
			Total      int  `json:"total"`
			NextOffset *int `json:"next_offset"`
			Items      []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(result.Output), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != 55 {
			t.Fatal("foreign account counted", page.Total)
		}
		for _, item := range page.Items {
			if item.ID == foreign.String() || seen[item.ID] {
				t.Fatal("cross-account or repeated server", item.ID)
			}
			seen[item.ID] = true
		}
		pageCount++
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset != offset+len(page.Items) || *page.NextOffset <= offset {
			t.Fatal("skipped/looping cursor", page)
		}
		offset = *page.NextOffset
	}
	if len(seen) != 55 || pageCount < 3 {
		t.Fatal("oversize page not reduced with resumable cursor", len(seen), pageCount)
	}
	for _, name := range []string{"monitor_server", "monitor_history", "monitor_alerts"} {
		args := map[string]any{"server_id": foreign}
		if name == "monitor_history" {
			args["range"] = "1h"
		}
		encoded, _ := json.Marshal(args)
		action := panelReadAction(t, name, string(encoded))
		result, err := ExecuteAIPanelReadAction(context.Background(), db, user, action)
		if err == nil || result.Output != "" || !strings.Contains(err.Error(), "not found in this account") {
			t.Fatal("foreign panel read succeeded", name, result, err)
		}
	}
}

func TestAIPanelHistoryReaggregatesWholeRangeToFitOutput(t *testing.T) {
	raw := testdb.Open(t)
	user, server, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw}
	now := time.Now().UTC()
	if _, err := raw.Exec(`INSERT INTO server_metrics(server_id,cpu_percent,memory_used,memory_total,disk_used_bytes,latency_ms,recorded_at)
 SELECT $1,99,100,1000,20,5,$2::timestamptz-i*INTERVAL '29 seconds' FROM generate_series(1,122) i`, server, now); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"server_id": server, "range": "1h", "limit": 120})
	action := panelReadAction(t, "monitor_history", string(encoded))
	result, err := ExecuteAIPanelReadAction(context.Background(), db, user, action)
	if err != nil {
		t.Fatal(err)
	}
	var history models.AIPanelHistory
	if err := json.Unmarshal([]byte(result.Output), &history); err != nil {
		t.Fatal(err)
	}
	if len(result.Output) > aiPanelJSONLimit || result.Truncated || history.ObservedBuckets >= 120 || history.ObservedBuckets < 2 {
		t.Fatal("history not safely reaggregated", history.ObservedBuckets, len(result.Output))
	}
	var first, last struct {
		First time.Time `json:"first_sample_at"`
		Last  time.Time `json:"last_sample_at"`
	}
	if err := json.Unmarshal(history.Points[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(history.Points[len(history.Points)-1], &last); err != nil {
		t.Fatal(err)
	}
	if first.First.After(now.Add(-58*time.Minute)) || last.Last.Before(now.Add(-time.Minute)) {
		t.Fatal("output limit discarded range endpoints", first, last)
	}
}
