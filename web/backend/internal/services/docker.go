package services

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Docker commands are run over SSH, so both their wall-clock duration and the
// amount of output retained in memory need an explicit bound. A host with a
// large number of containers can still fit comfortably within this limit while
// a broken CLI cannot make an API request grow without bound.
const (
	DockerCommandTimeout   = 15 * time.Second
	DockerListOutputLimit  = 512 * 1024
	DockerStatsOutputLimit = 1024 * 1024
)

// DockerContainerStats contains the values reported by `docker stats` and
// `docker ps --size`. All byte values are bytes. Block I/O values are cumulative
// since the container started. A stopped container normally has no stats row,
// so StatsAvailable lets callers distinguish that case from a genuine zero
// reading.
type DockerContainerStats struct {
	CPUPercent       float64
	MemoryUsage      int64
	MemoryLimit      int64
	MemoryPercent    float64
	DiskReadBytes    int64
	DiskWriteBytes   int64
	BlockIOAvailable bool
	DiskUsage        int64
	StatsAvailable   bool
}

// RunDockerCmdBounded runs a non-interactive Docker command with a timeout and
// output cap. It retries with passwordless sudo when the current SSH user does
// not have permission to talk to the Docker daemon. Keeping sudo non-
// interactive is important: a password prompt must never hold an API request
// open until the SSH server closes the channel.
func RunDockerCmdBounded(client *ssh.Client, args string, timeout time.Duration, limit int) (string, bool, error) {
	out, truncated, err := RunCmdBounded(client, "docker "+args, timeout, limit)
	if err == nil {
		return out, truncated, nil
	}
	// A timeout is a transport/remote execution failure, not a permissions
	// failure. Retrying the same command through sudo would double the wait and
	// can outlive the HTTP request without improving the result.
	if strings.Contains(err.Error(), "timed out after") {
		return out, truncated, err
	}
	out, truncated, sudoErr := RunCmdBounded(client, "sudo -n docker "+args, timeout, limit)
	if sudoErr != nil {
		return out, truncated, sudoErr
	}
	return out, truncated, nil
}

type dockerStatsRow struct {
	ID        string `json:"ID"`
	Container string `json:"Container"`
	CPUPerc   string `json:"CPUPerc"`
	MemUsage  string `json:"MemUsage"`
	MemPerc   string `json:"MemPerc"`
	BlockIO   string `json:"BlockIO"`
}

// ParseDockerStats parses one JSON object per line as emitted by
// `docker stats --no-stream --format '{{json .}}'`. Invalid lines (for
// example, a daemon warning printed by an older Docker version) are ignored so
// one noisy container does not hide all of the valid readings.
func ParseDockerStats(output string) map[string]DockerContainerStats {
	stats := make(map[string]DockerContainerStats)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		var row dockerStatsRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		id := strings.TrimSpace(row.ID)
		if id == "" {
			id = strings.TrimSpace(row.Container)
		}
		if id == "" {
			continue
		}
		cpu := parseDockerPercent(row.CPUPerc)
		memUsed, memLimit := parseDockerMemoryPair(row.MemUsage)
		readBytes, writeBytes, blockIOAvailable := parseDockerBlockIO(row.BlockIO)
		stats[id] = DockerContainerStats{
			CPUPercent:       cpu,
			MemoryUsage:      memUsed,
			MemoryLimit:      memLimit,
			MemoryPercent:    parseDockerPercent(row.MemPerc),
			DiskReadBytes:    readBytes,
			DiskWriteBytes:   writeBytes,
			BlockIOAvailable: blockIOAvailable,
			StatsAvailable:   true,
		}
	}
	return stats
}

// parseDockerBlockIO parses Docker's cumulative block I/O pair. Docker may
// return a dash while the storage driver has no accounting support; that is
// different from a valid 0B / 0B reading.
func parseDockerBlockIO(raw string) (readBytes, writeBytes int64, available bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "-" || strings.TrimSpace(parts[1]) == "-" {
		return 0, 0, false
	}
	left, right := ParseDockerSize(parts[0]), ParseDockerSize(parts[1])
	leftOK := dockerSizeRE.MatchString(strings.TrimSpace(parts[0]))
	rightOK := dockerSizeRE.MatchString(strings.TrimSpace(parts[1]))
	return left, right, leftOK && rightOK
}

func parseDockerPercent(raw string) float64 {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	if raw == "" || raw == "-" || raw == "--" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func parseDockerMemoryPair(raw string) (int64, int64) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	return ParseDockerSize(parts[0]), ParseDockerSize(parts[1])
}

// ParseDockerSize converts Docker's human-readable size format (for example
// "12.5MiB", "1.2GB", or "0B") into bytes. Docker uses both SI and IEC
// suffixes depending on the command and version, so both are accepted.
func ParseDockerSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" || raw == "--" {
		return 0
	}
	// `docker ps --size` may append "(virtual ... )". The writable layer is
	// the first value, which is the one useful for per-container disk usage.
	if i := strings.IndexByte(raw, '('); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	m := dockerSizeRE.FindStringSubmatch(raw)
	if len(m) != 3 {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil || n < 0 {
		return 0
	}
	multiplier := float64(1)
	switch strings.ToUpper(m[2]) {
	case "KB":
		multiplier = 1e3
	case "MB":
		multiplier = 1e6
	case "GB":
		multiplier = 1e9
	case "TB":
		multiplier = 1e12
	case "PB":
		multiplier = 1e15
	case "KIB":
		multiplier = 1 << 10
	case "MIB":
		multiplier = 1 << 20
	case "GIB":
		multiplier = 1 << 30
	case "TIB":
		multiplier = 1 << 40
	case "PIB":
		multiplier = 1 << 50
	case "B":
		multiplier = 1
	default:
		return 0
	}
	bytes := n * multiplier
	if bytes > float64(^uint64(0)>>1) {
		return int64(^uint64(0) >> 1)
	}
	return int64(bytes + 0.5)
}

var dockerSizeRE = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*([kmgtp]?i?b)$`)
var dockerVirtualSizeRE = regexp.MustCompile(`(?i)\(\s*virtual\s+([^)]*)\)`)

// DockerSizeFromPS extracts the writable layer size from the value returned
// by the `{{.Size}}` docker ps template. It is kept separate from the command
// runner so the parser can be unit-tested without an SSH daemon.
func DockerSizeFromPS(raw string) int64 { return ParseDockerSize(raw) }

// DockerSizesFromPS returns the writable layer and virtual total reported by
// `docker ps --size`. The virtual value includes the image layers, while the
// writable value is the container's own changed layer. ok is false when the
// CLI did not return a parseable size (for example, on an older Docker CLI).
func DockerSizesFromPS(raw string) (writable, virtual int64, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" || raw == "--" {
		return 0, 0, false
	}
	writable = ParseDockerSize(raw)
	// ParseDockerSize returns zero for both a real 0B and malformed input, so
	// inspect the first token separately to distinguish those cases.
	first := raw
	if i := strings.IndexByte(first, '('); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	firstOK := dockerSizeRE.MatchString(first)
	match := dockerVirtualSizeRE.FindStringSubmatch(raw)
	if len(match) == 2 {
		virtual = ParseDockerSize(match[1])
	}
	return writable, virtual, firstOK
}

// MergeDockerDiskUsage applies sizes collected from `docker ps --size` to an
// existing stats map. The map is intentionally keyed by container ID because
// stats may only return running containers.
func MergeDockerDiskUsage(stats map[string]DockerContainerStats, id, size string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	entry := stats[id]
	entry.DiskUsage = ParseDockerSize(size)
	stats[id] = entry
}
