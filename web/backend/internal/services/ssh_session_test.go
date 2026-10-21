package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Keep channel acceptance under the test's control while the real SSH
// transport continues handling sibling channels and global keepalives.
func startSessionTestSSHServer(t *testing.T) (string, <-chan ssh.NewChannel, *atomic.Int32) {
	t.Helper()
	return startSessionTestSSHServerWithSilence(t, nil)
}

// Dropping server writes after handshake simulates a peer that accepts the
// channel but stops responding to exec and close requests.
type silentSSHTestConn struct {
	net.Conn
	silent     *atomic.Bool
	pauseReads *atomic.Bool
	closed     chan struct{}
	closeOnce  sync.Once
}

func (conn *silentSSHTestConn) Write(data []byte) (int, error) {
	if conn.silent != nil && conn.silent.Load() {
		return len(data), nil
	}
	return conn.Conn.Write(data)
}

func (conn *silentSSHTestConn) Read(data []byte) (int, error) {
	n, err := conn.Conn.Read(data)
	if conn.pauseReads != nil && conn.pauseReads.Load() {
		// Stop delivery even if Read was already pending when the test paused
		// it. Closing the test connection always releases this wait.
		<-conn.closed
		return 0, net.ErrClosed
	}
	return n, err
}

func (conn *silentSSHTestConn) Close() error {
	conn.closeOnce.Do(func() { close(conn.closed) })
	return conn.Conn.Close()
}

func startSessionTestSSHServerWithSilence(t *testing.T, silent *atomic.Bool) (string, <-chan ssh.NewChannel, *atomic.Int32) {
	t.Helper()
	return startSessionTestSSHServerWithControls(t, silent, nil)
}

func startSessionTestSSHServerWithControls(t *testing.T, silent, pauseReads *atomic.Bool) (string, <-chan ssh.NewChannel, *atomic.Int32) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recordTestSSHHostKey(t, listener.Addr().String(), signer)
	channels := make(chan ssh.NewChannel, 16)
	stopped, accepted := make(chan struct{}), make(chan struct{})
	var connections sync.Map
	var dials atomic.Int32
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if silent != nil || pauseReads != nil {
				conn = &silentSSHTestConn{Conn: conn, silent: silent, pauseReads: pauseReads, closed: make(chan struct{})}
			}
			connections.Store(conn, struct{}{})
			dials.Add(1)
			go func() {
				defer conn.Close()
				defer connections.Delete(conn)
				_, incoming, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for channel := range incoming {
					select {
					case channels <- channel:
					case <-stopped:
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		close(stopped)
		listener.Close()
		<-accepted
		connections.Range(func(conn, _ any) bool { _ = conn.(net.Conn).Close(); return true })
	})
	return listener.Addr().String(), channels, &dials
}

func awaitSessionChannel(t *testing.T, channels <-chan ssh.NewChannel) ssh.NewChannel {
	t.Helper()
	select {
	case channel := <-channels:
		return channel
	case <-time.After(2 * time.Second):
		t.Fatal("SSH session did not request a channel")
		return nil
	}
}

func assertLateSessionClosed(t *testing.T, incoming ssh.NewChannel) {
	t.Helper()
	channel, requests, err := incoming.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	select {
	case request, ok := <-requests:
		if ok {
			t.Fatalf("canceled session unexpectedly sent %q", request.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late SSH session was not closed")
	}
}

func awaitCanceledSSH(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the peer's channel reply")
	}
}

func TestSSHSessionCancellationCleansLateChannelWithoutClosingSibling(t *testing.T) {
	t.Parallel()
	addr, channels, _ := startSessionTestSSHServer(t)
	cache := newTestCache(t)
	client, err := cache.Get(testCacheServer(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	// Keep an independent session alive while another session is canceled.
	sibling := make(chan *ssh.Session, 1)
	go func() { session, _ := client.NewSession(); sibling <- session }()
	siblingChannel, requests, err := awaitSessionChannel(t, channels).Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer siblingChannel.Close()
	go ssh.DiscardRequests(requests)
	siblingSession := <-sibling
	if siblingSession == nil {
		t.Fatal("sibling session did not open")
	}
	defer siblingSession.Close()
	output, err := siblingSession.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := newSSHSession(ctx, client); result <- err }()
	incoming := awaitSessionChannel(t, channels)
	cancel()
	awaitCanceledSSH(t, result)
	assertLateSessionClosed(t, incoming)
	// A completed cleanup must disarm the connection-close watchdog.
	time.Sleep(sshSessionCleanupGrace + 50*time.Millisecond)
	if _, err := siblingChannel.Write([]byte("still alive")); err != nil {
		t.Fatalf("cancellation closed a sibling session: %v", err)
	}
	read := make(chan error, 1)
	go func() {
		buffer := make([]byte, len("still alive"))
		_, err := io.ReadFull(output, buffer)
		if err == nil && string(buffer) != "still alive" {
			err = fmt.Errorf("sibling output = %q", buffer)
		}
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sibling output stalled after cancellation")
	}
}

func TestSSHSessionUnresponsiveOpenClosesTransportAndCacheRedials(t *testing.T) {
	t.Parallel()
	addr, channels, dials := startSessionTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)
	client, err := cache.Get(server)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := newSSHSession(ctx, client); result <- err }()
	awaitSessionChannel(t, channels) // Deliberately never accept or reject it.
	cancel()
	awaitCanceledSSH(t, result)
	closed := make(chan error, 1)
	go func() { closed <- client.Wait() }()
	select {
	case <-closed:
	case <-time.After(sshSessionCleanupGrace + 2*time.Second):
		t.Fatal("unanswered channel request retained its SSH transport")
	}
	fresh, err := cache.Get(server)
	if err != nil {
		t.Fatalf("Get after cleanup: %v", err)
	}
	if fresh == client || dials.Load() != 2 {
		t.Fatal("cache did not replace the transport closed by cleanup")
	}
}

