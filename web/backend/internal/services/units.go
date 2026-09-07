package services

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ServiceUnit is one entry of a host's service inventory. systemd reports three
// independent state axes; hosts that report fewer leave the extras empty rather
// than have a plausible value invented for them.
type ServiceUnit struct {
	Name string `json:"name"`
	// Load is whether the unit file itself resolved ("loaded", "not-found",
	// "masked"). A not-found unit still appearing in the list is usually a stale
	// reference from some other unit's dependency.
	Load string `json:"load"`
	// Active is the high-level state ("active", "inactive", "failed").
	Active string `json:"active"`
	// Sub is the type-specific detail ("running", "exited", "dead"), which is
	// what separates a one-shot that finished from a daemon that died.
	Sub string `json:"sub"`
	// Enabled is the boot-time disposition ("enabled", "disabled", "static",
	// "masked"), empty when the host does not report it. It answers a different
	// question from Active: a stopped-but-enabled service returns on reboot, a
	// stopped-and-disabled one does not.
	Enabled     string `json:"enabled"`
	Description string `json:"description"`
}

// MaxServiceUnits bounds the response. A full desktop install lists a few
// hundred units, so the cap is high enough never to bite in practice and low
// enough to keep the payload small.
const MaxServiceUnits = 500

// ServiceManager names the mechanism that answered. It is reported because the
// sysv fallback yields strictly less — no sub-state and no boot disposition — and
// the UI needs to know to hide those columns rather than render them empty, as
// though the data were missing.
type ServiceManager string

const (
	ManagerSystemd ServiceManager = "systemd"
	ManagerSysv    ServiceManager = "sysv"
	ManagerWindows ServiceManager = "windows"
)

// ErrNoServiceManager means the host offers no way to enumerate services. It is
// reported as its own case because an empty list would read as "this host has no
// services", which is never true.
var ErrNoServiceManager = errors.New("no service manager available to this SSH user: neither systemctl nor a service script is on its PATH")

// ErrServiceManagerUnreachable means systemd is installed but did not answer —
// almost always an unprivileged SSH user with no bus to talk to.
//
// This is deliberately distinct from having no manager at all: the two send an
// operator looking in completely different places.
var ErrServiceManagerUnreachable = errors.New("systemd is installed but did not respond; this SSH user may not be permitted to query it")

// ServiceInventoryUnavailable reports whether the failure is a property of the
// host rather than of the request — a state to explain in the UI, not an error
// to log.
func ServiceInventoryUnavailable(err error) bool {
	return errors.Is(err, ErrNoServiceManager) || errors.Is(err, ErrServiceManagerUnreachable)
}

// --plain drops the tree-drawing bullet from the UNIT column and --no-legend
// drops both the header and the trailing summary, leaving only data rows.
const linuxUnitsCommand = `systemctl list-units --type=service --all --no-pager --plain --no-legend 2>/dev/null | head -c 500000`

// A second query, because list-units does not report the boot-time disposition
// at all. Runs on the same pooled connection, so it costs a session, not a
// handshake.
const linuxUnitFilesCommand = `systemctl list-unit-files --type=service --no-pager --plain --no-legend 2>/dev/null | head -c 500000`

// linuxServiceFallbackCommand covers hosts with no systemd — most containers,
// and anything still on sysvinit. It reports far less: a name, and whether the
// init script believes it is running. stderr is folded in because sysv-rc warns
// on stderr while still printing usable rows on stdout.
const linuxServiceFallbackCommand = `service --status-all 2>&1 | head -c 200000`

// ListLinuxServices reads the service inventory over an existing SSH
// connection, falling back to sysv init scripts when systemd is absent.
func ListLinuxServices(client *ssh.Client) ([]ServiceUnit, ServiceManager, error) {
	out, _ := RunCmd(client, linuxUnitsCommand)
	if units := parseSystemdUnits(out); len(units) > 0 {
		// The boot disposition is a nice-to-have: if the second query fails the
		// inventory is still worth returning without it.
		if files, filesErr := RunCmd(client, linuxUnitFilesCommand); filesErr == nil {
			applyUnitFileStates(units, parseUnitFileStates(files))
		}
		return units, ManagerSystemd, nil
	}

	// systemd produced nothing usable. Which case that is decides both the
	// message and whether falling back is even correct, so ask the host.
	tools := probeServiceTools(client)
	if tools.systemctl {
		// On a systemd host the init scripts answer without consulting systemd,
		// and without privilege they answer wrongly — nginx reads as stopped
		// while it is serving traffic. Reporting that we could not ask beats
		// showing data that is confidently incorrect.
		return nil, "", ErrServiceManagerUnreachable
	}
	if tools.service {
		fallback, fallbackErr := RunCmd(client, linuxServiceFallbackCommand)
		if legacy := parseSysvServices(fallback); len(legacy) > 0 {
			return legacy, ManagerSysv, nil
		}
		if fallbackErr != nil {
			return nil, "", fmt.Errorf("list services: %w", fallbackErr)
		}
	}
	return nil, "", ErrNoServiceManager
}

