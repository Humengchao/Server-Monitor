package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type Config struct {
	ServerURL  string `json:"server_url"`
	AgentID    string `json:"agent_id"`
	Token      string `json:"token"`
	HostKeyPEM string `json:"host_key_pem"`
}

func (c Config) Validate() (ssh.Signer, error) {
	if _, err := endpoint(c.ServerURL, ControlPath, true); err != nil {
		return nil, err
	}
	id, err := uuid.Parse(c.AgentID)
	if err != nil || id == uuid.Nil || len(c.Token) < 32 || len(c.Token) > 4096 || strings.ContainsAny(c.Token, "\r\n") {
		return nil, errors.New("invalid agent configuration")
	}
	signer, err := ssh.ParsePrivateKey([]byte(c.HostKeyPEM))
	if err != nil {
		return nil, errors.New("invalid agent host key")
	}
	return signer, nil
}

func LoadConfig(path string) (Config, error) {
	var config Config
	file, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return config, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return config, errors.New("invalid agent config file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return config, errors.New("agent config must only be readable by its owner (chmod 600)")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, errors.New("invalid agent config JSON")
	}
	_, err = config.Validate()
	return config, err
}

func SaveConfig(path string, config Config) error {
	if _, err := config.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := protectConfig(file.Name()); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(config); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func NewIdentity(version string) (Hello, string, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Hello{}, "", err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return Hello{}, "", err
	}
	block, err := ssh.MarshalPrivateKey(key, "server-monitor-agent")
	if err != nil {
		return Hello{}, "", err
	}
	hello, err := identity(signer, version)
	return hello, string(pem.EncodeToMemory(block)), err
}

func identity(signer ssh.Signer, version string) (Hello, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return Hello{}, err
	}
	current, err := user.Current()
	if err != nil {
		return Hello{}, err
	}
	return Hello{Type: "hello", Protocol: ProtocolVersion, Version: version, Hostname: hostname,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Username: current.Username,
		HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), Capabilities: []string{"exec", "pty", "sftp"}}, nil
}

// Enroll exchanges an expiring, single-use ticket. No SSH credentials or
// listening sockets are needed. The config is returned only after validation.
func Enroll(ctx context.Context, serverURL, ticket, version string) (Config, error) {
	var config Config
	address, err := endpoint(serverURL, RegisterPath, false)
	if err != nil {
		return config, err
	}
	hello, private, err := NewIdentity(version)
	if err != nil {
		return config, err
	}
	if _, err := hello.Validate(); err != nil {
		return config, err
	}
	if ticket == "" || len(ticket) > 4096 || strings.ContainsAny(ticket, "\r\n") {
		return config, errors.New("invalid enrollment ticket")
	}
	body, err := json.Marshal(RegisterRequest{Ticket: ticket, Hello: hello})
	if err != nil {
		return config, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return config, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("enrollment redirects are refused") }}
	response, err := client.Do(request)
	if err != nil {
		return config, fmt.Errorf("enrollment request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return config, fmt.Errorf("enrollment rejected (HTTP %d)", response.StatusCode)
	}
	var result RegisterResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result); err != nil {
		return config, errors.New("invalid enrollment response")
	}
	if result.ServerID != "" && result.ServerID != result.AgentID {
		return config, errors.New("enrollment identity mismatch")
	}
	config = Config{ServerURL: strings.TrimRight(serverURL, "/"), AgentID: result.AgentID, Token: result.Token, HostKeyPEM: private}
	_, err = config.Validate()
	return config, err
}

func endpoint(base, path string, websocketURL bool) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid panel URL")
	}
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		if parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return "", errors.New("agent requires HTTPS (HTTP is only allowed on loopback for local tests)")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawPath = ""
	if websocketURL {
		if parsed.Scheme == "https" {
			parsed.Scheme = "wss"
		} else {
			parsed.Scheme = "ws"
		}
	}
	return parsed.String(), nil
}

type Agent struct {
	Config  Config
	Version string
	Logger  *log.Logger
	// Dialer and Server can be supplied by local transport tests. Production
	// uses system TLS verification and a server running as the current OS user.
	Dialer *websocket.Dialer
	Server *SSHServer
}

