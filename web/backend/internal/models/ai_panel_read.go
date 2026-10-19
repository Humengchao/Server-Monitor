package models

// These projections deliberately do not use Server or NotificationChannel:
// neither connection credentials nor notification configuration are selected.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AIPanelPage struct {
	ObservedAt time.Time         `json:"observed_at"`
	Total      int               `json:"total"`
	Offset     int               `json:"offset"`
	NextOffset *int              `json:"next_offset"`
	Items      []json.RawMessage `json:"items"`
	Notes      string            `json:"notes,omitempty"`
}

type AIPanelFleet struct {
	AIPanelPage
	Summary      json.RawMessage   `json:"summary"`
	TopIssues    []json.RawMessage `json:"top_issues"`
	TopResources []json.RawMessage `json:"top_resources"`
}

// All SQL uses the authenticated task owner, not a model-supplied user ID.
// A recent successful sample is evidence of collection, not an ICMP probe.
const aiPanelFleetCTE = `WITH fleet AS (
 SELECT s.id,LEFT(s.name,160) AS name,s.server_type AS platform,s.connection_mode,s.is_public,
 s.expires_at,s.monitoring_enabled,s.cpu_cores,NULLIF(s.memory_total_bytes,0) AS memory_capacity_bytes,m.recorded_at AS last_sample_at,
 CASE WHEN NOT s.monitoring_enabled THEN 'paused'
 WHEN m.recorded_at IS NULL AND s.connection_mode='agent' AND NOT EXISTS(SELECT 1 FROM server_agents a WHERE a.server_id=s.id) THEN 'pending'
 WHEN m.recorded_at IS NULL OR m.recorded_at>$2::timestamptz+INTERVAL '30 seconds' THEN 'unknown'
 WHEN m.recorded_at<=$2::timestamptz-INTERVAL '120 seconds' THEN 'offline' ELSE 'fresh' END AS status,
 CASE WHEN m.cpu_percent>=0 THEN LEAST(m.cpu_percent,100) END AS cpu_percent,
 CASE WHEN m.memory_used>=0 AND m.memory_total>0 THEN ROUND(LEAST(m.memory_used::numeric/m.memory_total*100,100),2) END AS memory_percent,
 CASE WHEN m.disk_used_bytes>=0 AND s.disk_total_bytes>0 THEN ROUND(LEAST(m.disk_used_bytes::numeric/s.disk_total_bytes*100,100),2) END AS disk_percent,
 CASE WHEN m.load_1>=0 THEN m.load_1 END AS load_1,
 CASE WHEN m.latency_ms>=0 THEN m.latency_ms END AS latency_ms,
 m.memory_used,m.memory_total,m.disk_used_bytes,s.disk_total_bytes,m.uptime_seconds,
 m.network_rx_bytes AS network_rx_bytes_per_second,m.network_tx_bytes AS network_tx_bytes_per_second
 FROM servers s LEFT JOIN server_latest_metrics m ON m.server_id=s.id WHERE s.user_id=$1
) `

const aiPanelSnapshotJSON = `jsonb_build_object('id',f.id,'name',f.name,'platform',f.platform,'connection_mode',f.connection_mode,
 'status',f.status,'is_public',f.is_public,'monitoring_enabled',f.monitoring_enabled,'expires_at',f.expires_at,
 'last_sample_at',f.last_sample_at,'cpu_percent',f.cpu_percent,'memory_percent',f.memory_percent,'disk_percent',f.disk_percent,
 'load_1',f.load_1,'latency_ms',f.latency_ms,'memory_used_bytes',f.memory_used,'memory_total_bytes',f.memory_total,
 'disk_used_bytes',f.disk_used_bytes,'disk_total_bytes',NULLIF(f.disk_total_bytes,0),'cpu_cores',NULLIF(f.cpu_cores,0),
 'memory_capacity_bytes',COALESCE(NULLIF(f.memory_total,0),f.memory_capacity_bytes),'uptime_seconds',f.uptime_seconds,
 'network_rx_bytes_per_second',f.network_rx_bytes_per_second,'network_tx_bytes_per_second',f.network_tx_bytes_per_second,
 'tags',COALESCE((SELECT jsonb_agg(t.name ORDER BY t.name) FROM
   (SELECT LEFT(t.name,80) AS name FROM tags t JOIN server_tags st ON st.tag_id=t.id WHERE st.server_id=f.id AND t.user_id=$1 ORDER BY t.name,t.id LIMIT 8) t),'[]'::jsonb),
 'tag_count',(SELECT COUNT(*) FROM server_tags st JOIN tags t ON t.id=st.tag_id WHERE st.server_id=f.id AND t.user_id=$1))`

