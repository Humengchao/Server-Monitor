package services

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

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

type Collector struct {
	db       *models.DB
	interval time.Duration
	mu       sync.Mutex
	prev     map[uuid.UUID]*prevStats
}

func NewCollector(db *models.DB, interval time.Duration) *Collector {
	return &Collector{db: db, interval: interval, prev: make(map[uuid.UUID]*prevStats)}
}

func (c *Collector) Start() {
	go func() {
		for {
			c.pollAll()
			time.Sleep(c.interval)
		}
	}()
	// Cleanup metrics older than 30 days every hour
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			c.cleanupOldMetrics()
		}
	}()
}

func (c *Collector) cleanupOldMetrics() {
	cutoff := time.Now().AddDate(0, 0, -30)
	rows, err := models.DeleteOldMetrics(c.db.Raw, cutoff)
	if err != nil {
		log.Printf("collector: cleanup failed: %v", err)
		return
	}
	if rows > 0 {
		log.Printf("collector: cleaned up %d old metrics rows", rows)
	}
}

func (c *Collector) PollNow(serverID uuid.UUID) (*models.MetricPoint, error) {
	s, err := models.GetServerByID(c.db.Raw, serverID)
	if err != nil {
		return nil, fmt.Errorf("server not found: %w", err)
	}
	return c.collectOne(s)
}

func (c *Collector) pollAll() {
	servers, err := models.GetAllServers(c.db)
	if err != nil {
		log.Printf("collector: failed to list servers: %v", err)
		return
	}
	for _, s := range servers {
		m, err := c.collectOne(&s)
		if err != nil {
			log.Printf("collector: poll %s failed: %v", s.Name, err)
			continue
		}
		if err := models.InsertMetric(c.db.Raw, s.ID, m); err != nil {
			log.Printf("collector: insert metric for %s failed: %v", s.Name, err)
		}
	}
}

func (c *Collector) collectOne(s *models.Server) (*models.MetricPoint, error) {
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

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	m := &models.MetricPoint{RecordedAt: time.Now()}

	// CPU from /proc/stat
	m.CPUPercent = collectCPU(client)

	// Memory from /proc/meminfo
	m.MemoryUsed, m.MemoryTotal = collectMemory(client)

	// Network from /proc/net/dev (cumulative counters -> bytes/sec)
	netRxRaw, netTxRaw := collectNetwork(client)
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
	out, err := RunCmd(client, "cat /proc/stat")
	if err != nil {
		return 0
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 8 {
				return 0
			}
			// Parse: user nice system idle iowait irq softirq steal
			var vals [8]int64
			for i := 0; i < 8 && i < len(fields)-1; i++ {
				vals[i], _ = strconv.ParseInt(fields[i+1], 10, 64)
			}
			idle := vals[3] + vals[4]
			total := vals[0] + vals[1] + vals[2] + vals[3] + vals[4] + vals[5] + vals[6] + vals[7]
			if total > 0 {
				return (1.0 - float64(idle)/float64(total)) * 100
			}
		}
	}
	return 0
}

func collectMemory(client *ssh.Client) (int64, int64) {
	out, err := RunCmd(client, "cat /proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var memTotal, memAvailable int64
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
		case "MemAvailable:":
			memAvailable = val
		}
	}
	// /proc/meminfo is in kB, convert to bytes
	return (memTotal - memAvailable) * 1024, memTotal * 1024
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
