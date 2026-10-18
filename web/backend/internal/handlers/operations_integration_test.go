package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func operationsRouter(t *testing.T) (*gin.Engine, *models.DB, uuid.UUID) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := &models.DB{Raw: testdb.Open(t), EncryptionKey: "0123456789abcdef0123456789abcdef"}
	user, _, _ := testdb.Seed(t, db.Raw)
	hash, err := bcrypt.GenerateFromPassword([]byte("account-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Raw.Exec(`UPDATE users SET password_hash=$1 WHERE id=$2`, string(hash), user); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("db", db); c.Set("user_id", user) })
	return r, db, user
}

func operationsRequest(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	return response
}

func TestServerVisibilityAndAgentConfigurationThroughHTTP(t *testing.T) {
	r, db, user := operationsRouter(t)
	h := NewServerHandler(nil, nil)
	r.POST("/servers", h.Create)
	r.PUT("/servers/:id", h.Update)
	created := operationsRequest(t, r, "POST", "/servers", gin.H{"name": "private windows", "connection_mode": "agent", "server_type": "windows", "ssh_password": "discard-me", "ssh_key": "discard-me"})
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var server models.Server
	if err := json.Unmarshal(created.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	if server.IsPublic || !server.MonitoringEnabled || !server.AutoRenewEnabled || server.ConnectionMode != "agent" {
		t.Fatalf("defaults: %+v", server)
	}
	stored, err := models.GetServerByIDAndUser(db, server.ID, user)
	if err != nil || stored.SSHPassword != "" || stored.SSHKey != "" || stored.CredentialID != nil {
		t.Fatalf("agent auth: %+v %v", stored, err)
	}
	publicNames := func() string {
		items, err := models.GetPublicServerMetrics(db.Raw, time.Now().Add(-30*24*time.Hour), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range items {
			names = append(names, item.Name)
		}
		return strings.Join(names, ",")
	}
	if names := publicNames(); names != "test" {
		t.Fatalf("private host exposed or existing visibility lost: %q", names)
	}
	// Windows service and domain account names can contain spaces. Agent
	// metadata is not an SSH login field, and editing the host must preserve it.
	const serviceUser = `NT AUTHORITY\SYSTEM`
	if _, err := db.Raw.Exec(`UPDATE servers SET ssh_username=$1 WHERE id=$2`, serviceUser, server.ID); err != nil {
		t.Fatal(err)
	}
	path := "/servers/" + server.ID.String()
	updated := operationsRequest(t, r, "PUT", path, gin.H{"name": "public windows", "server_type": "windows", "is_public": true, "monitoring_enabled": false, "auto_renew_enabled": false})
	if updated.Code != 200 {
		t.Fatalf("update: %d %s", updated.Code, updated.Body)
	}
	var changed models.Server
	if err := json.Unmarshal(updated.Body.Bytes(), &changed); err != nil || !changed.IsPublic || changed.MonitoringEnabled || changed.AutoRenewEnabled {
		t.Fatalf("update response differs from saved options: %s %v", updated.Body, err)
	}
	if !strings.Contains(publicNames(), "public windows") {
		t.Fatal("visibility selection was not applied")
	}
	// Older clients omit the new options; a metadata edit must preserve them.
	updated = operationsRequest(t, r, "PUT", path, gin.H{"name": "renamed windows"})
	if updated.Code != 200 {
		t.Fatalf("legacy update: %d %s", updated.Code, updated.Body)
	}
	stored, err = models.GetServerByIDAndUser(db, server.ID, user)
	if err != nil || !stored.IsPublic || stored.MonitoringEnabled || stored.AutoRenewEnabled || stored.ServerType != "windows" {
		t.Fatalf("options reset: %+v %v", stored, err)
	}
	if stored.SSHUsername != serviceUser {
		t.Fatalf("Agent service identity changed: %q", stored.SSHUsername)
	}
	for _, change := range []gin.H{{"name": "bad", "connection_mode": "ssh"}, {"name": "bad", "server_type": "linux"}} {
		if response := operationsRequest(t, r, "PUT", path, change); response.Code != 400 {
			t.Fatalf("unsafe identity change: %d", response.Code)
		}
	}
	if response := operationsRequest(t, r, "PUT", "/servers/"+uuid.NewString(), gin.H{"name": "foreign"}); response.Code != 404 {
		t.Fatalf("ownership: %d", response.Code)
	}
}

func TestBackupHTTPReauthenticationPreviewAndAtomicImport(t *testing.T) {
	r, db, user := operationsRouter(t)
	h := NewBackupHandler()
	r.POST("/export", h.Export)
	r.POST("/preview", h.Preview)
	r.POST("/import", h.Import)
	request := gin.H{"current_password": "wrong", "password": "backup-password-123", "include_credentials": true}
	if response := operationsRequest(t, r, "POST", "/export", request); response.Code != 403 {
		t.Fatalf("reauth: %d", response.Code)
	}
	request["current_password"] = "account-password"
	exported := operationsRequest(t, r, "POST", "/export", request)
	if exported.Code != 200 || exported.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("export: %d %s", exported.Code, exported.Body)
	}
	request["data"] = base64.StdEncoding.EncodeToString(exported.Body.Bytes())
	request["password"] = "incorrect-backup-password"
	if response := operationsRequest(t, r, "POST", "/preview", request); response.Code != 400 {
		t.Fatalf("decryption: %d", response.Code)
	}
	request["password"] = "backup-password-123"
	preview := operationsRequest(t, r, "POST", "/preview", request)
	if preview.Code != 200 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body)
	}
	var result struct {
		Digest  string               `json:"digest"`
		Preview models.BackupPreview `json:"preview"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Digest == "" || result.Preview.AlreadyImported {
		t.Fatalf("preview: %s", preview.Body)
	}
	if response := operationsRequest(t, r, "POST", "/import", request); response.Code != 409 {
		t.Fatalf("digest guard: %d", response.Code)
	}
	request["digest"] = result.Digest
	imported := operationsRequest(t, r, "POST", "/import", request)
	if imported.Code != 201 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body)
	}
	var restored int
	if err := db.Raw.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM servers WHERE user_id=$1 AND NOT monitoring_enabled AND NOT auto_renew_enabled AND NOT is_public`, user).Scan(&restored); err != nil || restored != 1 {
		t.Fatalf("restored defaults: %d %v", restored, err)
	}
	if response := operationsRequest(t, r, "POST", "/import", request); response.Code != 409 {
		t.Fatalf("duplicate restore: %d", response.Code)
	}
	preview = operationsRequest(t, r, "POST", "/preview", request)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"already_imported":true`) {
		t.Fatalf("duplicate preview: %d %s", preview.Code, preview.Body)
	}
}
