package agenttransport

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type tunnelFixture struct {
	hub           *Hub
	agent         *Agent
	id            uuid.UUID
	cancel        context.CancelFunc
	done          <-chan error
	dir           string
	registeredKey *atomic.Value
}

func newTunnel(t *testing.T) *tunnelFixture {
	t.Helper()
	_, private, err := NewIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	token := strings.Repeat("a", 64)
	hub := NewHub()
	registeredKey := &atomic.Value{}
	registeredKey.Store(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		switch r.URL.Path {
		case ControlPath:
			_ = hub.ServeControl(r.Context(), id, ws, func(hello Hello) error {
				if hello.HostKey != registeredKey.Load().(string) {
					return errors.New("wrong key")
				}
				return nil
			})
		case DataPath:
			_ = hub.AttachData(id, r.URL.Query().Get("session"), ws)
		default:
			_ = ws.Close()
		}
	}))
	dir := t.TempDir()
	agent := &Agent{Config: Config{ServerURL: server.URL, AgentID: id.String(), Token: token, HostKeyPEM: private}, Version: "test", Server: &SSHServer{Signer: signer, WorkingDirectory: dir}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.RunOnce(ctx) }()
	fixture := &tunnelFixture{hub: hub, agent: agent, id: id, cancel: cancel, done: done, dir: dir, registeredKey: registeredKey}
	t.Cleanup(func() {
		cancel()
		hub.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent generation did not stop")
		}
		server.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for !hub.Online(id) {
		if time.Now().After(deadline) {
			t.Fatal("agent did not become online")
		}
		time.Sleep(time.Millisecond)
	}
	return fixture
}

func (f *tunnelFixture) dial(t *testing.T) *ssh.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := f.hub.Dial(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestTunnelSFTPPreservesBytesPermissionsAndExactNames(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	files, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	remote := filepath.ToSlash(filepath.Join(f.dir, "two  spaces 中文.txt"))
	file, err := files.Create(remote)
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{0, 1, 2, 255}, bytes.Repeat([]byte("contents"), 40000)...)
	if _, err := file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := files.Chmod(remote, 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err = files.Open(remote)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("SFTP roundtrip failed: %v", err)
	}
	if err := files.PosixRename(remote, remote+".renamed"); err != nil {
		t.Fatal(err)
	}
	entries, err := files.ReadDir(filepath.ToSlash(f.dir))
	if err != nil || len(entries) != 1 || entries[0].Name() != "two  spaces 中文.txt.renamed" {
		t.Fatalf("listing: %v %v", entries, err)
	}
	if runtime.GOOS != "windows" && entries[0].Mode().Perm() != 0600 {
		t.Fatal("permissions changed")
	}
	if err := files.Remove(remote + ".renamed"); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelConnectionsOwnTheirDeadlines(t *testing.T) {
	f := newTunnel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, conn, err := f.hub.Dial(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second := f.dial(t)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, err := first.SendRequest("keepalive@openssh.com", true, nil); err == nil {
		t.Fatal("first deadline was ignored")
	}
	if _, _, err := second.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("another tunnel was killed: %v", err)
	}
	if !f.hub.Online(f.id) {
		t.Fatal("data deadline killed control connection")
	}
}

func TestControlDisconnectClosesOwnedConnections(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	f.hub.Disconnect(f.id)
	if f.hub.Online(f.id) {
		t.Fatal("disconnected agent remains online")
	}
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSH connection survived agent revocation")
	}
	if _, _, err := f.hub.Dial(context.Background(), f.id); !errors.Is(err, ErrOffline) {
		t.Fatalf("dial offline: %v", err)
	}
}

func TestCanceledDialDoesNotConsumeConnectionCapacity(t *testing.T) {
	f := newTunnel(t)
	for i := 0; i < 40; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client, _, err := f.hub.Dial(ctx, f.id)
		if client != nil {
			client.Close()
		}
		if err == nil {
			t.Fatal("canceled dial succeeded")
		}
	}
	client := f.dial(t)
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentConnectionsAndUnknownChannelRejection(t *testing.T) {
	f := newTunnel(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := f.hub.Dial(ctx, f.id)
			if err != nil {
				t.Error(err)
				return
			}
			defer client.Close()
			if _, _, err := client.OpenChannel("direct-tcpip", nil); err == nil {
				t.Error("forwarding unexpectedly permitted")
			}
		}()
	}
	wg.Wait()
}

