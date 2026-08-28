package services

import (
	"errors"
	"testing"
)

func TestParseSystemdUnits(t *testing.T) {
	// Real "systemctl list-units --type=service --all --plain --no-legend"
	// output: a running daemon, a one-shot that exited, a failed unit, one whose
	// unit file is missing, and a description containing spaces.
	out := `  cron.service                loaded active   running Regular background program processing daemon
  keyboard-setup.service      loaded active   exited  Set the console keyboard layout
  nginx.service               loaded failed   failed  A high performance web server
  ghost.service               not-found inactive dead ghost.service
  ssh.service                 loaded active   running OpenBSD Secure Shell server
`
	units := parseSystemdUnits(out)
	if len(units) != 5 {
		t.Fatalf("got %d units, want 5: %+v", len(units), units)
	}
	if units[0].Name != "cron.service" || units[0].Active != "active" || units[0].Sub != "running" {
		t.Errorf("cron = %+v", units[0])
	}
	if units[0].Description != "Regular background program processing daemon" {
		t.Errorf("description not joined: %q", units[0].Description)
	}
	// A one-shot that finished is active/exited, not a failure — the sub-state is
	// the only thing that distinguishes it from a daemon that died.
	if units[1].Active != "active" || units[1].Sub != "exited" {
		t.Errorf("one-shot = %+v, want active/exited", units[1])
	}
	if units[2].Active != "failed" || units[2].Sub != "failed" {
		t.Errorf("failed unit = %+v", units[2])
	}
	if units[3].Load != "not-found" {
		t.Errorf("missing unit file = %+v, want load=not-found", units[3])
	}
}

func TestParseSystemdUnitsIgnoresNoise(t *testing.T) {
	// Headers, the trailing legend, and a bullet glyph an older systemd prints
	// even under --plain must all be discarded or absorbed.
	out := `UNIT LOAD ACTIVE SUB DESCRIPTION
● nginx.service loaded failed failed A high performance web server
  ssh.service   loaded active running OpenBSD Secure Shell server

LOAD   = Reflects whether the unit definition was properly loaded.
5 loaded units listed.
`
	units := parseSystemdUnits(out)
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2: %+v", len(units), units)
	}
	// The bullet must be stripped from the name, not left glued to it.
	if units[0].Name != "nginx.service" {
		t.Errorf("bullet not stripped: %q", units[0].Name)
	}
	if units[1].Name != "ssh.service" {
		t.Errorf("second unit = %q", units[1].Name)
	}
}

func TestParseUnitFileStatesAndApply(t *testing.T) {
	// Old systemd prints two columns; new adds a vendor preset. Both must read.
	out := `ssh.service     enabled  enabled
cron.service    enabled
nginx.service   masked   masked
getty@.service  static
`
	states := parseUnitFileStates(out)
	if len(states) != 4 {
		t.Fatalf("got %d states, want 4: %v", len(states), states)
	}
	if states["nginx.service"] != "masked" || states["cron.service"] != "enabled" {
		t.Errorf("states = %v", states)
	}

	units := []ServiceUnit{{Name: "ssh.service"}, {Name: "orphan.service"}}
	applyUnitFileStates(units, states)
	if units[0].Enabled != "enabled" {
		t.Errorf("ssh enabled = %q, want enabled", units[0].Enabled)
	}
	// A unit with no matching file entry keeps an empty disposition rather than
	// being labelled disabled, which would be a claim the data does not support.
	if units[1].Enabled != "" {
		t.Errorf("orphan enabled = %q, want empty", units[1].Enabled)
	}
}

func TestParseSysvServices(t *testing.T) {
	// Debian's "service --status-all" inside a container, warnings included.
	out := ` [ - ]  apache2
 [ + ]  cron
 [ ? ]  hwclock.sh
 [ + ]  ssh
sysv-rc-conf: warning: something on stderr
`
	units := parseSysvServices(out)
	if len(units) != 4 {
		t.Fatalf("got %d units, want 4: %+v", len(units), units)
	}
	if units[0].Name != "apache2" || units[0].Active != "inactive" {
		t.Errorf("apache2 = %+v, want inactive", units[0])
	}
	if units[1].Active != "active" {
		t.Errorf("cron = %+v, want active", units[1])
	}
	// "?" means the init script declines to report; calling that "inactive"
	// would be inventing a state.
	if units[2].Active != "unknown" {
		t.Errorf("hwclock = %+v, want unknown", units[2])
	}
	// sysv reports no sub-state or boot disposition, and none is guessed at.
	if units[1].Sub != "" || units[1].Enabled != "" {
		t.Errorf("sysv unit invented fields: %+v", units[1])
	}
}

