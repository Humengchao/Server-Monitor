package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type prevStats struct {
	netRx  int64
	netTx  int64
	diskRx int64
	diskTx int64
	time   time.Time
}

// sysInfoState and dockerInfoState mirror the values last written successfully
// to the servers table, so unchanged host facts don't cause an UPDATE on every
// poll.
type sysInfoState struct {
	cores     int
	memTotal  int64
	diskTotal int64
}

type dockerInfoState struct {
	hasDocker bool
	version   string
}

type collectionResult struct {
	metric *models.MetricPoint
	err    error
}

type pollFailureState struct {
	attempts        int
	retryAt         time.Time
	fingerprint     [32]byte
	securityFailure bool
}

// pooledClient is a persistent SSH connection reused across polls. conn is the
// underlying TCP connection so a fresh deadline can be armed per collection.
type pooledClient struct {
	client      *ssh.Client
	conn        net.Conn
	fingerprint [32]byte
}

const (
	sshDialTimeout       = 10 * time.Second
	sshCollectionTimeout = 20 * time.Second
	maintenanceDelay     = 15 * time.Second
	maintenanceInterval  = 1 * time.Minute
	metricDeleteBatch    = 5000
	metricDeleteRuns     = 20
	pollFailureBase      = 15 * time.Second
	pollFailureMax       = 15 * time.Minute
	securityFailureBase  = 10 * time.Minute
	securityFailureMax   = 1 * time.Hour
)

type Collector struct {
	db         *models.DB
	interval   time.Duration
	mu         sync.Mutex
	prev       map[uuid.UUID]*prevStats
	sysInfo    map[uuid.UUID]sysInfoState
	dockerInfo map[uuid.UUID]dockerInfoState
	pollMu     sync.Mutex
	inFlight   map[uuid.UUID]struct{}
	failures   map[uuid.UUID]pollFailureState
	clientMu   sync.Mutex
	clients    map[uuid.UUID]*pooledClient
	pollSlots  chan struct{}
	stopCh     chan struct{}
}

func NewCollector(db *models.DB, interval time.Duration) *Collector {
	return &Collector{
		db:         db,
		interval:   interval,
		prev:       make(map[uuid.UUID]*prevStats),
		sysInfo:    make(map[uuid.UUID]sysInfoState),
		dockerInfo: make(map[uuid.UUID]dockerInfoState),
		inFlight:   make(map[uuid.UUID]struct{}),
		failures:   make(map[uuid.UUID]pollFailureState),
		clients:    make(map[uuid.UUID]*pooledClient),
		pollSlots:  make(chan struct{}, 10),
		stopCh:     make(chan struct{}),
	}
}

func (c *Collector) Start() {
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		c.pollAll()
		for {
			select {
			case <-c.stopCh:
				return
			case <-ticker.C:
				c.pollAll()
			}
		}
	}()
	// Roll up and prune history independently so maintenance never blocks live
	// collection or API startup.
	go func() {
		timer := time.NewTimer(maintenanceDelay)
		defer timer.Stop()
		for {
			select {
			case <-c.stopCh:
				return
			case <-timer.C:
				c.maintainMetricHistory()
				timer.Reset(maintenanceInterval)
			}
		}
	}()
}

// Stop signals the collector to stop polling and closes pooled connections.
func (c *Collector) Stop() {
	close(c.stopCh)
	c.clientMu.Lock()
	clients := c.clients
	c.clients = make(map[uuid.UUID]*pooledClient)
	c.clientMu.Unlock()
	for _, pc := range clients {
		pc.client.Close()
	}
}

