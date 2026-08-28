package services

import "testing"

func TestParseSS(t *testing.T) {
	// Real "ss -tulnp" output: the header, a UDP socket (state UNCONN, not
	// LISTEN), IPv4 and IPv6 binds, a loopback-only bind, and one row with no
	// process attribution because the caller may not see the owner.
	out := `Netid State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
udp   UNCONN 0      0      127.0.0.1:323       0.0.0.0:*         users:(("chronyd",pid=812,fd=5))
tcp   LISTEN 0      128    0.0.0.0:22          0.0.0.0:*         users:(("sshd",pid=1,fd=3))
tcp   LISTEN 0      511    [::]:80             [::]:*            users:(("nginx",pid=940,fd=6),("nginx",pid=941,fd=6))
tcp   LISTEN 0      4096   10.0.0.5:5432       0.0.0.0:*
tcp   LISTEN 0      4096   [::1]:6379          [::]:*            users:(("redis-server",pid=1200,fd=7))
`
	ports := parseSS(out)
	if len(ports) != 5 {
		t.Fatalf("got %d ports, want 5: %+v", len(ports), ports)
	}

	// UDP is UNCONN, so filtering on the literal state "LISTEN" would silently
	// drop every UDP listener.
	if ports[0].Protocol != "udp" || ports[0].Port != 323 || ports[0].Process != "chronyd" {
		t.Errorf("udp row = %+v", ports[0])
	}
	if ports[0].Exposure != "loopback" {
		t.Errorf("127.0.0.1 exposure = %q, want loopback", ports[0].Exposure)
	}
	if ports[1].Port != 22 || ports[1].Exposure != "public" || ports[1].PID != 1 {
		t.Errorf("wildcard ssh row = %+v", ports[1])
	}
	// The IPv6 address must lose its brackets and keep its port; splitting on
	// the first colon instead of the last would produce nonsense here.
	if ports[2].Address != "::" || ports[2].Port != 80 {
		t.Errorf("ipv6 wildcard = %+v, want address :: port 80", ports[2])
	}
	if ports[2].Exposure != "public" {
		t.Errorf("[::] exposure = %q, want public", ports[2].Exposure)
	}
	// Two processes share the socket; the first is reported rather than a merge.
	if ports[2].Process != "nginx" || ports[2].PID != 940 {
		t.Errorf("shared socket = %+v", ports[2])
	}
	// No users:(...) field at all: the row still counts, with attribution blank
	// rather than dropped.
	if ports[3].Port != 5432 || ports[3].Process != "" || ports[3].PID != 0 {
		t.Errorf("unattributed row = %+v", ports[3])
	}
	if ports[3].Exposure != "private" {
		t.Errorf("10.0.0.5 exposure = %q, want private", ports[3].Exposure)
	}
	if ports[4].Address != "::1" || ports[4].Exposure != "loopback" {
		t.Errorf("ipv6 loopback = %+v", ports[4])
	}
}

func TestParseSSWildcardStar(t *testing.T) {
	// Some kernels render an IPv6 wildcard as "*" rather than "[::]".
	out := `tcp LISTEN 0 128 *:111 *:* users:(("rpcbind",pid=700,fd=8))`
	ports := parseSS(out)
	if len(ports) != 1 {
		t.Fatalf("got %d ports, want 1", len(ports))
	}
	if ports[0].Address != "::" || ports[0].Port != 111 {
		t.Errorf("star bind = %+v, want :: / 111", ports[0])
	}
	if ports[0].Exposure != "public" {
		t.Errorf("star exposure = %q, want public", ports[0].Exposure)
	}
}

func TestParseNetstat(t *testing.T) {
	// "netstat -tulnp": TCP rows carry a State column and UDP rows do not, so
	// the PID column lands at a different index per row.
	out := `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address    Foreign Address  State    PID/Program name
tcp        0      0 0.0.0.0:22       0.0.0.0:*        LISTEN   1/sshd
tcp6       0      0 :::80            :::*             LISTEN   940/nginx
udp        0      0 127.0.0.1:323    0.0.0.0:*                 812/chronyd
udp        0      0 0.0.0.0:68       0.0.0.0:*                 -
`
	ports := parseNetstat(out)
	if len(ports) != 4 {
		t.Fatalf("got %d ports, want 4: %+v", len(ports), ports)
	}
	if ports[0].Protocol != "tcp" || ports[0].Port != 22 || ports[0].Process != "sshd" || ports[0].PID != 1 {
		t.Errorf("tcp row = %+v", ports[0])
	}
	if ports[1].Protocol != "tcp6" || ports[1].Port != 80 || ports[1].Exposure != "public" {
		t.Errorf("tcp6 row = %+v", ports[1])
	}
	// The UDP row has no State column, so a fixed index would read "0.0.0.0:*"
	// as the PID field. Searching by shape finds 812/chronyd regardless.
	if ports[2].PID != 812 || ports[2].Process != "chronyd" {
		t.Errorf("udp row = %+v, want pid 812 chronyd", ports[2])
	}
	// "-" is not a pid/name pair, so attribution stays blank.
	if ports[3].Port != 68 || ports[3].PID != 0 || ports[3].Process != "" {
		t.Errorf("unattributed udp row = %+v", ports[3])
	}
}

