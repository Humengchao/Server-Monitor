package services

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestNextMonthlyExpiryClampsLocalCalendarMonth(t *testing.T) {
	for _, test := range []struct{ zone, from, want string }{
		{"Asia/Shanghai", "2030-01-31T00:00:00+08:00", "2030-02-28T00:00:00+08:00"},
		{"Asia/Shanghai", "2024-01-31T23:45:00+08:00", "2024-02-29T23:45:00+08:00"},
		{"Asia/Shanghai", "2030-12-31T18:20:15+08:00", "2031-01-31T18:20:15+08:00"},
		{"Asia/Shanghai", "2030-08-31T03:00:00+08:00", "2030-09-30T03:00:00+08:00"},
		{"UTC", "2030-02-28T12:00:00Z", "2030-03-28T12:00:00Z"},
		{"America/New_York", "2030-02-15T12:00:00-05:00", "2030-03-15T12:00:00-04:00"},
	} {
		t.Run(test.from, func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			from, _ := time.Parse(time.RFC3339, test.from)
			want, _ := time.Parse(time.RFC3339, test.want)
			if got := NextMonthlyExpiry(from, location); !got.Equal(want) {
				t.Fatalf("got %s want %s", got, want)
			}
		})
	}
}

func seedRenewal(t *testing.T, db *sql.DB, now time.Time, minutes int) models.ExpiryRenewalCandidate {
	t.Helper()
	user, id, _ := testdb.Seed(t, db)
	old := now.Add(-11 * 24 * time.Hour)
	if _, err := db.Exec(`UPDATE servers SET expires_at=$1,created_at=$2,notes='keep',billing_price=12,billing_cycle='year' WHERE id=$3`, old, now.Add(-60*24*time.Hour), id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO server_latest_metrics(server_id,recorded_at) VALUES($1,$2)`, id, now); err != nil {
		t.Fatal(err)
	}
	seedRenewalHistory(t, db, id.String(), now, minutes)
	return models.ExpiryRenewalCandidate{ServerID: id, UserID: user, ExpiresAt: old}
}

func seedRenewalHistory(t *testing.T, db *sql.DB, id string, now time.Time, minutes int) {
	t.Helper()
	start := TierEnd(now, 60).Add(-24 * time.Hour)
	if _, err := db.Exec(`INSERT INTO server_metrics_1m(server_id,recorded_at,sample_count)
	 SELECT $1,$2::timestamptz+n*INTERVAL '1 minute',1 FROM generate_series(0,$3::int-1)n`, id, start, minutes); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticExpiryEligibilityIntegration(t *testing.T) {
	db := testdb.Open(t)
	now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		minutes int
		mutate  string
		want    bool
	}{
		{"complete healthy window", 1440, "", true},
		{"at least 99 percent", 1426, "", true},
		{"below 99 percent", 1425, "", false},
		{"missing history", 1, "", false},
		{"no expiry", 1440, `UPDATE servers SET expires_at=NULL WHERE id=$1`, false},
		{"exactly ten days", 1440, `UPDATE servers SET expires_at=$2::timestamptz-INTERVAL '10 days' WHERE id=$1`, false},
		{"future expiry", 1440, `UPDATE servers SET expires_at=$2::timestamptz+INTERVAL '1 day' WHERE id=$1`, false},
		{"newly added host", 1440, `UPDATE servers SET created_at=$2::timestamptz-INTERVAL '23 hours' WHERE id=$1`, false},
		{"offline now", 1440, `UPDATE server_latest_metrics SET recorded_at=$2::timestamptz-INTERVAL '2 minutes' WHERE server_id=$1`, false},
		{"future sample", 1440, `UPDATE server_latest_metrics SET recorded_at=$2::timestamptz+INTERVAL '31 seconds' WHERE server_id=$1`, false},
		{"known poll failure", 1440, `UPDATE servers SET last_error_kind='auth' WHERE id=$1`, false},
		{"empty rollup buckets", 1440, `UPDATE server_metrics_1m SET sample_count=0 WHERE server_id=$1`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := seedRenewal(t, db, now, tc.minutes)
			if tc.mutate != "" {
				args := []any{candidate.ServerID}
				// Mutations without a clock parameter remain valid prepared statements.
				if tc.name != "no expiry" && tc.name != "known poll failure" && tc.name != "empty rollup buckets" {
					args = append(args, now)
				}
				if _, err := db.Exec(tc.mutate, args...); err != nil {
					t.Fatal(err)
				}
				var expiry sql.NullTime
				if err := db.QueryRow(`SELECT expires_at FROM servers WHERE id=$1`, candidate.ServerID).Scan(&expiry); err != nil {
					t.Fatal(err)
				}
				if expiry.Valid {
					candidate.ExpiresAt = expiry.Time
				}
			}
			changed, err := models.RenewServerExpiry(context.Background(), db, candidate, NextMonthlyExpiry(candidate.ExpiresAt, time.UTC), now, TierEnd(now, 60), "UTC")
			if err != nil || changed != tc.want {
				t.Fatalf("renewed=%v want=%v err=%v", changed, tc.want, err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM server_expiry_renewals WHERE server_id=$1`, candidate.ServerID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if (count == 1) != tc.want {
				t.Fatalf("audit count=%d", count)
			}
		})
	}
}

