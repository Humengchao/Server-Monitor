package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/crypto"
	"server-monitor/internal/testdb"
)

const backupSourceKey = "0123456789abcdef0123456789abcdef"
const backupTargetKey = "abcdef0123456789abcdef0123456789"

func backupFixture(t *testing.T) (*sql.DB, uuid.UUID, uuid.UUID) {
	t.Helper()
	db := testdb.Open(t)
	source, server, rule := testdb.Seed(t, db)
	destination := uuid.New()
	if _, err := db.Exec(`INSERT INTO users(id,username,password_hash) VALUES($1,$2,'test')`, destination, destination.String()); err != nil {
		t.Fatal(err)
	}
	password, err := crypto.Encrypt("ssh-password", backupSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := crypto.Encrypt("ssh-private-key", backupSourceKey)
	if err != nil {
		t.Fatal(err)
	}
	credential := uuid.New()
	if _, err := db.Exec(`INSERT INTO credentials(id,user_id,name,ssh_username,ssh_password,ssh_key) VALUES($1,$2,'shared','root',$3,$4)`, credential, source, password, privateKey); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE servers SET credential_id=$1,ssh_password=$2,ssh_key=$3 WHERE id=$4`, credential, password, privateKey, server); err != nil {
		t.Fatal(err)
	}
	tag := uuid.New()
	if _, err := db.Exec(`INSERT INTO tags(id,user_id,name,color) VALUES($1,$2,'prod','#123456')`, tag, source); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO server_tags(server_id,tag_id) VALUES($1,$2)`, server, tag); err != nil {
		t.Fatal(err)
	}
	channel := NotificationChannel{UserID: source, Name: "ops", Type: "telegram", Enabled: true, Config: NotificationConfig{BotToken: "bot-secret", ChatID: "123"}}
	if err := CreateNotificationChannel(context.Background(), &DB{Raw: db, EncryptionKey: backupSourceKey}, &channel); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2)`, rule, channel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO network_checks(id,user_id,name,target_server_id) VALUES($1,$2,'network',$3)`, uuid.New(), source, server); err != nil {
		t.Fatal(err)
	}
	return db, source, destination
}

func TestConfigurationBackupCrossKeyRestoreAndIsolation(t *testing.T) {
	db, source, destination := backupFixture(t)
	ctx := context.Background()
	b, err := ExportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupSourceKey}, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigurationBackup(b); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := crypto.SealBackup(plain, "long backup password")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := crypto.OpenBackup(sealed, "long backup password")
	if err != nil {
		t.Fatal(err)
	}
	var restored ConfigurationBackup
	if err = json.Unmarshal(opened, &restored); err != nil {
		t.Fatal(err)
	}
	// A forged source owner must never determine imported ownership.
	restored.Servers[0].UserID = source
	target := &DB{Raw: db, EncryptionKey: backupTargetKey}
	if _, err := ImportConfiguration(ctx, target, destination, &restored); err != nil {
		t.Fatal(err)
	}
	hosts, err := GetServersByUserID(db, destination)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts: %v %v", hosts, err)
	}
	host := hosts[0]
	if host.ID == b.Servers[0].ID || host.UserID != destination || host.IsPublic || host.MonitoringEnabled || host.AutoRenewEnabled {
		t.Fatalf("unsafe restore flags: %+v", host)
	}
	effective, err := GetServerByIDAndUser(target, host.ID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if effective.SSHPassword != "ssh-password" || effective.SSHKey != "ssh-private-key" {
		t.Fatal("cross-instance credentials failed")
	}
	if _, err := GetServerByIDAndUser(target, host.ID, source); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("source user can access destination")
	}
	tags, err := GetServerTags(db, host.ID)
	if err != nil || len(tags) != 1 || tags[0].UserID != destination {
		t.Fatalf("tag reference lost: %v %v", tags, err)
	}
	rules, err := GetAlertRulesByUserID(db, destination)
	if err != nil || len(rules) != 1 || rules[0].Enabled || len(rules[0].ChannelIDs) != 1 {
		t.Fatalf("rule restore: %v %v", rules, err)
	}
	channels, err := ListNotificationChannels(ctx, target, destination)
	if err != nil || len(channels) != 1 || channels[0].Enabled || channels[0].Config.BotToken != "bot-secret" {
		t.Fatalf("notification restore: %v %v", channels, err)
	}
	var deliveries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE user_id=$1`, destination).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatal("restore replayed notifications")
	}
	if _, err := ImportConfiguration(ctx, target, destination, &restored); !errors.Is(err, ErrBackupImported) {
		t.Fatalf("duplicate import: %v", err)
	}
	original, err := GetServersByUserID(db, source)
	if err != nil || len(original) != 1 || !original[0].MonitoringEnabled {
		t.Fatal("restore changed source")
	}
}

func TestConfigurationBackupWithoutSecretsAndInvalidReferences(t *testing.T) {
	db, source, destination := backupFixture(t)
	ctx := context.Background()
	b, err := ExportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupSourceKey}, source, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	for _, secret := range []string{"ssh-password", "ssh-private-key", "bot-secret", "https://example.invalid"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	foreign := uuid.New()
	b.Servers[0].CredentialID = &foreign
	if _, err := ImportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupTargetKey}, destination, b); err == nil {
		t.Fatal("dangling reference accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM servers WHERE user_id=$1`, destination).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid restore partially committed")
	}
}

func TestConfigurationRestoreRollsBackAndKeepsExistingTags(t *testing.T) {
	db, source, destination := backupFixture(t)
	ctx := context.Background()
	b, err := ExportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupSourceKey}, source, true)
	if err != nil {
		t.Fatal(err)
	}
	existing := uuid.New()
	if _, err := db.Exec(`INSERT INTO tags(id,user_id,name,color) VALUES($1,$2,'prod','#abcdef')`, existing, destination); err != nil {
		t.Fatal(err)
	}
	b.Servers[0].BillingCurrency = strings.Repeat("x", 100)
	if _, err := ImportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupTargetKey}, destination, b); err == nil {
		t.Fatal("invalid value should fail whole transaction")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE user_id=$1`, destination).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial credentials committed")
	}
	b.Servers[0].BillingCurrency = "CNY"
	if _, err := ImportConfiguration(ctx, &DB{Raw: db, EncryptionKey: backupTargetKey}, destination, b); err != nil {
		t.Fatal(err)
	}
	var color string
	if err := db.QueryRow(`SELECT color FROM tags WHERE id=$1`, existing).Scan(&color); err != nil || color != "#abcdef" {
		t.Fatal("existing tag overwritten")
	}
}