func (c *Collector) maintainMetricHistory() {
	now := time.Now()
	backfilled, err := models.MetricRollupBackfilled(c.db.Raw)
	if err != nil {
		log.Printf("collector: read metric maintenance state: %v", err)
		return
	}

	if !backfilled {
		// Preserve existing history before applying the shorter raw retention.
		if err := models.RollupMetrics1m(c.db.Raw, now.AddDate(0, 0, -30), now); err != nil {
			log.Printf("collector: initial one-minute rollup failed: %v", err)
			return
		}
		if err := models.RollupMetrics15m(c.db.Raw, now.AddDate(0, 0, -30), now); err != nil {
			log.Printf("collector: initial fifteen-minute rollup failed: %v", err)
			return
		}
		if err := models.MarkMetricRollupBackfilled(c.db.Raw); err != nil {
			log.Printf("collector: mark metric backfill complete: %v", err)
			return
		}
		log.Printf("collector: tiered metric history backfill completed")
	} else {
		// Rebuild a small overlap so incomplete time buckets and brief restarts
		// are repaired idempotently via ON CONFLICT. Starting from the last
		// completed rollup also catches up after a longer application outage.
		oneMinuteStart, err := models.MetricRollupStart(c.db.Raw, "server_metrics_1m", now.AddDate(0, 0, -30), 5*time.Minute)
		if err != nil {
			log.Printf("collector: find one-minute rollup cursor: %v", err)
			return
		}
		if err := models.RollupMetrics1m(c.db.Raw, oneMinuteStart, now); err != nil {
			log.Printf("collector: one-minute rollup failed: %v", err)
			return
		}
		fifteenMinuteStart, err := models.MetricRollupStart(c.db.Raw, "server_metrics_15m", now.AddDate(0, 0, -30), time.Hour)
		if err != nil {
			log.Printf("collector: find fifteen-minute rollup cursor: %v", err)
			return
		}
		if err := models.RollupMetrics15m(c.db.Raw, fifteenMinuteStart, now); err != nil {
			log.Printf("collector: fifteen-minute rollup failed: %v", err)
			return
		}
	}

	c.deleteMetricTier("server_metrics", now.Add(-24*time.Hour))
	c.deleteMetricTier("server_metrics_1m", now.AddDate(0, 0, -30))
	c.deleteMetricTier("server_metrics_15m", now.AddDate(-1, 0, 0))
}

func (c *Collector) deleteMetricTier(table string, before time.Time) {
	for range metricDeleteRuns {
		rows, err := models.DeleteMetricBatch(c.db.Raw, table, before, metricDeleteBatch)
		if err != nil {
			log.Printf("collector: prune %s failed: %v", table, err)
			return
		}
		if rows < metricDeleteBatch {
			return
		}
	}
}

func (c *Collector) pollAll() {
	servers, err := models.GetAllServers(c.db)
	if err != nil {
		log.Printf("collector: failed to list servers: %v", err)
		return
	}

	active := make(map[uuid.UUID]struct{}, len(servers))
	for i := range servers {
		active[servers[i].ID] = struct{}{}
	}
	c.pruneServerState(active)

	// Schedule each host independently. Slow hosts remain in-flight and are
	// skipped on the next tick, while healthy hosts keep their regular cadence.
	for i := range servers {
		s := servers[i]
		fingerprint := serverPollFingerprint(&s)
		if !c.beginPoll(s.ID, fingerprint) {
			continue
		}
		go func() {
			defer c.endPoll(s.ID)
			select {
			case c.pollSlots <- struct{}{}:
				defer func() { <-c.pollSlots }()
			case <-c.stopCh:
				return
			}
			m, err := c.collectOne(&s, fingerprint)
			if err != nil {
				delay := c.recordPollFailure(s.ID, fingerprint, err, time.Now())
				log.Printf("collector: poll %s failed: %v; retry in %s", s.Name, err, delay)
				return
			}
			if err := models.SaveMetric(c.db.Raw, s.ID, m); err != nil {
				delay := c.recordPollFailure(s.ID, fingerprint, err, time.Now())
				log.Printf("collector: save metric for %s failed: %v; retry in %s", s.Name, err, delay)
				return
			}
			c.clearPollFailure(s.ID)
		}()
	}
}