func TestExpiryRenewalConcurrentSchedulersAndStaleEditor(t *testing.T) {
	db := testdb.Open(t)
	now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
	candidate := seedRenewal(t, db, now, 1440)
	next := NextMonthlyExpiry(candidate.ExpiresAt, time.UTC)
	var wg sync.WaitGroup
	var changes atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			changed, err := models.RenewServerExpiry(context.Background(), db, candidate, next, now, TierEnd(now, 60), "UTC")
			if err != nil {
				t.Error(err)
			}
			if changed {
				changes.Add(1)
			}
		}()
	}
	wg.Wait()
	if changes.Load() != 1 {
		t.Fatalf("got %d renewals", changes.Load())
	}
	server := &models.Server{ID: candidate.ServerID, UserID: candidate.UserID, Name: "stale edit", Host: "192.0.2.1", SSHUsername: "root", ServerType: "linux", ExpiresAt: &candidate.ExpiresAt}
	wrapper := &models.DB{Raw: db}
	if err := models.UpdateServerWithExpiryExpectation(context.Background(), wrapper, server, &models.ExpiryExpectation{ExpiresAt: &candidate.ExpiresAt}); !errors.Is(err, models.ErrServerExpiryChanged) {
		t.Fatalf("stale editor overwrote renewal: %v", err)
	}
	var saved time.Time
	var name, notes, cycle string
	var price float64
	if err := db.QueryRow(`SELECT expires_at,name,notes,billing_price,billing_cycle FROM servers WHERE id=$1`, candidate.ServerID).Scan(&saved, &name, &notes, &price, &cycle); err != nil {
		t.Fatal(err)
	}
	if !saved.Equal(next) || name != "test" || notes != "keep" || price != 12 || cycle != "year" {
		t.Fatal("renewal or conflicting edit changed unrelated metadata")
	}
	manual := next.AddDate(0, 0, 12)
	server.ExpiresAt = &manual
	if err := models.UpdateServerWithExpiryExpectation(context.Background(), wrapper, server, &models.ExpiryExpectation{ExpiresAt: &next}); err != nil {
		t.Fatal(err)
	}
	if changed, err := models.RenewServerExpiry(context.Background(), db, candidate, next, now, TierEnd(now, 60), "UTC"); err != nil || changed {
		t.Fatalf("stale renewal replaced manual date: %v %v", changed, err)
	}
}

func TestExpiryRenewalDoesNotCatchUpManyMonthsOnRepeatedRuns(t *testing.T) {
	db := testdb.Open(t)
	now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
	candidate := seedRenewal(t, db, now, 1440)
	old := now.AddDate(0, -5, 0)
	if _, err := db.Exec(`UPDATE servers SET expires_at=$1 WHERE id=$2`, old, candidate.ServerID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := RenewExpiredServers(context.Background(), db, time.UTC, now); err != nil {
			t.Fatal(err)
		}
	}
	var saved time.Time
	var count int
	if err := db.QueryRow(`SELECT expires_at,(SELECT COUNT(*) FROM server_expiry_renewals r WHERE r.server_id=s.id) FROM servers s WHERE id=$1`, candidate.ServerID).Scan(&saved, &count); err != nil {
		t.Fatal(err)
	}
	if !saved.Equal(NextMonthlyExpiry(old, time.UTC)) || count != 1 {
		t.Fatalf("rapid repeat: expiry=%s count=%d", saved, count)
	}
}

