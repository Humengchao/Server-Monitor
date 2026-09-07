package services

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ListeningPort is one socket a host is accepting traffic on.
type ListeningPort struct {
	Protocol string `json:"protocol"`
	// Address is the bind address with any IPv6 brackets stripped, so "::" and
	// "127.0.0.1" read the same way in a table.
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Process and PID are only populated when the host reports them, which
	// requires the SSH user to be allowed to see the owning process. An
	// unprivileged user sees its own sockets attributed and everything else
	// blank.
	Process string `json:"process"`
	PID     int    `json:"pid"`
	// Exposure classifies the bind address, which is what actually decides who
	// can reach the port: "public" for a wildcard or routable bind, "private"
	// for RFC1918 and link-local, "loopback" for 127.0.0.0/8 and ::1.
	Exposure string `json:"exposure"`
}

// MaxListeningPorts bounds the response. A host with more listeners than this
// is unusual enough that the truncation notice is the useful signal.
const MaxListeningPorts = 300

// ss is the modern tool. -p attaches the owning process. The header line is left
// in and discarded by the parser rather than suppressed with -H, which older
// iproute2 does not accept.
const linuxPortsCommand = `ss -tulnp 2>/dev/null | head -c 500000`

// netstat covers hosts without iproute2. Its columns differ enough to need a
// separate parser.
const linuxPortsFallbackCommand = `netstat -tulnp 2>/dev/null | head -c 500000`

// ListLinuxPorts reads the listening sockets over an existing SSH connection.
func ListLinuxPorts(client *ssh.Client) ([]ListeningPort, error) {
	out, err := RunCmd(client, linuxPortsCommand)
	if ports := parseSS(out); len(ports) > 0 {
		return ports, nil
	}
	fallback, fallbackErr := RunCmd(client, linuxPortsFallbackCommand)
	if ports := parseNetstat(fallback); len(ports) > 0 {
		return ports, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list ports: %w", err)
	}
	if fallbackErr != nil {
		return nil, fmt.Errorf("list ports: %w", fallbackErr)
	}
	// Both tools ran and listed nothing. A host really can have no listeners
	// besides the SSH socket the query arrived on, so this is not an error.
	return []ListeningPort{}, nil
}

// ssProcess pulls the first entry out of ss's users:(("name",pid=N,fd=M)) field.
// Multiple processes can share a socket; the first is the one shown.
var ssProcess = regexp.MustCompile(`\(\("([^"]*)",pid=(\d+)`)

// parseSS reads "netid state recv-q send-q local peer [process]" rows. A row
// whose local address has no numeric port is skipped, which is also how the
// header row is discarded.
func parseSS(out string) []ListeningPort {
	var ports []ListeningPort
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		addr, port, ok := splitHostPort(fields[4])
		if !ok {
			continue
		}
		entry := ListeningPort{
			Protocol: strings.ToLower(fields[0]),
			Address:  addr,
			Port:     port,
			Exposure: classifyExposure(addr),
		}
		if match := ssProcess.FindStringSubmatch(line); match != nil {
			entry.Process = match[1]
			entry.PID, _ = strconv.Atoi(match[2])
		}
		ports = append(ports, entry)
	}
	return ports
}

// netstatProcess matches netstat's "PID/Program name" column. The program name
// may be blank or "-" when the caller may not see the owner.
var netstatProcess = regexp.MustCompile(`^(\d+)/(.*)$`)

// parseNetstat reads "proto recv-q send-q local foreign [state] [pid/program]".
// The state column only exists for TCP, so the trailing columns are searched by
// shape rather than by index.
func parseNetstat(out string) []ListeningPort {
	var ports []ListeningPort
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasPrefix(strings.ToLower(fields[0]), "tcp") &&
			!strings.HasPrefix(strings.ToLower(fields[0]), "udp") {
			continue
		}
		addr, port, ok := splitHostPort(fields[3])
		if !ok {
			continue
		}
		entry := ListeningPort{
			Protocol: strings.ToLower(fields[0]),
			Address:  addr,
			Port:     port,
			Exposure: classifyExposure(addr),
		}
		for _, field := range fields[4:] {
			if match := netstatProcess.FindStringSubmatch(field); match != nil {
				entry.PID, _ = strconv.Atoi(match[1])
				entry.Process = cleanNetstatProcessName(match[2])
				break
			}
		}
		ports = append(ports, entry)
	}
	return ports
}