// pruneServerState drops pooled connections and per-server bookkeeping for
// servers that no longer exist, so deleted hosts don't leak connections or
// stale backoff state.
func (c *Collector) pruneServerState(active map[uuid.UUID]struct{}) {
	var stale []*pooledClient
	c.clientMu.Lock()
	for id, pc := range c.clients {
		if _, ok := active[id]; !ok {
			delete(c.clients, id)
			stale = append(stale, pc)
		}
	}
	c.clientMu.Unlock()
	for _, pc := range stale {
		pc.client.Close()
	}

	c.pollMu.Lock()
	for id := range c.failures {
		if _, ok := active[id]; !ok {
			delete(c.failures, id)
		}
	}
	c.pollMu.Unlock()

	c.mu.Lock()
	for id := range c.prev {
		if _, ok := active[id]; !ok {
			delete(c.prev, id)
		}
	}
	for id := range c.sysInfo {
		if _, ok := active[id]; !ok {
			delete(c.sysInfo, id)
		}
	}
	for id := range c.dockerInfo {
		if _, ok := active[id]; !ok {
			delete(c.dockerInfo, id)
		}
	}
	c.mu.Unlock()
}

func serverPollFingerprint(server *models.Server) [32]byte {
	hash := sha256.New()
	for _, field := range []string{
		server.Host,
		strconv.Itoa(server.Port),
		server.SSHUsername,
		server.SSHPassword,
		server.SSHKey,
		server.SSHHostKey,
		server.ServerType,
	} {
		_, _ = io.WriteString(hash, field)
		_, _ = hash.Write([]byte{0})
	}
	var fingerprint [32]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint
}

func (c *Collector) beginPoll(serverID uuid.UUID, fingerprint [32]byte) bool {
	return c.beginPollAt(serverID, fingerprint, time.Now())
}

func (c *Collector) beginPollAt(serverID uuid.UUID, fingerprint [32]byte, now time.Time) bool {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	if _, exists := c.inFlight[serverID]; exists {
		return false
	}
	if failure, exists := c.failures[serverID]; exists {
		if failure.fingerprint != fingerprint {
			delete(c.failures, serverID)
		} else if now.Before(failure.retryAt) {
			return false
		}
	}
	c.inFlight[serverID] = struct{}{}
	return true
}

func (c *Collector) endPoll(serverID uuid.UUID) {
	c.pollMu.Lock()
	delete(c.inFlight, serverID)
	c.pollMu.Unlock()
}

func pollFailureDelay(attempt int) time.Duration {
	return exponentialDelay(attempt, pollFailureBase, pollFailureMax)
}

func securityFailureDelay(attempt int) time.Duration {
	return exponentialDelay(attempt, securityFailureBase, securityFailureMax)
}

func exponentialDelay(attempt int, base, maximum time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func isSSHAuthenticationFailure(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "ssh handshake:") &&
		strings.Contains(message, "unable to authenticate")
}

func (c *Collector) recordPollFailure(serverID uuid.UUID, fingerprint [32]byte, pollErr error, now time.Time) time.Duration {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	state := c.failures[serverID]
	if state.fingerprint != fingerprint {
		state = pollFailureState{fingerprint: fingerprint}
	}
	if isSSHAuthenticationFailure(pollErr) && !state.securityFailure {
		// Authentication failures use their own attempt counter so preceding
		// transient DNS/network errors cannot turn the first rejected password
		// into an unexpectedly long circuit-breaker delay.
		state.securityFailure = true
		state.attempts = 0
	}
	state.attempts++
	delay := pollFailureDelay(state.attempts)
	if state.securityFailure {
		delay = securityFailureDelay(state.attempts)
	}
	state.retryAt = now.Add(delay)
	c.failures[serverID] = state
	return delay
}

