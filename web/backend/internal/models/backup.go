package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/crypto"
)

type BackupServer struct {
	Server
	AgentAutoName bool        `json:"agent_auto_name,omitempty"`
	Password      string      `json:"ssh_password,omitempty"`
	PrivateKey    string      `json:"ssh_key,omitempty"`
	TagIDs        []uuid.UUID `json:"tag_ids"`
}
type BackupCredential struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	Username   string    `json:"ssh_username"`
	Password   string    `json:"ssh_password,omitempty"`
	PrivateKey string    `json:"ssh_key,omitempty"`
	Type       string    `json:"credential_type"`
}
type BackupChannel struct {
	ID     uuid.UUID          `json:"id"`
	Name   string             `json:"name"`
	Type   string             `json:"type"`
	Config NotificationConfig `json:"config"`
}
type ConfigurationBackup struct {
	Version             int                `json:"version"`
	ID                  uuid.UUID          `json:"backup_id"`
	CreatedAt           time.Time          `json:"created_at"`
	IncludesCredentials bool               `json:"includes_credentials"`
	Servers             []BackupServer     `json:"servers"`
	Credentials         []BackupCredential `json:"credentials"`
	Tags                []Tag              `json:"tags"`
	Rules               []AlertRule        `json:"rules"`
	Channels            []BackupChannel    `json:"notification_channels"`
	NetworkChecks       []NetworkCheck     `json:"network_checks"`
}
type BackupPreview struct {
	BackupID            uuid.UUID `json:"backup_id"`
	CreatedAt           time.Time `json:"created_at"`
	Servers             int       `json:"servers"`
	Credentials         int       `json:"credentials"`
	Tags                int       `json:"tags"`
	Rules               int       `json:"rules"`
	Channels            int       `json:"notification_channels"`
	NetworkChecks       int       `json:"network_checks"`
	IncludesCredentials bool      `json:"includes_credentials"`
	AgentServers        int       `json:"agent_servers"`
	ServerNames         []string  `json:"server_names"`
	AlreadyImported     bool      `json:"already_imported"`
}

func (b *ConfigurationBackup) Preview() BackupPreview {
	p := BackupPreview{BackupID: b.ID, CreatedAt: b.CreatedAt, Servers: len(b.Servers), Credentials: len(b.Credentials), Tags: len(b.Tags), Rules: len(b.Rules), Channels: len(b.Channels), NetworkChecks: len(b.NetworkChecks), IncludesCredentials: b.IncludesCredentials, ServerNames: []string{}}
	for _, s := range b.Servers {
		p.ServerNames = append(p.ServerNames, s.Name)
		if s.ConnectionMode == "agent" {
			p.AgentServers++
		}
	}
	return p
}

