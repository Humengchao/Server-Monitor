package services

import "testing"

// Real `ps -eo pid=,user=,pcpu=,pmem=,rss=,stat=,etimes=,args=` output:
// PIDs are right-aligned with leading spaces, and commands keep their own
// internal spacing.
const linuxPSSample = `      1 root      0.0  0.1  12345 Ss    864000 /sbin/init splash
    842 www-data  12.5  4.2 340128 Sl     43200 nginx: worker process
   1337 deploy     0.3  0.0   2048 R       3600 ps -eo pid=,user=,pcpu=
     12 root       0.0  0.0      0 I<     864000 [kworker/0:1H-events_highpri]
   9001 postgres  99.9 12.5 998877 Rs      7200 postgres: 16/main: deploy app [local] SELECT
`

func TestParseLinuxPS(t *testing.T) {
	procs := parseLinuxPS(linuxPSSample)
	if len(procs) != 5 {
		t.Fatalf("parsed %d rows, want 5", len(procs))
	}

	init := procs[0]
	if init.PID != 1 || init.User != "root" || init.State != "Ss" {
		t.Errorf("init row = %+v", init)
	}
	if init.RSSBytes != 12345*1024 {
		t.Errorf("rss = %d, want %d (ps reports kB)", init.RSSBytes, 12345*1024)
	}
	if init.ElapsedSec != 864000 {
		t.Errorf("elapsed = %d, want 864000", init.ElapsedSec)
	}
	if init.Command != "/sbin/init splash" {
		t.Errorf("command = %q", init.Command)
	}

	if procs[1].CPUPercent != 12.5 || procs[1].MemPercent != 4.2 {
		t.Errorf("nginx row = %+v", procs[1])
	}
	// A command containing the same characters as earlier columns must not
	// confuse the tail extraction.
	if procs[2].Command != "ps -eo pid=,user=,pcpu=" {
		t.Errorf("self row command = %q", procs[2].Command)
	}
	// Kernel threads have a bracketed name and an angle-bracketed state.
	if procs[3].State != "I<" || procs[3].Command != "[kworker/0:1H-events_highpri]" {
		t.Errorf("kthread row = %+v", procs[3])
	}
	// Internal spacing inside a command line survives.
	if procs[4].Command != "postgres: 16/main: deploy app [local] SELECT" {
		t.Errorf("postgres row command = %q", procs[4].Command)
	}
}

func TestParseLinuxPSRejectsNoise(t *testing.T) {
	// Header lines, usage errors and blank lines must not become rows.
	noisy := `error: unsupported option

  PID USER     %CPU %MEM   RSS STAT ELAPSED COMMAND
ps: invalid option -- 'q'
      7 root      0.0  0.0   512 S        100 /usr/sbin/cron
`
	procs := parseLinuxPS(noisy)
	if len(procs) != 1 {
		t.Fatalf("parsed %d rows, want only the real one: %+v", len(procs), procs)
	}
	if procs[0].PID != 7 {
		t.Errorf("pid = %d, want 7", procs[0].PID)
	}
}

const psAuxSample = `USER       PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root         1  0.0  0.1  22568  4096 ?        Ss   Aug01   0:12 /sbin/init
deploy    2048  7.5  2.0 998877 81920 pts/0    S+   10:30   0:03 python3 -m http.server 8080
`

func TestParsePSAux(t *testing.T) {
	procs := parsePSAux(psAuxSample)
	if len(procs) != 2 {
		t.Fatalf("parsed %d rows, want 2 (the header must be skipped)", len(procs))
	}
	if procs[0].PID != 1 || procs[0].User != "root" || procs[0].State != "Ss" {
		t.Errorf("init row = %+v", procs[0])
	}
	if procs[0].Command != "/sbin/init" {
		t.Errorf("command = %q", procs[0].Command)
	}
	if procs[1].CPUPercent != 7.5 || procs[1].RSSBytes != 81920*1024 {
		t.Errorf("python row = %+v", procs[1])
	}
	if procs[1].Command != "python3 -m http.server 8080" {
		t.Errorf("command = %q", procs[1].Command)
	}
	// ps aux has no elapsed-seconds column; the field stays zero rather than
	// being filled with a guess.
	if procs[1].ElapsedSec != 0 {
		t.Errorf("elapsed = %d, want 0", procs[1].ElapsedSec)
	}
}