func (c *Collector) clearPollFailure(serverID uuid.UUID) {
	c.pollMu.Lock()
	delete(c.failures, serverID)
	c.pollMu.Unlock()
}

// getClient returns the pooled SSH client for the server, dialing a new
// connection when none exists or the connection settings changed. Polls are
// serialized per server (inFlight), so two dials for one server cannot race.
func (c *Collector) getClient(s *models.Server, fingerprint [32]byte) (*pooledClient, error) {
	c.clientMu.Lock()
	pc := c.clients[s.ID]
	c.clientMu.Unlock()
	if pc != nil {
		if pc.fingerprint == fingerprint {
			return pc, nil
		}
		c.dropClient(s.ID, pc)
	}

	config, err := buildSSHClientConfig(s.SSHUsername, s.SSHPassword, s.SSHKey, s.SSHHostKey, sshDialTimeout)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tcpConn, err := net.DialTimeout("tcp", addr, sshDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	if err := tcpConn.SetDeadline(time.Now().Add(sshDialTimeout)); err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh deadline: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, config)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	pc = &pooledClient{client: ssh.NewClient(sshConn, chans, reqs), conn: tcpConn, fingerprint: fingerprint}
	c.clientMu.Lock()
	c.clients[s.ID] = pc
	c.clientMu.Unlock()
	return pc, nil
}

// dropClient closes a pooled connection and forgets it if it is still the one
// tracked for the server.
func (c *Collector) dropClient(serverID uuid.UUID, pc *pooledClient) {
	c.clientMu.Lock()
	if c.clients[serverID] == pc {
		delete(c.clients, serverID)
	}
	c.clientMu.Unlock()
	pc.client.Close()
}

func (c *Collector) collectOne(s *models.Server, fingerprint [32]byte) (*models.MetricPoint, error) {
	pingLatency := make(chan int, 1)
	go func() { pingLatency <- collectPingLatency(s.Host) }()

	pc, err := c.getClient(s, fingerprint)
	if err != nil {
		return nil, err
	}
	// Arm a deadline covering this collection so a silently dead connection
	// cannot block forever. It is cleared on success so the idle pooled
	// connection survives until the next poll.
	if err := pc.conn.SetDeadline(time.Now().Add(sshCollectionTimeout)); err != nil {
		c.dropClient(s.ID, pc)
		return nil, fmt.Errorf("ssh deadline: %w", err)
	}

	// A TCP deadline alone is not enough here: x/crypto/ssh can remain blocked
	// waiting for an open-channel response after the transport stops making
	// progress. Run the complete host collection behind a hard deadline and
	// close the SSH client on timeout so one broken host never blocks pollAll.
	resultCh := make(chan collectionResult, 1)
	go func() {
		var m *models.MetricPoint
		var err error
		if s.ServerType == "windows" {
			m, err = c.collectWindows(pc.client, s)
		} else {
			m, err = c.collectLinux(pc.client, s)
		}
		if m != nil {
			m.LatencyMS = <-pingLatency
		}
		resultCh <- collectionResult{metric: m, err: err}
	}()

	timer := time.NewTimer(sshCollectionTimeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.err != nil {
			// A failed command usually means a broken transport; drop the
			// connection so the next poll redials instead of reusing it.
			c.dropClient(s.ID, pc)
			return result.metric, result.err
		}
		_ = pc.conn.SetDeadline(time.Time{})
		return result.metric, nil
	case <-timer.C:
		c.dropClient(s.ID, pc)
		return nil, fmt.Errorf("collection timed out after %s", sshCollectionTimeout)
	}
}

