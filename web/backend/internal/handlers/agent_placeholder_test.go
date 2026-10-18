package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestNamelessAgentEnrollmentAndTicketIsolation(t *testing.T) {
	db := testdb.Open(t)
	owner, _, _ := testdb.Seed(t, db)
	foreign, foreignServer, _ := testdb.Seed(t, db)
	wrapper := &models.DB{Raw: db, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	hub := agenttransport.NewHub()
	defer hub.Close()
	agents := NewAgentHandler(hub, nil, "https://panel.invalid", "")
	servers := NewServerHandler(nil, nil)
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
		}
		c.Next()
	})
	engine.POST("/api/servers", servers.Create)
	engine.PUT("/api/servers/:id", servers.Update)
	engine.POST("/api/servers/:id/agent/enroll", agents.Enroll)
	engine.POST(agenttransport.RegisterPath, agents.Register)
	request := func(method, path string, user uuid.UUID, body any, want int) []byte {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		if user != uuid.Nil {
			req.Header.Set("X-Test-User", user.String())
		}
		req.RemoteAddr = "198.51.100.77:1234"
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.Code, want, response.Body.String())
		}
		return response.Body.Bytes()
	}

	for _, tc := range []struct {
		name     string
		payload  map[string]any
		edit     bool
		editName string
		wantName string
	}{
		{"no optional fields", map[string]any{"connection_mode": "agent"}, false, "", "machine-hostname"},
		{"blank name and address", map[string]any{"connection_mode": "agent", "name": " \t\n", "host": " \t", "ssh_username": " "}, false, "", "machine-hostname"},
		{"custom name retained", map[string]any{"connection_mode": "agent", "name": "My custom machine"}, false, "", "My custom machine"},
		{"manual edit wins", map[string]any{"connection_mode": "agent"}, true, "Manually renamed", "Manually renamed"},
		{"metadata edit keeps auto naming", map[string]any{"connection_mode": "agent"}, true, "", "machine-hostname"},
		{"client cannot set internal flag", map[string]any{"connection_mode": "agent", "name": "Preserve me", "agent_auto_name": true}, false, "", "Preserve me"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := request(http.MethodPost, "/api/servers", owner, tc.payload, http.StatusCreated)
			var host models.Server
			if err := json.Unmarshal(data, &host); err != nil {
				t.Fatal(err)
			}
			if host.ID == uuid.Nil || host.Host != "agent" || host.SSHUsername != "agent" || host.SSHPassword != "" || host.SSHKey != "" || host.CredentialID != nil {
				t.Fatalf("unexpected placeholder: %+v", host)
			}
			if bytes.Contains(data, []byte("agent_auto_name")) {
				t.Fatal("internal flag leaked into public server object")
			}
			if raw, ok := tc.payload["name"].(string); !ok || strings.TrimSpace(raw) == "" {
				if !strings.HasPrefix(host.Name, "Agent ") {
					t.Fatal("missing display placeholder")
				}
			}
			path := "/api/servers/" + host.ID.String()
			request(http.MethodPost, path+"/agent/enroll", foreign, nil, http.StatusNotFound)
			if tc.edit {
				name := tc.editName
				if name == "" {
					name = host.Name
				}
				request(http.MethodPut, path, owner, map[string]any{"name": name, "host": "", "notes": "edited before enrollment"}, http.StatusOK)
			}
			var ticket struct {
				Ticket string `json:"ticket"`
			}
			if err := json.Unmarshal(request(http.MethodPost, path+"/agent/enroll", owner, nil, http.StatusOK), &ticket); err != nil {
				t.Fatal(err)
			}
			hello, _, err := agenttransport.NewIdentity("placeholder-test")
			if err != nil {
				t.Fatal(err)
			}
			hello.Hostname = "machine-hostname"
			hello.Username = "agent-runtime-user"
			// Extra identifiers cannot redirect a bearer ticket to another row
			// or account, even when that account has a server with the same name.
			registration := map[string]any{"ticket": ticket.Ticket, "hello": hello, "server_id": foreignServer, "user_id": foreign}
			var result agenttransport.RegisterResponse
			if err := json.Unmarshal(request(http.MethodPost, agenttransport.RegisterPath, uuid.Nil, registration, http.StatusOK), &result); err != nil {
				t.Fatal(err)
			}
			if result.ServerID != host.ID.String() || result.AgentID != host.ID.String() {
				t.Fatal("ticket bound to wrong row")
			}
			request(http.MethodPost, agenttransport.RegisterPath, uuid.Nil, registration, http.StatusForbidden)
			var name, address, user string
			var flag bool
			var savedOwner uuid.UUID
			if err := db.QueryRow(`SELECT name,host,ssh_username,agent_auto_name,user_id FROM servers WHERE id=$1`, host.ID).Scan(&name, &address, &user, &flag, &savedOwner); err != nil {
				t.Fatal(err)
			}
			if name != tc.wantName || address != "agent" || user != hello.Username || flag || savedOwner != owner {
				t.Fatalf("binding result: name=%q address=%q user=%q pending=%v owner=%s", name, address, user, flag, savedOwner)
			}
			var foreignAgents int
			if err := db.QueryRow(`SELECT COUNT(*) FROM server_agents a JOIN servers s ON s.id=a.server_id WHERE s.user_id=$1`, foreign).Scan(&foreignAgents); err != nil || foreignAgents != 0 {
				t.Fatal("foreign user gained an agent", err)
			}
		})
	}
}

func TestSSHCreationStillRejectsMissingConnectionFields(t *testing.T) {
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("user_id", uuid.New()); c.Set("db", &models.DB{}); c.Next() })
	engine.POST("/api/servers", NewServerHandler(nil, nil).Create)
	for _, body := range []string{
		`{}`, `{"name":" \t","host":"example.invalid","ssh_password":"secret"}`,
		`{"name":"host","ssh_password":"secret"}`, `{"name":"host","host":"example.invalid"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/servers", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted incomplete SSH config %s: %d %s", body, response.Code, response.Body.String())
		}
	}
}
