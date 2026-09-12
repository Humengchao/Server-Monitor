package router

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/config"
)

type readinessConnector struct{ unavailable bool }

func (connector readinessConnector) Connect(context.Context) (driver.Conn, error) {
	return readinessConnection{unavailable: connector.unavailable}, nil
}
func (connector readinessConnector) Driver() driver.Driver { return readinessDriver{} }

type readinessDriver struct{}

func (readinessDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type readinessConnection struct{ unavailable bool }

func (readinessConnection) Close() error { return nil }
func (readinessConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (readinessConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (connection readinessConnection) Ping(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		return errors.New("missing readiness timeout")
	}
	if connection.unavailable {
		return errors.New("private database detail")
	}
	return nil
}
func TestReadinessChecksDatabaseWithoutAuthentication(testContext *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, unavailable := range []bool{false, true} {
		database := sql.OpenDB(readinessConnector{unavailable: unavailable})
		defer database.Close()
		engine, err := Setup(database, &config.Config{}, nil, nil)
		if err != nil {
			testContext.Fatal(err)
		}
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest("GET", "/api/ready", nil))
		expected, body := 200, `{"status":"ready"}`
		if unavailable {
			expected, body = 503, `{"status":"unavailable"}`
		}
		if response.Code != expected || response.Body.String() != body {
			testContext.Fatal(response.Code, response.Body.String())
		}
	}
}