func TestParseWindowsProcesses(t *testing.T) {
	sample := "4|0.0|0.1|1048576|864000|System\r\n" +
		"1234|45.5|12.3|536870912|3600|sqlservr\r\n" +
		"garbage line\r\n" +
		"0|0|0|0|0|Idle\r\n"
	procs := parseWindowsProcesses(sample)
	if len(procs) != 2 {
		t.Fatalf("parsed %d rows, want 2 (PID 0 and noise dropped): %+v", len(procs), procs)
	}
	if procs[1].PID != 1234 || procs[1].CPUPercent != 45.5 || procs[1].Command != "sqlservr" {
		t.Errorf("sqlservr row = %+v", procs[1])
	}
	// WorkingSetPrivate is already in bytes, unlike ps which reports kB.
	if procs[1].RSSBytes != 536870912 {
		t.Errorf("rss = %d, want 536870912", procs[1].RSSBytes)
	}
}

func TestTopProcessesSortsAndTruncates(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 1, CPUPercent: 0.5, MemPercent: 1},
		{PID: 2, CPUPercent: 90, MemPercent: 2},
		{PID: 3, CPUPercent: 0.5, MemPercent: 40},
		{PID: 4, CPUPercent: 12, MemPercent: 3},
	}
	top := TopProcesses(procs, 3)
	if len(top) != 3 {
		t.Fatalf("len = %d, want 3", len(top))
	}
	if top[0].PID != 2 || top[1].PID != 4 {
		t.Errorf("expected CPU-descending order, got %d, %d", top[0].PID, top[1].PID)
	}
	// Equal CPU falls back to memory, so the truncated list keeps the heavier
	// of the two idle processes.
	if top[2].PID != 3 {
		t.Errorf("tie-break = PID %d, want 3 (higher memory)", top[2].PID)
	}
}

func TestTopProcessesNoLimit(t *testing.T) {
	procs := []ProcessInfo{{PID: 1, CPUPercent: 1}, {PID: 2, CPUPercent: 2}}
	if got := TopProcesses(procs, 0); len(got) != 2 {
		t.Fatalf("len = %d, want 2 when no limit is set", len(got))
	}
}

func TestJoinRest(t *testing.T) {
	line := "   42 root  0.0 /usr/bin/env  FOO=bar  run"
	got := joinRest(line, []string{"42", "root", "0.0"})
	if got != "/usr/bin/env  FOO=bar  run" {
		t.Fatalf("joinRest = %q, want the tail with its spacing intact", got)
	}
}

func TestKillProcessRefusesInit(t *testing.T) {
	// A nil client would panic if the guard did not short-circuit first, so
	// this also proves nothing is sent to the host.
	for _, pid := range []int{0, 1, -5} {
		if err := KillProcess(nil, pid, "linux", false); err == nil {
			t.Errorf("KillProcess(pid=%d) returned nil, want a refusal", pid)
		}
	}
}

func TestCleanCommandError(t *testing.T) {
	tests := map[string]string{
		"bash: line 1: kill: (60000) - No such process":   "kill: (60000) - No such process",
		"sh: 1: kill: Operation not permitted":            "kill: Operation not permitted",
		"kill: (10) - Operation not permitted":            "kill: (10) - Operation not permitted",
		"  Stop-Process : Cannot find a process\nAt line": "Stop-Process : Cannot find a process",
		"": "",
	}
	for input, want := range tests {
		if got := cleanCommandError(input); got != want {
			t.Errorf("cleanCommandError(%q) = %q, want %q", input, got, want)
		}
	}
}