const aiPanelSnapshotNotes = "fresh means a successful monitor sample less than 120 seconds old (up to 30 seconds clock skew); paused means monitoring disabled; pending means Agent not enrolled; offline means stale sample, not independently verified unreachable; unknown means absent/invalid sample. Non-fresh metrics are historical, not current. Null means unknown, never zero. Names are limited to 160 characters and tags to 8 names of 80 characters."

type aiPanelQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func aiPanelReadRows(ctx context.Context, q aiPanelQuerier, query string, args ...any) ([]json.RawMessage, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		items = append(items, raw)
	}
	return items, rows.Err()
}

func aiPanelSetNext(page *AIPanelPage) {
	page.NextOffset = nil
	if next := page.Offset + len(page.Items); next < page.Total {
		page.NextOffset = &next
	}
}

func aiPanelValidatePage(userID uuid.UUID, offset, limit int) error {
	if userID == uuid.Nil || offset < 0 || offset > 1000000 || limit < 1 || limit > 50 {
		return errors.New("invalid panel page")
	}
	return nil
}

func GetAIPanelFleet(ctx context.Context, db *sql.DB, userID uuid.UUID, now time.Time, offset, limit int) (AIPanelFleet, error) {
	result := AIPanelFleet{AIPanelPage: AIPanelPage{ObservedAt: now, Offset: offset, Notes: aiPanelSnapshotNotes}}
	if err := aiPanelValidatePage(userID, offset, limit); err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, aiPanelFleetCTE+`SELECT COUNT(*),jsonb_build_object(
 'scope','all_servers_in_account','total_servers',COUNT(*),'fresh',COUNT(*) FILTER(WHERE status='fresh'),
 'offline',COUNT(*) FILTER(WHERE status='offline'),'paused',COUNT(*) FILTER(WHERE status='paused'),
 'pending',COUNT(*) FILTER(WHERE status='pending'),'unknown',COUNT(*) FILTER(WHERE status='unknown'),
 'expired',COUNT(*) FILTER(WHERE expires_at<$2),'expires_within_30_days',COUNT(*) FILTER(WHERE expires_at>=$2 AND expires_at<=$2::timestamptz+INTERVAL '30 days'),
 'fresh_high_cpu_90_percent',COUNT(*) FILTER(WHERE status='fresh' AND cpu_percent>=90),
 'fresh_high_memory_90_percent',COUNT(*) FILTER(WHERE status='fresh' AND memory_percent>=90),
 'fresh_high_disk_90_percent',COUNT(*) FILTER(WHERE status='fresh' AND disk_percent>=90),
 'fresh_high_latency_250_ms',COUNT(*) FILTER(WHERE status='fresh' AND latency_ms>=250),
 'fresh_cpu_samples',COUNT(cpu_percent) FILTER(WHERE status='fresh'),'fresh_memory_samples',COUNT(memory_percent) FILTER(WHERE status='fresh'),
 'fresh_disk_samples',COUNT(disk_percent) FILTER(WHERE status='fresh'),'fresh_latency_samples',COUNT(latency_ms) FILTER(WHERE status='fresh'),
 'fresh_cpu_average_percent',ROUND(AVG(cpu_percent) FILTER(WHERE status='fresh'),2),
 'fresh_memory_average_percent',ROUND(AVG(memory_percent) FILTER(WHERE status='fresh'),2),
 'fresh_disk_average_percent',ROUND(AVG(disk_percent) FILTER(WHERE status='fresh'),2),
 'fresh_latency_average_ms',ROUND(AVG(latency_ms) FILTER(WHERE status='fresh'),2),
 'fresh_cpu_max_percent',MAX(cpu_percent) FILTER(WHERE status='fresh'),
 'fresh_memory_max_percent',MAX(memory_percent) FILTER(WHERE status='fresh'),
 'fresh_disk_max_percent',MAX(disk_percent) FILTER(WHERE status='fresh'),
 'latest_sample_at',MAX(last_sample_at) FILTER(WHERE last_sample_at<=$2::timestamptz+INTERVAL '30 seconds'),
 'top_issues_limit',10) FROM fleet`, userID, now).Scan(&result.Total, &result.Summary)
	if err != nil {
		return result, err
	}
	result.Items, err = aiPanelReadRows(ctx, tx, aiPanelFleetCTE+`SELECT `+aiPanelSnapshotJSON+` FROM fleet f ORDER BY f.name,f.id OFFSET $3 LIMIT $4`, userID, now, offset, limit)
	if err != nil {
		return result, err
	}
	result.TopIssues, err = aiPanelReadRows(ctx, tx, aiPanelFleetCTE+`SELECT jsonb_build_object('id',id,'name',name,'status',status,'expires_at',expires_at,
 'last_sample_at',last_sample_at,'cpu_percent',cpu_percent,'memory_percent',memory_percent,'disk_percent',disk_percent,'latency_ms',latency_ms)
 FROM fleet WHERE status IN ('offline','unknown','pending') OR expires_at<$2 OR (status='fresh' AND (cpu_percent>=90 OR memory_percent>=90 OR disk_percent>=90 OR latency_ms>=250))
 ORDER BY (status='offline') DESC,(status='fresh' AND disk_percent>=95) DESC NULLS LAST,(expires_at<$2) DESC NULLS LAST,
 CASE WHEN status='fresh' THEN GREATEST(cpu_percent,memory_percent,disk_percent,LEAST(latency_ms::numeric/5,100)) END DESC NULLS LAST,name,id LIMIT 10`, userID, now)
	if err != nil {
		return result, err
	}
	// Each resource's highest fresh reading is independently visible even when
	// many offline hosts fill top_issues and the account spans hundreds of pages.
	result.TopResources, err = aiPanelReadRows(ctx, tx, aiPanelFleetCTE+`SELECT jsonb_build_object('metric',metric,'id',id,'name',name,'value',value,'unit',CASE WHEN metric='latency' THEN 'ms' ELSE '%' END,'last_sample_at',last_sample_at)
 FROM (SELECT DISTINCT ON(v.metric) v.metric,f.id,f.name,v.value,f.last_sample_at FROM fleet f
 CROSS JOIN LATERAL (VALUES ('cpu',f.cpu_percent),('memory',f.memory_percent),('disk',f.disk_percent),('latency',f.latency_ms::numeric)) v(metric,value)
 WHERE f.status='fresh' AND v.value IS NOT NULL ORDER BY v.metric,v.value DESC,f.id) worst ORDER BY metric`, userID, now)
	if err != nil {
		return result, err
	}
	aiPanelSetNext(&result.AIPanelPage)
	return result, tx.Commit()
}

