package models

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/agenttransport"
)

var ErrAgentTicket = errors.New("enrollment ticket is invalid, expired, or already used")

func randomAgentSecret() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
func agentSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func CreateAgentTicket(ctx context.Context, db *sql.DB, serverID, userID uuid.UUID) (string, time.Time, error) {
	ticket, err := randomAgentSecret()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	var id uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM servers WHERE id=$1 AND user_id=$2 AND connection_mode='agent' FOR UPDATE`, serverID, userID).Scan(&id); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollments WHERE server_id=$1`, serverID); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollments(id,server_id,user_id,ticket_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, uuid.New(), serverID, userID, agentSecretHash(ticket), expires); err != nil {
		return "", time.Time{}, err
	}
	return ticket, expires, tx.Commit()
}

func RegisterAgent(ctx context.Context, db *sql.DB, ticket string, hello agenttransport.Hello, address string) (uuid.UUID, string, error) {
	if len(ticket) != 64 {
		return uuid.Nil, "", ErrAgentTicket
	}
	if _, err := hello.Validate(); err != nil {
		return uuid.Nil, "", err
	}
	token, err := randomAgentSecret()
	if err != nil {
		return uuid.Nil, "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer tx.Rollback()
	var serverID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE agent_enrollments SET used_at=NOW() WHERE ticket_hash=$1 AND used_at IS NULL AND expires_at>NOW() RETURNING server_id`, agentSecretHash(ticket)).Scan(&serverID)
	if err == sql.ErrNoRows {
		return uuid.Nil, "", ErrAgentTicket
	}
	if err != nil {
		return uuid.Nil, "", err
	}
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT connection_mode FROM servers WHERE id=$1 FOR UPDATE`, serverID).Scan(&mode); err != nil {
		return uuid.Nil, "", err
	}
	if mode != "agent" {
		return uuid.Nil, "", ErrAgentTicket
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO server_agents(server_id,token_hash,host_key,version,hostname,os,arch,run_user,last_seen_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,NOW()) ON CONFLICT(server_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,host_key=EXCLUDED.host_key,
 version=EXCLUDED.version,hostname=EXCLUDED.hostname,os=EXCLUDED.os,arch=EXCLUDED.arch,run_user=EXCLUDED.run_user,enrolled_at=NOW(),last_seen_at=NOW()`,
		serverID, agentSecretHash(token), hello.HostKey, hello.Version, hello.Hostname, hello.OS, hello.Arch, hello.Username)
	if err != nil {
		return uuid.Nil, "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE servers SET server_type=$1,ssh_username=$2,host=CASE WHEN host='' OR host='agent' THEN $3 ELSE host END,
 ssh_password='',ssh_key='',ssh_host_key='',credential_id=NULL,last_error_kind='',last_error='',last_error_at=NULL WHERE id=$4`, hello.OS, hello.Username, address, serverID)
	if err != nil {
		return uuid.Nil, "", err
	}
	return serverID, token, tx.Commit()
}

func AuthenticateAgent(ctx context.Context, db *sql.DB, token string) (uuid.UUID, string, error) {
	if len(token) != 64 {
		return uuid.Nil, "", sql.ErrNoRows
	}
	var id uuid.UUID
	var hostKey string
	err := db.QueryRowContext(ctx, `SELECT a.server_id,a.host_key FROM server_agents a JOIN servers s ON s.id=a.server_id
 WHERE a.token_hash=$1 AND s.connection_mode='agent'`, agentSecretHash(token)).Scan(&id, &hostKey)
	return id, hostKey, err
}

func TouchAgent(ctx context.Context, db *sql.DB, id uuid.UUID, hello agenttransport.Hello) error {
	result, err := db.ExecContext(ctx, `UPDATE server_agents SET last_seen_at=NOW(),version=$2,hostname=$3,os=$4,arch=$5,run_user=$6 WHERE server_id=$1 AND host_key=$7`,
		id, hello.Version, hello.Hostname, hello.OS, hello.Arch, hello.Username, hello.HostKey)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type AgentStatus struct {
	Enrolled   bool       `json:"enrolled"`
	Online     bool       `json:"online"`
	Version    string     `json:"version"`
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`
	Arch       string     `json:"arch"`
	Username   string     `json:"username"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

func GetAgentStatus(ctx context.Context, db *sql.DB, id, userID uuid.UUID) (AgentStatus, error) {
	var s AgentStatus
	var last sql.NullTime
	err := db.QueryRowContext(ctx, `SELECT a.server_id IS NOT NULL,COALESCE(a.version,''),COALESCE(a.hostname,''),COALESCE(a.os,''),COALESCE(a.arch,''),COALESCE(a.run_user,''),a.last_seen_at
 FROM servers s LEFT JOIN server_agents a ON a.server_id=s.id WHERE s.id=$1 AND s.user_id=$2 AND s.connection_mode='agent'`, id, userID).
		Scan(&s.Enrolled, &s.Version, &s.Hostname, &s.OS, &s.Arch, &s.Username, &last)
	if last.Valid {
		s.LastSeenAt = &last.Time
	}
	return s, err
}
func RevokeAgent(ctx context.Context, db *sql.DB, id, userID uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM servers WHERE id=$1 AND user_id=$2 AND connection_mode='agent' FOR UPDATE`, id, userID).Scan(&owned); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollments WHERE server_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_agents WHERE server_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}
