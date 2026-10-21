package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"server-monitor/internal/testdb"
)

// Exercises the real handlers, DB authorization, Agent sockets and the exact
// SSH/SFTP entry points used by the UI. Only HTTP login is replaced by a test
// user header; no external host or existing OS SSH daemon participates.
func TestAgentHTTPEnrollmentRemoteOperationsRevocationAndReenrollment(t *testing.T) {
	db := testdb.Open(t)
	owner, _, _ := testdb.Seed(t, db)
	foreign, _, _ := testdb.Seed(t, db)
	// This test covers the Agent transport and collector persistence, not the
	// developer machine's optional Docker daemon. Keep the real OS probe while
	// making Docker deterministically unavailable only in the fixture's children.
	bin := t.TempDir()
	dockerName, dockerScript := "docker", "#!/bin/sh\nexit 1\n"
	if runtime.GOOS == "windows" {
		dockerName, dockerScript = "docker.cmd", "@exit /b 1\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, dockerName), []byte(dockerScript), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureEnvironment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "PATH") {
			fixtureEnvironment = append(fixtureEnvironment, entry)
		}
	}
	fixtureEnvironment = append(fixtureEnvironment, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := db.Exec(`UPDATE servers SET is_public=false`); err != nil {
		t.Fatal(err)
	}
	wrapper := &models.DB{Raw: db, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	hub := agenttransport.NewHub()
	t.Cleanup(hub.Close)
	services.SetAgentConnector(hub.Dial, hub.Disconnect)
	t.Cleanup(func() { services.SetAgentConnector(nil, nil) })
	cache := services.NewSSHConnCache()
	t.Cleanup(cache.Close)
	collector := services.NewCollector(wrapper, 3*time.Second)
	t.Cleanup(collector.Stop)
	agents := NewAgentHandler(hub, cache, "", t.TempDir())
	servers := NewServerHandler(cache, collector)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("db", wrapper)
		if raw := c.GetHeader("X-Test-User"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				c.AbortWithStatus(401)
				return
			}
			c.Set("user_id", id)
			// This fixture bypasses JWT parsing, so provide the same lifetime and
			// revocation version that authenticated terminal routes receive.
			c.Set("token_session_version", time.Unix(0, 0))
			c.Set("token_expires_at", time.Now().Add(time.Hour))
		}
		c.Next()
	})
	engine.POST("/api/servers", servers.Create)
	engine.GET("/api/servers/:id", servers.Get)
	engine.POST("/api/servers/:id/agent/enroll", agents.Enroll)
	engine.GET("/api/servers/:id/agent", agents.Status)
	engine.DELETE("/api/servers/:id/agent", agents.Revoke)
	engine.POST(agenttransport.RegisterPath, agents.Register)
	engine.GET(agenttransport.ControlPath, agents.Control)
	engine.GET(agenttransport.DataPath, agents.Data)
	engine.GET("/api/ssh/:id", NewSSHHandler().Handle)
	engine.GET("/api/public/status", NewPublicStatusHandler().Get)
	engine.POST("/api/network/checks", NewNetworkHandler(nil).Create)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	request := func(method, path string, user uuid.UUID, body any, want int) []byte {
		t.Helper()
		var input io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(encoded)
		}
		req, err := http.NewRequest(method, server.URL+path, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if user != uuid.Nil {
			req.Header.Set("X-Test-User", user.String())
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d response=%s", method, path, res.StatusCode, want, data)
		}
		return data
	}
	create := func(user uuid.UUID, name string) models.Server {
		t.Helper()
		data := request("POST", "/api/servers", user, map[string]any{"connection_mode": "agent", "name": name, "server_type": runtime.GOOS, "is_public": false}, 201)
		var host models.Server
		if err := json.Unmarshal(data, &host); err != nil {
			t.Fatal(err)
		}
		if host.ID == uuid.Nil || host.ConnectionMode != "agent" || host.IsPublic {
			t.Fatal("wrong agent host defaults")
		}
		return host
	}
	host := create(owner, "agent-private-integration")
	otherHost := create(foreign, "other-owner-private")
	base := "/api/servers/" + host.ID.String()
	for _, path := range []string{base, base + "/agent"} {
		request("GET", path, foreign, nil, 404)
	}
	request("POST", base+"/agent/enroll", foreign, nil, 404)
	request("DELETE", base+"/agent", foreign, nil, 404)
	request("POST", "/api/network/checks", owner, map[string]any{"name": "foreign source", "target_server_id": host.ID, "source_server_id": otherHost.ID, "packets": 3, "loss_threshold": 50}, 404)
	public := request("GET", "/api/public/status", uuid.Nil, nil, 200)
	if bytes.Contains(public, []byte(host.Name)) || bytes.Contains(public, []byte(otherHost.Name)) {
		t.Fatal("private server appeared on public page")
	}

	enroll := func() agenttransport.Config {
		t.Helper()
		data := request("POST", base+"/agent/enroll", owner, nil, 200)
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(data, &ticket); err != nil {
			t.Fatal(err)
		}
		config, err := agenttransport.Enroll(context.Background(), server.URL, ticket.Ticket, "integration")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agenttransport.Enroll(context.Background(), server.URL, ticket.Ticket, "integration"); err == nil {
			t.Fatal("ticket was accepted twice")
		}
		return config
	}
	launch := func(config agenttransport.Config) {
		t.Helper()
		signer, err := ssh.ParsePrivateKey([]byte(config.HostKeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		agent := &agenttransport.Agent{Config: config, Version: "integration", Server: &agenttransport.SSHServer{Signer: signer, WorkingDirectory: t.TempDir(), Environment: fixtureEnvironment}}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- agent.RunOnce(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("agent did not exit")
			}
		})
		until := time.Now().Add(5 * time.Second)
		for !hub.Online(host.ID) {
			if time.Now().After(until) {
				t.Fatal("agent did not connect")
			}
			time.Sleep(time.Millisecond)
		}
	}
	config := enroll()
	launch(config)
	loaded, err := models.GetServerByIDAndUser(wrapper, host.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SSHPassword != "" || loaded.SSHKey != "" || loaded.CredentialID != nil || loaded.ServerType != runtime.GOOS {
		t.Fatal("agent still requires SSH credentials")
	}
	// Collect the local disposable test host through the production collector;
	// this catches accidental fallthrough to TCP/22 and SQL scan mismatches.
	if err := collector.PollNow(loaded); err != nil {
		t.Fatalf("collector could not use Agent: %v", err)
	}
	metric, err := models.GetLatestMetric(context.Background(), db, loaded.ID)
	if err != nil || metric.MemoryTotal <= 0 {
		t.Fatalf("collector did not persist the Agent sample: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cached, err := cache.GetContext(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	session, err := cached.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.CombinedOutput("echo AGENT_EXEC_READY")
	_ = session.Close()
	if err != nil || !bytes.Contains(output, []byte("AGENT_EXEC_READY")) {
		t.Fatalf("cached exec: %s %v", output, err)
	}

	files, err := services.NewSFTPFiles(ctx, cached, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.ToSlash(filepath.Join(t.TempDir(), "agent exact  name.txt"))
	content := "local protocol integration 中文\n"
	if err := files.Write(filename, strings.NewReader(content), int64(len(content)), false); err != nil {
		t.Fatal(err)
	}
	var read bytes.Buffer
	if err := files.Read(filename, &read, 4096); err != nil || read.String() != content {
		t.Fatal("SFTP via real files service", err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}

	collection, raw, err := services.DialServer(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	defer collection.Close()
	terminal, _, err := services.DialServer(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	if collection == cached || terminal == cached || collection == terminal {
		t.Fatal("collector and terminal share the API connection")
	}
	if err := raw.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	for _, connection := range []*ssh.Client{cached, terminal} {
		if _, _, err := connection.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			t.Fatalf("collection timeout killed another feature: %v", err)
		}
	}

	// The browser terminal path creates and owns a third independent tunnel.
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ssh/" + host.ID.String()
	browser, _, err := websocket.DefaultDialer.Dial(websocketURL, http.Header{"X-Test-User": []string{owner.String()}})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	command := "printf 'AGENT_%s_READY\\n' TERMINAL\r"
	if runtime.GOOS == "windows" {
		command = "echo AGENT_TERM^INAL_READY\r"
	}
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte(command)); err != nil {
		t.Fatal(err)
	}
	_ = browser.SetReadDeadline(time.Now().Add(10 * time.Second))
	var terminalOutput []byte
	for !bytes.Contains(terminalOutput, []byte("AGENT_TERMINAL_READY")) {
		_, chunk, err := browser.ReadMessage()
		if err != nil {
			t.Fatalf("terminal: %v output=%q", err, terminalOutput)
		}
		terminalOutput = append(terminalOutput, chunk...)
	}

	request("DELETE", base+"/agent", owner, nil, 204)
	if hub.Online(host.ID) {
		t.Fatal("revoked agent remained online")
	}
	for _, connection := range []*ssh.Client{cached, terminal} {
		if _, _, err := connection.SendRequest("keepalive@openssh.com", true, nil); err == nil {
			t.Fatal("revocation left an SSH channel usable")
		}
	}
	if _, _, err := models.AuthenticateAgent(ctx, db, config.Token); err == nil {
		t.Fatal("revoked credential still authenticates")
	}
	if _, err := cache.GetContext(ctx, loaded); !errors.Is(err, agenttransport.ErrOffline) {
		t.Fatalf("cached revoked agent: %v", err)
	}
	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := browser.ReadMessage(); err != nil {
			break
		}
	}

	replacement := enroll()
	if replacement.Token == config.Token || replacement.HostKeyPEM == config.HostKeyPEM {
		t.Fatal("reenrollment reused old secrets")
	}
	launch(replacement)
	fresh, err := cache.GetContext(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
	// Control/data auth rejects the old token before websocket upgrade.
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+agenttransport.ControlPath, http.Header{"Authorization": []string{"Bearer " + config.Token}})
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatal("old token connected after reenrollment")
	}
	if response.Body != nil {
		response.Body.Close()
	}
}
