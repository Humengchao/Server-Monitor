package services

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ProcessInfo is one row of a host's process table. Fields a platform cannot
// report are left at their zero value rather than guessed at.
type ProcessInfo struct {
	PID        int     `json:"pid"`
	User       string  `json:"user"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float64 `json:"mem_percent"`
	RSSBytes   int64   `json:"rss_bytes"`
	State      string  `json:"state"`
	ElapsedSec int64   `json:"elapsed_seconds"`
	Command    string  `json:"command"`
}

// MaxProcesses bounds the response so a host running thousands of processes
// can't push a multi-megabyte payload into the browser.
const MaxProcesses = 300

// linuxProcessCommand asks procps for exactly the columns we render, with the
// per-column "=" suffix suppressing headers. args must stay last: it is the
// only field that can contain spaces.
const linuxProcessCommand = `ps -eo pid=,user=,pcpu=,pmem=,rss=,stat=,etimes=,args= 2>/dev/null | head -c 2000000`

// linuxProcessFallbackCommand is the portable form. BusyBox and BSD ps reject
// the -o column list above, but "ps aux" is available essentially everywhere.
// It reports no elapsed seconds, so that column comes back empty.
const linuxProcessFallbackCommand = `ps aux 2>/dev/null | head -c 2000000`

// ListLinuxProcesses reads the process table over an existing SSH connection,
// falling back to the portable "ps aux" form when the host's ps does not
// understand the richer column list.
func ListLinuxProcesses(client *ssh.Client) ([]ProcessInfo, error) {
	out, err := RunCmd(client, linuxProcessCommand)
	if procs := parseLinuxPS(out); len(procs) > 0 {
		return procs, nil
	}
	fallback, fallbackErr := RunCmd(client, linuxProcessFallbackCommand)
	if procs := parsePSAux(fallback); len(procs) > 0 {
		return procs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	if fallbackErr != nil {
		return nil, fmt.Errorf("list processes: %w", fallbackErr)
	}
	return []ProcessInfo{}, nil
}

// parseLinuxPS reads "pid user pcpu pmem rss stat etimes args" rows. A row
// whose PID does not parse is skipped, which also discards any header or error
// text a host might have printed.
func parseLinuxPS(out string) []ProcessInfo {
	var procs []ProcessInfo
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		mem, _ := strconv.ParseFloat(fields[3], 64)
		rssKB, _ := strconv.ParseInt(fields[4], 10, 64)
		elapsed, _ := strconv.ParseInt(fields[6], 10, 64)
		procs = append(procs, ProcessInfo{
			PID:        pid,
			User:       fields[1],
			CPUPercent: cpu,
			MemPercent: mem,
			RSSBytes:   rssKB * 1024,
			State:      fields[5],
			ElapsedSec: elapsed,
			// Rejoin from the raw line so multiple spaces inside a command line
			// don't get collapsed away.
			Command: joinRest(line, fields[:7]),
		})
	}
	return procs
}

// parsePSAux reads the BSD-style "USER PID %CPU %MEM VSZ RSS TTY STAT START
// TIME COMMAND" layout.
func parsePSAux(out string) []ProcessInfo {
	var procs []ProcessInfo
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 11 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		mem, _ := strconv.ParseFloat(fields[3], 64)
		rssKB, _ := strconv.ParseInt(fields[5], 10, 64)
		procs = append(procs, ProcessInfo{
			PID:        pid,
			User:       fields[0],
			CPUPercent: cpu,
			MemPercent: mem,
			RSSBytes:   rssKB * 1024,
			State:      fields[7],
			Command:    joinRest(line, fields[:10]),
		})
	}
	return procs
}

// joinRest returns the remainder of line after the given leading fields,
// preserving the original spacing of the tail.
func joinRest(line string, leading []string) string {
	rest := strings.TrimLeft(line, " \t")
	for _, field := range leading {
		rest = strings.TrimPrefix(rest, field)
		rest = strings.TrimLeft(rest, " \t")
	}
	return strings.TrimSpace(rest)
}

// windowsProcessScript emits pipe-delimited rows. PercentProcessorTime from the
// perf counters is summed across cores, so it is divided by the core count to
// match the 0-100 scale Linux reports. The owning user is deliberately not
// collected: it costs one GetOwner call per process.
const windowsProcessScript = `$ErrorActionPreference='SilentlyContinue'
$cs = Get-CimInstance Win32_ComputerSystem
$total = [double]$cs.TotalPhysicalMemory
$cores = [double]$cs.NumberOfLogicalProcessors
if ($cores -le 0) { $cores = 1 }
Get-CimInstance Win32_PerfFormattedData_PerfProc_Process |
  Where-Object { $_.IDProcess -gt 0 -and $_.Name -ne '_Total' } |
  ForEach-Object {
    $cpu = [math]::Round([double]$_.PercentProcessorTime / $cores, 1)
    $rss = [int64]$_.WorkingSetPrivate
    $mem = if ($total -gt 0) { [math]::Round(($rss / $total) * 100, 1) } else { 0 }
    Write-Output ("{0}|{1}|{2}|{3}|{4}|{5}" -f $_.IDProcess, $cpu, $mem, $rss, [int64]$_.ElapsedTime, $_.Name)
  }`

func ListWindowsProcesses(client *ssh.Client) ([]ProcessInfo, error) {
	out, err := RunCmd(client, "powershell -NoProfile -NonInteractive -EncodedCommand "+encodePowerShell(windowsProcessScript))
	procs := parseWindowsProcesses(out)
	if len(procs) == 0 && err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return procs, nil
}

// parseWindowsProcesses reads "pid|cpu|mem|rss|elapsed|name" rows.
func parseWindowsProcesses(out string) []ProcessInfo {
	var procs []ProcessInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) < 6 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || pid <= 0 {
			continue
		}
		cpu, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			continue
		}
		mem, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		rss, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)
		elapsed, _ := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64)
		procs = append(procs, ProcessInfo{
			PID:        pid,
			CPUPercent: cpu,
			MemPercent: mem,
			RSSBytes:   rss,
			State:      "running",
			ElapsedSec: elapsed,
			Command:    strings.Join(parts[5:], "|"),
		})
	}
	return procs
}

// TopProcesses sorts by CPU (memory breaks ties) and trims to the response cap,
// so the busiest processes survive truncation.
func TopProcesses(procs []ProcessInfo, limit int) []ProcessInfo {
	sort.SliceStable(procs, func(i, j int) bool {
		if procs[i].CPUPercent != procs[j].CPUPercent {
			return procs[i].CPUPercent > procs[j].CPUPercent
		}
		return procs[i].MemPercent > procs[j].MemPercent
	})
	if limit > 0 && len(procs) > limit {
		procs = procs[:limit]
	}
	return procs
}

// KillProcess signals a process by PID. The PID is formatted as an integer, so
// no caller-supplied text ever reaches the remote shell.
func KillProcess(client *ssh.Client, pid int, serverType string, force bool) error {
	if pid <= 1 {
		// PID 1 is init (or the container entrypoint); killing it takes the whole
		// host or container down, which is never what a "kill process" button
		// should do.
		return fmt.Errorf("refusing to signal PID %d", pid)
	}
	var cmd string
	if serverType == "windows" {
		cmd = fmt.Sprintf("powershell -NoProfile -NonInteractive -EncodedCommand %s",
			encodePowerShell(fmt.Sprintf("Stop-Process -Id %d -Force -ErrorAction Stop", pid)))
	} else {
		signal := "TERM"
		if force {
			signal = "KILL"
		}
		cmd = fmt.Sprintf("kill -%s %d", signal, pid)
	}
	// Combined output: the reason a kill was refused ("Operation not permitted",
	// "No such process") only ever appears on stderr.
	out, err := RunCmdCombined(client, cmd)
	if err != nil {
		if detail := cleanCommandError(out); detail != "" {
			return fmt.Errorf("%s", detail)
		}
		return err
	}
	return nil
}

// shellPrefix matches the location a shell prepends to builtin errors, e.g.
// "bash: line 1: " or "sh: 1: ".
var shellPrefix = regexp.MustCompile(`^[\w.-]+: (?:line )?\d+: `)

// cleanCommandError reduces a remote diagnostic to one presentable line.
// PowerShell prints multi-line stack detail, and shells prefix builtin errors
// with a script location that means nothing to the reader.
func cleanCommandError(text string) string {
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = text[:idx]
	}
	text = strings.TrimSpace(text)
	return strings.TrimSpace(shellPrefix.ReplaceAllString(text, ""))
}
