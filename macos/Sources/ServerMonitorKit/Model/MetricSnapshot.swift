import Foundation

/// A collection result in transit, before it is persisted.
///
/// Kept as a plain value type so the collector can run off the main actor and
/// hand results across without touching SwiftData.
public struct MetricSnapshot: Sendable, Equatable {
    public var cpuPercent: Double = 0
    public var load1: Double = 0
    public var load5: Double = 0
    public var load15: Double = 0
    public var memoryUsed: Int64 = 0
    public var memoryTotal: Int64 = 0
    public var diskUsed: Int64 = 0
    public var diskTotal: Int64 = 0
    public var netRxRate: Double = 0
    public var netTxRate: Double = 0
    public var diskReadRate: Double = 0
    public var diskWriteRate: Double = 0
    public var netRxTotal: Int64 = 0
    public var netTxTotal: Int64 = 0
    public var uptimeSeconds: Int64 = 0
    /// TCP handshake time to the SSH port, measured from this Mac.
    public var latencyMs: Double = 0

    // Detail for the machine screen. These are current-state only and are
    // deliberately not persisted with the history samples: the charts need the
    // scalars above, and a per-core/per-process array on every row would grow
    // the store for data nobody reads back.
    public var coreLoads: [CoreLoad] = []
    public var cpuBreakdown = CPUBreakdown()
    public var gpu = GPUStatus()
    public var memory = MemoryBreakdown()
    public var interfaces: [NetInterface] = []
    public var filesystems: [FilesystemUsage] = []
    public var processes: [HostProcess] = []
    public var identity = HostIdentity()

    // Host facts that ride along with the same round trip.
    public var cores: Int = 0
    /// Set when `.auto` collection worked out which script the host speaks, so
    /// the caller can record it and stop probing.
    public var detectedOS: OSKind?
    public var dockerVersion: String = ""
    /// Engine counts, when the host answered with them. Nil on a host without
    /// Docker, or one whose `docker info` only gave a version.
    public var dockerSummary: DockerSummary?

    public init() {}

    public var memoryPercent: Double {
        memoryTotal > 0 ? Double(memoryUsed) / Double(memoryTotal) * 100 : 0
    }

    public var diskPercent: Double {
        diskTotal > 0 ? Double(diskUsed) / Double(diskTotal) * 100 : 0
    }
}

/// Per-interface counters from the previous poll, keyed by interface name.
struct InterfaceBaseline {
    var counters: [String: (rx: Int64, tx: Int64)]
    var takenAt: Date
}

/// Cumulative counters carried between polls to turn deltas into rates.
struct CounterBaseline {
    var netRx: Int64
    var netTx: Int64
    var diskRead: Int64
    var diskWrite: Int64
    var takenAt: Date
}