func TestPanelURLRefusesPlaintextAndEmbeddedSecrets(t *testing.T) {
	for _, address := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?token=bad", "ftp://example.com"} {
		if _, err := endpoint(address, ControlPath, true); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	if got, err := endpoint("https://example.com/panel", ControlPath, true); err != nil || got != "wss://example.com/panel/api/agent/control" {
		t.Fatal(got, err)
	}
}

func TestConfigSaveDoesNotExposeSecretsOrMutateOnInvalidInput(t *testing.T) {
	_, private, err := NewIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	config := Config{ServerURL: "https://example.com", AgentID: uuid.NewString(), Token: strings.Repeat("x", 64), HostKeyPEM: private}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || got != config {
		t.Fatal("config roundtrip", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("config permissions")
		}
	}
	config.Token = "bad"
	if err := SaveConfig(path, config); err == nil {
		t.Fatal("invalid config saved")
	}
	got, err = LoadConfig(path)
	if err != nil || len(got.Token) != 64 {
		t.Fatal("valid config was damaged")
	}
}

func TestEnrollmentPostsTicketAndIdentityWithoutPlacingSecretsInURL(t *testing.T) {
	var received RegisterRequest
	id := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RegisterPath || r.URL.RawQuery != "" || r.Method != http.MethodPost {
			t.Error("wrong enrollment request")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(RegisterResponse{AgentID: id, ServerID: id, Token: strings.Repeat("t", 64)})
	}))
	defer server.Close()
	config, err := Enroll(context.Background(), server.URL, "one-use-ticket", "test")
	if err != nil {
		t.Fatal(err)
	}
	if received.Ticket != "one-use-ticket" || received.Hello.HostKey == "" || config.AgentID != id {
		t.Fatal("wrong enrollment fields")
	}
	signer, err := config.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if received.Hello.HostKey != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) {
		t.Fatal("enrollment changed identity")
	}
}

func TestAuthorizationIsRecheckedAfterControlBecomesVisible(t *testing.T) {
	hello, _, err := NewIdentity("test")
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	defer hub.Close()
	id := uuid.New()
	calls := 0
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		finished <- hub.ServeControl(r.Context(), id, ws, func(Hello) error {
			calls++
			if calls == 2 {
				hub.mu.Lock()
				attached := hub.controls[id] != nil
				hub.mu.Unlock()
				if !attached {
					t.Error("post-registration recheck ran too early")
				}
				return errors.New("revoked")
			}
			return nil
		})
	}))
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("revocation was ignored")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authorization recheck hung")
	}
	if calls != 2 || hub.Online(id) {
		t.Fatal("unauthorized control remained attached")
	}
}

