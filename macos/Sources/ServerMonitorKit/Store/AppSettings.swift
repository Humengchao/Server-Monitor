import Foundation
import SwiftUI

/// User preferences, in UserDefaults. Small enough that it does not warrant a
/// database table, and reading them must never block on the SQLite queue.
public final class AppSettings: ObservableObject {
    @Published public var pollInterval: TimeInterval {
        didSet { UserDefaults.standard.set(pollInterval, forKey: Keys.pollInterval) }
    }

    /// History window, in days.
    @Published public var retentionDays: Int {
        didSet { UserDefaults.standard.set(retentionDays, forKey: Keys.retentionDays) }
    }

    // MARK: - Alerts

    @Published public var notificationsEnabled: Bool {
        didSet { UserDefaults.standard.set(notificationsEnabled, forKey: Keys.notificationsEnabled) }
    }

    /// Notify when a host stops answering, and again when it recovers.
    @Published public var notifyOnOffline: Bool {
        didSet { UserDefaults.standard.set(notifyOnOffline, forKey: Keys.notifyOnOffline) }
    }

    /// Percentage above which a sustained breach raises an alert. 0 disables.
    @Published public var cpuThreshold: Int {
        didSet { UserDefaults.standard.set(cpuThreshold, forKey: Keys.cpuThreshold) }
    }

    @Published public var memoryThreshold: Int {
        didSet { UserDefaults.standard.set(memoryThreshold, forKey: Keys.memoryThreshold) }
    }

    @Published public var diskThreshold: Int {
        didSet { UserDefaults.standard.set(diskThreshold, forKey: Keys.diskThreshold) }
    }

    // MARK: - Terminal

    @Published public var terminalFontName: String {
        didSet { UserDefaults.standard.set(terminalFontName, forKey: Keys.terminalFontName) }
    }

    @Published public var terminalFontSize: Double {
        didSet { UserDefaults.standard.set(terminalFontSize, forKey: Keys.terminalFontSize) }
    }

    /// Monospaced faces present on every macOS install, so the list never
    /// offers something that silently falls back to the system default.
    public static let terminalFonts: [String] = [
        "SF Mono", "Menlo", "Monaco", "Courier New", "Andale Mono", "PT Mono",
    ]
    public static let terminalFontSizes: [Double] = [11, 12, 13, 14, 16, 18]

    @Published public var launchAtLogin: Bool {
        didSet { UserDefaults.standard.set(launchAtLogin, forKey: Keys.launchAtLogin) }
    }

    private enum Keys {
        static let pollInterval = "pollInterval"
        static let retentionDays = "retentionDays"
        static let notificationsEnabled = "notificationsEnabled"
        static let notifyOnOffline = "notifyOnOffline"
        static let cpuThreshold = "cpuThreshold"
        static let memoryThreshold = "memoryThreshold"
        static let diskThreshold = "diskThreshold"
        static let launchAtLogin = "launchAtLogin"
        static let terminalFontName = "terminalFontName"
        static let terminalFontSize = "terminalFontSize"
    }

    public static let thresholdChoices: [Int] = [0, 70, 80, 85, 90, 95]

    public static let allowedIntervals: [TimeInterval] = [3, 5, 10, 15, 30, 60]
    public static let allowedRetention: [Int] = [1, 3, 7, 14, 30]

    public init() {
        let storedInterval = UserDefaults.standard.double(forKey: Keys.pollInterval)
        pollInterval = Self.allowedIntervals.contains(storedInterval) ? storedInterval : 5
        let storedRetention = UserDefaults.standard.integer(forKey: Keys.retentionDays)
        retentionDays = Self.allowedRetention.contains(storedRetention) ? storedRetention : 7

        let defaults = UserDefaults.standard
        notificationsEnabled = defaults.bool(forKey: Keys.notificationsEnabled)
        // Offline alerts are the reason most people turn notifications on, so
        // they default to on once notifications are enabled at all.
        notifyOnOffline = defaults.object(forKey: Keys.notifyOnOffline) as? Bool ?? true
        cpuThreshold = defaults.object(forKey: Keys.cpuThreshold) as? Int ?? 0
        memoryThreshold = defaults.object(forKey: Keys.memoryThreshold) as? Int ?? 0
        diskThreshold = defaults.object(forKey: Keys.diskThreshold) as? Int ?? 90
        launchAtLogin = defaults.bool(forKey: Keys.launchAtLogin)
        let storedFont = defaults.string(forKey: Keys.terminalFontName)
        terminalFontName = Self.terminalFonts.contains(storedFont ?? "") ? storedFont! : "Menlo"
        let storedSize = defaults.double(forKey: Keys.terminalFontSize)
        terminalFontSize = Self.terminalFontSizes.contains(storedSize) ? storedSize : 13
    }

    public var retention: TimeInterval { TimeInterval(retentionDays) * 86_400 }
}
