package services

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

func TestTerminalSessionWritesBinaryChunks(t *testing.T) {
	// The first two chunks split the UTF-8 encoding of a Chinese character.
	// The last also covers bytes which cannot appear in a websocket text frame.
	chunks := [][]byte{{0xe4, 0xbd}, {0xa0, '\r', '\n'}, {0xff, 0x00}}
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		session := &TerminalSession{conn: conn}
		for _, chunk := range chunks {
			n, err := session.Write(chunk)
			if err != nil || n != len(chunk) {
				done <- fmt.Errorf("Write returned (%d, %v), want (%d, nil)", n, err, len(chunk))
				return
			}
		}
		done <- nil
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, want := range chunks {
		kind, got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read chunk %d: %v", i, err)
		}
		if kind != websocket.BinaryMessage || !bytes.Equal(got, want) {
			t.Fatalf("chunk %d = type %d, bytes %x; want binary, %x", i, kind, got, want)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDialSSHClientConnTimesOutSilentHandshake(t *testing.T) {
	const timeout = 100 * time.Millisecond
	testSilentSSHHandshake(t, timeout, func(host string, port int) (*ssh.Client, net.Conn, error) {
		return dialSSHClientConn(host, port, "root", testSSHPassword, "", "", timeout)
	})
}

func TestDialSSHTimesOutSilentHandshake(t *testing.T) {
	testSilentSSHHandshake(t, sshDialTimeout, func(host string, port int) (*ssh.Client, net.Conn, error) {
		client, err := DialSSH(host, port, "root", testSSHPassword, "", "")
		return client, nil, err
	})
}

func testSilentSSHHandshake(t *testing.T, timeout time.Duration, dial func(string, int) (*ssh.Client, net.Conn, error)) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerClosed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerClosed <- err
			return
		}
		defer conn.Close()
		// Read the client's banner but never send a server banner. The client
		// must time out and close the TCP connection instead of waiting forever.
		_ = conn.SetReadDeadline(time.Now().Add(timeout + 5*time.Second))
		_, err = io.Copy(io.Discard, conn)
		peerClosed <- err
	}()
	server := testCacheServer(t, listener.Addr().String())
	started := time.Now()
	client, conn, err := dial(server.Host, server.Port)
	if client != nil {
		defer client.Close()
	}
	if conn != nil {
		defer conn.Close()
	}
	if client != nil || conn != nil || err == nil {
		t.Fatalf("dial returned (%v, %v, %v), want nil connections and timeout", client, conn, err)
	}
	var networkErr net.Error
	if !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("dial error = %v, want network timeout", err)
	}
	if elapsed := time.Since(started); elapsed > timeout+2*time.Second {
		t.Fatalf("silent handshake took %s for a %s timeout", elapsed, timeout)
	}
	select {
	case err := <-peerClosed:
		if err != nil {
			t.Fatalf("timed-out handshake did not close its transport: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out handshake left its transport open")
	}
}

func TestDialSSHClientConnClearsHandshakeDeadline(t *testing.T) {
	addr, _ := startTestSSHServer(t)
	server := testCacheServer(t, addr)
	client, _, err := dialSSHClientConn(server.Host, server.Port, server.SSHUsername, server.SSHPassword, "", "", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	time.Sleep(250 * time.Millisecond)
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("connection failed after its handshake deadline: %v", err)
	}
}

func TestDialSSHConnects(t *testing.T) {
	addr, _ := startTestSSHServer(t)
	server := testCacheServer(t, addr)
	client, err := DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
}
