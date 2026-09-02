import Foundation
import GRDB

/// One collection result for one server, as stored.
///
/// The table grows by a row per server per poll, so it is indexed on
/// (serverID, timestamp) and pruned on a schedule — see `Database`.
public struct MetricSample: Identifiable, Codable, Hashable, Sendable, FetchableRecord, MutablePersistableRecord {
    public static let databaseTableName = "metricSample"

    public var id: Int64?
    public var serverID: UUID
    public var timestamp: Date

    public var cpuPercent: Double
    public var load1: Double
    public var load5: Double
    public var load15: Double

    public var memoryUsed: Int64
    public var memoryTotal: Int64
    public var diskUsed: Int64
    public var diskTotal: Int64

    /// Per-second rates, derived from the delta against the previous poll.
    public var netRxRate: Double
    public var netTxRate: Double
    public var diskReadRate: Double
    public var diskWriteRate: Double

    /// Cumulative counters as reported by the host, kept so totals survive
    /// independently of the derived rates.
    public var netRxTotal: Int64
    public var netTxTotal: Int64

    public var uptimeSeconds: Int64
    /// TCP handshake time to the SSH port, measured from this Mac.
    public var latencyMs: Double

    /// For the vectorised plots, which take a key path rather than a closure.
    public var memoryPercent: Double {
        memoryTotal > 0 ? Double(memoryUsed) / Double(memoryTotal) * 100 : 0
    }

    public init(serverID: UUID, timestamp: Date = Date(), snapshot: MetricSnapshot) {
        self.id = nil
        self.serverID = serverID
        self.timestamp = timestamp
        self.cpuPercent = snapshot.cpuPercent
        self.load1 = snapshot.load1
        self.load5 = snapshot.load5
        self.load15 = snapshot.load15
        self.memoryUsed = snapshot.memoryUsed
        self.memoryTotal = snapshot.memoryTotal
        self.diskUsed = snapshot.diskUsed
        self.diskTotal = snapshot.diskTotal
        self.netRxRate = snapshot.netRxRate
        self.netTxRate = snapshot.netTxRate
        self.diskReadRate = snapshot.diskReadRate
        self.diskWriteRate = snapshot.diskWriteRate
        self.netRxTotal = snapshot.netRxTotal
        self.netTxTotal = snapshot.netTxTotal
        self.uptimeSeconds = snapshot.uptimeSeconds
        self.latencyMs = snapshot.latencyMs
    }

    /// Lets GRDB fill in the rowid after insert.
    public mutating func didInsert(_ inserted: InsertionSuccess) {
        id = inserted.rowID
    }

    /// The stored values as a snapshot, for chart and tile rendering.
    public var snapshot: MetricSnapshot {
        var value = MetricSnapshot()
        value.cpuPercent = cpuPercent
        value.load1 = load1
        value.load5 = load5
        value.load15 = load15
        value.memoryUsed = memoryUsed
        value.memoryTotal = memoryTotal
        value.diskUsed = diskUsed
        value.diskTotal = diskTotal
        value.netRxRate = netRxRate
        value.netTxRate = netTxRate
        value.diskReadRate = diskReadRate
        value.diskWriteRate = diskWriteRate
        value.netRxTotal = netRxTotal
        value.netTxTotal = netTxTotal
        value.uptimeSeconds = uptimeSeconds
        value.latencyMs = latencyMs
        return value
    }
}
