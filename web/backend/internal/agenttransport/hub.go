package agenttransport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

const heartbeatInterval = 20 * time.Second
const heartbeatTimeout = 65 * time.Second
const controlWriteTimeout = 10 * time.Second

type control struct {
	ws          *websocket.Conn
	writeGate   chan struct{}
	done        chan struct{}
	hello       Hello
	key         ssh.PublicKey
	connections map[*WSConn]struct{}
	ready       bool
}

func (c *control) send(m Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), controlWriteTimeout)
	defer cancel()
	return c.sendContext(ctx, m)
}

func (c *control) sendContext(ctx context.Context, m Message) error {
	select {
	case c.writeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrOffline
	}
	defer func() { <-c.writeGate }()
	deadline := time.Now().Add(controlWriteTimeout)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	_ = c.ws.SetWriteDeadline(deadline)
	return c.ws.WriteJSON(m)
}

type pendingConnection struct {
	agent   uuid.UUID
	control *control
	secret  string
	data    chan *WSConn
	failure chan error
}

type Hub struct {
	mu       sync.Mutex
	controls map[uuid.UUID]*control
	pending  map[string]*pendingConnection
	closed   bool
}

func NewHub() *Hub {
	return &Hub{controls: make(map[uuid.UUID]*control), pending: make(map[string]*pendingConnection)}
}

// ServeControl takes ownership of an already authenticated WebSocket. onHello
// must verify the registered host key; replacing a key requires reenrollment.
// A replacement connection retires all sessions belonging to its predecessor.
func (h *Hub) ServeControl(ctx context.Context, agentID uuid.UUID, ws *websocket.Conn, onHello func(Hello) error) error {
	defer ws.Close()
	if agentID == uuid.Nil {
		return errors.New("missing agent identity")
	}
	ws.SetReadLimit(16 * 1024)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	var hello Hello
	if err := ws.ReadJSON(&hello); err != nil {
		return err
	}
	key, err := hello.Validate()
	if err != nil {
		return err
	}
	if onHello != nil {
		if err := onHello(hello); err != nil {
			return err
		}
	}
	c := &control{ws: ws, writeGate: make(chan struct{}, 1), done: make(chan struct{}), hello: hello, key: key, connections: make(map[*WSConn]struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return net.ErrClosed
	}
	old := h.controls[agentID]
	h.controls[agentID] = c
	h.mu.Unlock()
	if old != nil {
		h.retire(old)
	}
	defer func() {
		h.mu.Lock()
		if h.controls[agentID] == c {
			delete(h.controls, agentID)
		}
		h.mu.Unlock()
		h.retire(c)
	}()
	// Authorization can be revoked between the HTTP upgrade and insertion.
	// Recheck after publishing: revoke either sees this generation or this
	// callback observes the deleted/rotated database credential.
	if onHello != nil {
		if err := onHello(hello); err != nil {
			return err
		}
	}
	if err := c.send(Message{Type: "ready"}); err != nil {
		return err
	}
	h.mu.Lock()
	if h.controls[agentID] != c {
		h.mu.Unlock()
		return ErrOffline
	}
	c.ready = true
	h.mu.Unlock()
	_ = ws.SetReadDeadline(time.Now().Add(heartbeatTimeout))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(heartbeatTimeout)) })
	go heartbeat(ws, c.done)
	for {
		var message Message
		if err := ws.ReadJSON(&message); err != nil {
			return err
		}
		if message.Type != "error" || message.ID == "" {
			return errors.New("unexpected agent control message")
		}
		h.mu.Lock()
		pending := h.pending[message.ID]
		if pending != nil && pending.control == c {
			select {
			case pending.failure <- errors.New("agent could not open the requested session"):
			default:
			}
		}
		h.mu.Unlock()
	}
}

func heartbeat(ws *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(controlWriteTimeout)); err != nil {
				_ = ws.Close()
				return
			}
		}
	}
}