func TestSSHSessionLateChannelWithoutCloseAcknowledgmentReleasesTransport(t *testing.T) {
	t.Parallel()
	var pauseReads atomic.Bool
	addr, channels, _ := startSessionTestSSHServerWithControls(t, nil, &pauseReads)
	client, err := newTestCache(t).Get(testCacheServer(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := newSSHSession(ctx, client); result <- err }()
	incoming := awaitSessionChannel(t, channels)
	cancel()
	awaitCanceledSSH(t, result)
	// The late response includes EOF, but deliberately omits channel-close.
	// EOF alone must not disarm cleanup while SSH's request stream stays open.
	pauseReads.Store(true)
	channel, requests, err := incoming.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	_ = channel.CloseWrite()
	go ssh.DiscardRequests(requests)
	closed := make(chan error, 1)
	go func() { closed <- client.Wait() }()
	select {
	case <-closed:
	case <-time.After(sshSessionCleanupGrace + 2*time.Second):
		t.Fatal("late session disarmed cleanup before channel-close acknowledgment")
	}
}

func TestSSHCallersCancelWhileOpeningSession(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *ssh.Client) error
	}{
		{"docker", func(ctx context.Context, client *ssh.Client) error {
			_, err := RunDockerContext(ctx, client, "ps", 1024)
			return err
		}},
		{"sftp", func(ctx context.Context, client *ssh.Client) error {
			_, err := NewSFTPFiles(ctx, client, "linux")
			return err
		}},
		{"sftp-page", func(ctx context.Context, client *ssh.Client) error {
			store := &sftpFiles{transport: client, context: ctx}
			_, err := store.Page("/", 0, 50)
			return err
		}},
		{"container-probe", func(ctx context.Context, client *ssh.Client) error {
			_, err := NewContainerFiles(ctx, client, strings.Repeat("a", 64), "linux")
			return err
		}},
		{"container-command", func(ctx context.Context, client *ssh.Client) error {
			store := &containerFiles{client: client, context: ctx, command: "docker", containerID: strings.Repeat("a", 64)}
			_, err := store.List("/")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr, channels, _ := startSessionTestSSHServer(t)
			client, err := newTestCache(t).Get(testCacheServer(t, addr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- test.run(ctx, client) }()
			incoming := awaitSessionChannel(t, channels)
			cancel()
			awaitCanceledSSH(t, result)
			assertLateSessionClosed(t, incoming)
		})
	}
}

func TestDockerBoundedTimeoutIncludesChannelCreationAndSkipsRetry(t *testing.T) {
	addr, channels, _ := startSessionTestSSHServer(t)
	client, err := newTestCache(t).Get(testCacheServer(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := RunDockerContext(ctx, client, "ps", 1024)
		result <- err
	}()
	incoming := awaitSessionChannel(t, channels)
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want typed context timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded command did not time out during channel creation")
	}
	assertLateSessionClosed(t, incoming)
	select {
	case <-channels:
		t.Fatal("Docker retried a timed-out command with sudo")
	default:
	}
}

