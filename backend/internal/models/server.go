package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Server struct {
	ID            uuid.UUID      `json:"id"`
	UserID        uuid.UUID      `json:"user_id"`
	Name          string         `json:"name"`
	Host          string         `json:"host"`
	Port          int            `json:"port"`
	SSHUsername   string         `json:"ssh_username"`
	SSHPassword   string         `json:"-"`
	SSHKey        string         `json:"-"`
	LastSeenAt    *time.Time     `json:"last_seen_at"`
	CreatedAt     time.Time      `json:"created_at"`
	Tags          []Tag          `json:"tags,omitempty"`
	LatestMetrics *LatestMetrics `json:"latest_metrics,omitempty"`
}

type LatestMetrics struct {
	CPUPercent     float64   `json:"cpu_percent"`
	MemoryUsed     int64     `json:"memory_used"`
	MemoryTotal    int64     `json:"memory_total"`
	NetworkRxBytes int64     `json:"network_rx_bytes"`
	NetworkTxBytes int64     `json:"network_tx_bytes"`
	RecordedAt     time.Time `json:"recorded_at"`
}

func CreateServer(db *sql.DB, s *Server) error {
	s.ID = uuid.New()
	return db.QueryRow(
		`INSERT INTO servers (id, user_id, name, host, port, ssh_username, ssh_password, ssh_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`,
		s.ID, s.UserID, s.Name, s.Host, s.Port, s.SSHUsername, s.SSHPassword, s.SSHKey,
	).Scan(&s.CreatedAt)
}

func GetServersByUserID(db *sql.DB, userID uuid.UUID) ([]Server, error) {
	rows, err := db.Query(
		`SELECT s.id, s.user_id, s.name, s.host, s.port, s.ssh_username, s.last_seen_at, s.created_at,
		 COALESCE(sm.cpu_percent, 0), COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0),
		 COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0), sm.recorded_at
		 FROM servers s
		 LEFT JOIN LATERAL (
			 SELECT * FROM server_metrics WHERE server_id = s.id ORDER BY recorded_at DESC LIMIT 1
		 ) sm ON true
		 WHERE s.user_id = $1
		 ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []Server
	for rows.Next() {
		var s Server
		var m LatestMetrics
		var recordedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &s.LastSeenAt, &s.CreatedAt,
			&m.CPUPercent, &m.MemoryUsed, &m.MemoryTotal,
			&m.NetworkRxBytes, &m.NetworkTxBytes, &recordedAt); err != nil {
			return nil, err
		}
		if recordedAt.Valid {
			m.RecordedAt = recordedAt.Time
			s.LatestMetrics = &m
		}
		servers = append(servers, s)
	}
	return servers, nil
}

func GetServerByIDAndUser(db *sql.DB, id, userID uuid.UUID) (*Server, error) {
	s := &Server{}
	err := db.QueryRow(
		`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, last_seen_at, created_at
		 FROM servers WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &s.SSHPassword, &s.SSHKey, &s.LastSeenAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func UpdateServer(db *sql.DB, s *Server) error {
	_, err := db.Exec(
		`UPDATE servers SET name=$1, host=$2, port=$3, ssh_username=$4, ssh_password=$5, ssh_key=$6
		 WHERE id=$7 AND user_id=$8`,
		s.Name, s.Host, s.Port, s.SSHUsername, s.SSHPassword, s.SSHKey, s.ID, s.UserID)
	return err
}

func DeleteServer(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec("DELETE FROM servers WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

func SetServerTags(db *sql.DB, serverID, userID uuid.UUID, tagIDs []uuid.UUID) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM server_tags WHERE server_id=$1
		AND server_id IN (SELECT id FROM servers WHERE id=$1 AND user_id=$2)`, serverID, userID)
	if err != nil {
		return err
	}
	if len(tagIDs) > 0 {
		stmt, _ := tx.Prepare(`INSERT INTO server_tags (server_id, tag_id)
			SELECT $1, id FROM tags WHERE id = ANY($2) AND user_id = $3`)
		_, err = stmt.Exec(serverID, pq.Array(tagIDs), userID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func GetServerTags(db *sql.DB, serverID uuid.UUID) ([]Tag, error) {
	rows, err := db.Query(
		`SELECT t.id, t.user_id, t.name, t.color FROM tags t
		 JOIN server_tags st ON t.id = st.tag_id WHERE st.server_id = $1`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, nil
}

func GetServerByID(db *sql.DB, id uuid.UUID) (*Server, error) {
	s := &Server{}
	err := db.QueryRow(
		`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, last_seen_at, created_at
		 FROM servers WHERE id=$1`, id,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &s.SSHPassword, &s.SSHKey, &s.LastSeenAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func GetAllServers(db *sql.DB) ([]Server, error) {
	rows, err := db.Query(`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key FROM servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []Server
	for rows.Next() {
		var s Server
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &s.SSHPassword, &s.SSHKey); err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, nil
}
