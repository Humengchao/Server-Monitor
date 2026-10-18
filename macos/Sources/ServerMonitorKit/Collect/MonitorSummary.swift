import Foundation
import Observation

/// Small, independently observed state for the always-visible menu bar. Full
/// snapshots can wait for the main window; health and headline values cannot.
@Observable
@MainActor
public final class MonitorSummary {
    public struct Reading: Equatable {
        public var status: ServerStatus
        public var cpuPercent: Double?
        public var memoryPercent: Double?
    }

    public private(set) var readings: [UUID: Reading] = [:]

    func update(serverID: UUID, status: ServerStatus, snapshot: MetricSnapshot?) {
        let reading = Reading(
            status: status,
            cpuPercent: snapshot?.cpuPercent,
            memoryPercent: snapshot?.memoryPercent
        )
        if readings[serverID] != reading { readings[serverID] = reading }
    }

    func forget(_ serverID: UUID) { readings.removeValue(forKey: serverID) }

    public var offlineCount: Int {
        readings.values.filter { if case .offline = $0.status { return true }; return false }.count
    }

    public var peakCPU: Double? {
        readings.values.filter { $0.status.isOnline }.compactMap(\.cpuPercent).max()
    }
}
