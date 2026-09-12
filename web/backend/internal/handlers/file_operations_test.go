package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

type metadataStore struct {
	services.ManagedFiles
	entry services.RemoteFileEntry
	err   error
}

func (store metadataStore) Metadata(string) (services.RemoteFileEntry, error) {
	return store.entry, store.err
}
func TestUploadPreflightVersionAndTargetType(testContext *testing.T) {
	entry := services.RemoteFileEntry{Path: "/tmp/config", IsFile: true, Size: 3, ModifiedAt: time.Unix(100, 0), Mode: "644"}
	for _, scenario := range []struct {
		name     string
		store    metadataStore
		version  string
		expected error
	}{
		{"new", metadataStore{err: os.ErrNotExist}, "missing", nil},
		{"deleted", metadataStore{err: os.ErrNotExist}, "old", services.ErrFileChanged},
		{"appeared", metadataStore{entry: entry}, "missing", services.ErrFileExists},
		{"unchanged", metadataStore{entry: entry}, services.FileMetadataVersion(entry), nil},
		{"changed", metadataStore{entry: entry}, "stale", services.ErrFileChanged},
		{"missing version", metadataStore{entry: entry}, "", services.ErrFileChanged},
		{"permission", metadataStore{err: os.ErrPermission}, "missing", os.ErrPermission},
	} {
		testContext.Run(scenario.name, func(testContext *testing.T) {
			if err := expectedUpload(scenario.store, entry.Path, scenario.version); !errors.Is(err, scenario.expected) {
				testContext.Fatal(err)
			}
		})
	}
	for _, kind := range []string{"symlink", "directory"} {
		target := entry
		if kind == "symlink" {
			target.IsSymlink = true
		} else {
			target.IsFile = false
			target.IsDir = true
		}
		if err := expectedUpload(metadataStore{entry: target}, entry.Path, services.FileMetadataVersion(target)); !errors.Is(err, services.ErrFileNotRegular) {
			testContext.Fatal(kind, err)
		}
	}
}
func TestFileMutationRejectsRoots(testContext *testing.T) {
	for _, filename := range []string{"/", "C:/", "/C:/", "."} {
		if safeMutationPath(filename) {
			testContext.Fatal("root accepted", filename)
		}
	}
	for _, filename := range []string{"/tmp/file", "C:/Users", "/C:/Users"} {
		if !safeMutationPath(filename) {
			testContext.Fatal("file rejected", filename)
		}
	}
}

type auditConnector struct {
	fail    bool
	writes  [][]driver.NamedValue
	queries []string
}

func (connector *auditConnector) Connect(context.Context) (driver.Conn, error) {
	return &auditConnection{connector: connector}, nil
}
func (*auditConnector) Driver() driver.Driver { return fileOwnershipDriver{} }

type auditConnection struct {
	fileOwnershipConnection
	connector *auditConnector
}

func (connection *auditConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.connector.queries = append(connection.connector.queries, query)
	return &ownedRow{}, nil
}
func (connection *auditConnection) ExecContext(_ context.Context, _ string, values []driver.NamedValue) (driver.Result, error) {
	connection.connector.writes = append(connection.connector.writes, values)
	if connection.connector.fail {
		return nil, errors.New("database unavailable")
	}
	return driver.RowsAffected(1), nil
}

type ownedRow struct{ sent bool }

func (*ownedRow) Columns() []string { return []string{"exists"} }
func (*ownedRow) Close() error      { return nil }
func (row *ownedRow) Next(values []driver.Value) error {
	if row.sent {
		return io.EOF
	}
	values[0] = true
	row.sent = true
	return nil
}
func TestFileAuditFailsClosedAndRecordsOutcome(testContext *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fail := range []bool{false, true} {
		connector := &auditConnector{fail: fail}
		database := sql.OpenDB(connector)
		defer database.Close()
		engine := gin.New()
		user, server := uuid.New(), uuid.New()
		called := false
		engine.Use(func(ctx *gin.Context) { ctx.Set("db", &models.DB{Raw: database}); ctx.Set("user_id", user) })
		handler := NewFileHandler(nil)
		engine.POST("/servers/:id/files/change", handler.Audit, func(ctx *gin.Context) {
			called = true
			ctx.Set("file_action", "rename")
			ctx.Set("file_target", "/tmp/new")
			fileError(ctx, services.ErrFileChanged)
		})
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest("POST", "/servers/"+server.String()+"/files/change?path=/tmp/old", nil))
		if fail {
			if called || response.Code != 503 || len(connector.writes) != 1 {
				testContext.Fatal("write did not fail closed", response.Code)
			}
		} else {
			if !called || response.Code != 409 || len(connector.writes) != 2 {
				testContext.Fatal("audit missing", response.Code)
			}
			update := connector.writes[1]
			if update[1].Value != "rename" || update[2].Value != "/tmp/new" || update[4].Value != "failed" || update[5].Value != "file_changed" {
				testContext.Fatal(update)
			}
		}
		if len(connector.queries) != 1 || !strings.Contains(connector.queries[0], "WHERE id=$1 AND user_id=$2") {
			testContext.Fatal("unscoped audit", connector.queries)
		}
		insert := connector.writes[0]
		if insert[1].Value != user.String() || insert[2].Value != server.String() || insert[5].Value != "/tmp/old" {
			testContext.Fatal(insert)
		}
	}
}