func GetAIPanelServer(ctx context.Context, db *sql.DB, userID, serverID uuid.UUID, now time.Time) (json.RawMessage, error) {
	if userID == uuid.Nil || serverID == uuid.Nil {
		return nil, sql.ErrNoRows
	}
	var result json.RawMessage
	err := db.QueryRowContext(ctx, aiPanelFleetCTE+`SELECT jsonb_build_object('observed_at',$2::timestamptz,'server',`+aiPanelSnapshotJSON+`,'notes',$4::text) FROM fleet f WHERE f.id=$3`, userID, now, serverID, aiPanelSnapshotNotes).Scan(&result)
	return result, err
}

type AIPanelHistory struct {
	ObservedAt      time.Time         `json:"observed_at"`
	ServerID        uuid.UUID         `json:"server_id"`
	Range           string            `json:"range"`
	Since           time.Time         `json:"since"`
	BucketSeconds   int               `json:"bucket_seconds"`
	ExpectedBuckets int               `json:"expected_buckets"`
	ObservedBuckets int               `json:"observed_buckets"`
	MissingBuckets  int               `json:"missing_buckets"`
	Points          []json.RawMessage `json:"points"`
	Notes           string            `json:"notes"`
}

func GetAIPanelHistory(ctx context.Context, db *sql.DB, userID, serverID uuid.UUID, now time.Time, window string, limit int) (AIPanelHistory, error) {
	result := AIPanelHistory{ObservedAt: now, ServerID: serverID, Range: window, Points: []json.RawMessage{}, Notes: "Only observed buckets are returned; missing buckets mean no collected data, not zero usage or proven downtime. Averages are weighted by recorded sample count. History uses raw samples for 24 hours and one-minute rollups before that; rollup samples near the retention boundary may be unavailable. Disk percentages use current known disk capacity. This is monitoring history, not measured packet loss, jitter, or uptime availability."}
	durations := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}
	duration, ok := durations[window]
	if !ok || limit < 1 || limit > 120 || userID == uuid.Nil || serverID == uuid.Nil {
		return result, errors.New("invalid panel history request")
	}
	owned, err := ServerOwnedByUserContext(ctx, db, serverID, userID)
	if err != nil {
		return result, err
	}
	if !owned {
		return result, sql.ErrNoRows
	}
	result.Since = now.Add(-duration)
	result.BucketSeconds = (int(duration.Seconds()) + limit - 1) / limit
	result.ExpectedBuckets = (int(duration.Seconds()) + result.BucketSeconds - 1) / result.BucketSeconds
	// Both branches repeat the ownership condition to remain scoped if a server
	// is removed or transferred after the initial existence check.
	query := `WITH source AS (
 SELECT m.recorded_at,m.cpu_percent,m.memory_used,m.memory_total,m.disk_used_bytes,s.disk_total_bytes,m.latency_ms,1::bigint AS samples
 FROM server_metrics m JOIN servers s ON s.id=m.server_id WHERE s.user_id=$1 AND s.id=$2 AND m.recorded_at>=$3 AND m.recorded_at<=$4 AND m.recorded_at>=$6
 UNION ALL
 SELECT m.recorded_at,m.cpu_percent,m.memory_used,m.memory_total,m.disk_used_bytes,s.disk_total_bytes,m.latency_ms,GREATEST(m.sample_count,1)::bigint
 FROM server_metrics_1m m JOIN servers s ON s.id=m.server_id WHERE s.user_id=$1 AND s.id=$2 AND m.recorded_at>=$3 AND m.recorded_at<=$4 AND m.recorded_at<$6
 ),bucketed AS (SELECT *,LEAST(FLOOR(EXTRACT(EPOCH FROM(recorded_at-$3::timestamptz))/$5)::int,$7-1) AS bucket FROM source)
 SELECT jsonb_build_object('bucket_start',$3::timestamptz+bucket*$5*INTERVAL '1 second','samples',SUM(samples),
 'first_sample_at',MIN(recorded_at),'last_sample_at',MAX(recorded_at),
 'cpu_average_percent',ROUND(SUM(cpu_percent*samples) FILTER(WHERE cpu_percent>=0)/NULLIF(SUM(samples) FILTER(WHERE cpu_percent>=0),0),2),
 'memory_average_percent',ROUND(SUM(LEAST(memory_used::numeric/NULLIF(memory_total,0)*100,100)*samples) FILTER(WHERE memory_used>=0 AND memory_total>0)/NULLIF(SUM(samples) FILTER(WHERE memory_used>=0 AND memory_total>0),0),2),
 'disk_average_percent',ROUND(SUM(LEAST(disk_used_bytes::numeric/NULLIF(disk_total_bytes,0)*100,100)*samples) FILTER(WHERE disk_used_bytes>=0 AND disk_total_bytes>0)/NULLIF(SUM(samples) FILTER(WHERE disk_used_bytes>=0 AND disk_total_bytes>0),0),2),
 'latency_average_ms',ROUND(SUM(latency_ms::numeric*samples) FILTER(WHERE latency_ms>=0)/NULLIF(SUM(samples) FILTER(WHERE latency_ms>=0),0),2))
 FROM bucketed GROUP BY bucket ORDER BY bucket`
	result.Points, err = aiPanelReadRows(ctx, db, query, userID, serverID, result.Since, now, result.BucketSeconds, now.Add(-24*time.Hour), result.ExpectedBuckets)
	result.ObservedBuckets = len(result.Points)
	result.MissingBuckets = result.ExpectedBuckets - result.ObservedBuckets
	return result, err
}