// applyRates converts cumulative network/disk counters into per-second rates
// using the previous poll's sample for this server.
func (c *Collector) applyRates(serverID uuid.UUID, m *models.MetricPoint, netRx, netTx, diskRx, diskTx int64) {
	now := m.RecordedAt
	c.mu.Lock()
	if prev, ok := c.prev[serverID]; ok && prev.netRx <= netRx && prev.netTx <= netTx {
		elapsed := now.Sub(prev.time).Seconds()
		if elapsed > 0 {
			m.NetworkRxBytes = int64(float64(netRx-prev.netRx) / elapsed)
			m.NetworkTxBytes = int64(float64(netTx-prev.netTx) / elapsed)
			m.DiskRxBytes = int64(float64(diskRx-prev.diskRx) / elapsed)
			m.DiskTxBytes = int64(float64(diskTx-prev.diskTx) / elapsed)
		}
	}
	c.prev[serverID] = &prevStats{netRx: netRx, netTx: netTx, diskRx: diskRx, diskTx: diskTx, time: now}
	c.mu.Unlock()
}

// syncHostInfo persists slow-changing host facts, writing only when they
// differ from the last successfully stored values so steady-state polls don't
// UPDATE the servers table every few seconds.
func (c *Collector) syncHostInfo(serverID uuid.UUID, cores int, memTotal, diskTotal int64, dockerVersion string) {
	if cores > 0 {
		info := sysInfoState{cores: cores, memTotal: memTotal, diskTotal: diskTotal}
		c.mu.Lock()
		prev, known := c.sysInfo[serverID]
		c.mu.Unlock()
		if !known || prev != info {
			if err := models.UpdateServerSystemInfo(c.db.Raw, serverID, cores, memTotal, diskTotal); err != nil {
				log.Printf("collector: update system info for %s: %v", serverID, err)
			} else {
				c.mu.Lock()
				c.sysInfo[serverID] = info
				c.mu.Unlock()
			}
		}
	}

	docker := dockerInfoState{hasDocker: dockerVersion != "", version: dockerVersion}
	c.mu.Lock()
	prevDocker, known := c.dockerInfo[serverID]
	c.mu.Unlock()
	if !known || prevDocker != docker {
		if err := models.UpdateDockerInfo(c.db.Raw, serverID, docker.hasDocker, docker.version); err != nil {
			log.Printf("collector: update docker info for %s: %v", serverID, err)
		} else {
			c.mu.Lock()
			c.dockerInfo[serverID] = docker
			c.mu.Unlock()
		}
	}
}

// linuxSectionSeparator delimits command outputs inside the batched metrics
// command. Any line consisting solely of this marker is a section boundary.
const linuxSectionSeparator = "__SVRMON_SECTION__"

const (
	linuxSectionStatFirst = iota
	linuxSectionStatSecond
	linuxSectionMemInfo
	linuxSectionLoadAvg
	linuxSectionNetDev
	linuxSectionDiskStats
	linuxSectionUptime
	linuxSectionNproc
	linuxSectionDiskUsage
	linuxSectionDocker
	linuxSectionCount
)

// linuxMetricsCommand gathers every sample in a single SSH exec round trip
// instead of one session per file, which matters on high-latency links. The
// second /proc/stat read is delayed remotely so CPU usage still comes from two
// samples half a second apart. A command that fails leaves an empty section
// and parses to zero values, matching the previous per-command behaviour.
var linuxMetricsCommand = strings.Join([]string{
	"cat /proc/stat",
	"sleep 0.5 || sleep 1; cat /proc/stat",
	"cat /proc/meminfo",
	"cat /proc/loadavg",
	"cat /proc/net/dev",
	"cat /proc/diskstats",
	"cat /proc/uptime",
	"nproc",
	"df -P -B1 /",
	"docker info --format '{{.ServerVersion}}' || sudo -n docker info --format '{{.ServerVersion}}' || true",
}, "; echo "+linuxSectionSeparator+"; ")

