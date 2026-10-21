package models

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

const NetworkTrendMaxPoints = 500

var ErrNetworkCursor = errors.New("invalid network history cursor")

type NetworkTrendBucket struct {
	RecordedAt time.Time `json:"recorded_at"`
	Until      time.Time `json:"until"`
	Status     string    `json:"status"`
	NetworkSummary
}

type NetworkTrend struct {
	Buckets       []NetworkTrendBucket `json:"buckets"`
	Summary       NetworkSummary       `json:"summary"`
	Since         time.Time            `json:"since"`
	Until         time.Time            `json:"until"`
	BucketSeconds int                  `json:"bucket_seconds"`
}

// NetworkHistoryVersion validates current ownership on every request, even for
// cached results. Row xmin changes on configuration edits; the latest sample ID
// changes on collection and after a source/target edit deletes its history.
func NetworkHistoryVersion(ctx context.Context, db *sql.DB, id, user uuid.UUID) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, `SELECT c.xmin::text || ':' || COALESCE((SELECT s.id::text FROM network_samples s WHERE s.check_id=c.id ORDER BY s.recorded_at DESC,s.id DESC LIMIT 1),'0') FROM network_checks c WHERE c.id=$1 AND c.user_id=$2`, id, user).Scan(&version)
	return version, err
}

// NetworkTrendHistory covers the complete requested window. Empty buckets are
// explicit gaps, and outages/unknown probes and latency peaks survive bucketing.
// The average is weighted by received packets, as in the raw history summary.
func NetworkTrendHistory(ctx context.Context, db *sql.DB, id, user uuid.UUID, since, until time.Time) (NetworkTrend, error) {
	result := NetworkTrend{Since: since, Until: until}
	span := until.Sub(since)
	if span <= 0 || span > 7*24*time.Hour {
		return result, errors.New("network history must span at most seven days")
	}
	// Scheduled probes run once per minute. Finer empty buckets would split
	// every normal adjacent measurement into isolated points on short windows.
	result.BucketSeconds = max(60, int(math.Ceil(span.Seconds()/NetworkTrendMaxPoints)))
	width := time.Duration(result.BucketSeconds) * time.Second
	count := int(math.Ceil(float64(span) / float64(width)))
	result.Buckets = make([]NetworkTrendBucket, count)
	for i := range result.Buckets {
		start := since.Add(time.Duration(i) * width)
		end := start.Add(width)
		if end.After(until) {
			end = until
		}
		result.Buckets[i] = NetworkTrendBucket{RecordedAt: start, Until: end, Status: "none"}
	}
	rows, err := db.QueryContext(ctx, `WITH samples AS (
	 SELECT LEAST($6::int-1,FLOOR(EXTRACT(EPOCH FROM (s.recorded_at-$3::timestamptz))/$5::int)::int) AS bucket,
	 (sample->>'sent')::int sent,(sample->>'received')::int received,
	 (sample->>'avg_ms')::float8 avg_ms,(sample->>'min_ms')::float8 min_ms,(sample->>'max_ms')::float8 max_ms,
	 sample->>'status' status,COALESCE((sample->>'threshold_exceeded')::bool,FALSE) exceeded
	 FROM network_samples s JOIN network_checks c ON c.id=s.check_id
	 WHERE c.id=$1 AND c.user_id=$2 AND s.recorded_at >= $3 AND s.recorded_at <= $4
	) SELECT bucket,COALESCE(sum(sent),0),COALESCE(sum(received),0),
	 count(*) FILTER(WHERE sent IS NOT NULL),count(*) FILTER(WHERE sent IS NULL),
	 count(*) FILTER(WHERE status='loss'),count(*) FILTER(WHERE status='down'),
	 count(*) FILTER(WHERE exceeded AND sent IS NOT NULL),
	 sum(avg_ms*received) FILTER(WHERE received>0),
	 COALESCE(sum(received) FILTER(WHERE avg_ms IS NOT NULL AND received>0),0),
	 min(COALESCE(min_ms,avg_ms)),max(COALESCE(max_ms,avg_ms))
	 FROM samples GROUP BY bucket ORDER BY bucket`, id, user, since, until, result.BucketSeconds, count)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var totalLatency float64
	var totalLatencyPackets int
	for rows.Next() {
		var index, latencyPackets int
		var summary NetworkSummary
		var latency, minimum, maximum sql.NullFloat64
		if err := rows.Scan(&index, &summary.Sent, &summary.Received, &summary.KnownSamples, &summary.UnknownSamples, &summary.LossSamples, &summary.DownSamples, &summary.ThresholdExceededSamples, &latency, &latencyPackets, &minimum, &maximum); err != nil {
			return result, err
		}
		if index < 0 || index >= count {
			return result, errors.New("network trend bucket outside requested range")
		}
		summary.SampleCount = summary.KnownSamples + summary.UnknownSamples
		if latency.Valid && latencyPackets > 0 {
			value := latency.Float64 / float64(latencyPackets)
			summary.AvgMS = &value
			totalLatency += latency.Float64
			totalLatencyPackets += latencyPackets
		}
		if minimum.Valid {
			summary.MinMS = &minimum.Float64
		}
		if maximum.Valid {
			summary.MaxMS = &maximum.Float64
		}
		setNetworkLoss(&summary)
		bucket := &result.Buckets[index]
		bucket.NetworkSummary = summary
		switch {
		case summary.DownSamples > 0:
			bucket.Status = "down"
		case summary.LossSamples > 0:
			bucket.Status = "loss"
		case summary.UnknownSamples > 0:
			bucket.Status = "error"
		default:
			bucket.Status = "ok"
		}
		total := &result.Summary
		total.Sent += summary.Sent
		total.Received += summary.Received
		total.KnownSamples += summary.KnownSamples
		total.UnknownSamples += summary.UnknownSamples
		total.LossSamples += summary.LossSamples
		total.DownSamples += summary.DownSamples
		total.ThresholdExceededSamples += summary.ThresholdExceededSamples
		if summary.MinMS != nil && (total.MinMS == nil || *summary.MinMS < *total.MinMS) {
			total.MinMS = summary.MinMS
		}
		if summary.MaxMS != nil && (total.MaxMS == nil || *summary.MaxMS > *total.MaxMS) {
			total.MaxMS = summary.MaxMS
		}
	}
	result.Summary.SampleCount = result.Summary.KnownSamples + result.Summary.UnknownSamples
	setNetworkLoss(&result.Summary)
	if totalLatencyPackets > 0 {
		avg := totalLatency / float64(totalLatencyPackets)
		result.Summary.AvgMS = &avg
	}
	return result, rows.Err()
}

