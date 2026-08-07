package services

import (
	"bytes"
	"context"
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

type collectionResult struct {
	metric *models.MetricPoint
	err    error
}

const (
	sshDialTimeout       = 10 * time.Second
	sshCollectionTimeout = 20 * time.Second
	maintenanceDelay     = 15 * time.Second
	maintenanceInterval  = 1 * time.Minute
	metricDeleteBatch    = 5000
	metricDeleteRuns     = 20
)

type Collector struct {
	db       *models.DB
	interval time.Duration
	mu       sync.Mutex
	prev     map[uuid.UUID]*prevStats
	stopCh   chan struct{}
}

func NewCollector(db *models.DB, interval time.Duration) *Collector {
	return &Collector{
		db:       db,
		interval: interval,
		prev:     make(map[uuid.UUID]*prevStats),
		stopCh:   make(chan struct{}),
	}
}

func (c *Collector) Start() {
	go func() {
		for {
			select {
			case <-c.stopCh:
				return
			default:
			}
			c.pollAll()
			time.Sleep(c.interval)
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

// Stop signals the collector to stop polling.
func (c *Collector) Stop() {
	close(c.stopCh)
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

	// Parallel polling: one dead server shouldn't delay metrics for live ones.
	// A per-host deadline prevents one stalled command from blocking the batch.
	sem := make(chan struct{}, 10) // max 10 concurrent SSH connections
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(s *models.Server) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, err := c.collectOne(s)
			if err != nil {
				log.Printf("collector: poll %s failed: %v", s.Name, err)
				return
			}
			if err := models.SaveMetric(c.db.Raw, s.ID, m); err != nil {
				log.Printf("collector: save metric for %s failed: %v", s.Name, err)
			}
		}(&servers[i])
	}
	wg.Wait()
}

func (c *Collector) collectOne(s *models.Server) (*models.MetricPoint, error) {
	pingLatency := make(chan int, 1)
	go func() { pingLatency <- collectPingLatency(s.Host) }()

	config := &ssh.ClientConfig{
		User:            s.SSHUsername,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	if s.SSHPassword != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(s.SSHPassword)}
	} else if s.SSHKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(s.SSHKey))
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		return nil, fmt.Errorf("no auth method")
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tcpConn, err := net.DialTimeout("tcp", addr, sshDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	if err := tcpConn.SetDeadline(time.Now().Add(sshCollectionTimeout)); err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh deadline: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, config)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	// A TCP deadline alone is not enough here: x/crypto/ssh can remain blocked
	// waiting for an open-channel response after the transport stops making
	// progress. Run the complete host collection behind a hard deadline and
	// close the SSH client on timeout so one broken host never blocks pollAll.
	resultCh := make(chan collectionResult, 1)
	go func() {
		var m *models.MetricPoint
		var err error
		if s.ServerType == "windows" {
			m, err = c.collectWindows(client, s)
		} else {
			m, err = c.collectLinux(client, s)
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
		return result.metric, result.err
	case <-timer.C:
		_ = client.Close()
		return nil, fmt.Errorf("collection timed out after %s", sshCollectionTimeout)
	}
}

func (c *Collector) collectLinux(client *ssh.Client, s *models.Server) (*models.MetricPoint, error) {
	m := &models.MetricPoint{RecordedAt: time.Now()}

	// CPU from /proc/stat
	m.CPUPercent = collectCPU(client)
	m.Load1, m.Load5, m.Load15 = collectLoad(client)

	// Memory from /proc/meminfo
	m.MemoryUsed, m.MemoryTotal = collectMemory(client)

	// Network from /proc/net/dev (cumulative counters -> bytes/sec)
	netRxRaw, netTxRaw := collectNetwork(client)
	m.NetworkRxTotal, m.NetworkTxTotal = netRxRaw, netTxRaw
	// Disk I/O from /proc/diskstats (cumulative counters -> bytes/sec)
	diskRxRaw, diskTxRaw := collectDiskIO(client)
	now := m.RecordedAt
	c.mu.Lock()
	if prev, ok := c.prev[s.ID]; ok && prev.netRx <= netRxRaw && prev.netTx <= netTxRaw {
		elapsed := now.Sub(prev.time).Seconds()
		if elapsed > 0 {
			m.NetworkRxBytes = int64(float64(netRxRaw-prev.netRx) / elapsed)
			m.NetworkTxBytes = int64(float64(netTxRaw-prev.netTx) / elapsed)
			m.DiskRxBytes = int64(float64(diskRxRaw-prev.diskRx) / elapsed)
			m.DiskTxBytes = int64(float64(diskTxRaw-prev.diskTx) / elapsed)
		}
	}
	c.prev[s.ID] = &prevStats{netRx: netRxRaw, netTx: netTxRaw, diskRx: diskRxRaw, diskTx: diskTxRaw, time: now}
	c.mu.Unlock()

	// Uptime
	m.UptimeSeconds = collectUptime(client)
	m.DiskUsed = collectDiskUsed(client)

	// Collect and update system info (cores, memory total, disk total)
	cpuCores, memTotal, diskTotal := collectSystemInfo(client)
	if cpuCores > 0 {
		models.UpdateServerSystemInfo(c.db.Raw, s.ID, cpuCores, memTotal, diskTotal)
	}

	// Check Docker availability (cheap: reuses existing SSH connection)
	dockerVersion := collectDockerVersion(client)
	models.UpdateDockerInfo(c.db.Raw, s.ID, dockerVersion != "", dockerVersion)

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

func collectCPU(client *ssh.Client) float64 {
	// Take two samples 500ms apart to get instantaneous CPU usage
	out1, err := RunCmd(client, "cat /proc/stat")
	if err != nil {
		return 0
	}
	time.Sleep(500 * time.Millisecond)
	out2, err := RunCmd(client, "cat /proc/stat")
	if err != nil {
		return 0
	}

	parseCPULine := func(out string) (idle, total int64) {
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

	idle1, total1 := parseCPULine(out1)
	idle2, total2 := parseCPULine(out2)

	dIdle := idle2 - idle1
	dTotal := total2 - total1
	if dTotal > 0 {
		return (1.0 - float64(dIdle)/float64(dTotal)) * 100
	}
	return 0
}

func collectMemory(client *ssh.Client) (int64, int64) {
	out, err := RunCmd(client, "cat /proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var memTotal, memFree, buffers, cached int64
	lines := strings.Split(out, "\n")
	for _, line := range lines {
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

func collectLoad(client *ssh.Client) (float64, float64, float64) {
	out, err := RunCmd(client, "cat /proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return 0, 0, 0
	}
	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15
}

func collectNetwork(client *ssh.Client) (int64, int64) {
	out, err := RunCmd(client, "cat /proc/net/dev")
	if err != nil {
		return 0, 0
	}
	var rxTotal, txTotal int64
	lines := strings.Split(out, "\n")
	for _, line := range lines {
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

func collectUptime(client *ssh.Client) int64 {
	out, err := RunCmd(client, "cat /proc/uptime")
	if err != nil {
		return 0
	}
	parts := strings.Fields(out)
	if len(parts) > 0 {
		sec, _ := strconv.ParseFloat(parts[0], 64)
		return int64(sec)
	}
	return 0
}

func collectDiskUsed(client *ssh.Client) int64 {
	out, err := RunCmd(client, "df -B1 / | tail -1")
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 {
		return 0
	}
	used, _ := strconv.ParseInt(fields[2], 10, 64)
	return used
}

// collectDiskIO reads /proc/diskstats and returns cumulative bytes read/written for all disks.
// Fields: major minor name reads_completed reads_merged sectors_read time_reading writes_completed writes_merged sectors_written time_writing ...
// Sector size is 512 bytes. We sum sda/sdb/vda/nvme* etc, skip partitions (numbered).
func collectDiskIO(client *ssh.Client) (int64, int64) {
	out, err := RunCmd(client, "cat /proc/diskstats")
	if err != nil {
		return 0, 0
	}
	var readSectors, writeSectors int64
	lines := strings.Split(out, "\n")
	for _, line := range lines {
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

// collectSystemInfo returns cpu cores, total memory bytes, total disk bytes.
func collectSystemInfo(client *ssh.Client) (int, int64, int64) {
	// CPU cores
	out, _ := RunCmd(client, "nproc")
	cores, _ := strconv.Atoi(strings.TrimSpace(out))

	// Memory total from /proc/meminfo
	out, _ = RunCmd(client, "cat /proc/meminfo")
	var memTotalKB int64
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				memTotalKB, _ = strconv.ParseInt(fields[1], 10, 64)
			}
			break
		}
	}

	// Disk total from df (root filesystem)
	out, _ = RunCmd(client, "df -B1 / | tail -1")
	var diskTotal int64
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) >= 2 {
		diskTotal, _ = strconv.ParseInt(fields[1], 10, 64)
	}

	return cores, memTotalKB * 1024, diskTotal
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
Write-Output ("queue=" + [int64]$queue)`

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
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
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
	diskRxRaw, diskTxRaw := vals["diskread"], vals["diskwrite"]
	now := m.RecordedAt
	c.mu.Lock()
	if prev, ok := c.prev[s.ID]; ok && prev.netRx <= netRxRaw && prev.netTx <= netTxRaw {
		elapsed := now.Sub(prev.time).Seconds()
		if elapsed > 0 {
			m.NetworkRxBytes = int64(float64(netRxRaw-prev.netRx) / elapsed)
			m.NetworkTxBytes = int64(float64(netTxRaw-prev.netTx) / elapsed)
			m.DiskRxBytes = int64(float64(diskRxRaw-prev.diskRx) / elapsed)
			m.DiskTxBytes = int64(float64(diskTxRaw-prev.diskTx) / elapsed)
		}
	}
	c.prev[s.ID] = &prevStats{netRx: netRxRaw, netTx: netTxRaw, diskRx: diskRxRaw, diskTx: diskTxRaw, time: now}
	c.mu.Unlock()

	if cores := int(vals["cores"]); cores > 0 {
		models.UpdateServerSystemInfo(c.db.Raw, s.ID, cores, m.MemoryTotal, vals["disktotal"])
	}

	dockerVersion := collectWindowsDockerVersion(client)
	models.UpdateDockerInfo(c.db.Raw, s.ID, dockerVersion != "", dockerVersion)

	return m, nil
}

// collectWindowsDockerVersion uses double quotes: single quotes are not stripped
// by cmd.exe, which is the default shell for Windows OpenSSH.
func collectWindowsDockerVersion(client *ssh.Client) string {
	out, err := RunCmd(client, `docker info --format "{{.ServerVersion}}"`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func collectDockerVersion(client *ssh.Client) string {
	out, err := RunCmd(client, "docker info --format '{{.ServerVersion}}'")
	if err != nil {
		out, err = RunCmd(client, "sudo docker info --format '{{.ServerVersion}}'")
		if err != nil {
			return ""
		}
	}
	return strings.TrimSpace(out)
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