func TestParseNetstatCleansProcessTitle(t *testing.T) {
	// Verbatim from netstat -tulnp on Debian 12: the program column is the
	// process title truncated to the column width, so Fields() leaves "sshd:"
	// behind and the rest of the title in a separate field.
	out := `Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN      634/sshd: /usr/sbin
tcp6       0      0 :::22                   :::*                    LISTEN      634/sshd: /usr/sbin
`
	ports := parseNetstat(out)
	if len(ports) != 2 {
		t.Fatalf("got %d ports, want 2: %+v", len(ports), ports)
	}
	for i, p := range ports {
		if p.Process != "sshd" {
			t.Errorf("row %d process = %q, want %q", i, p.Process, "sshd")
		}
		if p.PID != 634 {
			t.Errorf("row %d pid = %d, want 634", i, p.PID)
		}
	}
	// netstat distinguishes the families in the proto column, unlike ss.
	if ports[0].Protocol != "tcp" || ports[1].Protocol != "tcp6" {
		t.Errorf("protocols = %q/%q, want tcp/tcp6", ports[0].Protocol, ports[1].Protocol)
	}
}

func TestCleanNetstatProcessName(t *testing.T) {
	cases := map[string]string{
		"sshd:":            "sshd",
		"sshd: /usr/sbin":  "sshd",
		"nginx":            "nginx",
		"postgres: writer": "postgres",
		"":                 "",
		"-":                "-",
	}
	for in, want := range cases {
		if got := cleanNetstatProcessName(in); got != want {
			t.Errorf("cleanNetstatProcessName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWindowsPorts(t *testing.T) {
	out := `tcp|0.0.0.0|3389|1044|svchost
tcp|::|445|4|System
udp|127.0.0.1|53|2100|dnscache
tcp|192.168.1.10|8080|3300|w3wp
`
	ports := parseWindowsPorts(out)
	if len(ports) != 4 {
		t.Fatalf("got %d ports, want 4: %+v", len(ports), ports)
	}
	if ports[0].Port != 3389 || ports[0].Exposure != "public" || ports[0].Process != "svchost" {
		t.Errorf("rdp row = %+v", ports[0])
	}
	if ports[1].Address != "::" || ports[1].Exposure != "public" {
		t.Errorf("smb row = %+v", ports[1])
	}
	if ports[2].Exposure != "loopback" {
		t.Errorf("dns row = %+v, want loopback", ports[2])
	}
	if ports[3].Exposure != "private" {
		t.Errorf("lan row = %+v, want private", ports[3])
	}
}

func TestSplitHostPortRejectsNonRows(t *testing.T) {
	// These are what the header and legend lines look like after Fields(); each
	// must fail to parse so the row is skipped rather than producing a port 0.
	for _, value := range []string{
		"Address:Port", "0.0.0.0:*", "[::]:*", "no-colon", "", ":", "1.2.3.4:0",
		"1.2.3.4:65536", "1.2.3.4:-1",
	} {
		if _, _, ok := splitHostPort(value); ok {
			t.Errorf("%q parsed as a data row", value)
		}
	}
	host, port, ok := splitHostPort("[fe80::1]:8080")
	if !ok || host != "fe80::1" || port != 8080 {
		t.Errorf("bracketed ipv6 = %q/%d/%v", host, port, ok)
	}
}

func TestClassifyExposure(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0":         "public",
		"::":              "public",
		"127.0.0.1":       "loopback",
		"127.0.1.1":       "loopback",
		"::1":             "loopback",
		"10.0.0.5":        "private",
		"172.16.4.4":      "private",
		"192.168.1.10":    "private",
		"169.254.10.10":   "private",
		"fe80::1":         "private",
		"8.8.8.8":         "public",
		"2606:4700::1111": "public",
		"not-an-ip":       "unknown",
	}
	for addr, want := range cases {
		if got := classifyExposure(addr); got != want {
			t.Errorf("classifyExposure(%q) = %q, want %q", addr, got, want)
		}
	}
	// 100.64.0.0/10 is carrier-grade NAT: routable-looking but not reachable
	// from the public internet. Go's IsPrivate does not cover it, so it lands in
	// "public" — flagging it is the safe direction to be wrong in.
	if got := classifyExposure("100.64.0.1"); got != "public" {
		t.Errorf("CGNAT = %q, want public (documented conservative default)", got)
	}
}

func TestSortListeningPorts(t *testing.T) {
	ports := []ListeningPort{
		{Port: 443, Protocol: "tcp", Address: "0.0.0.0"},
		{Port: 22, Protocol: "tcp", Address: "0.0.0.0"},
		{Port: 53, Protocol: "udp", Address: "127.0.0.1"},
		{Port: 53, Protocol: "tcp", Address: "127.0.0.1"},
		{Port: 22, Protocol: "tcp", Address: "10.0.0.1"},
	}
	SortListeningPorts(ports)
	want := []struct {
		port  int
		proto string
		addr  string
	}{
		{22, "tcp", "0.0.0.0"},
		{22, "tcp", "10.0.0.1"},
		{53, "tcp", "127.0.0.1"},
		{53, "udp", "127.0.0.1"},
		{443, "tcp", "0.0.0.0"},
	}
	for i, w := range want {
		if ports[i].Port != w.port || ports[i].Protocol != w.proto || ports[i].Address != w.addr {
			t.Errorf("position %d = %+v, want %v", i, ports[i], w)
		}
	}
}
