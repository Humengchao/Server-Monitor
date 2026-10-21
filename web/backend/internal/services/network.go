package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/models"
)

const NetworkTimeout = 25 * time.Second

var ErrNetworkBusy = errors.New("network check already running")
var networkHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
var pingCounts = regexp.MustCompile(`(?m)(\d+) packets transmitted,\s*(\d+) (?:packets )?received`)
var pingRTT = regexp.MustCompile(`(?:rtt|round-trip) min/avg/max(?:/(?:mdev|stddev))? = ([\d.]+)/([\d.]+)/([\d.]+)(?:/([\d.]+))? ms`)

// Counts come from the probe's summary, never from SSH reachability or a missing RTT.
func ParseNetworkPing(output string) models.NetworkSample {
	result := models.NetworkSample{Status: "unknown", ErrorCode: "unrecognized_output"}
	match := pingCounts.FindStringSubmatch(output)
	if match == nil {
		return result
	}
	sent, _ := strconv.Atoi(match[1])
	received, _ := strconv.Atoi(match[2])
	if sent < 1 || sent > 10 || received < 0 || received > sent {
		return result
	}
	result.Sent = &sent
	result.Received = &received
	loss := 100 * float64(sent-received) / float64(sent)
	result.LossPercent = &loss
	result.ErrorCode = ""
	result.Status = "ok"
	if received == 0 {
		result.Status = "down"
	} else if received < sent {
		result.Status = "loss"
	}
	if rtt := pingRTT.FindStringSubmatch(output); rtt != nil && received > 0 {
		values := make([]float64, 4)
		for i := range values {
			if i == 3 && rtt[i+1] == "" {
				continue
			}
			value, err := strconv.ParseFloat(rtt[i+1], 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return result
			}
			values[i] = value
		}
		result.MinMS = &values[0]
		result.AvgMS = &values[1]
		result.MaxMS = &values[2]
		if rtt[4] != "" && received >= 2 {
			result.MdevMS = &values[3]
		}
	}
	return result
}

