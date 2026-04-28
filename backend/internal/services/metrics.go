package services

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type Collector struct {
	db       *sql.DB
	interval time.Duration
}

func NewCollector(db *sql.DB, interval time.Duration) *Collector {
	return &Collector{db: db, interval: interval}
}

func (c *Collector) Start() {
	go func() {
		for {
			c.pollAll()
			time.Sleep(c.interval)
		}
	}()
}

func (c *Collector) PollNow(serverID uuid.UUID) (*models.MetricPoint, error) {
	s, err := models.GetServerByID(c.db, serverID)
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
		if err := models.InsertMetric(c.db, s.ID, m); err != nil {
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

	// Network from /proc/net/dev
	m.NetworkRxBytes, m.NetworkTxBytes = collectNetwork(client)

	// Uptime
	m.UptimeSeconds = collectUptime(client)

	return m, nil
}

func runCmd(client *ssh.Client, cmd string) (string, error) {
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
	out, err := runCmd(client, "cat /proc/stat")
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
	out, err := runCmd(client, "cat /proc/meminfo")
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
	return (memTotal - memAvailable), memTotal
}

func collectNetwork(client *ssh.Client) (int64, int64) {
	out, err := runCmd(client, "cat /proc/net/dev")
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
	out, err := runCmd(client, "cat /proc/uptime")
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