// retire never closes a connection while holding h.mu: its close callback also
// removes the connection from the Hub. It is safe to call more than once.
func (h *Hub) retire(c *control) {
	h.mu.Lock()
	select {
	case <-c.done:
		h.mu.Unlock()
		return
	default:
		close(c.done)
	}
	connections := make([]*WSConn, 0, len(c.connections))
	for conn := range c.connections {
		connections = append(connections, conn)
	}
	for id, pending := range h.pending {
		if pending.control == c {
			delete(h.pending, id)
		}
	}
	h.mu.Unlock()
	_ = c.ws.Close()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

// AttachData consumes a single pending request. HTTP must authenticate this
// data socket against the database again; the request ID is not authorization.
// On success ownership transfers to Hub.Dial. On failure the socket is closed.
func (h *Hub) AttachData(agentID uuid.UUID, requestID string, ws *websocket.Conn) error {
	h.mu.Lock()
	p := h.pending[requestID]
	if h.closed || p == nil || p.agent != agentID || h.controls[agentID] != p.control {
		h.mu.Unlock()
		_ = ws.Close()
		return ErrUnknownSession
	}
	select {
	case <-p.control.done:
		h.mu.Unlock()
		_ = ws.Close()
		return ErrOffline
	default:
	}
	delete(h.pending, requestID)
	var conn *WSConn
	conn = newWSConn(ws, func() { h.mu.Lock(); delete(p.control.connections, conn); h.mu.Unlock() })
	p.control.connections[conn] = struct{}{}
	p.data <- conn
	h.mu.Unlock()
	return nil
}

// Dial returns an independently owned SSH connection and the raw stream used
// for per-client deadlines. Context cancellation only controls dialing; after
// success the caller owns client.Close(), as with an ordinary SSH dial.
func (h *Hub) Dial(ctx context.Context, agentID uuid.UUID) (*ssh.Client, net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var expected ssh.PublicKey
	if encoded, pinned := ctx.Value(expectedHostKeyContextKey{}).(string); pinned {
		key, _, _, rest, parseErr := ssh.ParseAuthorizedKey([]byte(encoded))
		if parseErr != nil || len(strings.TrimSpace(string(rest))) != 0 {
			return nil, nil, ErrAgentIdentityChanged
		}
		expected = key
	}
	runtime, runtimePinned := ctx.Value(expectedRuntimeContextKey{}).(expectedRuntime)
	if runtimePinned && (runtime.username == "" || runtime.platform != "linux" && runtime.platform != "windows") {
		return nil, nil, ErrAgentIdentityChanged
	}
	id, err := randomSecret()
	if err != nil {
		return nil, nil, err
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, nil, err
	}
	h.mu.Lock()
	c := h.controls[agentID]
	if h.closed || c == nil || !c.ready {
		h.mu.Unlock()
		return nil, nil, ErrOffline
	}
	if expected != nil && !bytes.Equal(expected.Marshal(), c.key.Marshal()) {
		h.mu.Unlock()
		return nil, nil, ErrAgentIdentityChanged
	}
	if runtimePinned && (runtime.username != c.hello.Username || runtime.platform != c.hello.OS) {
		h.mu.Unlock()
		return nil, nil, ErrAgentIdentityChanged
	}
	count := len(c.connections)
	for _, p := range h.pending {
		if p.control == c {
			count++
		}
	}
	if count >= MaxConnections {
		h.mu.Unlock()
		return nil, nil, ErrBusy
	}
	p := &pendingConnection{agent: agentID, control: c, secret: secret, data: make(chan *WSConn, 1), failure: make(chan error, 1)}
	h.pending[id] = p
	h.mu.Unlock()
	transferred := false
	defer func() {
		var abandoned *WSConn
		h.mu.Lock()
		delete(h.pending, id)
		if !transferred {
			select {
			case conn := <-p.data:
				abandoned = conn
			default:
			}
		}
		h.mu.Unlock()
		if abandoned != nil {
			_ = abandoned.Close()
		}
		if !transferred {
			go func() { _ = c.send(Message{Type: "cancel", ID: id}) }()
		}
	}()
	if err := c.sendContext(ctx, Message{Type: "open", ID: id, Secret: secret}); err != nil {
		return nil, nil, err
	}
	var conn *WSConn
	select {
	case conn = <-p.data:
	case err := <-p.failure:
		return nil, nil, err
	case <-c.done:
		return nil, nil, ErrOffline
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	config := &ssh.ClientConfig{User: c.hello.Username, Auth: []ssh.AuthMethod{ssh.Password(secret)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if expected != nil && !bytes.Equal(key.Marshal(), expected.Marshal()) {
				return ErrAgentIdentityChanged
			}
			if !bytes.Equal(key.Marshal(), c.key.Marshal()) {
				return errors.New("agent SSH host key changed")
			}
			return nil
		}}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, "agent:"+agentID.String(), config)
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = sshConn.Close()
		return nil, nil, err
	}
	if !stop() {
		_ = sshConn.Close()
		return nil, nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	transferred = true
	return ssh.NewClient(sshConn, channels, requests), conn, nil
}

func (h *Hub) Online(id uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.controls[id]
	if c == nil || h.closed || !c.ready {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (h *Hub) Disconnect(id uuid.UUID) {
	h.mu.Lock()
	c := h.controls[id]
	delete(h.controls, id)
	h.mu.Unlock()
	if c != nil {
		h.retire(c)
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	controls := make([]*control, 0, len(h.controls))
	for _, c := range h.controls {
		controls = append(controls, c)
	}
	h.controls = make(map[uuid.UUID]*control)
	h.mu.Unlock()
	for _, c := range controls {
		h.retire(c)
	}
}

func randomSecret() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate session identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
