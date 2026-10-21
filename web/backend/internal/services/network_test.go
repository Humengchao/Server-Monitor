package services

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"github.com/google/uuid"
	"math"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNetworkUnknownNeverRecoversAnIncident(t *testing.T) {
	check := models.NetworkCheck{LossThreshold: 10, Latest: &models.NetworkSample{ThresholdExceeded: true}}
	unknown := models.NetworkSample{Status: "error"}
	if transition := models.NetworkSampleTransition(check, &unknown); transition != "" || !unknown.ThresholdExceeded {
		t.Fatalf("unknown recovered: %s %+v", transition, unknown)
	}
	five := 5
	loss := 0.0
	healthy := models.NetworkSample{Sent: &five, Received: &five, LossPercent: &loss}
	if transition := models.NetworkSampleTransition(check, &healthy); transition != "resolved" {
		t.Fatal(transition)
	}
	check.Latest = &healthy
	bad := ParseNetworkPing("5 packets transmitted, 0 received")
	if transition := models.NetworkSampleTransition(check, &bad); transition != "firing" {
		t.Fatal(transition)
	}
	check.Latest = &bad
	if transition := models.NetworkSampleTransition(check, &bad); transition != "" {
		t.Fatal("duplicate notification")
	}
}

func TestNetworkPausedTargetAndSourceNeverOpenTransport(t *testing.T) {
	db := testdb.Open(t)
	user, target, _ := testdb.Seed(t, db)
	check := models.NetworkCheck{ID: uuid.New(), UserID: user, Name: "paused", TargetServerID: target, Packets: 5, Enabled: true, LossThreshold: 10}
	if err := models.SaveNetworkCheck(context.Background(), db, &check, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE servers SET monitoring_enabled=false,connection_mode='agent' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	monitor := NewNetworkMonitor(&models.DB{Raw: db}, nil)
	sample, err := monitor.Run(context.Background(), user, check.ID)
	if err != nil || sample.ErrorCode != "paused" || sample.Sent != nil {
		t.Fatalf("%+v %v", sample, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM network_samples WHERE check_id=$1`, check.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("paused sample stored %d %v", count, err)
	}
}

func TestNetworkPingRealCountsAndUnknown(t *testing.T) {
	cases := []struct {
		output, status string
		sent, received int
		avg            *float64
	}{
		{"5 packets transmitted, 3 received, 40% packet loss\nrtt min/avg/max/mdev = 1.000/2.000/4.000/0.700 ms", "loss", 5, 3, floatPtr(2)},
		{"10 packets transmitted, 0 received, 100% packet loss", "down", 10, 0, nil},
		{"2 packets transmitted, 2 packets received, 0.0% packet loss\nround-trip min/avg/max/stddev = 0.1/0.2/0.3/0.1 ms", "ok", 2, 2, floatPtr(.2)},
		{"2 packets transmitted, 2 packets received, 0% packet loss\nround-trip min/avg/max = 0.1/0.2/0.3 ms", "ok", 2, 2, floatPtr(.2)},
		{"ping: Operation not permitted", "unknown", 0, 0, nil},
		{"0 packets transmitted, 0 received", "unknown", 0, 0, nil},
		{"3 packets transmitted, 7 received", "unknown", 0, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.status+c.output[:1], func(t *testing.T) {
			s := ParseNetworkPing(c.output)
			if s.Status != c.status {
				t.Fatalf("%+v", s)
			}
			if c.sent == 0 {
				if s.Sent != nil || s.LossPercent != nil {
					t.Fatal("unknown became zero loss")
				}
				return
			}
			if *s.Sent != c.sent || *s.Received != c.received {
				t.Fatalf("counts: %+v", s)
			}
			want := 100 * float64(c.sent-c.received) / float64(c.sent)
			if *s.LossPercent != want {
				t.Fatal("loss not based on packet counts")
			}
			if c.avg == nil {
				if s.AvgMS != nil {
					t.Fatal("invented RTT")
				}
			} else if s.AvgMS == nil || *s.AvgMS != *c.avg {
				t.Fatalf("RTT: %+v", s)
			}
		})
	}
}

func TestNetworkAgentWithoutTargetAddressDoesNotProbeProxyOrDNSPlaceholder(t *testing.T) {
	db := testdb.Open(t)
	user, target, _ := testdb.Seed(t, db)
	if _, err := db.Exec(`UPDATE servers SET connection_mode='agent',host='agent' WHERE id=$1`, target); err != nil {
		t.Fatal(err)
	}
	check := models.NetworkCheck{ID: uuid.New(), UserID: user, Name: "address required", SourceServerID: &target, TargetServerID: target, Packets: 5, Enabled: true, LossThreshold: 10}
	if err := models.SaveNetworkCheck(context.Background(), db, &check, true); err != nil {
		t.Fatal(err)
	}
	// A nil connection cache also proves no remote command is attempted.
	monitor := NewNetworkMonitor(&models.DB{Raw: db}, nil)
	sample, err := monitor.Run(context.Background(), user, check.ID)
	if err != nil || sample.Status != "error" || sample.ErrorCode != "target_address_required" || sample.Sent != nil || sample.LossPercent != nil {
		t.Fatalf("unconfigured address was measured: %+v %v", sample, err)
	}
}
func floatPtr(value float64) *float64 { return &value }
func TestNetworkWindowsDeviationAndSafeCommand(t *testing.T) {
	sample := parseNetworkWindows(`{"sent":5,"values":[1,3,5]}`)
	if sample.Status != "loss" || *sample.Received != 3 || *sample.AvgMS != 3 || math.Abs(*sample.MdevMS-math.Sqrt(8.0/3)) > .00001 {
		t.Fatalf("%+v", sample)
	}
	if parseNetworkWindows(`{"sent":1,"values":[-1]}`).Sent != nil {
		t.Fatal("negative RTT accepted")
	}
	command := networkPowerShell("example.test", 5)
	encoded := strings.Split(command, " ")
	raw, err := base64.StdEncoding.DecodeString(encoded[len(encoded)-1])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	script := string(utf16.Decode(units))
	if !strings.Contains(script, ".Send('example.test',1000)") {
		t.Fatal(script)
	}
	for _, host := range []string{"-c 1000", "x';reboot;#", "x\nreboot", "a/b", "$(id)"} {
		if networkHost.MatchString(host) {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, host := range []string{"::1", "2001:db8::1", "192.0.2.1", "example.test"} {
		if !validNetworkTarget(host) {
			t.Fatalf("valid target rejected: %s", host)
		}
	}
	busybox := ParseNetworkPing("2 packets transmitted, 2 received\nround-trip min/avg/max = 0.1/0.2/0.3 ms")
	if busybox.MdevMS != nil || busybox.AvgMS == nil {
		t.Fatalf("invented deviation or lost average: %+v", busybox)
	}
}