func setNetworkLoss(summary *NetworkSummary) {
	if summary.Sent > 0 {
		loss := 100 * float64(summary.Sent-summary.Received) / float64(summary.Sent)
		summary.LossPercent = &loss
	}
}

type networkHistoryCursor struct {
	CheckID    uuid.UUID `json:"c"`
	UserID     uuid.UUID `json:"u"`
	Since      time.Time `json:"s"`
	Until      time.Time `json:"e"`
	RecordedAt time.Time `json:"t"`
	ID         int64     `json:"i"`
}

// Cursor values are navigation state, not authorization: the SQL independently
// enforces ownership and time bounds. Binding navigation to its original query
// rejects accidental reuse across checks, owners or windows.
func parseNetworkHistoryCursor(value string, id, user uuid.UUID, since, until time.Time) (*networkHistoryCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 1024 {
		return nil, ErrNetworkCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrNetworkCursor
	}
	var cursor networkHistoryCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.CheckID != id || cursor.UserID != user || !cursor.Since.Equal(since) || !cursor.Until.Equal(until) || cursor.ID <= 0 || cursor.RecordedAt.Before(since) || cursor.RecordedAt.After(until) {
		return nil, ErrNetworkCursor
	}
	return &cursor, nil
}

func ValidateNetworkHistoryCursor(value string, id, user uuid.UUID, since, until time.Time) error {
	_, err := parseNetworkHistoryCursor(value, id, user, since, until)
	return err
}

// NetworkHistoryPage uses the stable (recorded_at,id) order, so equal timestamp
// samples and concurrently appended newer samples cannot skip or repeat rows.
func NetworkHistoryPage(ctx context.Context, db *sql.DB, id, user uuid.UUID, since, until time.Time, limit int, value string) ([]NetworkSample, *string, error) {
	if limit < 1 || limit > 2000 || !since.Before(until) || until.Sub(since) > 7*24*time.Hour {
		return nil, nil, errors.New("invalid network history range or limit")
	}
	cursor, err := parseNetworkHistoryCursor(value, id, user, since, until)
	if err != nil {
		return nil, nil, err
	}
	query := `SELECT s.id,s.recorded_at,s.sample FROM network_samples s JOIN network_checks c ON c.id=s.check_id WHERE c.id=$1 AND c.user_id=$2 AND s.recorded_at >= $3 AND s.recorded_at <= $4`
	args := []any{id, user, since, until, limit + 1}
	if cursor != nil {
		query += ` AND (s.recorded_at,s.id)<($6::timestamptz,$7::bigint)`
		args = append(args, cursor.RecordedAt, cursor.ID)
	}
	query += ` ORDER BY s.recorded_at DESC,s.id DESC LIMIT $5`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	result := []NetworkSample{}
	var last networkHistoryCursor
	var next *string
	for rows.Next() {
		var sampleID int64
		var at time.Time
		var body []byte
		if err := rows.Scan(&sampleID, &at, &body); err != nil {
			return nil, nil, err
		}
		if len(result) == limit {
			encoded, err := json.Marshal(last)
			if err != nil {
				return nil, nil, err
			}
			token := base64.RawURLEncoding.EncodeToString(encoded)
			next = &token
			break
		}
		var sample NetworkSample
		if err := json.Unmarshal(body, &sample); err != nil {
			return nil, nil, fmt.Errorf("decode network sample: %w", err)
		}
		sample.RecordedAt = at
		result = append(result, sample)
		last = networkHistoryCursor{CheckID: id, UserID: user, Since: since, Until: until, RecordedAt: at, ID: sampleID}
	}
	return result, next, rows.Err()
}
