import Foundation

/// Traffic in one vnStat bucket — a five-minute slot, an hour, a day, a month
/// or a year, depending on the series it came from.
public struct TrafficBucket: Sendable, Equatable, Identifiable {
    public var date: Date
    public var rx: Int64
    public var tx: Int64
    public var id: Date { date }
    public var total: Int64 { rx + tx }

    public init(date: Date, rx: Int64, tx: Int64) {
        self.date = date
        self.rx = rx
        self.tx = tx
    }
}

/// The series vnStat keeps, in the order the picker shows them.
public enum TrafficGranularity: String, CaseIterable, Identifiable, Sendable {
    case fiveMinute, hour, day, month, year
    public var id: String { rawValue }

    /// Key inside `traffic` in vnStat's JSON (version 2). Version 1 used plural
    /// keys and had no five-minute series; see `VnstatParser`.
    var jsonKey: String {
        switch self {
        case .fiveMinute: return "fiveminute"
        case .hour: return "hour"
        case .day: return "day"
        case .month: return "month"
        case .year: return "year"
        }
    }

    /// How many recent buckets the chart shows. vnStat keeps 288 five-minute
    /// slots; two hours of them is what fits and what anyone looks at.
    var shownCount: Int {
        switch self {
        case .fiveMinute: return 24
        case .hour: return 24
        case .day: return 30
        case .month: return 12
        case .year: return 10
        }
    }
}

public struct TrafficInterface: Sendable, Equatable, Identifiable {
    public var name: String
    public var alias: String
    public var totalRx: Int64
    public var totalTx: Int64
    public var series: [TrafficGranularity: [TrafficBucket]]
    public var id: String { name }

    public var displayName: String { alias.isEmpty ? name : "\(alias) · \(name)" }

    /// Oldest first, so the chart reads left to right in time.
    public func buckets(_ granularity: TrafficGranularity) -> [TrafficBucket] {
        (series[granularity] ?? []).sorted { $0.date < $1.date }
    }

    public var hasData: Bool { series.values.contains { !$0.isEmpty } }
}

public struct TrafficReport: Sendable, Equatable {
    public var vnstatVersion: String = ""
    public var interfaces: [TrafficInterface] = []

    public init() {}

    /// True right after installation: the daemon exists but has recorded
    /// nothing yet, which the card shows as "collecting" rather than a bare
    /// chart with no bars.
    public var isCollecting: Bool { !interfaces.contains { $0.hasData } }

    /// The interface people mean when they say "the server's traffic": a real
    /// link (not a container bridge or a tunnel) with the most bytes through it.
    public var primaryInterface: TrafficInterface? {
        let real = interfaces.filter { !Self.isVirtual($0.name) }
        return (real.isEmpty ? interfaces : real).max { $0.totalRx + $0.totalTx < $1.totalRx + $1.totalTx }
    }

    static func isVirtual(_ name: String) -> Bool {
        ["veth", "br-", "docker", "virbr", "cni", "flannel", "tun", "tap", "utun", "kube", "lo"]
            .contains { name.hasPrefix($0) }
    }
}

/// Reads `vnstat --json`.
///
/// vnStat is the tool SwiftServer leans on for traffic history, and it is worth
/// leaning on: the kernel counters the poll reads reset at every reboot, while
/// vnStat's database persists — it is the only way to answer "how much did this
/// box move last month".
public enum VnstatParser {

    /// `SM_NO_VNSTAT` rather than an error: most hosts have no vnStat, and the
    /// card explains how to install it instead of reporting a failure.
    public static let command =
        "command -v vnstat >/dev/null 2>&1 && vnstat --json 2>/dev/null || echo SM_NO_VNSTAT"

    public enum Outcome: Equatable, Sendable {
        case notInstalled
        case report(TrafficReport)
    }

    /// Nil when the output is neither the marker nor parseable JSON.
    public static func parse(_ output: String) -> Outcome? {
        if output.contains("SM_NO_VNSTAT") { return .notInstalled }
        guard let start = output.firstIndex(of: "{"),
              let end = output.lastIndex(of: "}"),
              start < end,
              let root = try? JSONSerialization.jsonObject(with: Data(output[start...end].utf8))
                as? [String: Any]
        else { return nil }

        var report = TrafficReport()
        report.vnstatVersion = root["vnstatversion"] as? String ?? ""
        // JSON version 1 (vnStat 1.x) reported KiB; version 2 reports bytes.
        let version = root["jsonversion"] as? String ?? "1"
        let scale: Int64 = version == "1" ? 1024 : 1

        for entry in root["interfaces"] as? [[String: Any]] ?? [] {
            // 1.x named the field "id", 2.x "name".
            guard let name = (entry["name"] ?? entry["id"]) as? String, !name.isEmpty else { continue }
            let traffic = entry["traffic"] as? [String: Any] ?? [:]
            let total = traffic["total"] as? [String: Any] ?? [:]
            var interface = TrafficInterface(
                name: name,
                alias: entry["alias"] as? String ?? "",
                totalRx: number(total["rx"]) * scale,
                totalTx: number(total["tx"]) * scale,
                series: [:]
            )
            for granularity in TrafficGranularity.allCases {
                // Version 1 pluralised the keys ("hours", "days", "months").
                let rows = (traffic[granularity.jsonKey] ?? traffic[granularity.jsonKey + "s"])
                    as? [[String: Any]] ?? []
                interface.series[granularity] = rows.compactMap { row in
                    guard let date = date(of: row, granularity: granularity) else { return nil }
                    return TrafficBucket(date: date, rx: number(row["rx"]) * scale, tx: number(row["tx"]) * scale)
                }
            }
            report.interfaces.append(interface)
        }
        return .report(report)
    }

    /// vnStat writes dates as the host's local calendar components with no zone.
    /// They are rebuilt in UTC and the chart labels them in UTC too, so the axis
    /// shows exactly the clock the host recorded — "14:00" on the server is
    /// "14:00" on the chart, whatever this Mac's zone is.
    static let calendar: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar
    }()

    static func date(of row: [String: Any], granularity: TrafficGranularity) -> Date? {
        let date = row["date"] as? [String: Any] ?? [:]
        let time = row["time"] as? [String: Any] ?? [:]
        var components = DateComponents()
        components.year = Int(number(date["year"]))
        components.month = date["month"].map { Int(number($0)) } ?? 1
        components.day = date["day"].map { Int(number($0)) } ?? 1
        switch granularity {
        case .fiveMinute, .hour:
            // Version 1 hourly rows carry the hour as `id` and have no `time`.
            components.hour = time["hour"].map { Int(number($0)) } ?? Int(number(row["id"]))
            components.minute = Int(number(time["minute"]))
        default:
            break
        }
        guard let year = components.year, year > 1970 else { return nil }
        return calendar.date(from: components)
    }

    static func number(_ value: Any?) -> Int64 {
        switch value {
        case let int as Int: return Int64(int)
        case let int64 as Int64: return int64
        case let double as Double: return Int64(double)
        case let number as NSNumber: return number.int64Value
        case let string as String: return Int64(string) ?? 0
        default: return 0
        }
    }
}
