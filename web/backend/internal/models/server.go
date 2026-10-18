package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Server struct {
	ConnectionMode    string     `json:"connection_mode"`
	IsPublic          bool       `json:"is_public"`
	MonitoringEnabled bool       `json:"monitoring_enabled"`
	AutoRenewEnabled  bool       `json:"auto_renew_enabled"`
	ID                uuid.UUID  `json:"id"`
	UserID            uuid.UUID  `json:"user_id"`
	Name              string     `json:"name"`
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	SSHUsername       string     `json:"ssh_username"`
	SSHPassword       string     `json:"-"`
	SSHKey            string     `json:"-"`
	SSHHostKey        string     `json:"ssh_host_key,omitempty"`
	CredentialID      *uuid.UUID `json:"credential_id,omitempty"`
	CredentialName    string     `json:"credential_name,omitempty"`
	CPUCores          int        `json:"cpu_cores"`
	MemoryTotal       int64      `json:"memory_total"`
	DiskTotal         int64      `json:"disk_total"`
	HasDocker         bool       `json:"has_docker"`
	DockerVersion     string     `json:"docker_version"`
	ExpiresAt         *time.Time `json:"expires_at"`
	BillingPrice      float64    `json:"billing_price"`
	BillingCurrency   string     `json:"billing_currency"`
	BillingCycle      string     `json:"billing_cycle"`
	TrafficLimit      int64      `json:"traffic_limit_bytes"`
	PublicLocation    string     `json:"public_location"`
	ServerType        string     `json:"server_type"`
	Notes             string     `json:"notes"`
	CreatedAt         time.Time  `json:"created_at"`
	// The last sustained collector failure is diagnostic state. It is cleared
	// after a successful sample and never contains credentials.
	LastErrorKind string         `json:"last_error_kind,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	LastErrorAt   *time.Time     `json:"last_error_at,omitempty"`
	Tags          []Tag          `json:"tags,omitempty"`
	LatestMetrics *LatestMetrics `json:"latest_metrics,omitempty"`
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
	if s.ConnectionMode == "" {
		s.ConnectionMode = "ssh"
		s.IsPublic = true
		s.MonitoringEnabled = true
		s.AutoRenewEnabled = true
	}
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
		 expires_at, notes, server_type, billing_price, billing_currency, billing_cycle, traffic_limit_bytes, public_location,
		 connection_mode,is_public,monitoring_enabled,auto_renew_enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) RETURNING created_at`,
		s.ID, s.UserID, s.Name, s.Host, s.Port, s.SSHUsername, encPassword, encKey, s.SSHHostKey, s.CredentialID,
		s.ExpiresAt, s.Notes, s.ServerType, s.BillingPrice, s.BillingCurrency, s.BillingCycle, s.TrafficLimit, s.PublicLocation,
		s.ConnectionMode, s.IsPublic, s.MonitoringEnabled, s.AutoRenewEnabled,
	).Scan(&s.CreatedAt)
}

