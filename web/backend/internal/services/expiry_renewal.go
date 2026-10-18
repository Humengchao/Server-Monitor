package services

import (
	"context"
	"database/sql"
	"log"
	"time"

	"server-monitor/internal/models"
)

// NextMonthlyExpiry preserves the local calendar day and time, clamping to
// the last day of a shorter month (January 31 -> February 28/29).
func NextMonthlyExpiry(previous time.Time, location *time.Location) time.Time {
	local := previous.In(location)
	year, month, day := local.Date()
	lastDay := time.Date(year, month+2, 0, 12, 0, 0, 0, location).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month+1, day, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), location).UTC()
}

type ExpiryRenewer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Renewal is a backend job, independent of clients or the public status page.
func StartExpiryRenewer(db *sql.DB, location *time.Location) *ExpiryRenewer {
	ctx, cancel := context.WithCancel(context.Background())
	r := &ExpiryRenewer{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		// Allow the collector's initial rollup and live samples to catch up.
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				run, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := RenewExpiredServers(run, db, location, time.Now()); err != nil && ctx.Err() == nil {
					log.Printf("expiry renewal: %v", err)
				}
				cancel()
				timer.Reset(10 * time.Minute)
			}
		}
	}()
	return r
}

func (r *ExpiryRenewer) Stop() { r.cancel(); <-r.done }

func RenewExpiredServers(ctx context.Context, db *sql.DB, location *time.Location, now time.Time) error {
	end := TierEnd(now, 60)
	candidates, err := models.GetExpiryRenewalCandidates(ctx, db, now, end)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		next := NextMonthlyExpiry(candidate.ExpiresAt, location)
		changed, err := models.RenewServerExpiry(ctx, db, candidate, next, now, end, location.String())
		if err != nil {
			return err
		}
		if changed {
			log.Printf("expiry renewal: server=%s previous=%s next=%s", candidate.ServerID, candidate.ExpiresAt.Format(time.RFC3339), next.Format(time.RFC3339))
		}
	}
	return nil
}
