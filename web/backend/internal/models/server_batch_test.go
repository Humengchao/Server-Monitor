package models

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func batchServerRow(id, userID uuid.UUID) []driver.Value {
	return []driver.Value{id.String(), userID.String(), "selected", "host.invalid", int64(22), "root", "", "",
		"pinned-host-key", nil, time.Unix(1000, 0), "linux", time.Unix(2000, 0), "notes",
		float64(12), "USD", "month", int64(1234), "location", nil, "", "", "ssh", true, true, true}
}

func batchServerFixture(rows ...[]driver.Value) *collectorConnector {
	return &collectorConnector{rows: rows, columns: []string{
		"id", "user_id", "name", "host", "port", "ssh_username", "ssh_password", "ssh_key", "ssh_host_key",
		"credential_id", "created_at", "server_type", "expires_at", "notes", "billing_price", "billing_currency",
		"billing_cycle", "traffic_limit_bytes", "public_location", "credential_username", "credential_password", "credential_key", "connection_mode", "is_public", "monitoring_enabled", "auto_renew_enabled",
	}}
}

func batchEncrypt(t *testing.T, value string) string {
	t.Helper()
	encoded, err := crypto.Encrypt(value, collectorTestKey)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestGetServersByIDsAndUserResolvesOneQueryInInputOrder(t *testing.T) {
	userID, inlineID, linkedID, missingID, foreignID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	inline := batchServerRow(inlineID, userID)
	inline[6], inline[7] = batchEncrypt(t, "inline-password"), batchEncrypt(t, "inline-key")
	linked := batchServerRow(linkedID, userID)
	credentialID := uuid.New()
	linked[9], linked[19], linked[20], linked[21] = credentialID.String(), "deploy", batchEncrypt(t, "shared-password"), batchEncrypt(t, "shared-key")
	// Only the linked credential is effective. Old inline ciphertext must not
	// prevent an otherwise usable target from participating in the batch.
	linked[6], linked[7] = "obsolete-invalid-ciphertext", "obsolete-invalid-ciphertext"
	fixture := batchServerFixture(inline, linked)
	ids := []uuid.UUID{linkedID, missingID, inlineID, foreignID, linkedID}
	servers, err := GetServersByIDsAndUser(collectorDatabase(t, fixture), ids, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 || servers[0].ID != linkedID || servers[1].ID != inlineID || servers[2].ID != linkedID {
		t.Fatalf("expected caller order, duplicates and omission of unowned IDs, got %#v", servers)
	}
	if servers[0] == servers[2] {
		t.Fatal("duplicate targets should remain independent server values")
	}
	if s := servers[0]; s.SSHUsername != "deploy" || s.SSHPassword != "shared-password" || s.SSHKey != "shared-key" ||
		s.CredentialID == nil || *s.CredentialID != credentialID || s.SSHHostKey != "pinned-host-key" {
		t.Fatal("linked credentials or pinned host key were not preserved")
	}
	if s := servers[1]; s.SSHUsername != "root" || s.SSHPassword != "inline-password" || s.SSHKey != "inline-key" || s.CredentialID != nil {
		t.Fatal("inline credentials changed")
	}
	if s := servers[0]; !s.CreatedAt.Equal(time.Unix(1000, 0)) || s.ExpiresAt == nil || !s.ExpiresAt.Equal(time.Unix(2000, 0)) ||
		s.Notes != "notes" || s.BillingPrice != 12 || s.BillingCurrency != "USD" || s.BillingCycle != "month" || s.TrafficLimit != 1234 || s.PublicLocation != "location" {
		t.Fatal("server metadata was not preserved")
	}
	if len(fixture.queries) != 1 {
		t.Fatalf("got %d queries, want one for all targets and credentials", len(fixture.queries))
	}
	query := strings.Join(strings.Fields(fixture.queries[0]), " ")
	if !strings.Contains(query, "LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id") ||
		!strings.Contains(query, "WHERE s.id = ANY($1) AND s.user_id = $2") {
		t.Fatal("both server selection and credential resolution must be owner-scoped")
	}
	args := fixture.args[0]
	if len(args) != 2 || args[1].Value != userID.String() {
		t.Fatalf("unexpected owner query argument: %#v", args)
	}
	var selected pq.StringArray
	if err := selected.Scan(args[0].Value); err != nil {
		t.Fatal(err)
	}
	if len(selected) != len(ids) {
		t.Fatalf("selected %d IDs, want %d", len(selected), len(ids))
	}
	for i, id := range ids {
		if selected[i] != id.String() {
			t.Fatalf("selection[%d] = %q, want %s", i, selected[i], id)
		}
	}
	if !fixture.closed {
		t.Fatal("server rows were not released")
	}
}

func TestGetServersByIDsAndUserEmptySelection(t *testing.T) {
	servers, err := GetServersByIDsAndUser(nil, nil, uuid.New())
	if err != nil || len(servers) != 0 {
		t.Fatalf("empty selection = (%v, %v)", servers, err)
	}
}

func TestGetServersByIDsAndUserRejectsUnresolvedCredential(t *testing.T) {
	userID, firstID, brokenID := uuid.New(), uuid.New(), uuid.New()
	broken := batchServerRow(brokenID, userID)
	broken[9], broken[6] = uuid.NewString(), batchEncrypt(t, "must-not-fall-back")
	fixture := batchServerFixture(batchServerRow(firstID, userID), broken)
	servers, err := GetServersByIDsAndUser(collectorDatabase(t, fixture), []uuid.UUID{firstID, brokenID}, userID)
	if servers != nil || !errors.Is(err, sql.ErrNoRows) || err == sql.ErrNoRows {
		t.Fatalf("unresolved linked credential must fail the whole batch with a wrapped error: (%v, %v)", servers, err)
	}
	if !fixture.closed {
		t.Fatal("failed rows were not released")
	}
}

func TestGetServersByIDsAndUserRejectsCorruptEffectiveSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		linked bool
		column int
	}{
		{"inline password", false, 6}, {"inline key", false, 7},
		{"linked password", true, 20}, {"linked key", true, 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID, id := uuid.New(), uuid.New()
			row := batchServerRow(id, userID)
			if tc.linked {
				row[9], row[19] = uuid.NewString(), "deploy"
			}
			row[tc.column] = "invalid-ciphertext"
			fixture := batchServerFixture(row)
			servers, err := GetServersByIDsAndUser(collectorDatabase(t, fixture), []uuid.UUID{id}, userID)
			if servers != nil || err == nil {
				t.Fatalf("corrupt secret must fail the batch: (%v, %v)", servers, err)
			}
			if !fixture.closed {
				t.Fatal("failed rows were not released")
			}
		})
	}
}

func TestGetServersByIDsAndUserPropagatesRowFailure(t *testing.T) {
	userID, id := uuid.New(), uuid.New()
	failure := errors.New("connection lost while streaming targets")
	fixture := batchServerFixture(batchServerRow(id, userID))
	fixture.rowErr = failure
	servers, err := GetServersByIDsAndUser(collectorDatabase(t, fixture), []uuid.UUID{id}, userID)
	if servers != nil || !errors.Is(err, failure) {
		t.Fatalf("row failure must not return a partial batch: (%v, %v)", servers, err)
	}
	if !fixture.closed {
		t.Fatal("failed rows were not released")
	}
}

func TestGetServersByIDsAndUserCancelsQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := batchServerFixture()
	fixture.queryHook = func(queryContext context.Context) error {
		if queryContext != ctx {
			t.Error("query did not receive the caller's context")
		}
		cancel()
		return queryContext.Err()
	}
	servers, err := GetServersByIDsAndUserContext(ctx, collectorDatabase(t, fixture), []uuid.UUID{uuid.New()}, uuid.New())
	if servers != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query = (%v, %v)", servers, err)
	}
}
