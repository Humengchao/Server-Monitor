package services

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/models"
)

var agentConnector struct {
	sync.RWMutex
	dial       func(context.Context, uuid.UUID) (*ssh.Client, net.Conn, error)
	disconnect func(uuid.UUID)
}

func SetAgentConnector(dial func(context.Context, uuid.UUID) (*ssh.Client, net.Conn, error), disconnect func(uuid.UUID)) {
	agentConnector.Lock()
	defer agentConnector.Unlock()
	agentConnector.dial = dial
	agentConnector.disconnect = disconnect
}

func DisconnectAgent(id uuid.UUID) {
	agentConnector.RLock()
	disconnect := agentConnector.disconnect
	agentConnector.RUnlock()
	if disconnect != nil {
		disconnect(id)
	}
}

// Every remote operation uses the same SSH channel protocol. Agent transports
// are independent reverse tunnels, so terminal and collector deadlines cannot
// close the interactive API's shared connection.
func DialServer(ctx context.Context, s *models.Server) (*ssh.Client, net.Conn, error) {
	if s.ConnectionMode == "agent" {
		agentConnector.RLock()
		dial := agentConnector.dial
		agentConnector.RUnlock()
		if dial == nil {
			return nil, nil, errors.New("agent connection unavailable")
		}
		if s.AgentHostKey != "" {
			ctx = agenttransport.WithExpectedHostKey(ctx, s.AgentHostKey)
		}
		return dial(ctx, s.ID)
	}
	return dialSSHClientConnContext(ctx, s.Host, s.Port, s.SSHUsername, s.SSHPassword, s.SSHKey, s.SSHHostKey, sshDialTimeout)
}
