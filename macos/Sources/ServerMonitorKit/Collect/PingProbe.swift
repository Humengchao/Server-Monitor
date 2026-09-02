import Darwin
import Foundation

/// ICMP round trip measured over a physical interface.
///
/// Binding the socket to the NIC is the whole point. When a VPN or proxy owns
/// the route (a `utun` carrying the traffic), an unbound ping never leaves the
/// machine — the tunnel answers it locally, and a server on another continent
/// appears to reply in under a millisecond. `ping -b en0` puts the packet on
/// the real network instead, which is the latency a person means when they ask
/// how far away a host is.
public struct PingProbe: Sendable {
    public init() {}

    public struct Reading: Sendable, Equatable {
        public let averageMs: Double
        /// 0–100.
        public let lossPercent: Double
    }

    /// Measures `host` over the primary physical interface, or nil when no such
    /// interface exists or ICMP does not come back.
    public func measure(host: String) async -> Reading? {
        guard !host.isEmpty, let interface = Self.primaryPhysicalInterface() else { return nil }
        guard let output = try? await SSHRunner.execute(
            executable: "/sbin/ping",
            arguments: [
                "-n",                 // no reverse DNS
                "-c", "3",
                "-t", "4",            // hard cap, so a black hole cannot stall a poll
                "-b", interface,      // the reason this works at all
                host,
            ],
            timeout: 6
        ) else { return nil }
        return Self.parse(output)
    }

    /// The active physical interface, e.g. "en0".
    ///
    /// Read with `getifaddrs` rather than by parsing `ifconfig`: it avoids a
    /// subprocess on a path that already runs once per host per poll, and the
    /// flags are exactly what needs testing. Tunnels are excluded by name,
    /// since including one would reintroduce the problem this solves.
    public static func primaryPhysicalInterface() -> String? {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return nil }
        defer { freeifaddrs(head) }

        var candidates: [String] = []
        for pointer in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let flags = Int32(pointer.pointee.ifa_flags)
            guard flags & IFF_UP != 0, flags & IFF_RUNNING != 0, flags & IFF_LOOPBACK == 0 else { continue }
            guard pointer.pointee.ifa_addr?.pointee.sa_family == UInt8(AF_INET) else { continue }

            let name = String(cString: pointer.pointee.ifa_name)
            // en* is Wi-Fi and Ethernet; utun/ipsec/ppp are exactly what we are
            // trying to step around.
            guard name.hasPrefix("en") else { continue }

            guard let address = pointer.pointee.ifa_addr else { continue }
            var storage = sockaddr_in()
            memcpy(&storage, address, MemoryLayout<sockaddr_in>.size)
            let ip = String(cString: inet_ntoa(storage.sin_addr))
            // A self-assigned address means the link is up but unusable.
            guard !ip.hasPrefix("169.254"), ip != "0.0.0.0" else { continue }

            candidates.append(name)
        }
        // Lowest index first, which is the built-in NIC on every Mac.
        return candidates.sorted().first
    }

    /// Parses the summary that macOS and Linux ping both print.
    public static func parse(_ output: String) -> Reading? {
        var loss = 0.0
        var average: Double?

        for line in output.lines() {
            let text = String(line)
            if text.contains("packet loss"),
               let range = text.range(of: #"[0-9.]+(?=% packet loss)"#, options: .regularExpression) {
                loss = Double(text[range]) ?? 0
            }
            guard text.contains("min/avg/max"), let values = text.split(separator: "=").last else { continue }
            let parts = values
                .trimmingCharacters(in: .whitespaces)
                .replacingOccurrences(of: " ms", with: "")
                .split(separator: "/")
            if parts.count >= 2 { average = Double(parts[1]) }
        }

        guard let average else { return nil }
        return Reading(averageMs: average, lossPercent: loss)
    }
}

/// Small actor cache so concurrent polls do not each shell out to `ssh -G`.
actor HostResolutionCache {
    private var entries: [String: String] = [:]

    func value(for alias: String) -> String? { entries[alias] }
    func store(_ address: String, for alias: String) { entries[alias] = address }
}