// splitLinuxSections splits batched command output on separator lines. If the
// output was truncated, the remaining sections come back empty.
func splitLinuxSections(out string, want int) []string {
	sections := make([]string, 0, want)
	var current strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == linuxSectionSeparator {
			sections = append(sections, current.String())
			current.Reset()
			continue
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	sections = append(sections, current.String())
	for len(sections) < want {
		sections = append(sections, "")
	}
	return sections
}

func (c *Collector) collectLinux(client *ssh.Client, s *models.Server) (*models.MetricPoint, error) {
	out, err := RunCmd(client, linuxMetricsCommand)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("linux metrics: %w", err)
	}
	sections := splitLinuxSections(out, linuxSectionCount)

	m := &models.MetricPoint{RecordedAt: time.Now()}
	m.CPUPercent = cpuPercentFromSamples(sections[linuxSectionStatFirst], sections[linuxSectionStatSecond])
	m.Load1, m.Load5, m.Load15 = parseLoadAvg(sections[linuxSectionLoadAvg])
	m.MemoryUsed, m.MemoryTotal = parseMemInfo(sections[linuxSectionMemInfo])

	// Cumulative counters from /proc/net/dev and /proc/diskstats -> bytes/sec.
	netRxRaw, netTxRaw := parseNetDev(sections[linuxSectionNetDev])
	m.NetworkRxTotal, m.NetworkTxTotal = netRxRaw, netTxRaw
	diskRxRaw, diskTxRaw := parseDiskStats(sections[linuxSectionDiskStats])
	c.applyRates(s.ID, m, netRxRaw, netTxRaw, diskRxRaw, diskTxRaw)

	m.UptimeSeconds = parseUptime(sections[linuxSectionUptime])
	diskUsed, diskTotal := parseDiskUsage(sections[linuxSectionDiskUsage])
	m.DiskUsed = diskUsed

	cores := parseNproc(sections[linuxSectionNproc])
	dockerVersion := parseDockerVersion(sections[linuxSectionDocker])
	c.syncHostInfo(s.ID, cores, m.MemoryTotal, diskTotal, dockerVersion)

	return m, nil
}

func collectPingLatency(host string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "ping", "-n", "-c", "3", "-W", "1", host).CombinedOutput()
	return parsePingAverage(string(out))
}

func parsePingAverage(output string) int {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "min/avg/max") && !strings.Contains(line, "round-trip") {
			continue
		}
		_, values, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		parts := strings.Split(strings.TrimSpace(values), "/")
		if len(parts) < 2 {
			continue
		}
		average, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err == nil {
			return max(1, int(average+0.5))
		}
	}
	return 0
}

func RunCmd(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = io.Discard
	err = sess.Run(cmd)
	return buf.String(), err
}

// cpuPercentFromSamples derives CPU usage from two /proc/stat snapshots taken
// half a second apart.
func cpuPercentFromSamples(first, second string) float64 {
	idle1, total1 := parseProcStatCPU(first)
	idle2, total2 := parseProcStatCPU(second)
	dIdle := idle2 - idle1
	dTotal := total2 - total1
	if dTotal > 0 {
		return (1.0 - float64(dIdle)/float64(dTotal)) * 100
	}
	return 0
}

func parseProcStatCPU(out string) (idle, total int64) {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 8 {
				return
			}
			var vals [8]int64
			for i := 0; i < 8 && i < len(fields)-1; i++ {
				vals[i], _ = strconv.ParseInt(fields[i+1], 10, 64)
			}
			idle = vals[3] + vals[4]
			total = vals[0] + vals[1] + vals[2] + vals[3] + vals[4] + vals[5] + vals[6] + vals[7]
			return
		}
	}
	return
}

// parseMemInfo returns used and total memory in bytes from /proc/meminfo.
func parseMemInfo(out string) (int64, int64) {
	var memTotal, memFree, buffers, cached int64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			memTotal = val
		case "MemFree:":
			memFree = val
		case "Buffers:":
			buffers = val
		case "Cached:":
			cached = val
		}
	}
	// /proc/meminfo is in kB, convert to bytes
	used := memTotal - memFree - buffers - cached
	if used < 0 {
		used = 0
	}
	return used * 1024, memTotal * 1024
}