func TestParseWindowsServices(t *testing.T) {
	out := `Spooler|active|running|automatic|Print Spooler
wuauserv|inactive|stopped|manual|Windows Update
BITS|inactive|stopped|manual|Background Intelligent Transfer | Service
`
	units := parseWindowsServices(out)
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3: %+v", len(units), units)
	}
	if units[0].Name != "Spooler" || units[0].Active != "active" || units[0].Enabled != "automatic" {
		t.Errorf("spooler = %+v", units[0])
	}
	// DisplayName is last precisely so a separator inside it cannot shift the
	// other columns; it must survive intact.
	if units[2].Description != "Background Intelligent Transfer | Service" {
		t.Errorf("display name truncated: %q", units[2].Description)
	}
}

func TestSortServiceUnitsSurfacesFailuresFirst(t *testing.T) {
	units := []ServiceUnit{
		{Name: "zzz.service", Active: "inactive", Sub: "dead"},
		{Name: "bbb.service", Active: "active", Sub: "running"},
		{Name: "aaa.service", Active: "inactive", Sub: "dead"},
		{Name: "yyy.service", Active: "failed", Sub: "failed"},
		{Name: "ccc.service", Active: "activating", Sub: "start"},
		{Name: "aab.service", Active: "active", Sub: "exited"},
	}
	SortServiceUnits(units)
	want := []string{
		"yyy.service",                // failed
		"aab.service", "bbb.service", // active, alphabetical
		"ccc.service",                // activating
		"aaa.service", "zzz.service", // the rest, alphabetical
	}
	for i, name := range want {
		if units[i].Name != name {
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, units[i].Name, name, names(units))
		}
	}
}

func names(units []ServiceUnit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.Name
	}
	return out
}

func TestValidServiceNameRejectsShellMetacharacters(t *testing.T) {
	// Every one of these is interpolated straight into a command line, so the
	// validator is the only thing standing between a request body and a remote
	// root shell.
	for _, name := range []string{
		"nginx; rm -rf /",
		"nginx && reboot",
		"nginx`id`",
		"nginx$(id)",
		"nginx|cat",
		"nginx service",
		"../../etc/passwd",
		"nginx\nreboot",
		`dev-disk-by\x2duuid.device`,
		"-nginx",
		"",
	} {
		if ValidServiceName(name) {
			t.Errorf("accepted dangerous name %q", name)
		}
	}
	for _, name := range []string{
		"nginx",
		"nginx.service",
		"ssh.service",
		"getty@tty1.service",
		"user@1000.service",
		"systemd-journald.service",
		"my_app.service",
		"Spooler",
	} {
		if !ValidServiceName(name) {
			t.Errorf("rejected legitimate name %q", name)
		}
	}
}

func TestValidServiceNameLengthBound(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if ValidServiceName(string(long)) {
		t.Error("accepted a 129-character name")
	}
	if !ValidServiceName(string(long[:128])) {
		t.Error("rejected a 128-character name, which is within the bound")
	}
}

func TestParseServiceTools(t *testing.T) {
	both := parseServiceTools("systemctl\nservice\n")
	if !both.systemctl || !both.service {
		t.Errorf("both present = %+v", both)
	}
	only := parseServiceTools("systemctl\n")
	if !only.systemctl || only.service {
		t.Errorf("systemctl only = %+v", only)
	}
	if empty := parseServiceTools(""); empty.systemctl || empty.service {
		t.Errorf("nothing present = %+v", empty)
	}
	// A login banner is the realistic way a substring search would be fooled
	// into reporting a capability the host does not have.
	banner := parseServiceTools("Welcome! Run service --status-all to inspect systemctl units.\n")
	if banner.systemctl || banner.service {
		t.Errorf("banner text was read as a capability: %+v", banner)
	}
}

func TestServiceInventoryUnavailableCoversBothHostStates(t *testing.T) {
	// Both are properties of the host, not failures of the request, and the
	// handler needs to answer them the same way while reporting different text.
	if !ServiceInventoryUnavailable(ErrNoServiceManager) {
		t.Error("ErrNoServiceManager should be a host state")
	}
	if !ServiceInventoryUnavailable(ErrServiceManagerUnreachable) {
		t.Error("ErrServiceManagerUnreachable should be a host state")
	}
	if ServiceInventoryUnavailable(nil) {
		t.Error("nil is not a host state")
	}
	if ServiceInventoryUnavailable(errors.New("ssh dial failed")) {
		t.Error("a transport failure is not a host state")
	}
	// The two must stay distinguishable: conflating "no systemd here" with
	// "systemd would not talk to this user" sends an operator to the wrong place.
	if errors.Is(ErrNoServiceManager, ErrServiceManagerUnreachable) {
		t.Error("the two host states must not be interchangeable")
	}
}

func TestServiceActionsAreClosed(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "reload"} {
		if !ServiceActions[action] {
			t.Errorf("%q should be accepted", action)
		}
	}
	for _, action := range []string{"mask", "disable", "enable", "kill", "", "START"} {
		if ServiceActions[action] {
			t.Errorf("%q should not be accepted", action)
		}
	}
}
