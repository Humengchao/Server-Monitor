package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server-monitor/internal/config"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type fileOwnershipConnector struct {
	query     string
	arguments []driver.NamedValue
}

func (connector *fileOwnershipConnector) Connect(context.Context) (driver.Conn, error) {
	return &fileOwnershipConnection{connector}, nil
}
func (*fileOwnershipConnector) Driver() driver.Driver { return fileOwnershipDriver{} }

type fileOwnershipDriver struct{}

func (fileOwnershipDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fileOwnershipConnection struct{ connector *fileOwnershipConnector }

func (*fileOwnershipConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (*fileOwnershipConnection) Close() error              { return nil }
func (*fileOwnershipConnection) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }
func (connection *fileOwnershipConnection) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	connection.connector.query, connection.connector.arguments = query, arguments
	return fileEmptyRows{}, nil
}

type fileEmptyRows struct{}

func (fileEmptyRows) Columns() []string         { return []string{"id"} }
func (fileEmptyRows) Close() error              { return nil }
func (fileEmptyRows) Next([]driver.Value) error { return io.EOF }

func fileHandlerRoutes(engine *gin.Engine, handler *FileHandler) {
	group := engine.Group("/servers/:id/files", handler.Deadline)
	group.GET("", handler.List)
	group.GET("/text", handler.ReadText)
	group.PUT("/text", handler.SaveText)
	group.GET("/download", handler.Download)
	group.POST("/upload", handler.Upload)
}

func TestFileRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.AuthRequired(&config.Config{}))
	fileHandlerRoutes(engine, NewFileHandler(nil))
	for _, request := range []struct{ method, suffix string }{{"GET", ""}, {"GET", "/text"}, {"PUT", "/text"}, {"GET", "/download"}, {"POST", "/upload"}} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(request.method, "/servers/"+uuid.NewString()+"/files"+request.suffix+"?path=/etc/passwd", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d", request.method, request.suffix, response.Code)
		}
	}
}

func TestEveryFileRouteChecksServerOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serverID, userID := uuid.New(), uuid.New()
	for _, request := range []struct{ method, suffix string }{{"GET", ""}, {"GET", "/text"}, {"PUT", "/text"}, {"GET", "/download"}, {"POST", "/upload"}} {
		t.Run(request.method+request.suffix, func(t *testing.T) {
			connector := &fileOwnershipConnector{}
			database := sql.OpenDB(connector)
			defer database.Close()
			engine := gin.New()
			engine.Use(func(httpContext *gin.Context) {
				httpContext.Set("user_id", userID)
				httpContext.Set("db", &models.DB{Raw: database})
				httpContext.Next()
			})
			fileHandlerRoutes(engine, NewFileHandler(nil))
			body := strings.NewReader(`{"content":"test","revision":"` + strings.Repeat("a", 64) + `"}`)
			response := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(request.method, "/servers/"+serverID.String()+"/files"+request.suffix+"?path=/tmp", body)
			httpRequest.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(response, httpRequest)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
			if !strings.Contains(connector.query, "WHERE id=$1 AND user_id=$2") || len(connector.arguments) != 2 {
				t.Fatalf("unscoped query: %s", connector.query)
			}
			if connector.arguments[0].Value != serverID.String() || connector.arguments[1].Value != userID.String() {
				t.Fatalf("incorrect ownership arguments: %v", connector.arguments)
			}
		})
	}
}

func TestFileValidationBeforeConnecting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	fileHandlerRoutes(engine, NewFileHandler(nil))
	for _, requestPath := range []string{
		"/servers/invalid/files?path=/tmp",
		"/servers/" + uuid.NewString() + "/files?path=relative",
		"/servers/" + uuid.NewString() + "/files?path=/tmp&container=--help",
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path %s: status %d", requestPath, response.Code)
		}
	}
}

func TestFileErrorsRemainActionable(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{services.ErrFileChanged, 409, "file_changed"}, {services.ErrFileExists, 409, "file_exists"},
		{services.ErrFileTooLarge, 413, "file_too_large"}, {services.ErrFileNotText, 415, "not_text"},
		{services.ErrFilesUnsupported, 422, "unsupported"}, {context.DeadlineExceeded, 504, "timeout"},
	} {
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		fileError(ctx, test.err)
		var result struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || result.Code != test.code {
			t.Fatalf("%v: status=%d code=%s", test.err, response.Code, result.Code)
		}
	}
}
