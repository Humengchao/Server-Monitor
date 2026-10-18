package models

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

const (
	ExpiryGracePeriod      = 10 * 24 * time.Hour
	RenewalWindow          = 24 * time.Hour
	RenewalExpectedMinutes = 1440
	// Compare coverage before rounding: 1425/1440 is below 99%.
	RenewalRequiredMinutes = 1426
)

type ExpiryRenewalCandidate struct {
	ServerID  uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
}

// A complete window is required. The display-only availability API's partial
// window adjustment must never make a newly added host eligible for renewal.
func GetExpiryRenewalCandidates(ctx context.Context, db *sql.DB, now, windowEnd time.Time) ([]ExpiryRenewalCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.id,s.user_id,s.expires_at
		FROM servers s JOIN server_latest_metrics m ON m.server_id=s.id
		WHERE s.auto_renew_enabled AND s.monitoring_enabled AND s.expires_at<$1 AND s.created_at<=$2
		AND m.recorded_at>$3 AND m.recorded_at<=$4 AND s.last_error_kind=''
		AND NOT EXISTS(SELECT 1 FROM server_expiry_renewals r WHERE r.server_id=s.id
		 AND (r.previous_expires_at=s.expires_at OR (r.new_expires_at=s.expires_at AND r.renewed_at-r.new_expires_at>INTERVAL '10 days')))
		ORDER BY s.expires_at,s.id`,
		now.Add(-ExpiryGracePeriod), windowEnd.Add(-RenewalWindow), now.Add(-2*time.Minute), now.Add(30*time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ExpiryRenewalCandidate
	for rows.Next() {
		var item ExpiryRenewalCandidate
		if err := rows.Scan(&item.ServerID, &item.UserID, &item.ExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// RenewServerExpiry atomically rechecks eligibility, advances exactly one
// month, and records the decision. The row lock and old-value predicate guard
// concurrent schedulers and manual edits. A very old expiry is advanced only
// once if it remains beyond the grace period, rather than rapidly adding
// months each tick. A date brought back inside the grace period can become
// eligible again when that new grace period has actually elapsed.
func RenewServerExpiry(ctx context.Context, db *sql.DB, candidate ExpiryRenewalCandidate, next, now, windowEnd time.Time, zone string) (bool, error) {
	if !next.After(candidate.ExpiresAt) {
		return false, nil
	}
	var id uuid.UUID
	err := db.QueryRowContext(ctx, `WITH eligible AS (
	 SELECT s.id,s.user_id,s.expires_at,
	  (SELECT COUNT(*) FROM server_metrics_1m h WHERE h.server_id=s.id
	   AND h.recorded_at>=$6 AND h.recorded_at<$7 AND h.sample_count>0) AS observed
	 FROM servers s JOIN server_latest_metrics m ON m.server_id=s.id
	 WHERE s.auto_renew_enabled AND s.monitoring_enabled AND s.id=$1 AND s.user_id=$2 AND s.expires_at=$3 AND s.expires_at<$5
	 AND s.created_at<=$6 AND m.recorded_at>$8 AND m.recorded_at<=$9 AND s.last_error_kind=''
	 AND NOT EXISTS(SELECT 1 FROM server_expiry_renewals r WHERE r.server_id=s.id
	  AND (r.previous_expires_at=s.expires_at OR (r.new_expires_at=s.expires_at AND r.renewed_at-r.new_expires_at>INTERVAL '10 days')))
	 FOR UPDATE OF s SKIP LOCKED
	), updated AS (
	 UPDATE servers s SET expires_at=$4 FROM eligible e
	 WHERE s.id=e.id AND s.expires_at=e.expires_at AND e.observed>=$10
	 RETURNING s.id,s.user_id,e.observed
	)
	INSERT INTO server_expiry_renewals(server_id,user_id,previous_expires_at,new_expires_at,renewed_at,
	 window_start,window_end,observed_minutes,expected_minutes,calendar_timezone)
	SELECT id,user_id,$3,$4,$11,$6,$7,observed,$12,$13 FROM updated RETURNING server_id`,
		candidate.ServerID, candidate.UserID, candidate.ExpiresAt, next, now.Add(-ExpiryGracePeriod),
		windowEnd.Add(-RenewalWindow), windowEnd, now.Add(-2*time.Minute), now.Add(30*time.Second),
		RenewalRequiredMinutes, now, RenewalExpectedMinutes, zone).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
