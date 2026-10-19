package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func panelAlertArguments(server uuid.UUID) AIPanelAlertArguments {
	return AIPanelAlertArguments{Name: "AI CPU", Scope: "server", ServerID: &server, Metric: "cpu", Comparator: ">", Threshold: 90, Duration: 300, Enabled: true, ChannelIDs: []uuid.UUID{}}
}

func TestAIPanelAlertNormalizationAndBoundary(t *testing.T) {
	server := uuid.New()
	for _, metric := range []string{"cpu", "memory", "disk", "load1", "latency", "offline"} {
		a := panelAlertArguments(server)
		a.Metric, a.Duration, a.Comparator = metric, 0, ""
		if err := NormalizeAIPanelAlert(&a); err != nil || a.Duration != 300 || a.Comparator != ">" {
			t.Fatalf("%s: %+v %v", metric, a, err)
		}
		if metric == "offline" && a.Threshold != 0 {
			t.Fatal("offline threshold not normalized")
		}
	}
	for _, change := range []func(*AIPanelAlertArguments){
		func(a *AIPanelAlertArguments) { a.Name = " " },
		func(a *AIPanelAlertArguments) { a.Name = strings.Repeat("x", 129) },
		func(a *AIPanelAlertArguments) { a.Scope = "all_owned" },
		func(a *AIPanelAlertArguments) { a.ServerID = nil },
		func(a *AIPanelAlertArguments) { a.Scope = "foreign" },
		func(a *AIPanelAlertArguments) { a.Metric = "network" },
		func(a *AIPanelAlertArguments) { a.Comparator = ">=" },
		func(a *AIPanelAlertArguments) { a.Threshold = 101 },
		func(a *AIPanelAlertArguments) { a.Threshold = -1 },
		func(a *AIPanelAlertArguments) { a.Threshold = math.Inf(1) },
		func(a *AIPanelAlertArguments) { a.Threshold = math.NaN() },
		func(a *AIPanelAlertArguments) { a.Duration = 29 },
		func(a *AIPanelAlertArguments) { a.Duration = 86401 },
		func(a *AIPanelAlertArguments) { id := uuid.New(); a.ChannelIDs = []uuid.UUID{id, id} },
		func(a *AIPanelAlertArguments) { a.ChannelIDs = []uuid.UUID{uuid.Nil} },
	} {
		a := panelAlertArguments(server)
		change(&a)
		if err := NormalizeAIPanelAlert(&a); err == nil {
			t.Fatalf("invalid arguments accepted: %+v", a)
		}
	}
}

