import Foundation
import UserNotifications

/// Turns poll results into desktop notifications.
///
/// The value of a always-on monitor is that it tells you *without* being
/// looked at, so the rules here are tuned against noise rather than latency:
/// a threshold must be breached for several consecutive polls before it fires,
/// and each alert then goes quiet for a cooldown period.
@MainActor
public final class AlertService: ObservableObject {
    /// Whether the user has granted notification permission.
    @Published public private(set) var authorized = false

    private let settings: AppSettings
    private let deliver: AlertDelivery

    /// Consecutive breaching polls per server and metric.
    private var breachRun: [String: Int] = [:]
    /// When each alert last fired, so a persistent problem does not repeat.
    private var lastFired: [String: Date] = [:]
    /// Previous status per server, to detect transitions rather than states.
    private var previousOnline: [UUID: Bool] = [:]

    /// A metric must breach this many polls in a row before it alerts. At the
    /// default 5-second interval that is ~15 seconds of sustained load, which
    /// filters out the spike from a build or a backup starting.
    private let sustainedPolls = 3
    private let cooldown: TimeInterval = 15 * 60

    public init(settings: AppSettings, delivery: @escaping AlertDelivery = AlertService.systemDelivery) {
        self.settings = settings
        self.deliver = delivery
    }

    public enum Metric: String, CaseIterable {
        case cpu, memory, disk

        var titleKey: String {
            switch self {
            case .cpu: return "metric.cpu"
            case .memory: return "metric.memory"
            case .disk: return "metric.disk"
            }
        }
    }

    // MARK: - Permission

    /// Asks the system for permission. Called when the user turns alerts on,
    /// rather than at launch, so the prompt has visible cause.
    public func requestAuthorization() async {
        guard Bundle.main.bundleIdentifier != nil else { return }
        do {
            let granted = try await UNUserNotificationCenter.current()
                .requestAuthorization(options: [.alert, .sound])
            authorized = granted
        } catch {
            authorized = false
        }
    }

    public func refreshAuthorization() async {
        guard Bundle.main.bundleIdentifier != nil else { return }
        let status = await UNUserNotificationCenter.current().notificationSettings()
        authorized = status.authorizationStatus == .authorized
            || status.authorizationStatus == .provisional
    }

    // MARK: - Evaluation

    /// Called after each poll. Compares against the previous result and emits
    /// at most one notification per condition per cooldown.
    public func evaluate(server: Server, status: ServerStatus, snapshot: MetricSnapshot?) {
        guard settings.notificationsEnabled else { return }

        let isOnline = status.isOnline
        let wasOnline = previousOnline[server.id]
        previousOnline[server.id] = isOnline

        if settings.notifyOnOffline, let wasOnline, wasOnline != isOnline {
            if isOnline {
                send(
                    key: "recovered:\(server.id)",
                    title: server.name,
                    body: L10nBridge.recovered,
                    ignoreCooldown: true
                )
            } else {
                let reason: String
                if case .offline(let message) = status { reason = message } else { reason = "" }
                send(
                    key: "offline:\(server.id)",
                    title: server.name,
                    body: reason.isEmpty ? L10nBridge.offline : "\(L10nBridge.offline) — \(reason)",
                    ignoreCooldown: true
                )
            }
        }

        // Thresholds only make sense for a host that answered.
        guard isOnline, let snapshot else {
            for metric in Metric.allCases { breachRun["\(metric.rawValue):\(server.id)"] = 0 }
            return
        }

        // A server's own limit wins; nil means it follows the global setting.
        check(.cpu, value: snapshot.cpuPercent,
              limit: server.cpuThreshold ?? settings.cpuThreshold, server: server)
        check(.memory, value: snapshot.memoryPercent,
              limit: server.memoryThreshold ?? settings.memoryThreshold, server: server)
        check(.disk, value: snapshot.diskPercent,
              limit: server.diskThreshold ?? settings.diskThreshold, server: server)
    }

    private func check(_ metric: Metric, value: Double, limit: Int, server: Server) {
        let key = "\(metric.rawValue):\(server.id)"
        guard limit > 0 else {
            breachRun[key] = 0
            return
        }
        guard value >= Double(limit) else {
            // Recovering resets the run so the next breach must build up again.
            breachRun[key] = 0
            return
        }
        let run = (breachRun[key] ?? 0) + 1
        breachRun[key] = run
        guard run == sustainedPolls else { return }

        send(
            key: key,
            title: server.name,
            body: L10nBridge.threshold(metric: metric, value: value, limit: limit)
        )
    }

    public func forget(serverID: UUID) {
        previousOnline.removeValue(forKey: serverID)
        for metric in Metric.allCases {
            breachRun.removeValue(forKey: "\(metric.rawValue):\(serverID)")
            lastFired.removeValue(forKey: "\(metric.rawValue):\(serverID)")
        }
    }

    // MARK: - Delivery

    private func send(key: String, title: String, body: String, ignoreCooldown: Bool = false) {
        if !ignoreCooldown, let last = lastFired[key], Date().timeIntervalSince(last) < cooldown {
            return
        }
        lastFired[key] = Date()
        deliver(title, body)
    }

    /// Hands a notification to the system.
    ///
    /// Guarded on the bundle identifier because `UNUserNotificationCenter`
    /// raises an Objective-C exception in a process with no bundle — which is
    /// exactly how the test runner executes.
    public static let systemDelivery: AlertDelivery = { title, body in
        guard Bundle.main.bundleIdentifier != nil else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = .default
        let request = UNNotificationRequest(
            identifier: UUID().uuidString,
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request)
    }
}

/// Where a notification is handed off. Injected so tests can observe what would
/// be delivered without a notification centre.
public typealias AlertDelivery = @MainActor (_ title: String, _ body: String) -> Void

/// Notification text. Separate from `Localization` because alerts are produced
/// off the view tree, where the environment object is not reachable.
enum L10nBridge {
    static var isChinese: Bool {
        (Locale.preferredLanguages.first ?? "en").hasPrefix("zh")
    }

    static var offline: String { isChinese ? "已离线" : "Offline" }
    static var recovered: String { isChinese ? "已恢复在线" : "Back online" }

    static func threshold(metric: AlertService.Metric, value: Double, limit: Int) -> String {
        let name: String
        switch metric {
        case .cpu: name = "CPU"
        case .memory: name = isChinese ? "内存" : "Memory"
        case .disk: name = isChinese ? "磁盘" : "Disk"
        }
        let rounded = Int(value.rounded())
        return isChinese
            ? "\(name) 持续超过 \(limit)%（当前 \(rounded)%）"
            : "\(name) above \(limit)% (now \(rounded)%)"
    }
}
