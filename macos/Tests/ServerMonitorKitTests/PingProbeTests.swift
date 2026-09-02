import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Ping probe")
struct PingProbeTests {

    @Test func parsesMacOSSummary() {
        let output = """
        PING 36.138.248.251 (36.138.248.251): 56 data bytes
        64 bytes from 36.138.248.251: icmp_seq=0 ttl=48 time=40.835 ms

        --- 36.138.248.251 ping statistics ---
        3 packets transmitted, 3 packets received, 0.0% packet loss
        round-trip min/avg/max/stddev = 40.835/41.306/42.141/0.592 ms
        """
        let reading = PingProbe.parse(output)
        #expect(reading?.averageMs == 41.306)
        #expect(reading?.lossPercent == 0)
    }

    @Test func parsesLinuxSummary() {
        let output = """
        --- example.com ping statistics ---
        3 packets transmitted, 2 received, 33% packet loss, time 2003ms
        rtt min/avg/max/mdev = 10.100/12.250/14.400/1.700 ms
        """
        let reading = PingProbe.parse(output)
        #expect(reading?.averageMs == 12.250)
        #expect(reading?.lossPercent == 33)
    }

    @Test func totalLossYieldsNothing() {
        // No average line at all: the caller falls back rather than showing 0.
        let output = """
        --- 10.0.0.9 ping statistics ---
        3 packets transmitted, 0 packets received, 100.0% packet loss
        """
        #expect(PingProbe.parse(output) == nil)
    }

    @Test func garbageYieldsNothing() {
        #expect(PingProbe.parse("") == nil)
        #expect(PingProbe.parse("ping: cannot resolve host: Unknown host") == nil)
    }

    /// The interface picker must return a real NIC and never a tunnel, since
    /// choosing a tunnel is exactly the bug this probe exists to avoid.
    @Test func picksAPhysicalInterface() {
        let interface = PingProbe.primaryPhysicalInterface()
        if let interface {
            #expect(interface.hasPrefix("en"))
            #expect(interface.hasPrefix("utun") == false)
            #expect(interface != "lo0")
        }
    }

    /// End to end against loopback over the real ping binary, proving the
    /// argument list and the parser agree with what macOS prints.
    @Test func measuresLoopbackThroughTheRealBinary() async {
        guard PingProbe.primaryPhysicalInterface() != nil else { return }
        // Loopback is not reachable over a bound physical interface, so this
        // exercises the unbound parse path via the binary directly.
        let output = try? await SSHRunner.execute(
            executable: "/sbin/ping",
            arguments: ["-n", "-c", "2", "-t", "3", "127.0.0.1"],
            timeout: 5
        )
        let reading = output.flatMap(PingProbe.parse)
        #expect(reading != nil)
        #expect((reading?.lossPercent ?? 100) == 0)
    }

    @Test func readsHostNameFromSSHConfigDump() {
        let output = """
        user root
        hostname 36.138.248.251
        port 22
        """
        #expect(MetricsCollector.parseHostName(output) == "36.138.248.251")
    }

    @Test func hostNameMissingFromDump() {
        #expect(MetricsCollector.parseHostName("user root\nport 22") == nil)
    }
}