// serviceTools records which managers the remote shell can actually invoke.
type serviceTools struct {
	systemctl bool
	service   bool
}

// probeServiceTools asks the remote shell what it can reach. "command -v" rather
// than a filesystem test because PATH is what decides: /usr/sbin is absent from
// a non-root SSH user's PATH, so a service binary sitting there may as well not
// exist. The trailing "true" keeps the exit status clean when neither is found.
func probeServiceTools(client *ssh.Client) serviceTools {
	out, _ := RunCmd(client,
		`command -v systemctl >/dev/null 2>&1 && echo systemctl; command -v service >/dev/null 2>&1 && echo service; true`)
	return parseServiceTools(out)
}

// parseServiceTools matches whole lines rather than searching the blob, so a
// login banner or an MOTD mentioning either word cannot fake a capability.
func parseServiceTools(out string) serviceTools {
	var tools serviceTools
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "systemctl":
			tools.systemctl = true
		case "service":
			tools.service = true
		}
	}
	return tools
}

// unitBullet matches the status glyph systemd prefixes to a row when it decides
// the output is a terminal. --plain suppresses it, but an older systemd may
// print it anyway.
var unitBullet = regexp.MustCompile(`^[\p{Zs}]*[●*→↻×✕✗]+[\p{Zs}]*`)

// parseSystemdUnits reads "UNIT LOAD ACTIVE SUB DESCRIPTION" rows. A row whose
// name does not end in .service is skipped, which also discards any header or
// error text the host printed.
func parseSystemdUnits(out string) []ServiceUnit {
	var units []ServiceUnit
	for _, line := range strings.Split(out, "\n") {
		line = unitBullet.ReplaceAllString(line, "")
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		units = append(units, ServiceUnit{
			Name:        fields[0],
			Load:        fields[1],
			Active:      fields[2],
			Sub:         fields[3],
			Description: strings.Join(fields[4:], " "),
		})
	}
	return units
}

// parseUnitFileStates reads "UNIT FILE STATE [VENDOR PRESET]" rows. The vendor
// preset column only exists on newer systemd, so only the first two fields are
// read.
func parseUnitFileStates(out string) map[string]string {
	states := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		states[fields[0]] = fields[1]
	}
	return states
}

func applyUnitFileStates(units []ServiceUnit, states map[string]string) {
	for i := range units {
		if state, ok := states[units[i].Name]; ok {
			units[i].Enabled = state
		}
	}
}

// sysvRow matches Debian's "service --status-all" output: a bracketed glyph
// followed by the script name. "+" is running, "-" is stopped and "?" means the
// script declines to say.
var sysvRow = regexp.MustCompile(`^\s*\[\s*([+?-])\s*\]\s+(\S+)\s*$`)

// parseSysvServices maps init-script rows onto the same shape. Only Active is
// populated: a sysv script reports nothing else, and guessing at Sub or the
// boot disposition would be inventing data.
func parseSysvServices(out string) []ServiceUnit {
	var units []ServiceUnit
	for _, line := range strings.Split(out, "\n") {
		match := sysvRow.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		active := "inactive"
		switch match[1] {
		case "+":
			active = "active"
		case "?":
			active = "unknown"
		}
		units = append(units, ServiceUnit{Name: match[2], Active: active})
	}
	return units
}

