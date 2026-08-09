package models

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Server struct {
	ID              uuid.UUID      `json:"id"`
	UserID          uuid.UUID      `json:"user_id"`
	Name            string         `json:"name"`
	Host            string         `json:"host"`
	Port            int            `json:"port"`
	SSHUsername     string         `json:"ssh_username"`
	SSHPassword     string         `json:"-"`
	SSHKey          string         `json:"-"`
	SSHHostKey      string         `json:"ssh_host_key,omitempty"`
	CredentialID    *uuid.UUID     `json:"credential_id,omitempty"`
	CredentialName  string         `json:"credential_name,omitempty"`
	CPUCores        int            `json:"cpu_cores"`
	MemoryTotal     int64          `json:"memory_total"`
	DiskTotal       int64          `json:"disk_total"`
	HasDocker       bool           `json:"has_docker"`
	DockerVersion   string         `json:"docker_version"`
	ExpiresAt       *time.Time     `json:"expires_at"`
	BillingPrice    float64        `json:"billing_price"`
	BillingCurrency string         `json:"billing_currency"`
	BillingCycle    string         `json:"billing_cycle"`
	TrafficLimit    int64          `json:"traffic_limit_bytes"`
	PublicLocation  string         `json:"public_location"`
	ServerType      string         `json:"server_type"`
	Notes           string         `json:"notes"`
	LastSeenAt      *time.Time     `json:"last_seen_at"`
	CreatedAt       time.Time      `json:"created_at"`
	Tags            []Tag          `json:"tags,omitempty"`
	LatestMetrics   *LatestMetrics `json:"latest_metrics,omitempty"`
}

type LatestMetrics struct {
	CPUPercent     float64   `json:"cpu_percent"`
	Load1          float64   `json:"load_1"`
	Load5          float64   `json:"load_5"`
	Load15         float64   `json:"load_15"`
	MemoryUsed     int64     `json:"memory_used"`
	MemoryTotal    int64     `json:"memory_total"`
	DiskUsed       int64     `json:"disk_used"`
	NetworkRxBytes int64     `json:"network_rx_bytes"`
	NetworkTxBytes int64     `json:"network_tx_bytes"`
	NetworkRxTotal int64     `json:"network_rx_total_bytes"`
	NetworkTxTotal int64     `json:"network_tx_total_bytes"`
	DiskRxBytes    int64     `json:"disk_rx_bytes"`
	DiskTxBytes    int64     `json:"disk_tx_bytes"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	LatencyMS      int       `json:"latency_ms"`
	RecordedAt     time.Time `json:"recorded_at"`
}

func CreateServer(db *DB, s *Server) error {
	s.ID = uuid.New()
	encPassword, err := crypto.Encrypt(s.SSHPassword, db.EncryptionKey)
	if err != nil {
		return err
	}
	encKey, err := crypto.Encrypt(s.SSHKey, db.EncryptionKey)
	if err != nil {
		return err
	}
	return db.Raw.QueryRow(
		`INSERT INTO servers (id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, ssh_host_key, credential_id,
		 expires_at, notes, server_type, billing_price, billing_currency, billing_cycle, traffic_limit_bytes, public_location)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING created_at`,
		s.ID, s.UserID, s.Name, s.Host, s.Port, s.SSHUsername, encPassword, encKey, s.SSHHostKey, s.CredentialID,
		s.ExpiresAt, s.Notes, s.ServerType, s.BillingPrice, s.BillingCurrency, s.BillingCycle, s.TrafficLimit, s.PublicLocation,
	).Scan(&s.CreatedAt)
}

// serverSummarySelect returns servers without decrypted secrets, joined with
// their linked credential name and latest metrics sample.
const serverSummarySelect = `SELECT s.id, s.user_id, s.name, s.host, s.port, s.ssh_username, s.last_seen_at, s.created_at,
	 COALESCE(s.ssh_host_key, ''),
	 COALESCE(s.credential_id::text, ''),
	 COALESCE(c.name, ''),
	 COALESCE(s.cpu_cores, 0), COALESCE(s.memory_total_bytes, 0), COALESCE(s.disk_total_bytes, 0),
	 COALESCE(s.has_docker, FALSE), COALESCE(s.docker_version, ''),
	 s.expires_at, COALESCE(s.server_type, 'linux'), COALESCE(s.notes, ''),
	 COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'), COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0), COALESCE(s.public_location, ''),
	 COALESCE(sm.cpu_percent, 0), COALESCE(sm.load_1, 0), COALESCE(sm.load_5, 0), COALESCE(sm.load_15, 0),
	 COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0), COALESCE(sm.disk_used_bytes, 0),
	 COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0),
	 COALESCE(sm.network_rx_total_bytes, 0), COALESCE(sm.network_tx_total_bytes, 0),
	 COALESCE(sm.disk_rx_bytes, 0), COALESCE(sm.disk_tx_bytes, 0),
	 COALESCE(sm.uptime_seconds, 0), COALESCE(sm.latency_ms, 0), sm.recorded_at
	 FROM servers s
	 LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id
	 LEFT JOIN server_latest_metrics sm ON sm.server_id = s.id`

func scanServerSummaries(rows *sql.Rows) ([]Server, error) {
	var servers []Server
	for rows.Next() {
		var s Server
		var m LatestMetrics
		var recordedAt sql.NullTime
		var credIDStr string
		var expiresAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &s.LastSeenAt, &s.CreatedAt, &s.SSHHostKey,
			&credIDStr, &s.CredentialName,
			&s.CPUCores, &s.MemoryTotal, &s.DiskTotal,
			&s.HasDocker, &s.DockerVersion,
			&expiresAt, &s.ServerType, &s.Notes,
			&s.BillingPrice, &s.BillingCurrency, &s.BillingCycle, &s.TrafficLimit, &s.PublicLocation,
			&m.CPUPercent, &m.Load1, &m.Load5, &m.Load15, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed,
			&m.NetworkRxBytes, &m.NetworkTxBytes,
			&m.NetworkRxTotal, &m.NetworkTxTotal,
			&m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.LatencyMS, &recordedAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			s.ExpiresAt = &expiresAt.Time
		}
		if credIDStr != "" {
			id, err := uuid.Parse(credIDStr)
			if err == nil {
				s.CredentialID = &id
			}
		}
		if recordedAt.Valid {
			m.RecordedAt = recordedAt.Time
			s.LatestMetrics = &m
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}

func GetServersByUserID(db *sql.DB, userID uuid.UUID) ([]Server, error) {
	rows, err := db.Query(serverSummarySelect+`
		 WHERE s.user_id = $1
		 ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServerSummaries(rows)
}