// cleanNetstatProcessName reduces netstat's program column to a program name.
//
// That column holds the process *title*, truncated to the column width, so sshd
// appears as "sshd: /usr/sbin" — which Fields() has already split, leaving
// "sshd:" behind. Everything from the first colon or space is dropped.
//
// Deliberately not applied to ss output: ss quotes the name, so it arrives
// intact, and trimming there would corrupt a name that legitimately contains a
// space.
func cleanNetstatProcessName(name string) string {
	if idx := strings.IndexAny(name, ": \t"); idx >= 0 {
		name = name[:idx]
	}
	return strings.TrimSpace(name)
}

// splitHostPort splits at the last colon, which is what makes it work for both
// "0.0.0.0:22" and "[::]:22". A non-numeric port means the line was not a data
// row at all.
func splitHostPort(value string) (string, int, bool) {
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[idx+1:])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	host := strings.TrimSuffix(strings.TrimPrefix(value[:idx], "["), "]")
	if host == "*" {
		// ss writes a wildcard bind as "*" for IPv6 on some kernels.
		host = "::"
	}
	return host, port, true
}

// classifyExposure maps a bind address to who can reach it. The wildcard bind is
// the one worth flagging: it accepts traffic on every interface the host has,
// including whichever one the cloud provider attached to the public internet.
func classifyExposure(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return "unknown"
	}
	switch {
	case ip.IsUnspecified():
		return "public"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "private"
	default:
		return "public"
	}
}

const windowsPortsScript = `$ErrorActionPreference='SilentlyContinue'
$names = @{}
Get-Process | ForEach-Object { $names[[int]$_.Id] = $_.ProcessName }
Get-NetTCPConnection -State Listen | ForEach-Object {
  Write-Output ("{0}|{1}|{2}|{3}|{4}" -f 'tcp', $_.LocalAddress, $_.LocalPort, $_.OwningProcess, $names[[int]$_.OwningProcess])
}
Get-NetUDPEndpoint | ForEach-Object {
  Write-Output ("{0}|{1}|{2}|{3}|{4}" -f 'udp', $_.LocalAddress, $_.LocalPort, $_.OwningProcess, $names[[int]$_.OwningProcess])
}`

// ListWindowsPorts reads the listening sockets from the TCP/IP stack. Process
// names are resolved from one Get-Process snapshot rather than per socket,
// which turns N remote calls into one.
func ListWindowsPorts(client *ssh.Client) ([]ListeningPort, error) {
	cmd := "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(windowsPortsScript)
	out, err := RunCmd(client, cmd)
	ports := parseWindowsPorts(out)
	if len(ports) == 0 && err != nil {
		return nil, fmt.Errorf("list ports: %w", err)
	}
	return ports, nil
}

// parseWindowsPorts reads "proto|address|port|pid|process" rows.
func parseWindowsPorts(out string) []ListeningPort {
	var ports []ListeningPort
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 4 {
			continue
		}
		port, err := strconv.Atoi(fields[2])
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		addr := strings.TrimSuffix(strings.TrimPrefix(fields[1], "["), "]")
		entry := ListeningPort{
			Protocol: fields[0],
			Address:  addr,
			Port:     port,
			Exposure: classifyExposure(addr),
		}
		entry.PID, _ = strconv.Atoi(fields[3])
		if len(fields) > 4 {
			entry.Process = fields[4]
		}
		ports = append(ports, entry)
	}
	return ports
}

// SortListeningPorts orders by port, then protocol, then address — the order
// someone reading down a port table expects. Exposure is deliberately not the
// primary key: it is a filter in the UI, and reordering by it would scramble
// the one sequence that makes a long list scannable.
func SortListeningPorts(ports []ListeningPort) {
	sort.SliceStable(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Address < ports[j].Address
	})
}
