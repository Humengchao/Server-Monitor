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
)

var ErrNetworkLimit = errors.New("at most 100 network checks per account")

type NetworkCheck struct {
	ID                 uuid.UUID      `json:"id"`
	UserID             uuid.UUID      `json:"-"`
	Name               string         `json:"name"`
	SourceServerID     *uuid.UUID     `json:"source_server_id"`
	TargetServerID     uuid.UUID      `json:"target_server_id"`
	Packets            int            `json:"packets"`
	Enabled            bool           `json:"enabled"`
	LossThreshold      float64        `json:"loss_threshold"`
	LatencyThresholdMS *float64       `json:"latency_threshold_ms"`
	CreatedAt          time.Time      `json:"created_at"`
	Latest             *NetworkSample `json:"latest"`
}

type NetworkSample struct {
	RecordedAt        time.Time `json:"recorded_at"`
	Status            string    `json:"status"`
	Sent              *int      `json:"sent"`
	Received          *int      `json:"received"`
	LossPercent       *float64  `json:"loss_percent"`
	AvgMS             *float64  `json:"avg_ms"`
	MinMS             *float64  `json:"min_ms"`
	MaxMS             *float64  `json:"max_ms"`
	MdevMS            *float64  `json:"mdev_ms"`
	ErrorCode         string    `json:"error_code,omitempty"`
	ThresholdExceeded bool      `json:"threshold_exceeded"`
}

const networkColumns = `c.id,c.user_id,c.name,c.source_server_id,c.target_server_id,c.packets,c.enabled,c.loss_threshold,c.latency_threshold_ms,c.created_at`

type networkScanner interface{ Scan(...any) error }

func scanNetworkCheck(row networkScanner) (NetworkCheck, error) {
	var c NetworkCheck
	var source sql.NullString
	var latency sql.NullFloat64
	var sample []byte
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &source, &c.TargetServerID, &c.Packets, &c.Enabled, &c.LossThreshold, &latency, &c.CreatedAt, &sample)
	if err != nil {
		return c, err
	}
	if source.Valid {
		id, err := uuid.Parse(source.String)
		if err != nil {
			return c, err
		}
		c.SourceServerID = &id
	}
	if latency.Valid {
		c.LatencyThresholdMS = &latency.Float64
	}
	if len(sample) > 0 {
		c.Latest = &NetworkSample{}
		if err = json.Unmarshal(sample, c.Latest); err != nil {
			return c, err
		}
	}
	return c, nil
}

func ListNetworkChecks(ctx context.Context, db *sql.DB, userID *uuid.UUID, enabledOnly bool) ([]NetworkCheck, error) {
	query := `SELECT ` + networkColumns + `,(SELECT sample FROM network_samples WHERE check_id=c.id ORDER BY recorded_at DESC,id DESC LIMIT 1) FROM network_checks c WHERE TRUE`
	args := []any{}
	if userID != nil {
		query += ` AND c.user_id=$1`
		args = append(args, *userID)
	}
	if enabledOnly {
		query += ` AND c.enabled`
	}
	query += ` ORDER BY c.created_at,c.id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NetworkCheck{}
	for rows.Next() {
		c, err := scanNetworkCheck(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func GetNetworkCheck(ctx context.Context, db *sql.DB, id, userID uuid.UUID) (NetworkCheck, error) {
	return scanNetworkCheck(db.QueryRowContext(ctx, `SELECT `+networkColumns+`,(SELECT sample FROM network_samples WHERE check_id=c.id ORDER BY recorded_at DESC,id DESC LIMIT 1) FROM network_checks c WHERE c.id=$1 AND c.user_id=$2`, id, userID))
}

func SaveNetworkCheck(ctx context.Context, db *sql.DB, c *NetworkCheck, create bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize account mutations and enforce ownership in SQL as well as the handler.
	var user uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, c.UserID).Scan(&user); err != nil {
		return err
	}
	var owned bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1 AND user_id=$2) AND ($3::uuid IS NULL OR EXISTS(SELECT 1 FROM servers WHERE id=$3 AND user_id=$2))`, c.TargetServerID, c.UserID, c.SourceServerID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return sql.ErrNoRows
	}
	args := []any{c.ID, c.UserID, c.Name, c.SourceServerID, c.TargetServerID, c.Packets, c.Enabled, c.LossThreshold, c.LatencyThresholdMS}
	if create {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM network_checks WHERE user_id=$1`, c.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return ErrNetworkLimit
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO network_checks(id,user_id,name,source_server_id,target_server_id,packets,enabled,loss_threshold,latency_threshold_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, args...)
	} else {
		var locked uuid.UUID
		if err = tx.QueryRowContext(ctx, `SELECT id FROM network_checks WHERE id=$1 AND user_id=$2 FOR UPDATE`, c.ID, c.UserID).Scan(&locked); err != nil {
			return err
		}
		var changed bool
		if err = tx.QueryRowContext(ctx, `SELECT source_server_id IS DISTINCT FROM $3::uuid OR target_server_id<>$4 OR loss_threshold<>$5
		 OR latency_threshold_ms IS DISTINCT FROM $6::float8 FROM network_checks WHERE id=$1 AND user_id=$2`, c.ID, c.UserID, c.SourceServerID, c.TargetServerID, c.LossThreshold, c.LatencyThresholdMS).Scan(&changed); err != nil {
			return err
		}
		if changed || !c.Enabled {
			if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Network check changed or disabled'
			 WHERE user_id=$1 AND source_key=$2 AND status IN ('pending','retry','sending')`, c.UserID, "network:"+c.ID.String()); err != nil {
				return err
			}
		}
		// Histories from another source/target are not comparable. Remove them in
		// the same transaction as changing that pair.
		if _, err = tx.ExecContext(ctx, `DELETE FROM network_samples WHERE check_id IN (SELECT id FROM network_checks WHERE id=$1 AND user_id=$2 AND (source_server_id IS DISTINCT FROM $3::uuid OR target_server_id<>$4))`, c.ID, c.UserID, c.SourceServerID, c.TargetServerID); err != nil {
			return err
		}
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE network_checks SET name=$3,source_server_id=$4,target_server_id=$5,packets=$6,enabled=$7,loss_threshold=$8,latency_threshold_ms=$9 WHERE id=$1 AND user_id=$2`, args...)
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				return sql.ErrNoRows
			}
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func DeleteNetworkCheck(ctx context.Context, db *sql.DB, id, userID uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM network_checks WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&found); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Network check deleted'
	 WHERE user_id=$1 AND source_key=$2 AND status IN ('pending','retry','sending')`, userID, "network:"+id.String()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM network_checks WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func InsertNetworkSample(ctx context.Context, db *sql.DB, check NetworkCheck, sample NetworkSample) error {
	return insertNetworkSample(ctx, db, check, sample)
}

type networkExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertNetworkSample(ctx context.Context, db networkExecutor, check NetworkCheck, sample NetworkSample) error {
	body, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	// A probe finishing after a configuration edit must not contaminate the new pair.
	result, err := db.ExecContext(ctx, `INSERT INTO network_samples(check_id,recorded_at,sample)
	 SELECT id,$2,$3::jsonb FROM network_checks WHERE id=$1 AND user_id=$4
	 AND source_server_id IS NOT DISTINCT FROM $5::uuid AND target_server_id=$6 AND packets=$7
	 AND loss_threshold=$8 AND latency_threshold_ms IS NOT DISTINCT FROM $9::float8 AND enabled
	 AND EXISTS(SELECT 1 FROM servers target WHERE target.id=$6 AND target.user_id=$4 AND target.monitoring_enabled)
	 AND ($5::uuid IS NULL OR EXISTS(SELECT 1 FROM servers source WHERE source.id=$5 AND source.user_id=$4 AND source.monitoring_enabled))
	 FOR SHARE`, check.ID, sample.RecordedAt, string(body), check.UserID, check.SourceServerID, check.TargetServerID, check.Packets, check.LossThreshold, check.LatencyThresholdMS)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// NetworkSampleTransition carries the last known threshold state across an
// unknown probe, so a missing measurement is neither recovery nor packet loss.
func NetworkSampleTransition(check NetworkCheck, sample *NetworkSample) string {
	sample.ThresholdExceeded = sample.LossPercent != nil && *sample.LossPercent > check.LossThreshold || sample.AvgMS != nil && check.LatencyThresholdMS != nil && *sample.AvgMS > *check.LatencyThresholdMS
	if sample.Sent == nil || (!sample.ThresholdExceeded && (sample.LossPercent == nil || check.LatencyThresholdMS != nil && sample.AvgMS == nil)) {
		if check.Latest != nil {
			sample.ThresholdExceeded = check.Latest.ThresholdExceeded
		}
		return ""
	}
	if check.Latest == nil && !sample.ThresholdExceeded || check.Latest != nil && check.Latest.ThresholdExceeded == sample.ThresholdExceeded {
		return ""
	}
	if sample.ThresholdExceeded {
		return "firing"
	}
	return "resolved"
}

// The sample and its transition delivery commit together. Serializing on the
// check also lets multiple workers recompute against the latest committed state
// instead of sending the same transition from two stale baseline snapshots.
func InsertNetworkSampleWithNotification(ctx context.Context, db *DB, check NetworkCheck, sample NetworkSample, payload json.RawMessage) (NetworkSample, error) {
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return sample, err
	}
	defer tx.Rollback()
	var locked uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM network_checks WHERE id=$1 AND user_id=$2 FOR UPDATE`, check.ID, check.UserID).Scan(&locked); err != nil {
		return sample, err
	}
	var latest []byte
	err = tx.QueryRowContext(ctx, `SELECT sample FROM network_samples WHERE check_id=$1 ORDER BY recorded_at DESC,id DESC LIMIT 1`, check.ID).Scan(&latest)
	if err != nil && err != sql.ErrNoRows {
		return sample, err
	}
	check.Latest = nil
	if len(latest) > 0 {
		check.Latest = &NetworkSample{}
		if err = json.Unmarshal(latest, check.Latest); err != nil {
			return sample, err
		}
		if !sample.RecordedAt.After(check.Latest.RecordedAt) {
			return sample, sql.ErrNoRows
		}
	}
	transition := NetworkSampleTransition(check, &sample)
	if err = insertNetworkSample(ctx, tx, check, sample); err != nil {
		return sample, err
	}
	if transition != "" {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(payload, &fields); err != nil || fields == nil {
			return sample, errors.New("invalid network notification payload")
		}
		fields["status"], _ = json.Marshal(transition)
		fields["source_key"], _ = json.Marshal("network:" + check.ID.String())
		fields["sample"], _ = json.Marshal(sample)
		fields["message"], _ = json.Marshal(networkNotificationMessage(check, sample, transition))
		encoded, encodeErr := json.Marshal(fields)
		if encodeErr != nil {
			return sample, encodeErr
		}
		if _, err = EnqueueUserNotificationTx(ctx, tx, check.UserID, nil, transition, encoded); err != nil {
			return sample, err
		}
	}
	err = tx.Commit()
	if err == nil && transition != "" {
		signalNotificationWork(db.Raw)
	}
	return sample, err
}

func networkNotificationMessage(check NetworkCheck, sample NetworkSample, transition string) string {
	parts := []string{}
	if sample.LossPercent != nil {
		parts = append(parts, fmt.Sprintf("packet loss %.1f%% (threshold %.1f%%)", *sample.LossPercent, check.LossThreshold))
	}
	if sample.AvgMS != nil {
		detail := fmt.Sprintf("average RTT %.1f ms", *sample.AvgMS)
		if check.LatencyThresholdMS != nil {
			detail += fmt.Sprintf(" (threshold %.1f ms)", *check.LatencyThresholdMS)
		}
		parts = append(parts, detail)
	}
	prefix := "Network threshold exceeded: "
	if transition == "resolved" {
		prefix = "Network recovered: "
	}
	return prefix + strings.Join(parts, "; ")
}

type NetworkSummary struct {
	Sent           int      `json:"sent"`
	Received       int      `json:"received"`
	LossPercent    *float64 `json:"loss_percent"`
	AvgMS          *float64 `json:"avg_ms"`
	KnownSamples   int      `json:"known_samples"`
	UnknownSamples int      `json:"unknown_samples"`
}

func NetworkHistory(ctx context.Context, db *sql.DB, id, user uuid.UUID, since, until time.Time, limit int) ([]NetworkSample, NetworkSummary, error) {
	result := []NetworkSample{}
	var summary NetworkSummary
	rows, err := db.QueryContext(ctx, `SELECT s.sample FROM network_samples s JOIN network_checks c ON c.id=s.check_id WHERE c.id=$1 AND c.user_id=$2 AND s.recorded_at >= $3 AND s.recorded_at <= $4 ORDER BY s.recorded_at DESC,s.id DESC LIMIT $5`, id, user, since, until, limit)
	if err != nil {
		return nil, summary, err
	}
	for rows.Next() {
		var body []byte
		var sample NetworkSample
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return nil, summary, err
		}
		if err = json.Unmarshal(body, &sample); err != nil {
			rows.Close()
			return nil, summary, err
		}
		result = append(result, sample)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, summary, err
	}
	var avg sql.NullFloat64
	err = db.QueryRowContext(ctx, `SELECT COALESCE(sum((sample->>'sent')::int),0),COALESCE(sum((sample->>'received')::int),0),count(*) FILTER(WHERE sample->>'sent' IS NOT NULL),count(*) FILTER(WHERE sample->>'sent' IS NULL),sum((sample->>'avg_ms')::float8*(sample->>'received')::int)/NULLIF(sum((sample->>'received')::int) FILTER(WHERE sample->>'avg_ms' IS NOT NULL),0) FROM network_samples s JOIN network_checks c ON c.id=s.check_id WHERE c.id=$1 AND c.user_id=$2 AND s.recorded_at >= $3 AND s.recorded_at <= $4`, id, user, since, until).Scan(&summary.Sent, &summary.Received, &summary.KnownSamples, &summary.UnknownSamples, &avg)
	if summary.Sent > 0 {
		loss := 100 * float64(summary.Sent-summary.Received) / float64(summary.Sent)
		summary.LossPercent = &loss
	}
	if avg.Valid {
		summary.AvgMS = &avg.Float64
	}
	return result, summary, err
}