// GetServerSummary returns a single server (without decrypted secrets) with
// its latest metrics, scoped to the owning user.
func GetServerSummary(db *sql.DB, id, userID uuid.UUID) (*Server, error) {
	rows, err := db.Query(serverSummarySelect+`
		 WHERE s.id = $1 AND s.user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers, err := scanServerSummaries(rows)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, sql.ErrNoRows
	}
	return &servers[0], nil
}

func ServerOwnedByUser(db *sql.DB, id, userID uuid.UUID) (bool, error) {
	var owned bool
	err := db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1 AND user_id=$2)`,
		id, userID,
	).Scan(&owned)
	return owned, err
}

func GetServerByIDAndUser(db *DB, id, userID uuid.UUID) (*Server, error) {
	s := &Server{}
	var encPassword, encKey string
	var credID sql.NullString
	var expiresAt sql.NullTime
	err := db.Raw.QueryRow(
		`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, COALESCE(ssh_host_key, ''),
		 credential_id, last_seen_at, created_at, COALESCE(server_type, 'linux'), expires_at, COALESCE(notes, ''),
		 COALESCE(billing_price, 0), COALESCE(billing_currency, 'CNY'), COALESCE(billing_cycle, 'year'), COALESCE(traffic_limit_bytes, 0), COALESCE(public_location, '')
		 FROM servers WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &encPassword, &encKey, &s.SSHHostKey, &credID, &s.LastSeenAt, &s.CreatedAt, &s.ServerType, &expiresAt, &s.Notes,
		&s.BillingPrice, &s.BillingCurrency, &s.BillingCycle, &s.TrafficLimit, &s.PublicLocation)
	if err != nil {
		return nil, err
	}
	if credID.Valid {
		cid, err := uuid.Parse(credID.String)
		if err == nil {
			s.CredentialID = &cid
		}
	}
	if expiresAt.Valid {
		s.ExpiresAt = &expiresAt.Time
	}
	s.SSHPassword, err = crypto.Decrypt(encPassword, db.EncryptionKey)
	if err != nil {
		return nil, err
	}
	s.SSHKey, err = crypto.Decrypt(encKey, db.EncryptionKey)
	if err != nil {
		return nil, err
	}
	// Override with the linked credential, scoped to the same owner.
	if err := s.ResolveCredentials(db); err != nil {
		return nil, fmt.Errorf("resolve credentials for server %s: %w", s.Name, err)
	}
	return s, nil
}

func UpdateServer(db *DB, s *Server) error {
	tx, err := db.Raw.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE servers SET name=$1, host=$2, port=$3, ssh_username=$4, ssh_host_key=$5, credential_id=$6,
		 expires_at=$7, notes=$8, server_type=$9, billing_price=$10, billing_currency=$11, billing_cycle=$12, traffic_limit_bytes=$13, public_location=$14
		 WHERE id=$15 AND user_id=$16`,
		s.Name, s.Host, s.Port, s.SSHUsername, s.SSHHostKey, s.CredentialID, s.ExpiresAt, s.Notes, s.ServerType,
		s.BillingPrice, s.BillingCurrency, s.BillingCycle, s.TrafficLimit, s.PublicLocation, s.ID, s.UserID)
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
	if s.SSHPassword != "" {
		enc, err := crypto.Encrypt(s.SSHPassword, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE servers SET ssh_password=$1 WHERE id=$2 AND user_id=$3`, enc, s.ID, s.UserID)
		if err != nil {
			return err
		}
	}
	if s.SSHKey != "" {
		enc, err := crypto.Encrypt(s.SSHKey, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE servers SET ssh_key=$1 WHERE id=$2 AND user_id=$3`, enc, s.ID, s.UserID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func UpdateServerSystemInfo(db *sql.DB, id uuid.UUID, cpuCores int, memTotal, diskTotal int64) error {
	_, err := db.Exec(
		`UPDATE servers SET cpu_cores=$1, memory_total_bytes=$2, disk_total_bytes=$3 WHERE id=$4`,
		cpuCores, memTotal, diskTotal, id)
	return err
}

func UpdateDockerInfo(db *sql.DB, id uuid.UUID, hasDocker bool, version string) error {
	_, err := db.Exec(
		`UPDATE servers SET has_docker=$1, docker_version=$2 WHERE id=$3`,
		hasDocker, version, id)
	return err
}

// ResolveCredentials overwrites SSHUsername/SSHPassword/SSHKey from the linked credential
// when CredentialID is set. Call after fetching a server that needs decrypted SSH creds.
func (s *Server) ResolveCredentials(db *DB) error {
	if s.CredentialID == nil {
		return nil
	}
	cred, err := GetCredentialByID(db, *s.CredentialID, s.UserID)
	if err != nil {
		return err
	}
	s.SSHUsername = cred.SSHUsername
	s.SSHPassword = cred.SSHPassword
	s.SSHKey = cred.SSHKey
	return nil
}

func ServerHasDirectSSHAuth(db *sql.DB, id, userID uuid.UUID) (bool, error) {
	var hasAuth bool
	err := db.QueryRow(
		`SELECT COALESCE(ssh_password, '') <> '' OR COALESCE(ssh_key, '') <> ''
		 FROM servers WHERE id=$1 AND user_id=$2`,
		id, userID,
	).Scan(&hasAuth)
	return hasAuth, err
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
		_, err = tx.Exec(`INSERT INTO server_tags (server_id, tag_id)
			SELECT $1, id FROM tags WHERE id = ANY($2) AND user_id = $3`,
			serverID, pq.Array(tagIDs), userID)
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
		`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, COALESCE(ssh_host_key, ''), last_seen_at, created_at
		 FROM servers WHERE id=$1`, id,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &s.SSHPassword, &s.SSHKey, &s.SSHHostKey, &s.LastSeenAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetAllServers returns all servers with decrypted credentials (for collector).
func GetAllServers(db *DB) ([]Server, error) {
	rows, err := db.Raw.Query(`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, COALESCE(ssh_host_key, ''), credential_id, COALESCE(server_type, 'linux') FROM servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []Server
	for rows.Next() {
		var s Server
		var encPassword, encKey string
		var credID sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &encPassword, &encKey, &s.SSHHostKey, &credID, &s.ServerType); err != nil {
			return nil, err
		}
		if credID.Valid {
			id, err := uuid.Parse(credID.String)
			if err == nil {
				s.CredentialID = &id
			}
		}
		var decErr error
		s.SSHPassword, decErr = crypto.Decrypt(encPassword, db.EncryptionKey)
		if decErr != nil {
			log.Printf("collector: decrypt password for %s failed: %v", s.Name, decErr)
			continue
		}
		s.SSHKey, decErr = crypto.Decrypt(encKey, db.EncryptionKey)
		if decErr != nil {
			log.Printf("collector: decrypt key for %s failed: %v", s.Name, decErr)
			continue
		}
		// Override with credential if linked
		if err := s.ResolveCredentials(db); err != nil {
			log.Printf("collector: resolve credentials for %s: %v", s.Name, err)
			continue
		}
		servers = append(servers, s)
	}
	return servers, nil
}