func (a *Agent) Run(ctx context.Context) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return errors.New("this agent supports Linux and Windows")
	}
	if _, err := a.Config.Validate(); err != nil {
		return err
	}
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := a.RunOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		if a.Logger != nil {
			a.Logger.Printf("control disconnected (%v); reconnecting", err)
		}
		if time.Since(started) >= time.Minute {
			delay = time.Second
		}
		var random [1]byte
		_, _ = rand.Read(random[:])
		wait := delay + time.Duration(random[0])*delay/512
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 60*time.Second)
	}
	return ctx.Err()
}

// RunOnce is one control generation. Losing it closes every data tunnel and
// local process belonging to that generation before reconnecting.
func (a *Agent) RunOnce(ctx context.Context) error {
	signer, err := a.Config.Validate()
	if err != nil {
		return err
	}
	hello, err := identity(signer, a.Version)
	if err != nil {
		return err
	}
	address, _ := endpoint(a.Config.ServerURL, ControlPath, true)
	dialer := a.Dialer
	if dialer == nil {
		clone := *websocket.DefaultDialer
		clone.HandshakeTimeout = 15 * time.Second
		dialer = &clone
	}
	headers := http.Header{"Authorization": []string{"Bearer " + a.Config.Token}}
	ws, response, err := dialer.DialContext(ctx, address, headers)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect panel: %w", err)
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	ws.SetReadLimit(16 * 1024)
	_ = ws.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
	if err := ws.WriteJSON(hello); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Now().Add(heartbeatTimeout))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(heartbeatTimeout)) })
	var ready Message
	if err := ws.ReadJSON(&ready); err != nil {
		return err
	}
	if ready.Type != "ready" {
		return errors.New("panel refused agent protocol")
	}
	if a.Logger != nil {
		a.Logger.Printf("connected to panel as agent %s", a.Config.AgentID)
	}
	go heartbeat(ws, ctx.Done())
	server := a.Server
	if server == nil {
		server = &SSHServer{Signer: signer}
	}
	var mu sync.Mutex
	active := make(map[string]context.CancelFunc)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	var writeMu sync.Mutex
	report := func(id string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
		_ = ws.WriteJSON(Message{Type: "error", ID: id, Error: "session could not be opened"})
	}
	for {
		var message Message
		if err := ws.ReadJSON(&message); err != nil {
			return err
		}
		switch message.Type {
		case "cancel":
			mu.Lock()
			stop := active[message.ID]
			mu.Unlock()
			if stop != nil {
				stop()
			}
		case "open":
			if len(message.ID) != 64 || len(message.Secret) != 64 {
				return errors.New("invalid session request")
			}
			mu.Lock()
			if _, exists := active[message.ID]; exists || len(active) >= MaxConnections {
				mu.Unlock()
				report(message.ID)
				continue
			}
			sessionCtx, sessionCancel := context.WithCancel(ctx)
			active[message.ID] = sessionCancel
			mu.Unlock()
			wg.Add(1)
			go func(message Message) {
				defer wg.Done()
				defer sessionCancel()
				defer func() { mu.Lock(); delete(active, message.ID); mu.Unlock() }()
				if err := a.dataSession(sessionCtx, dialer, headers, server, message); err != nil && sessionCtx.Err() == nil {
					report(message.ID)
				}
			}(message)
		default:
			return errors.New("unexpected panel control message")
		}
	}
}

func (a *Agent) dataSession(ctx context.Context, dialer *websocket.Dialer, headers http.Header, server *SSHServer, message Message) error {
	address, _ := endpoint(a.Config.ServerURL, DataPath, true)
	parsed, _ := url.Parse(address)
	query := parsed.Query()
	query.Set("session", message.ID)
	parsed.RawQuery = query.Encode()
	ws, response, err := dialer.DialContext(ctx, parsed.String(), headers)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return err
	}
	return server.Serve(ctx, NewWSConn(ws), message.Secret)
}