func TestReplacementGenerationClosesOldSessionsWithoutGoingOffline(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	f.hub.mu.Lock()
	old := f.hub.controls[f.id]
	f.hub.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.agent.RunOnce(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("replacement agent did not stop")
		}
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		f.hub.mu.Lock()
		current := f.hub.controls[f.id]
		f.hub.mu.Unlock()
		if current != nil && current != old && f.hub.Online(f.id) {
			break
		}
		if time.Now().After(until) {
			t.Fatal("replacement not attached")
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
		t.Fatal("old generation kept session alive")
	}
	fresh := f.dial(t)
	if _, _, err := fresh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReconnectsAfterControlDisconnectAndStopsOnCancellation(t *testing.T) {
	f := newTunnel(t)
	f.hub.mu.Lock()
	original := f.hub.controls[f.id]
	f.hub.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.agent.Run(ctx) }()
	defer cancel()
	wait := func(predicate func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(until) {
				t.Fatal("agent did not reconnect")
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(func() bool {
		f.hub.mu.Lock()
		defer f.hub.mu.Unlock()
		return f.hub.controls[f.id] != nil && f.hub.controls[f.id] != original && f.hub.controls[f.id].ready
	})
	f.hub.Disconnect(f.id)
	wait(func() bool { return f.hub.Online(f.id) })
	client := f.dial(t)
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent shutdown did not finish")
	}
}

func TestPinnedDialRefusesReenrolledMachineWhileUnpinnedDialFollowsIt(t *testing.T) {
	f := newTunnel(t)
	oldKey := f.registeredKey.Load().(string)
	client, _, err := f.hub.Dial(WithExpectedHostKey(context.Background(), oldKey), f.id)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	hello, private, err := NewIdentity("replacement")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		t.Fatal(err)
	}
	replacement := *f.agent
	replacement.Config.HostKeyPEM = private
	replacement.Server = &SSHServer{Signer: signer, WorkingDirectory: f.dir}
	f.registeredKey.Store(hello.HostKey)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- replacement.RunOnce(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("replacement did not stop")
		}
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		f.hub.mu.Lock()
		current := f.hub.controls[f.id]
		ready := current != nil && current.ready && current.hello.HostKey == hello.HostKey
		f.hub.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(until) {
			t.Fatal("replacement did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	if client, _, err := f.hub.Dial(WithExpectedHostKey(context.Background(), oldKey), f.id); !errors.Is(err, ErrAgentIdentityChanged) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("old approved identity reached replacement: %v", err)
	}
	for _, pin := range []string{"", "invalid", "ssh-ed25519 broken"} {
		if _, _, err := f.hub.Dial(WithExpectedHostKey(context.Background(), pin), f.id); !errors.Is(err, ErrAgentIdentityChanged) {
			t.Fatal("invalid pin did not fail closed", err)
		}
	}
	fresh := f.dial(t)
	if _, _, err := fresh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal("ordinary dial no longer follows enrollment", err)
	}
	pinned, _, err := f.hub.Dial(WithExpectedHostKey(context.Background(), hello.HostKey), f.id)
	if err != nil {
		t.Fatal("new approved identity did not connect", err)
	}
	pinned.Close()
}

func TestPinnedSSHHandshakeChecksActualDataChannelKey(t *testing.T) {
	f := newTunnel(t)
	key := f.registeredKey.Load().(string)
	_, private, err := NewIdentity("different-data-key")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		t.Fatal(err)
	}
	replacement := *f.agent
	replacement.Server = &SSHServer{Signer: signer, WorkingDirectory: f.dir}
	f.hub.mu.Lock()
	original := f.hub.controls[f.id]
	f.hub.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- replacement.RunOnce(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("test agent did not stop")
		}
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		f.hub.mu.Lock()
		current := f.hub.controls[f.id]
		ready := current != nil && current != original && current.ready
		f.hub.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(until) {
			t.Fatal("test agent not ready")
		}
		time.Sleep(time.Millisecond)
	}
	client, _, err := f.hub.Dial(WithExpectedHostKey(context.Background(), key), f.id)
	if err == nil {
		client.Close()
		t.Fatal("data channel with different key passed pinned handshake")
	}
	if !strings.Contains(err.Error(), ErrAgentIdentityChanged.Error()) {
		t.Fatalf("unexpected handshake failure: %v", err)
	}
}

func TestPinnedDialAlsoRequiresTheReviewedRuntimeAccountAndOS(t *testing.T) {
	f := newTunnel(t)
	f.hub.mu.Lock()
	hello := f.hub.controls[f.id].hello
	f.hub.mu.Unlock()
	for _, runtime := range []struct{ username, platform string }{
		{"different-service-account", hello.OS},
		{hello.Username, map[string]string{"linux": "windows", "windows": "linux"}[hello.OS]},
		{"", hello.OS},
		{hello.Username, ""},
	} {
		ctx := WithExpectedIdentity(context.Background(), hello.HostKey, runtime.username, runtime.platform)
		client, _, err := f.hub.Dial(ctx, f.id)
		if client != nil {
			client.Close()
		}
		if !errors.Is(err, ErrAgentIdentityChanged) {
			t.Fatalf("accepted changed runtime %q/%q: %v", runtime.username, runtime.platform, err)
		}
	}
	ctx := WithExpectedIdentity(context.Background(), hello.HostKey, hello.Username, hello.OS)
	client, _, err := f.hub.Dial(ctx, f.id)
	if err != nil {
		t.Fatal("exact reviewed runtime did not connect", err)
	}
	defer client.Close()
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal("runtime pin did not preserve the real SSH path", err)
	}
}