func TestSSHCommandCancellationPreservesSharedConnection(t *testing.T) {
	t.Parallel()
	addr, channels, _ := startSessionTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)
	client, err := cache.Get(server)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := runCmdContext(ctx, client, "sleep 60", 1024); result <- err }()
	channel, requests, err := awaitSessionChannel(t, channels).Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	select {
	case request := <-requests:
		if request.Type != "exec" {
			t.Fatalf("request = %s, want exec", request.Type)
		}
		if err := request.Reply(true, nil); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not start")
	}
	cancel()
	awaitCanceledSSH(t, result)
	select {
	case _, ok := <-requests:
		if ok {
			t.Fatal("unexpected request while closing the command")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled command's session stayed open")
	}
	time.Sleep(sshSessionCleanupGrace + 50*time.Millisecond)
	reused, err := cache.Get(server)
	if err != nil || reused != client {
		t.Fatalf("command cancellation killed the shared connection: reused=%v err=%v", reused == client, err)
	}
}

func TestSSHCommandUnresponsiveExecAndCloseReleasesTransport(t *testing.T) {
	t.Parallel()
	var silent atomic.Bool
	addr, channels, _ := startSessionTestSSHServerWithSilence(t, &silent)
	client, err := newTestCache(t).Get(testCacheServer(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := runCmdContext(ctx, client, "sleep 60", 1024); result <- err }()
	channel, requests, err := awaitSessionChannel(t, channels).Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	select {
	case request := <-requests:
		if request.Type != "exec" {
			t.Fatalf("request = %s, want exec", request.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not request execution")
	}
	// Do not answer exec, and suppress the transport's automatic close reply.
	silent.Store(true)
	cancel()
	awaitCanceledSSH(t, result)
	closed := make(chan error, 1)
	go func() { closed <- client.Wait() }()
	select {
	case <-closed:
	case <-time.After(sshSessionCleanupGrace + 2*time.Second):
		t.Fatal("unresponsive exec/close retained its SSH transport")
	}
}

func TestDockerContextRetainsFallbackAndOutputLimit(t *testing.T) {
	addr, channels, _ := startSessionTestSSHServer(t)
	client, err := newTestCache(t).Get(testCacheServer(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		output, err := RunDockerContext(ctx, client, "ps", 128)
		if err == nil || err.Error() != "docker output limit exceeded" || output != "" {
			result <- fmt.Errorf("output=%q error=%v, want output limit error", output, err)
			return
		}
		result <- nil
	}()
	for _, expected := range []string{"docker ps", "sudo -n docker ps"} {
		channel, requests, err := awaitSessionChannel(t, channels).Accept()
		if err != nil {
			t.Fatal(err)
		}
		request := <-requests
		var payload struct{ Command string }
		if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != expected {
			t.Fatalf("command = %q, want %q", payload.Command, expected)
		}
		_ = request.Reply(true, nil)
		status := uint32(1)
		if !strings.HasPrefix(expected, "sudo") {
			_, _ = channel.Stderr().Write([]byte("permission denied connecting to docker daemon socket docker.sock"))
		}
		if strings.HasPrefix(expected, "sudo") {
			_, _ = channel.Write([]byte(strings.Repeat("x", 129)))
			status = 0
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		_ = channel.Close()
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestDockerOutputLimitDoesNotRepeatCommandWithSudo(t *testing.T) {
	for _, runner := range []string{"context", "bounded"} {
		t.Run(runner, func(t *testing.T) {
			addr, channels, _ := startSessionTestSSHServer(t)
			client, err := newTestCache(t).Get(testCacheServer(t, addr))
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				if runner == "context" {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					_, err := RunDockerContext(ctx, client, "ps", 4)
					if !errors.Is(err, errDockerOutputLimit) {
						result <- fmt.Errorf("error = %v, want output limit", err)
						return
					}
				} else {
					output, truncated, err := RunCmdBounded(client, "docker ps", time.Second, 4)
					var exit *ssh.ExitError
					if !truncated || output != "too " || !errors.As(err, &exit) {
						result <- fmt.Errorf("output=%q truncated=%v err=%v", output, truncated, err)
						return
					}
				}
				result <- nil
			}()
			channel, requests, err := awaitSessionChannel(t, channels).Accept()
			if err != nil {
				t.Fatal(err)
			}
			request := <-requests
			_ = request.Reply(true, nil)
			_, _ = channel.Write([]byte("too much output"))
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
			_ = channel.Close()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("Docker started a second command after exceeding the output limit")
			}
			select {
			case <-channels:
				t.Fatal("Docker retried truncated output with sudo")
			default:
			}
		})
	}
}