func GetAIPanelChannels(ctx context.Context, db *sql.DB, userID uuid.UUID, now time.Time, offset, limit int) (AIPanelPage, error) {
	return aiPanelSimplePage(ctx, db, userID, now, offset, limit,
		`SELECT COUNT(*) FROM notification_channels WHERE user_id=$1`,
		`SELECT jsonb_build_object('id',id,'name',LEFT(name,160),'type',type,'enabled',enabled) FROM notification_channels WHERE user_id=$1 ORDER BY name,id OFFSET $2 LIMIT $3`, nil)
}

func GetAIPanelAlerts(ctx context.Context, db *sql.DB, userID uuid.UUID, serverID *uuid.UUID, now time.Time, kind string, offset, limit int) (AIPanelPage, error) {
	if kind != "rules" && kind != "active" {
		return AIPanelPage{}, errors.New("invalid alert list kind")
	}
	if serverID != nil {
		owned, err := ServerOwnedByUserContext(ctx, db, *serverID, userID)
		if err != nil {
			return AIPanelPage{}, err
		}
		if !owned {
			return AIPanelPage{}, sql.ErrNoRows
		}
	}
	// Server-specific rules include global rules which also apply to that host.
	where := ` FROM alert_rules r WHERE r.user_id=$1 AND ($4::uuid IS NULL OR r.server_id IS NULL OR r.server_id=$4)`
	selectSQL := `SELECT jsonb_build_object('id',r.id,'name',LEFT(r.name,160),'server_id',r.server_id,'metric',r.metric,'comparator',r.comparator,
 'threshold',r.threshold,'duration_seconds',r.duration_seconds,'enabled',r.enabled,
 'firing_count',(SELECT COUNT(*) FROM alert_events e WHERE e.rule_id=r.id AND e.user_id=$1 AND e.resolved_at IS NULL),
 'channels',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'name',LEFT(c.name,160),'enabled',c.enabled) ORDER BY c.name,c.id)
 FROM alert_rule_channels rc JOIN notification_channels c ON c.id=rc.channel_id AND c.user_id=$1 WHERE rc.rule_id=r.id),'[]'::jsonb))` + where + ` ORDER BY r.name,r.id OFFSET $2 LIMIT $3`
	countSQL := `SELECT COUNT(*)` + where
	if kind == "active" {
		where = ` FROM alert_events e JOIN alert_rules r ON r.id=e.rule_id AND r.user_id=$1 WHERE e.user_id=$1 AND e.resolved_at IS NULL AND ($4::uuid IS NULL OR e.server_id=$4)`
		countSQL = `SELECT COUNT(*)` + where
		selectSQL = `SELECT jsonb_build_object('id',e.id,'rule_id',e.rule_id,'server_id',e.server_id,
 'server_name',(SELECT LEFT(s.name,160) FROM servers s WHERE s.id=e.server_id AND s.user_id=$1),
 'rule_name',LEFT(r.name,160),
 'value',e.value,'started_at',e.started_at,'metric',r.metric,'comparator',r.comparator,'threshold',r.threshold,'duration_seconds',r.duration_seconds)` + where + ` ORDER BY e.started_at DESC,e.id DESC OFFSET $2 LIMIT $3`
	}
	result, err := aiPanelSimplePage(ctx, db, userID, now, offset, limit, countSQL, selectSQL, []any{serverID})
	result.Notes = "kind=" + kind + "; active events reflect the alert engine's stored state; thresholds and durations describe the current rule configuration, which may have changed since the event started. Rules scoped to all servers have server_id=null. Notification secrets, endpoints and recipient settings are omitted."
	return result, err
}

func aiPanelSimplePage(ctx context.Context, db *sql.DB, userID uuid.UUID, now time.Time, offset, limit int, countSQL, listSQL string, extra []any) (AIPanelPage, error) {
	result := AIPanelPage{ObservedAt: now, Offset: offset, Items: []json.RawMessage{}}
	if err := aiPanelValidatePage(userID, offset, limit); err != nil {
		return result, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	args := append([]any{userID, offset, limit}, extra...)
	// Alerts use $4, with otherwise unused page parameters in the count query.
	if len(extra) > 0 {
		countSQL = `SELECT count_value FROM (` + countSQL + `) c(count_value) WHERE $2::int>=0 AND $3::int>0`
	} else {
		args = []any{userID}
	}
	if err = tx.QueryRowContext(ctx, countSQL, args...).Scan(&result.Total); err != nil {
		return result, fmt.Errorf("read panel count: %w", err)
	}
	args = append([]any{userID, offset, limit}, extra...)
	result.Items, err = aiPanelReadRows(ctx, tx, listSQL, args...)
	if err != nil {
		return result, err
	}
	aiPanelSetNext(&result)
	return result, tx.Commit()
}