func networkPowerShell(host string, count int) string {
	// .NET Ping is locale-independent and exposes a status for each attempted packet.
	script := fmt.Sprintf(`$ErrorActionPreference='Stop';$p=New-Object System.Net.NetworkInformation.Ping;$values=@();$sent=0;try {for($i=0;$i -lt %d;$i++) {$sent++;$r=$p.Send('%s',1000);if($r.Status -eq 'Success'){$values+=([double]$r.RoundtripTime)}};@{sent=$sent;values=@($values)}|ConvertTo-Json -Compress} finally {$p.Dispose()}`, count, host)
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(script)
}
func parseNetworkWindows(output string) models.NetworkSample {
	var decoded struct {
		Sent   int       `json:"sent"`
		Values []float64 `json:"values"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil || decoded.Sent < 1 || decoded.Sent > 10 || len(decoded.Values) > decoded.Sent {
		return models.NetworkSample{Status: "unknown", ErrorCode: "unrecognized_output"}
	}
	received := len(decoded.Values)
	loss := 100 * float64(decoded.Sent-received) / float64(decoded.Sent)
	s := models.NetworkSample{Status: "ok", Sent: &decoded.Sent, Received: &received, LossPercent: &loss}
	if received == 0 {
		s.Status = "down"
		return s
	}
	if received < decoded.Sent {
		s.Status = "loss"
	}
	low, high, sum := math.Inf(1), float64(0), float64(0)
	for _, v := range decoded.Values {
		if v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
			return models.NetworkSample{Status: "unknown", ErrorCode: "unrecognized_output"}
		}
		low = math.Min(low, v)
		high = math.Max(high, v)
		sum += v
	}
	avg := sum / float64(received)
	variance := float64(0)
	for _, v := range decoded.Values {
		variance += (v - avg) * (v - avg)
	}
	mdev := math.Sqrt(variance / float64(received))
	s.MinMS = &low
	s.MaxMS = &high
	s.AvgMS = &avg
	if received >= 2 {
		s.MdevMS = &mdev
	}
	return s
}

type NetworkMonitor struct {
	db      *models.DB
	cache   *SSHConnCache
	slots   chan struct{}
	mu      sync.Mutex
	running map[uuid.UUID]bool
	cancel  context.CancelFunc
	done    chan struct{}
	start   sync.Once
}

func NewNetworkMonitor(db *models.DB, cache *SSHConnCache) *NetworkMonitor {
	return &NetworkMonitor{db: db, cache: cache, slots: make(chan struct{}, 4), running: map[uuid.UUID]bool{}}
}

func (m *NetworkMonitor) Start() {
	m.start.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.done = make(chan struct{})
		go func() {
			defer close(m.done)
			timer := time.NewTicker(time.Minute)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					m.cycle(ctx)
				}
			}
		}()
	})
}
func (m *NetworkMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
}
func (m *NetworkMonitor) cycle(ctx context.Context) {
	checks, err := models.ListNetworkChecks(ctx, m.db.Raw, nil, true)
	if err != nil {
		log.Printf("network checks: %v", err)
		return
	}
	var wg sync.WaitGroup
	// Queue a bounded worker pool, not one goroutine per check.
	jobs := make(chan models.NetworkCheck)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if _, err := m.Run(ctx, c.UserID, c.ID); err != nil && ctx.Err() == nil && !errors.Is(err, ErrNetworkBusy) {
					log.Printf("network sample %s: %v", c.ID, err)
				}
			}
		}()
	}
	for _, check := range checks {
		select {
		case jobs <- check:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
	_, _ = m.db.Raw.ExecContext(ctx, `DELETE FROM network_samples WHERE recorded_at<NOW()-INTERVAL '7 days'`)
}
func (m *NetworkMonitor) Run(ctx context.Context, user, id uuid.UUID) (models.NetworkSample, error) {
	callerContext := ctx
	m.mu.Lock()
	if m.running[id] {
		m.mu.Unlock()
		return models.NetworkSample{}, ErrNetworkBusy
	}
	m.running[id] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.running, id); m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, NetworkTimeout)
	defer cancel()
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return models.NetworkSample{}, ctx.Err()
	}
	check, err := models.GetNetworkCheck(ctx, m.db.Raw, id, user)
	if err != nil {
		return models.NetworkSample{}, err
	}
	target, err := models.GetServerByIDAndUser(m.db, check.TargetServerID, user)
	if err != nil {
		return models.NetworkSample{}, err
	}
	var source *models.Server
	if check.SourceServerID != nil {
		source, err = models.GetServerByIDAndUser(m.db, *check.SourceServerID, user)
		if err != nil {
			return models.NetworkSample{}, err
		}
	}
	sample := models.NetworkSample{RecordedAt: time.Now().UTC(), Status: "unknown"}
	if !check.Enabled || !target.MonitoringEnabled || (source != nil && !source.MonitoringEnabled) {
		sample.ErrorCode = "paused"
		return sample, nil
	}
	hostname := strings.Trim(target.Host, "[]")
	if target.ConnectionMode == "agent" && (hostname == "" || hostname == "agent") {
		sample.Status = "error"
		sample.ErrorCode = "target_address_required"
	} else if !validNetworkTarget(hostname) {
		sample.Status = "error"
		sample.ErrorCode = "invalid_target"
	} else {
		windows := source != nil && source.ServerType == "windows" || source == nil && runtime.GOOS == "windows"
		var output string
		var truncated bool
		if source == nil {
			var command *exec.Cmd
			if windows {
				parts := strings.Split(networkPowerShell(hostname, check.Packets), " ")
				command = exec.CommandContext(ctx, parts[0], parts[1:]...)
			} else {
				command = exec.CommandContext(ctx, "ping", "-n", "-i", "0.2", "-c", strconv.Itoa(check.Packets), "-W", "1", "-w", "15", "--", hostname)
				command.Env = append(command.Environ(), "LC_ALL=C")
			}
			writer := &syncLimitedWriter{limit: 32 * 1024}
			command.Stdout, command.Stderr = writer, writer
			err = command.Run()
			output, truncated = writer.result()
		} else {
			client, connectErr := m.cache.GetContext(ctx, source)
			if connectErr != nil {
				sample.Status = "error"
				sample.ErrorCode = "source_unreachable"
				err = connectErr
			} else {
				command := fmt.Sprintf("LC_ALL=C ping -n -i 0.2 -c %d -W 1 -w 15 -- '%s'", check.Packets, hostname)
				if windows {
					command = networkPowerShell(hostname, check.Packets)
				}
				output, truncated, err = runCmdContext(ctx, client, command, 32*1024)
			}
		}
		if sample.ErrorCode == "" {
			parsed := ParseNetworkPing(output)
			if windows {
				parsed = parseNetworkWindows(output)
			}
			parsed.RecordedAt = sample.RecordedAt
			sample = parsed
			if truncated {
				sample = models.NetworkSample{RecordedAt: sample.RecordedAt, Status: "unknown", ErrorCode: "output_limit"}
			}
			if err != nil && sample.Sent == nil {
				sample.Status = "error"
				sample.ErrorCode = "probe_failed"
				if ctx.Err() != nil {
					sample.ErrorCode = "timeout"
				}
			}
		}
	}
	if ctx.Err() != nil {
		if callerContext.Err() != nil {
			return sample, callerContext.Err()
		}
		// A completed deadline is a failed measurement, not a silently missing
		// row. Preserve unknown packet counts instead of inventing 100% loss.
		sample = models.NetworkSample{RecordedAt: sample.RecordedAt, Status: "error", ErrorCode: "timeout"}
		var saveCancel context.CancelFunc
		ctx, saveCancel = context.WithTimeout(callerContext, 3*time.Second)
		defer saveCancel()
	}
	payload, err := json.Marshal(map[string]any{"rule": check.Name, "server": target.Name, "metric": "network", "timestamp": sample.RecordedAt,
		"check_id": check.ID, "sample": sample})
	if err != nil {
		return sample, err
	}
	return models.InsertNetworkSampleWithNotification(ctx, m.db, check, sample, payload)
}

func validNetworkTarget(host string) bool {
	return net.ParseIP(host) != nil || networkHost.MatchString(host) && !strings.Contains(host, ":")
}
