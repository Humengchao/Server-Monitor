package models

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
)

// A small SQL driver keeps collector regressions runnable without PostgreSQL.
// It records queries and can fail midway through a streamed result set.
type collectorConnector struct {
	rows    [][]driver.Value
	rowErr  error
	queries []string
	closed  bool
}

func (c *collectorConnector) Connect(context.Context) (driver.Conn, error) {
	return &collectorConnection{c}, nil
}
func (*collectorConnector) Driver() driver.Driver { return collectorDriver{} }

type collectorDriver struct{}

func (collectorDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type collectorConnection struct{ fixture *collectorConnector }

func (*collectorConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*collectorConnection) Close() error { return nil }
func (*collectorConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *collectorConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.fixture.queries = append(c.fixture.queries, query)
	return &collectorRows{fixture: c.fixture}, nil
}

type collectorRows struct {
	fixture *collectorConnector
	index   int
}

func (*collectorRows) Columns() []string {
	return []string{"id", "user_id", "name", "host", "port", "ssh_username", "ssh_password", "ssh_key",
		"ssh_host_key", "credential_id", "server_type", "credential_username", "credential_password", "credential_key"}
}
func (r *collectorRows) Close() error { r.fixture.closed = true; return nil }
func (r *collectorRows) Next(dest []driver.Value) error {
	if r.index == len(r.fixture.rows) {
		if r.fixture.rowErr != nil {
			return r.fixture.rowErr
		}
		return io.EOF
	}
	copy(dest, r.fixture.rows[r.index])
	r.index++
	return nil
}

const collectorTestKey = "0123456789abcdef0123456789abcdef"

func collectorRow(name string) []driver.Value {
	return []driver.Value{uuid.NewString(), uuid.NewString(), name, "host.invalid", int64(22), "root", "", "",
		"pinned-host-key", nil, "linux", nil, "", ""}
}

func collectorDatabase(t *testing.T, fixture *collectorConnector) *DB {
	t.Helper()
	db := sql.OpenDB(fixture)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return &DB{Raw: db, EncryptionKey: collectorTestKey}
}

func TestGetAllServersResolvesCredentialsInOneQuery(t *testing.T) {
	encrypt := func(value string) string {
		t.Helper()
		encoded, err := crypto.Encrypt(value, collectorTestKey)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	inline := collectorRow("inline")
	inline[6] = encrypt("inline-password")
	linked := collectorRow("linked")
	credentialID := uuid.NewString()
	linked[9], linked[11], linked[12], linked[13] = credentialID, "deploy", encrypt("shared-password"), encrypt("shared-key")
	// Unused inline ciphertext must not prevent a valid linked credential
	// from working, and the server's host key must remain pinned.
	linked[6], linked[7] = "obsolete-invalid-ciphertext", "obsolete-invalid-ciphertext"
	missing := collectorRow("unresolved-linked")
	missing[9], missing[6] = uuid.NewString(), encrypt("must-not-fall-back")
	corrupt := collectorRow("corrupt-inline")
	corrupt[7] = "invalid-ciphertext"
	corruptLinked := collectorRow("corrupt-linked")
	corruptLinked[9], corruptLinked[11], corruptLinked[12] = credentialID, "deploy", "invalid-ciphertext"
	fixture := &collectorConnector{rows: [][]driver.Value{inline, linked, missing, corrupt, corruptLinked}}
	servers, err := GetAllServers(collectorDatabase(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want only the two with usable credentials", len(servers))
	}
	if servers[0].SSHUsername != "root" || servers[0].SSHPassword != "inline-password" || servers[0].CredentialID != nil {
		t.Fatal("inline credentials changed")
	}
	shared := servers[1]
	if shared.SSHUsername != "deploy" || shared.SSHPassword != "shared-password" || shared.SSHKey != "shared-key" ||
		shared.CredentialID == nil || shared.CredentialID.String() != credentialID || shared.SSHHostKey != "pinned-host-key" {
		t.Fatal("linked credentials or host key were not preserved")
	}
	if len(fixture.queries) != 1 {
		t.Fatalf("got %d queries, want one regardless of fleet size", len(fixture.queries))
	}
	query := strings.Join(strings.Fields(fixture.queries[0]), " ")
	if !strings.Contains(query, "LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id") {
		t.Fatal("credential join must be scoped to the server owner")
	}
	if !fixture.closed {
		t.Fatal("server rows were not released")
	}
}

func TestGetAllServersPropagatesRowErrors(t *testing.T) {
	failure := errors.New("connection lost while streaming servers")
	fixture := &collectorConnector{rows: [][]driver.Value{collectorRow("first")}, rowErr: failure}
	_, err := GetAllServers(collectorDatabase(t, fixture))
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the row error instead of a successful partial fleet", err)
	}
	if !fixture.closed {
		t.Fatal("failed rows were not released")
	}
}
