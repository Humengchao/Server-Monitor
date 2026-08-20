package handlers

import (
	"log"
	"net/http"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
	// Clients offer ("bearer", <jwt>) as subprotocols to authenticate without
	// putting the token in the URL; echo "bearer" so browsers accept the
	// handshake.
	Subprotocols: []string{"bearer"},
}

type SSHHandler struct{}

func NewSSHHandler() *SSHHandler { return &SSHHandler{} }

func (h *SSHHandler) Handle(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed: " + err.Error()})
		return
	}
	defer client.Close()

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	ts, err := services.NewTerminalSession(conn, client)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("PTY allocation failed: "+err.Error()))
		return
	}
	defer ts.Close()

	// stdin: websocket → SSH, including resize control messages
	// (NewTerminalSession already handles stdout/stderr → websocket)
	go ts.PumpStdin()

	// Wait for session to complete (client disconnect or shell exit)
	<-ts.Done()
}