// DisplayName stays last for the same reason ps keeps args last: it is the only
// field that can plausibly contain the separator.
const windowsServicesScript = `$ErrorActionPreference='SilentlyContinue'
Get-Service | ForEach-Object {
  $state = if ($_.Status -eq 'Running') { 'active' } else { 'inactive' }
  Write-Output ("{0}|{1}|{2}|{3}|{4}" -f $_.Name, $state, "$($_.Status)".ToLower(), "$($_.StartType)".ToLower(), $_.DisplayName)
}`

// ListWindowsServices reads the Service Control Manager inventory. Windows has
// only one state axis, so Active carries the mapped value and Sub keeps the raw
// one; Load stays empty because there is no equivalent notion.
func ListWindowsServices(client *ssh.Client) ([]ServiceUnit, error) {
	cmd := "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(windowsServicesScript)
	out, err := RunCmd(client, cmd)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return parseWindowsServices(out), nil
}

// parseWindowsServices reads "name|active|status|starttype|displayname" rows.
func parseWindowsServices(out string) []ServiceUnit {
	var units []ServiceUnit
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 5)
		if len(fields) < 4 || fields[0] == "" {
			continue
		}
		unit := ServiceUnit{
			Name:    fields[0],
			Active:  fields[1],
			Sub:     fields[2],
			Enabled: fields[3],
		}
		if len(fields) > 4 {
			unit.Description = fields[4]
		}
		units = append(units, unit)
	}
	return units
}

// SortServiceUnits surfaces problems first: failed units, then running ones,
// then everything else, alphabetically inside each group. Someone opening this
// tab is nearly always looking for what broke, and the cap below truncates the
// tail — so the ordering decides what survives truncation.
func SortServiceUnits(units []ServiceUnit) {
	sort.SliceStable(units, func(i, j int) bool {
		if ri, rj := unitRank(units[i]), unitRank(units[j]); ri != rj {
			return ri < rj
		}
		return units[i].Name < units[j].Name
	})
}

func unitRank(u ServiceUnit) int {
	switch {
	case u.Active == "failed" || u.Sub == "failed":
		return 0
	case u.Active == "active":
		return 1
	case u.Active == "activating" || u.Active == "deactivating":
		return 2
	default:
		return 3
	}
}

// ServiceActions are the only control verbs accepted. reload earns its place
// because restarting a web server drops every connection it is serving, and a
// reload does not.
var ServiceActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "reload": true,
}

// unitNamePattern is deliberately narrow: every character it admits is inert to
// a shell, which is what makes interpolating a name straight into a command line
// safe without quoting. Systemd's own escape syntax uses backslashes, excluded
// for exactly that reason — a unit needing them cannot be controlled from here,
// which is the right trade against handing a shell metacharacter to a remote
// root shell.
var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`)

// ValidServiceName reports whether a name is safe to pass to a control verb.
func ValidServiceName(name string) bool { return unitNamePattern.MatchString(name) }

// ControlService runs one control verb against one unit and reports the host's
// own diagnostic on failure.
//
// Nothing here escalates privilege. systemctl and the Service Control Manager
// both refuse a non-privileged caller, and that refusal is surfaced verbatim
// rather than retried under sudo: silently gaining privilege is not a decision
// a monitoring panel should make on the operator's behalf.
func ControlService(client *ssh.Client, name, action, serverType string) error {
	if !ServiceActions[action] {
		return fmt.Errorf("unsupported action %q", action)
	}
	if !ValidServiceName(name) {
		return errors.New("unsupported service name")
	}

	var cmd string
	if serverType == "windows" {
		verb := map[string]string{
			"start": "Start-Service", "stop": "Stop-Service", "restart": "Restart-Service",
		}[action]
		if verb == "" {
			// Mapping reload onto Restart-Service would quietly drop every
			// connection the service is holding — the exact thing the caller
			// chose reload to avoid.
			return fmt.Errorf("%s is not supported on Windows", action)
		}
		cmd = "powershell -NoProfile -NonInteractive -EncodedCommand " +
			encodePowerShell(fmt.Sprintf("%s -Name '%s' -ErrorAction Stop", verb, name))
	} else {
		cmd = fmt.Sprintf("systemctl %s %s", action, name)
	}

	// Combined output: "Access denied", "Interactive authentication required"
	// and "Unit not found" all arrive on stderr.
	out, err := RunCmdCombined(client, cmd)
	if err != nil {
		if detail := cleanCommandError(out); detail != "" {
			return errors.New(detail)
		}
		return err
	}
	return nil
}
