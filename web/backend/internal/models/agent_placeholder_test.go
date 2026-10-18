package models

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/testdb"
)

func TestCreateAgentPersistsAutomaticNameWithoutAddingAnAddress(t *testing.T) {
	fixture := &collectorConnector{columns: []string{"created_at"}, rows: [][]driver.Value{{time.Now()}}}
	db := sql.OpenDB(fixture)
	defer db.Close()
	host := &Server{ConnectionMode: "agent", AgentAutoName: true, Name: "Agent", Host: "agent", SSHUsername: "agent", Port: 22}
	if err := CreateServer(&DB{Raw: db}, host); err != nil {
		t.Fatal(err)
	}
	if host.Name != "Agent "+host.ID.String()[:8] || host.Host != "agent" {
		t.Fatal("placeholder changed connection data")
	}
	if len(fixture.args) != 1 || len(fixture.args[0]) != 23 || fixture.args[0][22].Value != true {
		t.Fatal("automatic-name flag not persisted")
	}
}

func TestAgentHostnameDisplayNameFitsDatabaseAndRejectsEmptyNames(t *testing.T) {
	for _, name := range []string{"", " \t\n", "bad\x00name", "bad\u200bname"} {
		if _, err := agentHostnameDisplayName(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	got, err := agentHostnameDisplayName(strings.Repeat("机", 140))
	if err != nil || len([]rune(got)) != 128 {
		t.Fatal("display hostname not clamped by characters", err)
	}
}

func TestBackupAgentNamingFieldIsOptionalAndCannotRenameSSHHosts(t *testing.T) {
	backup := ConfigurationBackup{Version: 1, ID: uuid.New(), CreatedAt: time.Now(), Servers: []BackupServer{{
		Server:        Server{ID: uuid.New(), Name: "Agent 12345678", Host: "agent", ConnectionMode: "agent", ServerType: "linux", Port: 22},
		AgentAutoName: true,
	}}}
	encoded, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ConfigurationBackup
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Servers[0].AgentAutoName {
		t.Fatal("backup JSON lost pending naming state")
	}
	if err := ValidateConfigurationBackup(&decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Servers[0].ConnectionMode = "ssh"
	if err := ValidateConfigurationBackup(&decoded); err == nil {
		t.Fatal("SSH record accepted automatic Agent naming")
	}
	var legacy BackupServer
	if err := json.Unmarshal([]byte(`{"name":"Agent deadbeef","host":"agent","connection_mode":"agent","server_type":"linux","port":22}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.AgentAutoName {
		t.Fatal("legacy custom name inferred from its spelling")
	}
}

func TestBackupPreservesPendingAgentNamingAndLegacyCustomNames(t *testing.T) {
	db := testdb.Open(t)
	source, _, _ := testdb.Seed(t, db)
	destination, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	wrapper := &DB{Raw: db, EncryptionKey: backupSourceKey}
	pending := &Server{ConnectionMode: "agent", AgentAutoName: true, Name: "Agent", Host: "agent", SSHUsername: "agent", Port: 22, UserID: source, ServerType: "linux", BillingCurrency: "CNY", BillingCycle: "year"}
	if err := CreateServer(wrapper, pending); err != nil {
		t.Fatal(err)
	}
	backup, err := ExportConfiguration(ctx, wrapper, source, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, host := range backup.Servers {
		if host.ID == pending.ID {
			found = host.AgentAutoName
		}
	}
	if !found {
		t.Fatal("export lost pending automatic name")
	}
	encoded, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ConfigurationBackup
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportConfiguration(ctx, wrapper, destination, &decoded); err != nil {
		t.Fatal(err)
	}
	var restoredID uuid.UUID
	var auto, published, monitoring, renewal bool
	if err := db.QueryRow(`SELECT id,agent_auto_name,is_public,monitoring_enabled,auto_renew_enabled FROM servers WHERE user_id=$1 AND name=$2`, destination, pending.Name).Scan(&restoredID, &auto, &published, &monitoring, &renewal); err != nil {
		t.Fatal(err)
	}
	if restoredID == pending.ID || !auto || published || monitoring || renewal {
		t.Fatal("restore lost identity mapping, pending naming, or paused defaults")
	}
	ticket, _, err := CreateAgentTicket(ctx, db, restoredID, destination)
	if err != nil {
		t.Fatal(err)
	}
	hello, _, err := agenttransport.NewIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	hello.Hostname = "restored-machine"
	if _, _, err := RegisterAgent(ctx, db, ticket, hello); err != nil {
		t.Fatal(err)
	}
	var name, address string
	if err := db.QueryRow(`SELECT name,host,agent_auto_name,monitoring_enabled,auto_renew_enabled FROM servers WHERE id=$1`, restoredID).Scan(&name, &address, &auto, &monitoring, &renewal); err != nil {
		t.Fatal(err)
	}
	if name != hello.Hostname || address != "agent" || auto || monitoring || renewal {
		t.Fatal("registration incorrectly resumed restored automation")
	}
}