func parseLoadAvg(out string) (float64, float64, float64) {
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15
}

func parseNetDev(out string) (int64, int64) {
	var rxTotal, txTotal int64
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, ":") {
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				if strings.TrimSuffix(fields[0], ":") == "lo" {
					continue
				}
				rx, _ := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
				tx, _ := strconv.ParseInt(strings.TrimSpace(fields[9]), 10, 64)
				rxTotal += rx
				txTotal += tx
			}
		}
	}
	return rxTotal, txTotal
}

func parseUptime(out string) int64 {
	parts := strings.Fields(out)
	if len(parts) > 0 {
		sec, _ := strconv.ParseFloat(parts[0], 64)
		return int64(sec)
	}
	return 0
}

// parseDiskStats reads /proc/diskstats content and returns cumulative bytes
// read/written for all disks.
// Fields: major minor name reads_completed reads_merged sectors_read time_reading writes_completed writes_merged sectors_written time_writing ...
// Sector size is 512 bytes. We sum sda/sdb/vda/nvme* etc, skip partitions (numbered).
func parseDiskStats(out string) (int64, int64) {
	var readSectors, writeSectors int64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		// Only count whole disks, not partitions
		if !(strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "nvme")) {
			continue
		}
		// Skip partitions (e.g., sda1, vda1, nvme0n1p1)
		last := name[len(name)-1]
		if last >= '0' && last <= '9' && !strings.Contains(name, "nvme") {
			// For sd/vd: ends with digit = partition, skip
			continue
		}
		if strings.Contains(name, "p") && strings.Contains(name, "nvme") {
			// nvme partition like nvme0n1p1
			continue
		}
		sectorsRead, _ := strconv.ParseInt(fields[5], 10, 64)
		sectorsWritten, _ := strconv.ParseInt(fields[9], 10, 64)
		readSectors += sectorsRead
		writeSectors += sectorsWritten
	}
	// 1 sector = 512 bytes
	return readSectors * 512, writeSectors * 512
}

// parseDiskUsage parses `df -P -B1 /` output and returns used and total bytes
// for the root filesystem. Fields are anchored from the end of the line
// (…blocks used available capacity mount) so output still parses when a df
// without -P wraps a long device name onto its own line.
func parseDiskUsage(out string) (used, total int64) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, 0
	}
	lines := strings.Split(trimmed, "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, 0
	}
	total, _ = strconv.ParseInt(fields[len(fields)-5], 10, 64)
	used, _ = strconv.ParseInt(fields[len(fields)-4], 10, 64)
	return used, total
}

func parseNproc(out string) int {
	cores, _ := strconv.Atoi(strings.TrimSpace(out))
	return cores
}

// parseDockerVersion sanity-checks the docker section of a metrics run: a real
// server version is a short single token like "24.0.7". Anything else (error
// text, sudo noise) means docker is not usable on the host.
func parseDockerVersion(out string) string {
	version := strings.TrimSpace(out)
	if version == "" || len(version) > 31 || strings.ContainsAny(version, " \t\n") {
		return ""
	}
	return version
}

