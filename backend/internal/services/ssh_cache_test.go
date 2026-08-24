package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const testSSHPassword = "cache-test-secret"

// startTestSSHServer runs a minimal in-memory SSH server that accepts the
// test password, answers global requests (keepalives), and counts accepted
// connections so tests can assert how many dials actually happened.
func startTestSSHServer(t *testing.T) (addr string, dials *atomic.Int32) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == testSSHPassword {
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var count atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				// DiscardRequests replies false to want-reply globals, which is
				// all the client keepalive needs to consider the link alive.
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.UnknownChannelType, "test server has no channels")
				}
			}()
		}
	}()
	return ln.Addr().String(), &count
}

func testCacheServer(t *testing.T, addr string) *models.Server {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split test server addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return &models.Server{
		ID:          uuid.New(),
		Host:        host,
		Port:        port,
		SSHUsername: "root",
		SSHPassword: testSSHPassword,
	}
}

func newTestCache(t *testing.T) *SSHConnCache {
	t.Helper()
	cache := NewSSHConnCache()
	t.Cleanup(cache.Close)
	return cache
}

func TestSSHConnCacheReusesConnection(t *testing.T) {
	addr, dials := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	first, err := cache.Get(server)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := cache.Get(server)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if first != second {
		t.Fatal("Get returned a different client for an unchanged server")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
}

func TestSSHConnCacheRedialsOnFingerprintChange(t *testing.T) {
	addr, dials := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	first, err := cache.Get(server)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// The server test config accepts only one password, so change a
	// non-authentication field that still alters the fingerprint.
	server.SSHHostKey = ""
	server.SSHUsername = "deploy"
	second, err := cache.Get(server)
	if err != nil {
		t.Fatalf("Get after settings change: %v", err)
	}
	if first == second {
		t.Fatal("Get reused the cached client despite changed settings")
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
	// The replaced connection must be closed, not leaked.
	if err := first.Wait(); err == nil {
		t.Fatal("stale client is still connected after fingerprint change")
	}
}

func TestSSHConnCacheRedialsWhenConnectionDies(t *testing.T) {
	addr, dials := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	first, err := cache.Get(server)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// Kill the transport behind the cache's back; the keepalive health check
	// must notice and redial instead of handing out the dead client.
	first.Close()
	second, err := cache.Get(server)
	if err != nil {
		t.Fatalf("Get after connection death: %v", err)
	}
	if first == second {
		t.Fatal("Get returned the dead client")
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
}

func TestSSHConnCacheDropClosesConnection(t *testing.T) {
	addr, dials := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	first, err := cache.Get(server)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	cache.Drop(server.ID)
	if err := first.Wait(); err == nil {
		t.Fatal("client is still connected after Drop")
	}
	second, err := cache.Get(server)
	if err != nil {
		t.Fatalf("Get after Drop: %v", err)
	}
	if first == second {
		t.Fatal("Get returned the dropped client")
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
}

func TestSSHConnCacheSweepClosesIdleConnections(t *testing.T) {
	addr, _ := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	client, err := cache.Get(server)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Just under the idle timeout the connection must survive.
	cache.sweep(time.Now().Add(sshCacheIdleTimeout - time.Second))
	cache.mu.Lock()
	entry := cache.entries[server.ID]
	cache.mu.Unlock()
	entry.mu.Lock()
	live := entry.client != nil
	entry.mu.Unlock()
	if !live {
		t.Fatal("sweep closed a connection that was not yet idle long enough")
	}

	cache.sweep(time.Now().Add(sshCacheIdleTimeout + time.Second))
	entry.mu.Lock()
	live = entry.client != nil
	entry.mu.Unlock()
	if live {
		t.Fatal("sweep left an expired idle connection open")
	}
	if err := client.Wait(); err == nil {
		t.Fatal("idle client is still connected after sweep")
	}
}

func TestSSHConnCacheConcurrentGetDialsOnce(t *testing.T) {
	addr, dials := startTestSSHServer(t)
	cache := newTestCache(t)
	server := testCacheServer(t, addr)

	const workers = 8
	clients := make([]*ssh.Client, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clients[i], errs[i] = cache.Get(server)
		}()
	}
	wg.Wait()

	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("concurrent Get %d: %v", i, errs[i])
		}
		if clients[i] != clients[0] {
			t.Fatal("concurrent Gets returned different clients")
		}
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
}
