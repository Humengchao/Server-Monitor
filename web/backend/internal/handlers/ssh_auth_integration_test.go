package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/models"
)

func TestCredentialAuthenticationChangesAreAtomic(t *testing.T) {
	router, db, user := operationsRouter(t)
	h := NewCredentialHandler()
	router.POST("/credentials", h.Create)
	router.PUT("/credentials/:id", h.Update)
	created := operationsRequest(t, router, "POST", "/credentials", gin.H{"name": "key only", "ssh_username": "root", "credential_type": "linux", "ssh_auth_method": "key", "ssh_key": "test-private-key"})
	var credential models.Credential
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &credential) != nil {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	path := "/credentials/" + credential.ID.String()
	request := gin.H{"name": "windows", "ssh_username": "Administrator", "credential_type": "windows", "ssh_auth_method": "password"}
	if response := operationsRequest(t, router, "PUT", path, request); response.Code != 400 {
		t.Fatalf("cleared the only auth material: %d %s", response.Code, response.Body)
	}
	saved, err := models.GetCredentialByID(db, credential.ID, user)
	if err != nil || saved.CredType != "linux" || saved.SSHKey != "test-private-key" {
		t.Fatalf("failed edit escaped transaction: %+v %v", saved, err)
	}
	request["ssh_password"] = "new-password"
	if response := operationsRequest(t, router, "PUT", path, request); response.Code != 200 {
		t.Fatalf("valid Windows transition: %d %s", response.Code, response.Body)
	}
	saved, err = models.GetCredentialByID(db, credential.ID, user)
	if err != nil || saved.SSHPassword != "new-password" || saved.SSHKey != "" || saved.SSHAuthMethod != "password" {
		t.Fatalf("Windows auth: %+v %v", saved, err)
	}
	request = gin.H{"name": "key switch", "ssh_username": "root", "credential_type": "linux", "ssh_auth_method": "key", "ssh_key": "replacement-key"}
	if response := operationsRequest(t, router, "PUT", path, request); response.Code != 200 {
		t.Fatalf("key transition: %d %s", response.Code, response.Body)
	}
	saved, err = models.GetCredentialByID(db, credential.ID, user)
	if err != nil || saved.SSHPassword != "" || saved.SSHKey != "replacement-key" || saved.SSHAuthMethod != "key" {
		t.Fatalf("old password survived switch: %+v %v", saved, err)
	}
	delete(request, "ssh_key")
	request["clear_ssh_key"] = true
	if response := operationsRequest(t, router, "PUT", path, request); response.Code != 400 {
		t.Fatalf("cleared selected key: %d %s", response.Code, response.Body)
	}
	delete(request, "clear_ssh_key")
	if response := operationsRequest(t, router, "PUT", path, request); response.Code != 200 {
		t.Fatalf("blank retains key: %d %s", response.Code, response.Body)
	}
}

func TestServerConfigurationPreservesNotesAndWindowsHostKey(t *testing.T) {
	router, db, user := operationsRouter(t)
	h := NewServerHandler(nil, nil)
	router.POST("/servers", h.Create)
	router.PUT("/servers/:id", h.Update)
	router.GET("/servers/:id", h.Get)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	pin := string(ssh.MarshalAuthorizedKey(key))
	create := gin.H{"name": "Windows", "host": "192.0.2.1", "server_type": "windows", "ssh_password": "old-password", "ssh_host_key": pin, "notes": "original"}
	response := operationsRequest(t, router, "POST", "/servers", create)
	var server models.Server
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &server) != nil {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	if server.SSHHostKey != pin {
		t.Fatal("Windows creation discarded trusted host key")
	}
	if _, err := db.Raw.Exec(`UPDATE servers SET notes='saved in another tab' WHERE id=$1`, server.ID); err != nil {
		t.Fatal(err)
	}
	path := "/servers/" + server.ID.String()
	update := gin.H{"name": "renamed", "host": "192.0.2.1", "server_type": "windows"}
	if response := operationsRequest(t, router, "PUT", path, update); response.Code != 200 {
		t.Fatalf("update: %d %s", response.Code, response.Body)
	}
	saved, err := models.GetServerByIDAndUser(db, server.ID, user)
	if err != nil || saved.SSHHostKey != pin || saved.Notes != "saved in another tab" {
		t.Fatalf("unsubmitted fields lost: %+v %v", saved, err)
	}
	update["server_type"] = "linux"
	update["ssh_auth_method"] = "key"
	update["ssh_key"] = "replacement-key"
	if response := operationsRequest(t, router, "PUT", path, update); response.Code != 200 {
		t.Fatalf("switch authentication: %d %s", response.Code, response.Body)
	}
	saved, err = models.GetServerByIDAndUser(db, server.ID, user)
	if err != nil || saved.SSHPassword != "" || saved.SSHKey != "replacement-key" {
		t.Fatalf("password survived key selection: %+v %v", saved, err)
	}
	response = operationsRequest(t, router, "GET", path, nil)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &server) != nil || server.SSHAuthMethod != "key" {
		t.Fatalf("effective auth absent from summary: %d %s", response.Code, response.Body)
	}
	delete(update, "ssh_key")
	update["server_type"] = "windows"
	update["ssh_auth_method"] = "password"
	if response := operationsRequest(t, router, "PUT", path, update); response.Code != 400 {
		t.Fatalf("Windows switch removed only key: %d %s", response.Code, response.Body)
	}
	update["server_type"] = "linux"
	update["ssh_auth_method"] = "key"
	update["notes"] = ""
	if response := operationsRequest(t, router, "PUT", path, update); response.Code != 200 {
		t.Fatalf("explicit empty notes: %d %s", response.Code, response.Body)
	}
	saved, err = models.GetServerByIDAndUser(db, server.ID, user)
	if err != nil || saved.Notes != "" {
		t.Fatalf("explicit notes clearing failed: %+v %v", saved, err)
	}
}