// windowsMetricsScript gathers all metrics in a single PowerShell invocation as
// key=value lines. Uses CIM classes (locale-independent, unlike Get-Counter).
// Perf raw counters (BytesReceivedPersec etc.) are cumulative despite the name,
// matching the /proc counter semantics used for rate calculation.
const windowsMetricsScript = `$ErrorActionPreference='SilentlyContinue'
$os = Get-CimInstance Win32_OperatingSystem
$cpu = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average
$cores = (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
$nics = Get-CimInstance Win32_PerfRawData_Tcpip_NetworkInterface
$netrx = ($nics | Measure-Object -Property BytesReceivedPersec -Sum).Sum
$nettx = ($nics | Measure-Object -Property BytesSentPersec -Sum).Sum
$disk = Get-CimInstance Win32_PerfRawData_PerfDisk_PhysicalDisk -Filter "Name='_Total'"
$uptime = [int64]((Get-Date) - $os.LastBootUpTime).TotalSeconds
$disks = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'
$disktotal = ($disks | Measure-Object -Property Size -Sum).Sum
$diskfree = ($disks | Measure-Object -Property FreeSpace -Sum).Sum
$queue = (Get-CimInstance Win32_PerfFormattedData_PerfOS_System).ProcessorQueueLength
$docker = if (Get-Command docker -ErrorAction SilentlyContinue) { docker info --format "{{.ServerVersion}}" } else { '' }
Write-Output ("cpu=" + [int64][math]::Round([double]$cpu))
Write-Output ("memtotal=" + $os.TotalVisibleMemorySize)
Write-Output ("memfree=" + $os.FreePhysicalMemory)
Write-Output ("netrx=" + [int64]$netrx)
Write-Output ("nettx=" + [int64]$nettx)
Write-Output ("diskread=" + [int64]$disk.DiskReadBytesPersec)
Write-Output ("diskwrite=" + [int64]$disk.DiskWriteBytesPersec)
Write-Output ("uptime=" + $uptime)
Write-Output ("cores=" + $cores)
Write-Output ("disktotal=" + [int64]$disktotal)
Write-Output ("diskfree=" + [int64]$diskfree)
Write-Output ("queue=" + [int64]$queue)
Write-Output ("docker=" + $docker)`

// encodePowerShell encodes a script as UTF-16LE base64 for -EncodedCommand,
// which sidesteps quoting differences between cmd.exe and powershell default shells.
func encodePowerShell(script string) string {
	codes := utf16.Encode([]rune(script))
	buf := make([]byte, len(codes)*2)
	for i, r := range codes {
		buf[i*2] = byte(r)
		buf[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func (c *Collector) collectWindows(client *ssh.Client, s *models.Server) (*models.MetricPoint, error) {
	cmd := "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(windowsMetricsScript)
	out, err := RunCmd(client, cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("windows metrics: %w", err)
	}

	vals := make(map[string]int64)
	dockerVersion := ""
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if k == "docker" {
			dockerVersion = parseDockerVersion(v)
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			vals[k] = n
		}
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("windows metrics: no data in output %q", strings.TrimSpace(out))
	}

	m := &models.MetricPoint{RecordedAt: time.Now()}
	m.CPUPercent = float64(vals["cpu"])
	// TotalVisibleMemorySize / FreePhysicalMemory are in kB
	m.MemoryTotal = vals["memtotal"] * 1024
	m.MemoryUsed = (vals["memtotal"] - vals["memfree"]) * 1024
	if m.MemoryUsed < 0 {
		m.MemoryUsed = 0
	}
	m.UptimeSeconds = vals["uptime"]
	m.DiskUsed = vals["disktotal"] - vals["diskfree"]
	if m.DiskUsed < 0 {
		m.DiskUsed = 0
	}
	m.Load1 = float64(vals["queue"])

	netRxRaw, netTxRaw := vals["netrx"], vals["nettx"]
	m.NetworkRxTotal, m.NetworkTxTotal = netRxRaw, netTxRaw
	c.applyRates(s.ID, m, netRxRaw, netTxRaw, vals["diskread"], vals["diskwrite"])

	c.syncHostInfo(s.ID, int(vals["cores"]), m.MemoryTotal, vals["disktotal"], dockerVersion)

	return m, nil
}

// RunDockerCmd runs a docker command, falling back to sudo docker if needed.
func RunDockerCmd(client *ssh.Client, args string) (string, error) {
	out, err := RunCmd(client, "docker "+args)
	if err != nil {
		out, err = RunCmd(client, "sudo docker "+args)
		if err != nil {
			return "", err
		}
	}
	return out, nil
}