// serverSummarySelect returns servers without decrypted secrets, joined with
// their linked credential name and latest metrics sample.
const serverSummarySelect = `SELECT s.connection_mode,s.is_public,s.monitoring_enabled,s.auto_renew_enabled,
	 s.id, s.user_id, s.name, s.host, s.port, s.ssh_username, s.created_at,
	 COALESCE(s.ssh_host_key, ''),
	 COALESCE(s.credential_id::text, ''),
	 COALESCE(c.name, ''),
	 COALESCE(s.cpu_cores, 0), COALESCE(s.memory_total_bytes, 0), COALESCE(s.disk_total_bytes, 0),
	 COALESCE(s.has_docker, FALSE), COALESCE(s.docker_version, ''),
	 s.expires_at, COALESCE(s.server_type, 'linux'), COALESCE(s.notes, ''),
	 COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'), COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0), COALESCE(s.public_location, ''),
	 COALESCE(s.last_error_kind, ''), COALESCE(s.last_error, ''), s.last_error_at,
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
		var lastErrorAt sql.NullTime
		if err := rows.Scan(&s.ConnectionMode, &s.IsPublic, &s.MonitoringEnabled, &s.AutoRenewEnabled, &s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &s.CreatedAt, &s.SSHHostKey,
			&credIDStr, &s.CredentialName,
			&s.CPUCores, &s.MemoryTotal, &s.DiskTotal,
			&s.HasDocker, &s.DockerVersion,
			&expiresAt, &s.ServerType, &s.Notes,
			&s.BillingPrice, &s.BillingCurrency, &s.BillingCycle, &s.TrafficLimit, &s.PublicLocation,
			&s.LastErrorKind, &s.LastError, &lastErrorAt,
			&m.CPUPercent, &m.Load1, &m.Load5, &m.Load15, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed,
			&m.NetworkRxBytes, &m.NetworkTxBytes,
			&m.NetworkRxTotal, &m.NetworkTxTotal,
			&m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.LatencyMS, &recordedAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			s.ExpiresAt = &expiresAt.Time
		}
		if lastErrorAt.Valid {
			s.LastErrorAt = &lastErrorAt.Time
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

// RecordServerPollError stores a stable reason only when it changes. A host
// that stays unreachable for hours must not cause a database write on every
// retry.
func RecordServerPollError(db *sql.DB, id uuid.UUID, kind, detail string, at time.Time) error {
	_, err := db.Exec(
		`UPDATE servers SET last_error_kind=$2, last_error=$3, last_error_at=$4
		 WHERE id=$1 AND (last_error_kind IS DISTINCT FROM $2 OR last_error IS DISTINCT FROM $3 OR last_error_at IS NULL)`,
		id, kind, detail, at)
	return err
}

// ClearServerPollError removes the diagnostic after a successful sample.
func ClearServerPollError(db *sql.DB, id uuid.UUID) error {
	_, err := db.Exec(
		`UPDATE servers SET last_error_kind='', last_error='', last_error_at=NULL
		 WHERE id=$1 AND (last_error_kind IS DISTINCT FROM '' OR last_error IS DISTINCT FROM '' OR last_error_at IS NOT NULL)`, id)
	return err
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
	return ServerOwnedByUserContext(context.Background(), db, id, userID)
}

func ServerOwnedByUserContext(ctx context.Context, db *sql.DB, id, userID uuid.UUID) (bool, error) {
	var owned bool
	err := db.QueryRowContext(ctx,
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
		 credential_id, created_at, COALESCE(server_type, 'linux'), expires_at, COALESCE(notes, ''),
		 COALESCE(billing_price, 0), COALESCE(billing_currency, 'CNY'), COALESCE(billing_cycle, 'year'), COALESCE(traffic_limit_bytes, 0), COALESCE(public_location, ''),
		 connection_mode,is_public,monitoring_enabled,auto_renew_enabled
		 FROM servers WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &encPassword, &encKey, &s.SSHHostKey, &credID, &s.CreatedAt, &s.ServerType, &expiresAt, &s.Notes,
		&s.BillingPrice, &s.BillingCurrency, &s.BillingCycle, &s.TrafficLimit, &s.PublicLocation,
		&s.ConnectionMode, &s.IsPublic, &s.MonitoringEnabled, &s.AutoRenewEnabled)
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
	if s.ConnectionMode == "agent" {
		return s, nil
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
	return UpdateServerWithExpiryExpectation(context.Background(), db, s, nil)
}

var ErrServerExpiryChanged = errors.New("server expiry changed")

// A nil expectation preserves compatibility for API clients that explicitly
// replace a server. A non-nil expectation also represents an expected NULL.
type ExpiryExpectation struct{ ExpiresAt *time.Time }

type ServerOptions struct {
	IsPublic          *bool
	MonitoringEnabled *bool
	AutoRenewEnabled  *bool
}

func UpdateServerWithExpiryExpectation(ctx context.Context, db *DB, s *Server, expected *ExpiryExpectation, options ...ServerOptions) error {
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expected != nil {
		var current sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM servers WHERE id=$1 AND user_id=$2 FOR UPDATE`, s.ID, s.UserID).Scan(&current); err != nil {
			return err
		}
		if current.Valid != (expected.ExpiresAt != nil) || (current.Valid && !current.Time.Equal(*expected.ExpiresAt)) {
			return ErrServerExpiryChanged
		}
	}

	result, err := tx.ExecContext(ctx,
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
	if len(options) > 0 {
		o := options[0]
		if err := tx.QueryRowContext(ctx, `UPDATE servers SET is_public=COALESCE($1,is_public),monitoring_enabled=COALESCE($2,monitoring_enabled),auto_renew_enabled=COALESCE($3,auto_renew_enabled) WHERE id=$4 AND user_id=$5 RETURNING is_public,monitoring_enabled,auto_renew_enabled`, o.IsPublic, o.MonitoringEnabled, o.AutoRenewEnabled, s.ID, s.UserID).Scan(&s.IsPublic, &s.MonitoringEnabled, &s.AutoRenewEnabled); err != nil {
			return err
		}
	}
	if s.SSHPassword != "" {
		enc, err := crypto.Encrypt(s.SSHPassword, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE servers SET ssh_password=$1 WHERE id=$2 AND user_id=$3`, enc, s.ID, s.UserID)
		if err != nil {
			return err
		}
	}
	if s.ServerType == "windows" {
		_, err = tx.ExecContext(ctx, `UPDATE servers SET ssh_key='', ssh_host_key='' WHERE id=$1 AND user_id=$2`, s.ID, s.UserID)
		if err != nil {
			return err
		}
	} else if s.SSHKey != "" {
		enc, err := crypto.Encrypt(s.SSHKey, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE servers SET ssh_key=$1 WHERE id=$2 AND user_id=$3`, enc, s.ID, s.UserID)
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
	if s.ConnectionMode == "agent" || s.CredentialID == nil {
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

// GetServerDockerInfo returns the cached Docker availability for a server,
// scoped to the owning user, without touching SSH credentials.
func GetServerDockerInfo(db *sql.DB, id, userID uuid.UUID) (bool, string, error) {
	var hasDocker bool
	var version string
	err := db.QueryRow(
		`SELECT COALESCE(has_docker, FALSE), COALESCE(docker_version, '')
		 FROM servers WHERE id=$1 AND user_id=$2`,
		id, userID,
	).Scan(&hasDocker, &version)
	return hasDocker, version, err
}

// DockerInfoRow is one server's persisted Docker facts, for seeding the
// collector's change-detection cache at startup.
type DockerInfoRow struct {
	ServerID  uuid.UUID
	HasDocker bool
	Version   string
}

// GetAllDockerInfo returns the persisted Docker facts for every server. The
// collector loads these once at startup so its "last written value" cache is
// not empty on the first poll — an empty cache treats every server as unknown,
// and a first poll that happened to miss the daemon would then write
// has_docker=false over a row that was correct.
func GetAllDockerInfo(db *sql.DB) ([]DockerInfoRow, error) {
	rows, err := db.Query(
		`SELECT id, COALESCE(has_docker, FALSE), COALESCE(docker_version, '') FROM servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DockerInfoRow, 0)
	for rows.Next() {
		var r DockerInfoRow
		if err := rows.Scan(&r.ServerID, &r.HasDocker, &r.Version); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
	result, err := db.Exec("DELETE FROM servers WHERE id=$1 AND user_id=$2", id, userID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetServerTags replaces a server's tag set. Both statements are owner-scoped
// and a foreign server is refused up front with sql.ErrNoRows (the handler
// maps that to 404 like every other "not your resource" path), so calling this
// with somebody else's server ID never writes anything.
func SetServerTags(db *sql.DB, serverID, userID uuid.UUID, tagIDs []uuid.UUID) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var owned bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1 AND user_id=$2)`,
		serverID, userID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return sql.ErrNoRows
	}

	_, err = tx.Exec(`DELETE FROM server_tags WHERE server_id=$1
		AND server_id IN (SELECT id FROM servers WHERE id=$1 AND user_id=$2)`, serverID, userID)
	if err != nil {
		return err
	}
	if len(tagIDs) > 0 {
		_, err = tx.Exec(`INSERT INTO server_tags (server_id, tag_id)
			SELECT $1, id FROM tags WHERE id = ANY($2) AND user_id = $3
			AND EXISTS (SELECT 1 FROM servers WHERE id = $1 AND user_id = $3)`,
			serverID, pq.Array(tagIDs), userID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddTagsToServers attaches every tag to every server in one statement. Both
// sides are filtered by owner, so IDs belonging to another user are silently
// skipped rather than granting cross-tenant access. Already-present pairs are
// left alone, which makes the operation idempotent.
func AddTagsToServers(db *sql.DB, serverIDs, tagIDs []uuid.UUID, userID uuid.UUID) error {
	if len(serverIDs) == 0 || len(tagIDs) == 0 {
		return nil
	}
	_, err := db.Exec(
		`INSERT INTO server_tags (server_id, tag_id)
		 SELECT s.id, t.id FROM servers s CROSS JOIN tags t
		 WHERE s.id = ANY($1) AND s.user_id = $3
		   AND t.id = ANY($2) AND t.user_id = $3
		 ON CONFLICT (server_id, tag_id) DO NOTHING`,
		pq.Array(serverIDs), pq.Array(tagIDs), userID)
	return err
}

func RemoveTagsFromServers(db *sql.DB, serverIDs, tagIDs []uuid.UUID, userID uuid.UUID) error {
	if len(serverIDs) == 0 || len(tagIDs) == 0 {
		return nil
	}
	_, err := db.Exec(
		`DELETE FROM server_tags st
		 USING servers s
		 WHERE st.server_id = s.id AND s.user_id = $3
		   AND st.server_id = ANY($1) AND st.tag_id = ANY($2)`,
		pq.Array(serverIDs), pq.Array(tagIDs), userID)
	return err
}

// DeleteServers removes several servers at once and reports how many rows the
// owner actually had, so the caller can tell the user if some IDs were ignored.
func DeleteServers(db *sql.DB, serverIDs []uuid.UUID, userID uuid.UUID) (int64, error) {
	deleted, err := DeleteServersReturningIDs(context.Background(), db, serverIDs, userID)
	return int64(len(deleted)), err
}

// Return only rows this owner actually deleted. Side effects such as closing
// SSH/Agent transports must use these IDs, never the untrusted request list.
// The transaction commits only after every returned ID has been read.
func DeleteServersReturningIDs(ctx context.Context, db *sql.DB, serverIDs []uuid.UUID, userID uuid.UUID) ([]uuid.UUID, error) {
	if len(serverIDs) == 0 {
		return []uuid.UUID{}, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`DELETE FROM servers WHERE id = ANY($1) AND user_id = $2 RETURNING id`,
		pq.Array(serverIDs), userID)
	if err != nil {
		return nil, err
	}
	deleted := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		deleted = append(deleted, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

// CountOwnedServers reports how many of the given IDs the user actually owns.
// Bulk tag statements silently skip rows belonging to someone else, so this is
// what lets the API report the number it really touched instead of the number
// it was asked about.
func CountOwnedServers(db *sql.DB, serverIDs []uuid.UUID, userID uuid.UUID) (int, error) {
	if len(serverIDs) == 0 {
		return 0, nil
	}
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM servers WHERE id = ANY($1) AND user_id = $2`,
		pq.Array(serverIDs), userID).Scan(&count)
	return count, err
}

// GetServersByIDsAndUser returns servers with decrypted, credential-resolved
// SSH settings, in the order the IDs were given. IDs the user does not own are
// omitted, so callers must compare lengths if that matters.
func GetServersByIDsAndUser(db *DB, ids []uuid.UUID, userID uuid.UUID) ([]*Server, error) {
	return GetServersByIDsAndUserContext(context.Background(), db, ids, userID)
}

func GetServersByIDsAndUserContext(ctx context.Context, db *DB, ids []uuid.UUID, userID uuid.UUID) ([]*Server, error) {
	servers := make([]*Server, 0, len(ids))
	if len(ids) == 0 {
		return servers, nil
	}
	// Fetch all targets and their effective credentials in one owner-scoped
	// query, instead of opening one or two queries for every selected server.
	rows, err := db.Raw.QueryContext(ctx, `SELECT s.id, s.user_id, s.name, s.host, s.port,
		s.ssh_username, COALESCE(s.ssh_password, ''), COALESCE(s.ssh_key, ''), COALESCE(s.ssh_host_key, ''),
		s.credential_id, s.created_at, COALESCE(s.server_type, 'linux'), s.expires_at, COALESCE(s.notes, ''),
		COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'), COALESCE(s.billing_cycle, 'year'),
		COALESCE(s.traffic_limit_bytes, 0), COALESCE(s.public_location, ''),
		c.ssh_username, COALESCE(c.ssh_password, ''), COALESCE(c.ssh_key, ''),
		s.connection_mode,s.is_public,s.monitoring_enabled,s.auto_renew_enabled
		FROM servers s
		LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id
		WHERE s.id = ANY($1) AND s.user_id = $2`, pq.Array(ids), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[uuid.UUID]Server, len(ids))
	for rows.Next() {
		var s Server
		var encPassword, encKey string
		var credID, credentialUsername sql.NullString
		var credentialPassword, credentialKey string
		var expiresAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &encPassword, &encKey, &s.SSHHostKey, &credID, &s.CreatedAt, &s.ServerType, &expiresAt, &s.Notes,
			&s.BillingPrice, &s.BillingCurrency, &s.BillingCycle, &s.TrafficLimit, &s.PublicLocation,
			&credentialUsername, &credentialPassword, &credentialKey, &s.ConnectionMode, &s.IsPublic, &s.MonitoringEnabled, &s.AutoRenewEnabled); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			s.ExpiresAt = &expiresAt.Time
		}
		if s.ConnectionMode == "agent" {
			byID[s.ID] = s
			continue
		}
		if credID.Valid {
			id, err := uuid.Parse(credID.String)
			if err != nil {
				return nil, fmt.Errorf("resolve credentials for server %s: %w", s.Name, err)
			}
			s.CredentialID = &id
			// An unresolved link must fail the batch, never fall back to
			// stale inline secrets or authorize another owner's credential.
			if !credentialUsername.Valid {
				return nil, fmt.Errorf("resolve credentials for server %s: %w", s.Name, sql.ErrNoRows)
			}
			s.SSHUsername = credentialUsername.String
			encPassword, encKey = credentialPassword, credentialKey
		}
		if s.SSHPassword, err = crypto.Decrypt(encPassword, db.EncryptionKey); err != nil {
			return nil, fmt.Errorf("decrypt password for server %s: %w", s.Name, err)
		}
		if s.SSHKey, err = crypto.Decrypt(encKey, db.EncryptionKey); err != nil {
			return nil, fmt.Errorf("decrypt key for server %s: %w", s.Name, err)
		}
		byID[s.ID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// ANY does not preserve order or duplicates. Restore both to retain the
	// model's existing behavior; the batch handler deduplicates its own input.
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s, ok := byID[id]; ok {
			servers = append(servers, &s)
		}
	}
	return servers, nil
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
		`SELECT id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, COALESCE(ssh_host_key, ''), created_at,
		 COALESCE(has_docker, FALSE), COALESCE(docker_version, '')
		 FROM servers WHERE id=$1`, id,
	).Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
		&s.SSHUsername, &s.SSHPassword, &s.SSHKey, &s.SSHHostKey, &s.CreatedAt,
		&s.HasDocker, &s.DockerVersion)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetAllServers returns all servers with decrypted credentials (for collector).
func GetAllServers(db *DB) ([]Server, error) {
	// Resolve linked credentials in the same query. Querying once per server
	// both multiplied each collector tick's database work and could exhaust the
	// pool while the server rows were still holding a connection.
	rows, err := db.Raw.Query(`SELECT s.id, s.user_id, s.name, s.host, s.port,
		s.ssh_username, COALESCE(s.ssh_password, ''), COALESCE(s.ssh_key, ''),
		COALESCE(s.ssh_host_key, ''), s.credential_id, COALESCE(s.server_type, 'linux'),
		c.ssh_username, COALESCE(c.ssh_password, ''), COALESCE(c.ssh_key, ''),
		s.connection_mode,s.is_public,s.monitoring_enabled,s.auto_renew_enabled
		FROM servers s
		LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id WHERE s.monitoring_enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []Server
	for rows.Next() {
		var s Server
		var encPassword, encKey string
		var credID sql.NullString
		var credentialUsername sql.NullString
		var credentialPassword, credentialKey string
		if err := rows.Scan(&s.ID, &s.UserID, &s.Name, &s.Host, &s.Port,
			&s.SSHUsername, &encPassword, &encKey, &s.SSHHostKey, &credID, &s.ServerType,
			&credentialUsername, &credentialPassword, &credentialKey, &s.ConnectionMode, &s.IsPublic, &s.MonitoringEnabled, &s.AutoRenewEnabled); err != nil {
			return nil, err
		}
		if s.ConnectionMode == "agent" {
			servers = append(servers, s)
			continue
		}
		if credID.Valid {
			id, err := uuid.Parse(credID.String)
			if err == nil {
				s.CredentialID = &id
			}
			// A missing or cross-owner link must never fall back to the
			// server's old inline credentials. Username is NOT NULL for an
			// existing credential, so NULL means the scoped join found none.
			if !credentialUsername.Valid {
				log.Printf("collector: resolve credentials for %s: %v", s.Name, sql.ErrNoRows)
				continue
			}
			s.SSHUsername = credentialUsername.String
			encPassword, encKey = credentialPassword, credentialKey
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
		servers = append(servers, s)
	}
	return servers, rows.Err()
}