func TestExpiryRenewalAndAuditRollBackTogether(t *testing.T) {
	db := testdb.Open(t)
	now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
	candidate := seedRenewal(t, db, now, 1440)
	if _, err := db.Exec(`ALTER TABLE server_expiry_renewals ADD CONSTRAINT reject_audit CHECK(observed_minutes<0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.RenewServerExpiry(context.Background(), db, candidate, NextMonthlyExpiry(candidate.ExpiresAt, time.UTC), now, TierEnd(now, 60), "UTC"); err == nil {
		t.Fatal("audit failure was ignored")
	}
	var saved time.Time
	if err := db.QueryRow(`SELECT expires_at FROM servers WHERE id=$1`, candidate.ServerID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Equal(candidate.ExpiresAt) {
		t.Fatal("expiry changed despite failed audit transaction")
	}
}

func TestExpiryRenewalCanRepeatInTheNextMonthlyCycle(t *testing.T) {
	db := testdb.Open(t)
	now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
	candidate := seedRenewal(t, db, now, 1440)
	if err := RenewExpiredServers(context.Background(), db, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	next := NextMonthlyExpiry(candidate.ExpiresAt, time.UTC)
	later := next.Add(11 * 24 * time.Hour)
	if _, err := db.Exec(`UPDATE server_latest_metrics SET recorded_at=$1 WHERE server_id=$2`, later, candidate.ServerID); err != nil {
		t.Fatal(err)
	}
	seedRenewalHistory(t, db, candidate.ServerID.String(), later, 1440)
	if err := RenewExpiredServers(context.Background(), db, time.UTC, later); err != nil {
		t.Fatal(err)
	}
	var saved time.Time
	var count int
	if err := db.QueryRow(`SELECT expires_at,(SELECT COUNT(*) FROM server_expiry_renewals r WHERE r.server_id=s.id) FROM servers s WHERE id=$1`, candidate.ServerID).Scan(&saved, &count); err != nil {
		t.Fatal(err)
	}
	if !saved.Equal(NextMonthlyExpiry(next, time.UTC)) || count != 2 {
		t.Fatalf("next cycle blocked: expiry=%s count=%d", saved, count)
	}
}

func TestExpiryRenewalInsideGraceCanBecomeEligibleAgain(t *testing.T) {
	for _, pastDays := range []int{0, 1, 10} {
		t.Run((time.Duration(pastDays) * 24 * time.Hour).String(), func(t *testing.T) {
			db := testdb.Open(t)
			now := time.Date(2030, 6, 15, 10, 5, 0, 0, time.UTC)
			candidate := seedRenewal(t, db, now, 1440)
			firstExpiry := now.Add(-time.Duration(pastDays) * 24 * time.Hour)
			old := firstExpiry.AddDate(0, -1, 0)
			if _, err := db.Exec(`UPDATE servers SET expires_at=$1 WHERE id=$2`, old, candidate.ServerID); err != nil {
				t.Fatal(err)
			}
			if err := RenewExpiredServers(context.Background(), db, time.UTC, now); err != nil {
				t.Fatal(err)
			}
			if err := RenewExpiredServers(context.Background(), db, time.UTC, now); err != nil {
				t.Fatal(err)
			}
			later := firstExpiry.Add(11 * 24 * time.Hour)
			if _, err := db.Exec(`UPDATE server_latest_metrics SET recorded_at=$1 WHERE server_id=$2`, later, candidate.ServerID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM server_metrics_1m WHERE server_id=$1`, candidate.ServerID); err != nil {
				t.Fatal(err)
			}
			seedRenewalHistory(t, db, candidate.ServerID.String(), later, 1440)
			if err := RenewExpiredServers(context.Background(), db, time.UTC, later); err != nil {
				t.Fatal(err)
			}
			var saved time.Time
			var count int
			if err := db.QueryRow(`SELECT expires_at,(SELECT COUNT(*) FROM server_expiry_renewals r WHERE r.server_id=s.id) FROM servers s WHERE id=$1`, candidate.ServerID).Scan(&saved, &count); err != nil {
				t.Fatal(err)
			}
			if !saved.Equal(NextMonthlyExpiry(firstExpiry, time.UTC)) || count != 2 {
				t.Fatalf("new grace period blocked: expiry=%s count=%d", saved, count)
			}
		})
	}
}