func panelAlertTask(t *testing.T, db *sql.DB, user, server uuid.UUID) AITask {
	t.Helper()
	seedAITask(t, db, user, server)
	task, err := ClaimAITask(context.Background(), db, "panel-worker")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func panelAlertAction(t *testing.T, db *sql.DB, task *AITask, operation string, args AIPanelAlertArguments, approve bool) AIAction {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	action := AIAction{CallID: uuid.NewString(), ToolName: operation, Arguments: raw, Summary: "change monitor rule", Mutating: true, RequiresApproval: approve}
	ctx := context.Background()
	if err := InsertAIAction(ctx, db, task.ID, task.WorkerID, &action); err != nil {
		t.Fatal(err)
	}
	if approve {
		if _, err := DecideAIAction(ctx, db, task.ID, action.ID, task.UserID, true); err != nil {
			t.Fatal(err)
		}
		*task, err = ClaimAITask(ctx, db, "panel-worker")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := StartAIAction(ctx, db, task.ID, action.ID, task.WorkerID); err != nil {
		t.Fatal(err)
	}
	return action
}

func panelAlertApply(ctx context.Context, db *sql.DB, task AITask, action AIAction) (AIPanelAlertResult, error) {
	return ApplyAIPanelAlert(ctx, db, task.ID, task.UserID, action.ID, task.WorkerID, action.ToolName, action.Arguments)
}

func panelAlertChannel(t *testing.T, db *sql.DB, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Exec(`INSERT INTO notification_channels(id,user_id,name,type,config_encrypted) VALUES($1,$2,'test channel','telegram','opaque')`, id, user); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAIPanelAlertCreateIdempotencyAndAuthority(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	foreign, foreignServer, foreignRule := testdb.Seed(t, db)
	task := panelAlertTask(t, db, user, server)
	ctx := context.Background()
	a := panelAlertArguments(server)
	a.ChannelIDs = []uuid.UUID{panelAlertChannel(t, db, user)}
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_create_alert", &a); err != nil {
		t.Fatal(err)
	}
	action := panelAlertAction(t, db, &task, "monitor_create_alert", a, false)
	first, err := panelAlertApply(ctx, db, task, action)
	if err != nil || first.RuleID == uuid.Nil || first.AlreadyExists {
		t.Fatal(first, err)
	}
	second, err := panelAlertApply(ctx, db, task, action)
	if err != nil || second != first {
		t.Fatal("replayed mutation changed result", second, err)
	}
	newAction := panelAlertAction(t, db, &task, "monitor_create_alert", a, false)
	duplicate, err := panelAlertApply(ctx, db, task, newAction)
	if err != nil || duplicate.RuleID != first.RuleID || !duplicate.AlreadyExists {
		t.Fatal("model retry created duplicate", duplicate, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alert_rules WHERE user_id=$1 AND name='AI CPU'`, user).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	a.Threshold = 70
	conflict := panelAlertAction(t, db, &task, "monitor_create_alert", a, false)
	if _, err = panelAlertApply(ctx, db, task, conflict); !errors.Is(err, ErrAIPanelAlertExists) {
		t.Fatal("same name silently overwritten", err)
	}
	bad := panelAlertArguments(foreignServer)
	if err := PrepareAIPanelAlert(ctx, db, user, uuid.Nil, "monitor_create_alert", &bad); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign server accepted", err)
	}
	bad = panelAlertArguments(server)
	bad.ChannelIDs = []uuid.UUID{panelAlertChannel(t, db, foreign)}
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_create_alert", &bad); !errors.Is(err, ErrNotificationChannels) {
		t.Fatal("foreign channel accepted", err)
	}
	bad = panelAlertArguments(server)
	bad.RuleID = foreignRule
	if err := PrepareAIPanelAlert(ctx, db, user, uuid.Nil, "monitor_update_alert", &bad); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign rule accepted", err)
	}
	bad = panelAlertArguments(server)
	bad.Scope, bad.ServerID = "all_owned", nil
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_create_alert", &bad); !errors.Is(err, ErrAIPanelAlertScope) {
		t.Fatal("single host escaped to fleet", err)
	}
	changed := action
	changed.Arguments = json.RawMessage(strings.Replace(string(changed.Arguments), `"threshold":90`, `"threshold":1`, 1))
	if _, err := panelAlertApply(ctx, db, task, changed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("altered saved arguments accepted", err)
	}
	foreignTask := task
	foreignTask.UserID = foreign
	if _, err := panelAlertApply(ctx, db, foreignTask, action); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign account reused action", err)
	}
	if _, err := db.Exec(`UPDATE ai_tasks SET status='cancel_requested' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := panelAlertApply(ctx, db, task, action); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("canceled action replay accepted", err)
	}
}

func TestAIPanelAlertUpdatePreservesWebhookAndCancelsRemovedChannel(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	ctx := context.Background()
	channel := panelAlertChannel(t, db, user)
	if _, err := db.Exec(`INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2)`, rule, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notification_deliveries(user_id,channel_id,channel_name,channel_type,rule_id,server_id,transition,payload) VALUES($1,$2,'test','telegram',$3,$4,'firing','{}')`, user, channel, rule, server); err != nil {
		t.Fatal(err)
	}
	task := panelAlertTask(t, db, user, server)
	a := panelAlertArguments(server)
	a.RuleID, a.Name, a.Threshold = rule, "Updated CPU", 75
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_update_alert", &a); err != nil {
		t.Fatal(err)
	}
	action := panelAlertAction(t, db, &task, "monitor_update_alert", a, true)
	result, err := panelAlertApply(ctx, db, task, action)
	if err != nil || result.RuleID != rule {
		t.Fatal(result, err)
	}
	r, err := aiPanelAlertRule(ctx, db, user, rule, false)
	if err != nil || r.WebhookURL != "https://example.invalid" || r.Threshold != 75 || len(r.ChannelIDs) != 0 {
		t.Fatal(r, err)
	}
	var status string
	var version int
	if err := db.QueryRow(`SELECT status,lease_version FROM notification_deliveries WHERE rule_id=$1`, rule).Scan(&status, &version); err != nil || status != "canceled" || version != 1 {
		t.Fatal(status, version, err)
	}
	if replay, err := panelAlertApply(ctx, db, task, action); err != nil || replay != result {
		t.Fatal("update replay after changed revision", replay, err)
	}
}

func TestAIPanelAlertRefusesStaleRuleAndChannelApproval(t *testing.T) {
	for _, change := range []string{"rule", "channel", "webhook"} {
		t.Run(change, func(t *testing.T) {
			db := testdb.Open(t)
			user, server, rule := testdb.Seed(t, db)
			ctx := context.Background()
			task := panelAlertTask(t, db, user, server)
			channel := panelAlertChannel(t, db, user)
			a := panelAlertArguments(server)
			a.RuleID, a.ChannelIDs = rule, []uuid.UUID{channel}
			if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_update_alert", &a); err != nil {
				t.Fatal(err)
			}
			action := panelAlertAction(t, db, &task, "monitor_update_alert", a, true)
			var err error
			switch change {
			case "rule":
				_, err = db.Exec(`UPDATE alert_rules SET threshold=42 WHERE id=$1`, rule)
			case "webhook":
				_, err = db.Exec(`UPDATE alert_rules SET webhook_url='https://changed.invalid/secret' WHERE id=$1`, rule)
			case "channel":
				_, err = db.Exec(`UPDATE notification_channels SET revision=revision+1 WHERE id=$1`, channel)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_update_alert", &a); !errors.Is(err, ErrAIPanelAlertRevision) {
				t.Fatal("saved approval refreshed silently", err)
			}
			if _, err := panelAlertApply(ctx, db, task, action); !errors.Is(err, ErrAIPanelAlertRevision) {
				t.Fatal("stale approval applied", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ai_panel_alert_mutations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed change has a success receipt", count, err)
			}
		})
	}
}

func TestAIPanelAlertEnforcesApprovalModesAndLiveLease(t *testing.T) {
	for _, mode := range []string{"readonly", "standard-unapproved", "expired", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			db := testdb.Open(t)
			user, server, rule := testdb.Seed(t, db)
			task := panelAlertTask(t, db, user, server)
			ctx := context.Background()
			a := panelAlertArguments(server)
			a.RuleID, a.Enabled = rule, false
			if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_update_alert", &a); err != nil {
				t.Fatal(err)
			}
			action := panelAlertAction(t, db, &task, "monitor_update_alert", a, false)
			if mode == "expired" {
				if _, err := db.Exec(`UPDATE ai_tasks SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, task.ID); err != nil {
					t.Fatal(err)
				}
			} else if mode == "readonly" || mode == "automatic" {
				if _, err := db.Exec(`UPDATE ai_tasks SET approval_mode=$1 WHERE id=$2`, mode, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := panelAlertApply(ctx, db, task, action)
			if mode == "automatic" && err != nil || mode != "automatic" && err == nil {
				t.Fatal(mode, err)
			}
			var enabled bool
			if err := db.QueryRow(`SELECT enabled FROM alert_rules WHERE id=$1`, rule).Scan(&enabled); err != nil {
				t.Fatal(err)
			}
			if enabled == (mode == "automatic") {
				t.Fatal("wrong mode changed alert", mode, enabled)
			}
		})
	}
}

func TestAIPanelAlertConcurrentReceiptAndDeleteCancelsPendingNotifications(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	ctx := context.Background()
	task := panelAlertTask(t, db, user, server)
	a := panelAlertArguments(server)
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_create_alert", &a); err != nil {
		t.Fatal(err)
	}
	action := panelAlertAction(t, db, &task, "monitor_create_alert", a, false)
	results := make(chan AIPanelAlertResult, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := panelAlertApply(ctx, db, task, action)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	var created uuid.UUID
	for result := range results {
		if created == uuid.Nil {
			created = result.RuleID
		}
		if result.RuleID != created {
			t.Fatal("concurrent replay wrote two rules")
		}
	}
	var receipts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_panel_alert_mutations WHERE action_id=$1`, action.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal(receipts, err)
	}
	channel := panelAlertChannel(t, db, user)
	var delivery int64
	if err := db.QueryRow(`INSERT INTO notification_deliveries(user_id,channel_id,channel_name,channel_type,rule_id,server_id,transition,payload) VALUES($1,$2,'test','telegram',$3,$4,'firing','{}') RETURNING id`, user, channel, rule, server).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	del := AIPanelAlertArguments{RuleID: rule}
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_delete_alert", &del); err != nil {
		t.Fatal(err)
	}
	deletion := panelAlertAction(t, db, &task, "monitor_delete_alert", del, true)
	deleted, err := panelAlertApply(ctx, db, task, deletion)
	if err != nil || deleted.RuleID != rule {
		t.Fatal(deleted, err)
	}
	var status string
	var currentRule uuid.NullUUID
	if err := db.QueryRow(`SELECT status,rule_id FROM notification_deliveries WHERE id=$1`, delivery).Scan(&status, &currentRule); err != nil || status != "canceled" || currentRule.Valid {
		t.Fatal(status, currentRule, err)
	}
	if _, err := aiPanelAlertRule(ctx, db, user, rule, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rule not deleted", err)
	}
	if replay, err := panelAlertApply(ctx, db, task, deletion); err != nil || replay != deleted {
		t.Fatal("delete replay failed", replay, err)
	}
}

func TestAIPanelAlertLeaseExpiryWhileWaitingRollsBackTheWrite(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task := panelAlertTask(t, db, user, server)
	a := panelAlertArguments(server)
	a.RuleID, a.Threshold = rule, 95
	if err := PrepareAIPanelAlert(ctx, db, user, server, "monitor_update_alert", &a); err != nil {
		t.Fatal(err)
	}
	action := panelAlertAction(t, db, &task, "monitor_update_alert", a, true)
	if _, err := db.Exec(`UPDATE ai_tasks SET lease_until=clock_timestamp()+INTERVAL '1 second' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	blocking, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocking.Rollback()
	if _, err := blocking.ExecContext(ctx, `SELECT id FROM alert_rules WHERE id=$1 FOR UPDATE`, rule); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := panelAlertApply(ctx, db, task, action); done <- err }()
	time.Sleep(1100 * time.Millisecond)
	if err := blocking.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("operation committed after lease expired while waiting for the rule lock")
	}
	var threshold float64
	if err := db.QueryRowContext(ctx, `SELECT threshold FROM alert_rules WHERE id=$1`, rule).Scan(&threshold); err != nil || threshold != 0 {
		t.Fatal("expired lease left a mutation behind", threshold, err)
	}
}