func ExportConfiguration(ctx context.Context, db *DB, user uuid.UUID, secrets bool) (*ConfigurationBackup, error) {
	tx, err := db.Raw.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	b := &ConfigurationBackup{Version: 1, ID: uuid.New(), CreatedAt: time.Now().UTC(), IncludesCredentials: secrets, Servers: []BackupServer{}, Credentials: []BackupCredential{}, Tags: []Tag{}, Rules: []AlertRule{}, Channels: []BackupChannel{}, NetworkChecks: []NetworkCheck{}}
	rows, err := tx.QueryContext(ctx, `SELECT to_jsonb(s)-ARRAY['user_id','ssh_password','ssh_key'],COALESCE(ssh_password,''),COALESCE(ssh_key,'') FROM servers s WHERE user_id=$1 ORDER BY created_at,id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var password, key string
		var s BackupServer
		if err = rows.Scan(&raw, &password, &key); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &s); err != nil {
			break
		}
		s.UserID = uuid.Nil
		s.LatestMetrics = nil
		s.LastError = ""
		s.LastErrorKind = ""
		s.LastErrorAt = nil
		if secrets {
			if s.Password, err = crypto.Decrypt(password, db.EncryptionKey); err != nil {
				break
			}
			if s.PrivateKey, err = crypto.Decrypt(key, db.EncryptionKey); err != nil {
				break
			}
		}
		b.Servers = append(b.Servers, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,name,ssh_username,COALESCE(ssh_password,''),COALESCE(ssh_key,''),COALESCE(credential_type,'linux') FROM credentials WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c BackupCredential
		var password, key string
		if err = rows.Scan(&c.ID, &c.Name, &c.Username, &password, &key, &c.Type); err != nil {
			break
		}
		if secrets {
			if c.Password, err = crypto.Decrypt(password, db.EncryptionKey); err != nil {
				break
			}
			if c.PrivateKey, err = crypto.Decrypt(key, db.EncryptionKey); err != nil {
				break
			}
		}
		b.Credentials = append(b.Credentials, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,name,color FROM tags WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t Tag
		if err = rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			break
		}
		b.Tags = append(b.Tags, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT st.server_id,st.tag_id FROM server_tags st JOIN servers s ON s.id=st.server_id JOIN tags t ON t.id=st.tag_id WHERE s.user_id=$1 AND t.user_id=$1`, user)
	if err != nil {
		return nil, err
	}
	tags := map[uuid.UUID][]uuid.UUID{}
	for rows.Next() {
		var server, tag uuid.UUID
		if err = rows.Scan(&server, &tag); err != nil {
			break
		}
		tags[server] = append(tags[server], tag)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range b.Servers {
		b.Servers[i].TagIDs = tags[b.Servers[i].ID]
	}
	rows, err = tx.QueryContext(ctx, `SELECT to_jsonb(r)-'user_id',COALESCE((SELECT jsonb_agg(rc.channel_id) FROM alert_rule_channels rc JOIN notification_channels c ON c.id=rc.channel_id WHERE rc.rule_id=r.id AND c.user_id=r.user_id),'[]') FROM alert_rules r WHERE r.user_id=$1 ORDER BY r.id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw, ids []byte
		var rule AlertRule
		if err = rows.Scan(&raw, &ids); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &rule); err != nil {
			break
		}
		if err = json.Unmarshal(ids, &rule.ChannelIDs); err != nil {
			break
		}
		if !secrets {
			rule.WebhookURL = ""
		}
		b.Rules = append(b.Rules, rule)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,name,type,config_encrypted FROM notification_channels WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var channel BackupChannel
		var encrypted string
		if err = rows.Scan(&channel.ID, &channel.Name, &channel.Type, &encrypted); err != nil {
			break
		}
		channel.Config, err = DecryptNotificationConfig(encrypted, db.EncryptionKey)
		if err != nil {
			break
		}
		if !secrets {
			channel.Config = (NotificationChannel{Config: channel.Config}).Public().Config
		}
		b.Channels = append(b.Channels, channel)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT to_jsonb(c)-'user_id' FROM network_checks c WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		var check NetworkCheck
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &check); err != nil {
			break
		}
		check.Latest = nil
		b.NetworkChecks = append(b.NetworkChecks, check)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return b, tx.Commit()
}

func ValidateConfigurationBackup(b *ConfigurationBackup) error {
	if b.Version != 1 || b.ID == uuid.Nil || b.CreatedAt.IsZero() {
		return errors.New("unsupported or invalid backup format")
	}
	if len(b.Servers) > 10000 || len(b.Credentials) > 10000 || len(b.Tags) > 10000 || len(b.Rules) > 10000 || len(b.Channels) > 100 || len(b.NetworkChecks) > 100 {
		return errors.New("backup exceeds configuration limits")
	}
	ids := func(values []uuid.UUID) (map[uuid.UUID]bool, error) {
		seen := map[uuid.UUID]bool{}
		for _, id := range values {
			if id == uuid.Nil || seen[id] {
				return nil, errors.New("duplicate or invalid source ID")
			}
			seen[id] = true
		}
		return seen, nil
	}
	getIDs := func(kind string) []uuid.UUID {
		var result []uuid.UUID
		switch kind {
		case "servers":
			for _, s := range b.Servers {
				result = append(result, s.ID)
			}
		case "credentials":
			for _, c := range b.Credentials {
				result = append(result, c.ID)
			}
		case "tags":
			for _, t := range b.Tags {
				result = append(result, t.ID)
			}
		case "rules":
			for _, r := range b.Rules {
				result = append(result, r.ID)
			}
		case "channels":
			for _, c := range b.Channels {
				result = append(result, c.ID)
			}
		case "network":
			for _, c := range b.NetworkChecks {
				result = append(result, c.ID)
			}
		}
		return result
	}
	maps := map[string]map[uuid.UUID]bool{}
	allIDs := map[uuid.UUID]bool{}
	for _, kind := range []string{"servers", "credentials", "tags", "rules", "channels", "network"} {
		set, err := ids(getIDs(kind))
		if err != nil {
			return err
		}
		maps[kind] = set
		for id := range set {
			if allIDs[id] {
				return errors.New("source IDs must be unique across configuration types")
			}
			allIDs[id] = true
		}
	}
	validName := func(name string) bool { return strings.TrimSpace(name) != "" && len([]rune(name)) <= 128 }
	for _, s := range b.Servers {
		if !validName(s.Name) || len(s.Host) > 256 || s.Host == "" || (s.ServerType != "linux" && s.ServerType != "windows") || (s.ConnectionMode != "ssh" && s.ConnectionMode != "agent") || s.Port < 1 || s.Port > 65535 || (s.AgentAutoName && s.ConnectionMode != "agent") {
			return errors.New("invalid server configuration")
		}
		if s.CredentialID != nil && !maps["credentials"][*s.CredentialID] {
			return errors.New("server references a missing credential")
		}
		for _, tag := range s.TagIDs {
			if !maps["tags"][tag] {
				return errors.New("server references a missing tag")
			}
		}
	}
	for _, c := range b.Credentials {
		if !validName(c.Name) || (c.Type != "linux" && c.Type != "windows") {
			return errors.New("invalid credential configuration")
		}
	}
	names := map[string]bool{}
	for _, tag := range b.Tags {
		if tag.Name == "" || len([]rune(tag.Name)) > 64 || names[tag.Name] {
			return errors.New("invalid or duplicate tag name")
		}
		names[tag.Name] = true
	}
	for _, r := range b.Rules {
		if !validName(r.Name) || !ValidAlertMetric(r.Metric) || (r.Comparator != "<" && r.Comparator != ">") || r.Duration < 30 || r.Duration > 86400 {
			return errors.New("invalid alert rule")
		}
		if r.ServerID != nil && !maps["servers"][*r.ServerID] {
			return errors.New("alert references a missing server")
		}
		for _, id := range r.ChannelIDs {
			if !maps["channels"][id] {
				return errors.New("alert references a missing channel")
			}
		}
	}
	for _, c := range b.Channels {
		if !validName(c.Name) {
			return errors.New("invalid notification channel")
		}
		switch c.Type {
		case "telegram", "wecom", "dingtalk", "email", "webhook":
		default:
			return errors.New("unsupported notification channel")
		}
	}
	for _, c := range b.NetworkChecks {
		if !maps["servers"][c.TargetServerID] || (c.SourceServerID != nil && !maps["servers"][*c.SourceServerID]) || c.Packets < 1 || c.Packets > 10 || c.LossThreshold < 0 || c.LossThreshold > 100 {
			return errors.New("invalid network check or missing server")
		}
	}
	return nil
}

var ErrBackupImported = errors.New("this backup was already restored into this account")

func ImportConfiguration(ctx context.Context, db *DB, user uuid.UUID, b *ConfigurationBackup) (BackupPreview, error) {
	p := b.Preview()
	if err := ValidateConfigurationBackup(b); err != nil {
		return p, err
	}
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var locked uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&locked); err != nil {
		return p, err
	}
	var already bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_imports WHERE user_id=$1 AND backup_id=$2)`, user, b.ID).Scan(&already); err != nil {
		return p, err
	}
	if already {
		return p, ErrBackupImported
	}
	for _, limit := range []struct {
		table      string
		added, max int
	}{{"network_checks", len(b.NetworkChecks), 100}, {"notification_channels", len(b.Channels), 100}} {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+limit.table+" WHERE user_id=$1", user).Scan(&count); err != nil {
			return p, err
		}
		if count+limit.added > limit.max {
			return p, errors.New("restore would exceed account configuration limits")
		}
	}
	// Every object gets a fresh ID, and ownership comes only from authentication.
	remap := map[uuid.UUID]uuid.UUID{}
	for _, s := range b.Servers {
		remap[s.ID] = uuid.New()
	}
	for _, c := range b.Credentials {
		remap[c.ID] = uuid.New()
	}
	for _, t := range b.Tags {
		remap[t.ID] = uuid.New()
	}
	for _, r := range b.Rules {
		remap[r.ID] = uuid.New()
	}
	for _, c := range b.Channels {
		remap[c.ID] = uuid.New()
	}
	secret := func(value string) (string, error) {
		if !b.IncludesCredentials {
			return "", nil
		}
		return crypto.Encrypt(value, db.EncryptionKey)
	}
	for _, c := range b.Credentials {
		password, e := secret(c.Password)
		if e != nil {
			return p, e
		}
		key, e := secret(c.PrivateKey)
		if e != nil {
			return p, e
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(id,user_id,name,ssh_username,ssh_password,ssh_key,credential_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, remap[c.ID], user, c.Name, c.Username, password, key, c.Type); err != nil {
			return p, err
		}
	}
	for _, tag := range b.Tags {
		var id uuid.UUID
		if err := tx.QueryRowContext(ctx, `INSERT INTO tags(id,user_id,name,color) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, remap[tag.ID], user, tag.Name, tag.Color).Scan(&id); err != nil {
			return p, err
		}
		remap[tag.ID] = id
	}
	for _, s := range b.Servers {
		password, e := secret(s.Password)
		if e != nil {
			return p, e
		}
		key, e := secret(s.PrivateKey)
		if e != nil {
			return p, e
		}
		var credential any
		if s.CredentialID != nil {
			credential = remap[*s.CredentialID]
		}
		if s.ConnectionMode == "agent" {
			password = ""
			key = ""
			credential = nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO servers(id,user_id,name,host,port,ssh_username,ssh_password,ssh_key,ssh_host_key,credential_id,server_type,
   expires_at,notes,billing_price,billing_currency,billing_cycle,traffic_limit_bytes,public_location,connection_mode,is_public,monitoring_enabled,auto_renew_enabled,agent_auto_name)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,FALSE,FALSE,FALSE,$20)`, remap[s.ID], user, s.Name, s.Host, s.Port, s.SSHUsername, password, key, s.SSHHostKey, credential, s.ServerType, s.ExpiresAt, s.Notes, s.BillingPrice, s.BillingCurrency, s.BillingCycle, s.TrafficLimit, s.PublicLocation, s.ConnectionMode, s.AgentAutoName); err != nil {
			return p, err
		}
		for _, tag := range s.TagIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO server_tags(server_id,tag_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, remap[s.ID], remap[tag]); err != nil {
				return p, err
			}
		}
	}
	for _, c := range b.Channels {
		config := c.Config
		if !b.IncludesCredentials {
			config = (NotificationChannel{Config: config}).Public().Config
		}
		encrypted, err := EncryptNotificationConfig(config, db.EncryptionKey)
		if err != nil {
			return p, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_channels(id,user_id,name,type,enabled,config_encrypted) VALUES($1,$2,$3,$4,FALSE,$5)`, remap[c.ID], user, c.Name, c.Type, encrypted); err != nil {
			return p, err
		}
	}
	for _, r := range b.Rules {
		var server any
		if r.ServerID != nil {
			server = remap[*r.ServerID]
		}
		webhook := r.WebhookURL
		if !b.IncludesCredentials {
			webhook = ""
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rules(id,user_id,name,server_id,metric,comparator,threshold,duration_seconds,enabled,webhook_url) VALUES($1,$2,$3,$4,$5,$6,$7,$8,FALSE,$9)`, remap[r.ID], user, r.Name, server, r.Metric, r.Comparator, r.Threshold, r.Duration, webhook); err != nil {
			return p, err
		}
		for _, channel := range r.ChannelIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, remap[r.ID], remap[channel]); err != nil {
				return p, err
			}
		}
	}
	for _, c := range b.NetworkChecks {
		var source any
		if c.SourceServerID != nil {
			source = remap[*c.SourceServerID]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO network_checks(id,user_id,name,source_server_id,target_server_id,packets,enabled,loss_threshold,latency_threshold_ms) VALUES($1,$2,$3,$4,$5,$6,FALSE,$7,$8)`, uuid.New(), user, c.Name, source, remap[c.TargetServerID], c.Packets, c.LossThreshold, c.LatencyThresholdMS); err != nil {
			return p, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO configuration_imports(user_id,backup_id) VALUES($1,$2)`, user, b.ID); err != nil {
		return p, err
	}
	details, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `INSERT INTO configuration_audit(user_id,action,details) VALUES($1,'restore',$2)`, user, string(details)); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func ConfigurationImported(ctx context.Context, db *sql.DB, user, id uuid.UUID) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_imports WHERE user_id=$1 AND backup_id=$2)`, user, id).Scan(&exists)
	return exists, err
}
func BackupValidationError(err error) error {
	return fmt.Errorf("configuration backup is invalid: %w", err)
}
