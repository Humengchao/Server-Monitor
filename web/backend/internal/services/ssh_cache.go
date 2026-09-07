package services

import (
	"net"
	"sync"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	sshCacheIdleTimeout   = 5 * time.Minute
	sshCacheSweepInterval = 1 * time.Minute
	sshKeepaliveTimeout   = 3 * time.Second
)

// SSHConnCache reuses SSH connections across interactive API calls (Docker
// management), so each request doesn't pay a full TCP + SSH handshake.
// Entries are keyed by server ID and validated against a fingerprint of the
// connection settings, so edits redial transparently. A cached connection is
// health-checked with a keepalive before reuse and closed once idle.
//
// This cache is intentionally separate from the Collector's pool: the
// Collector arms collection deadlines on its transports and drops them on
// poll timeouts, which would tear down connections mid-use for API requests.
type SSHConnCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]*sshCacheEntry
	stop    sync.Once
	stopCh  chan struct{}
}

// sshCacheEntry.mu serializes health checks and redials per server, so a burst
// of requests for one server performs a single dial. lastUsed and dropped are
// guarded by the same mutex.
type sshCacheEntry struct {
	mu          sync.Mutex
	client      *ssh.Client
	conn        net.Conn
	fingerprint [32]byte
	lastUsed    time.Time
	dropped     bool
}

func NewSSHConnCache() *SSHConnCache {
	c := &SSHConnCache{
		entries: make(map[uuid.UUID]*sshCacheEntry),
		stopCh:  make(chan struct{}),
	}
	go c.sweepLoop()
	return c
}

// Get returns a live SSH client for the server, reusing the cached connection
// when the settings fingerprint matches and the transport still responds.
// Returned clients are shared: callers may open sessions concurrently but must
// never Close them.
func (c *SSHConnCache) Get(s *models.Server) (*ssh.Client, error) {
	fingerprint := serverPollFingerprint(s)
	for {
		c.mu.Lock()
		e := c.entries[s.ID]
		if e == nil {
			e = &sshCacheEntry{}
			c.entries[s.ID] = e
		}
		c.mu.Unlock()

		e.mu.Lock()
		if e.dropped {
			// Lost a race with Drop: this entry is no longer in the map, so a
			// dial stored here would leak. Start over with a fresh entry.
			e.mu.Unlock()
			continue
		}
		client, err := c.ensureLive(e, s, fingerprint)
		e.mu.Unlock()
		return client, err
	}
}

// ensureLive returns the entry's connection, redialing when it is missing,
// stale, or unresponsive. Callers must hold e.mu.
func (c *SSHConnCache) ensureLive(e *sshCacheEntry, s *models.Server, fingerprint [32]byte) (*ssh.Client, error) {
	if e.client != nil {
		if e.fingerprint == fingerprint && keepaliveOK(e.client, e.conn) {
			e.lastUsed = time.Now()
			return e.client, nil
		}
		e.client.Close()
		e.client = nil
		e.conn = nil
	}
	client, conn, err := dialSSHClientConn(s.Host, s.Port, s.SSHUsername, s.SSHPassword, s.SSHKey, s.SSHHostKey, sshDialTimeout)
	if err != nil {
		return nil, err
	}
	e.client = client
	e.conn = conn
	e.fingerprint = fingerprint
	e.lastUsed = time.Now()
	return client, nil
}

// keepaliveOK verifies the cached transport still responds within a short
// deadline. Servers reply (if only with a failure) to unknown global requests,
// and x/crypto/ssh surfaces transport errors, so any reply means the link is
// live. The deadline is armed on the raw connection because SendRequest has no
// timeout of its own and would otherwise block on a half-dead peer.
func keepaliveOK(client *ssh.Client, conn net.Conn) bool {
	if err := conn.SetDeadline(time.Now().Add(sshKeepaliveTimeout)); err != nil {
		return false
	}
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		return false
	}
	return conn.SetDeadline(time.Time{}) == nil
}

// Drop closes and forgets the cached connection for a server. Call when the
// server is deleted so no idle connection outlives it.
func (c *SSHConnCache) Drop(serverID uuid.UUID) {
	c.mu.Lock()
	e := c.entries[serverID]
	delete(c.entries, serverID)
	c.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	e.dropped = true
	if e.client != nil {
		e.client.Close()
		e.client = nil
		e.conn = nil
	}
	e.mu.Unlock()
}

// Close stops the sweeper and closes every cached connection.
func (c *SSHConnCache) Close() {
	c.stop.Do(func() { close(c.stopCh) })
	c.mu.Lock()
	entries := c.entries
	c.entries = make(map[uuid.UUID]*sshCacheEntry)
	c.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		e.dropped = true
		if e.client != nil {
			e.client.Close()
			e.client = nil
			e.conn = nil
		}
		e.mu.Unlock()
	}
}

func (c *SSHConnCache) sweepLoop() {
	ticker := time.NewTicker(sshCacheSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.sweep(time.Now())
		}
	}
}

// sweep closes connections that have sat idle past the timeout. Entries stay
// in the map so an in-flight Get never stores a fresh dial into a forgotten
// entry; empty entries are a few words each and bounded by the server count.
func (c *SSHConnCache) sweep(now time.Time) {
	c.mu.Lock()
	entries := make([]*sshCacheEntry, 0, len(c.entries))
	for _, e := range c.entries {
		entries = append(entries, e)
	}
	c.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		if e.client != nil && now.Sub(e.lastUsed) > sshCacheIdleTimeout {
			e.client.Close()
			e.client = nil
			e.conn = nil
		}
		e.mu.Unlock()
	}
}
