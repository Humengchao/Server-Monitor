// Package agenttransport carries ordinary SSH sessions over authenticated,
// outbound-only WebSockets. HTTP enrollment and authorization belong to the
// application; the Hub never accepts an unauthenticated HTTP request itself.
package agenttransport

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	ProtocolVersion = 1
	ControlPath     = "/api/agent/control"
	DataPath        = "/api/agent/data"
	RegisterPath    = "/api/agent/register"
	MaxConnections  = 32
)

var (
	ErrOffline        = errors.New("agent is offline")
	ErrBusy           = errors.New("agent connection limit reached")
	ErrUnknownSession = errors.New("unknown or expired agent session")
)

type Hello struct {
	Type         string   `json:"type"`
	Protocol     int      `json:"protocol"`
	Version      string   `json:"version"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Username     string   `json:"username"`
	HostKey      string   `json:"host_key"`
	Capabilities []string `json:"capabilities"`
}

func (h Hello) Validate() (ssh.PublicKey, error) {
	if h.Type != "hello" || h.Protocol != ProtocolVersion || (h.OS != "linux" && h.OS != "windows") ||
		h.Hostname == "" || len(h.Hostname) > 256 || h.Username == "" || len(h.Username) > 128 ||
		(h.Arch != "amd64" && h.Arch != "arm64") || len(h.Version) > 128 || len(h.HostKey) > 4096 {
		return nil, fmt.Errorf("unsupported agent identity or protocol")
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("invalid agent host key")
	}
	for _, required := range []string{"exec", "pty", "sftp"} {
		found := false
		for _, capability := range h.Capabilities {
			found = found || capability == required
		}
		if !found {
			return nil, fmt.Errorf("agent lacks %s capability", required)
		}
	}
	return key, nil
}

type Message struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Secret string `json:"secret,omitempty"`
	Error  string `json:"error,omitempty"`
}

type RegisterRequest struct {
	Ticket string `json:"ticket"`
	Hello  Hello  `json:"hello"`
}

type RegisterResponse struct {
	AgentID  string `json:"agent_id"`
	ServerID string `json:"server_id"`
	Token    string `json:"token"`
}
