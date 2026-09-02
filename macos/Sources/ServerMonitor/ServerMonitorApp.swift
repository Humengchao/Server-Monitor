import ServerMonitorKit
import AppKit
import SwiftUI

@main
struct ServerMonitorApp: App {
    @StateObject private var localization = Localization()
    @StateObject private var settings: AppSettings
    @StateObject private var monitor: MonitorService
    @StateObject private var sessions: SessionManager
    @StateObject private var alerts: AlertService
    /// Non-nil when the store could not be opened; the app then shows why
    /// instead of launching into an empty window that silently drops writes.
    private let startupFailure: String?

    init() {
        let settings = AppSettings()
        var failure: String?
        let database: Database
        do {
            database = try Database(url: try Database.defaultURL())
        } catch let openFailure {
            // Fall back to a scratch store so the UI can still render and
            // explain the problem. This used to be `try!`, which meant the
            // fallback for a failure could itself crash the app on launch with
            // nothing on screen to say why.
            // If even an in-memory store will not open, there is nothing to
            // build the services on. Show the reason and stop — better than a
            // crash with no message, which is what `try!` gave.
            guard let scratch = Database.scratch() else {
                Self.reportFatalStartupFailure(openFailure)
            }
            failure = openFailure.localizedDescription
            database = scratch
        }
        _settings = StateObject(wrappedValue: settings)
        _monitor = StateObject(wrappedValue: MonitorService(database: database, settings: settings))
        _sessions = StateObject(wrappedValue: SessionManager(database: database))
        let alertService = AlertService(settings: settings)
        _alerts = StateObject(wrappedValue: alertService)
        startupFailure = failure
    }

    /// Reports window visibility to the service for as long as the app runs.
    ///
    /// `occlusionState` covers minimised, hidden behind another window, and on
    /// another Space — everything where laying the dashboard out is wasted.
    /// The menu bar keeps working: it reads the same state, which catches up
    /// the moment a window is shown again.
    private func observeOcclusion(_ monitor: MonitorService) async {
        let notifications = NotificationCenter.default.notifications(
            named: NSApplication.didChangeOcclusionStateNotification
        )
        monitor.setUIVisible(Self.anyWindowVisible)
        for await _ in notifications {
            monitor.setUIVisible(Self.anyWindowVisible)
        }
    }

    /// True when at least one of the app's own windows is on screen. The menu
    /// bar's own status item is not a window here, which is the point.
    @MainActor
    private static var anyWindowVisible: Bool {
        NSApp.windows.contains { window in
            window.isVisible && window.occlusionState.contains(.visible)
        }
    }

    /// Puts the reason in front of the user before exiting. `init` cannot
    /// return without initialising the scene's state, and there is no store to
    /// initialise it with, so this is the one place the app gives up — loudly,
    /// with an alert rather than a silent crash log.
    private static func reportFatalStartupFailure(_ error: Error) -> Never {
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "Server Monitor cannot start"
        alert.informativeText = """
            Its database could not be opened, and neither could a temporary \
            one in memory.

            \(error.localizedDescription)
            """
        alert.addButton(withTitle: "Quit")
        alert.runModal()
        exit(EXIT_FAILURE)
    }

    var body: some Scene {
        WindowGroup(id: "main") {
            Group {
                if let startupFailure {
                    ContentUnavailableView(
                        localization.t("common.error"),
                        systemImage: "exclamationmark.triangle",
                        description: Text(startupFailure)
                    )
                } else {
                    RootView()
                }
            }
            .environmentObject(monitor)
            .environmentObject(sessions)
            .environmentObject(alerts)
            .environmentObject(localization)
            .environmentObject(settings)
            .frame(minWidth: 940, minHeight: 600)
            .task {
                monitor.alerts = alerts
                await alerts.refreshAuthorization()
                // Keep the login item in step with what the system reports, in
                // case it was changed in System Settings.
                settings.launchAtLogin = LoginItem.isEnabled
                monitor.start()
                // SwiftUI lays views out even when every window is covered, so
                // tell the service when there is nothing to draw for.
                await observeOcclusion(monitor)
            }
        }
        .windowToolbarStyle(.unified)
        .commands {
            CommandGroup(after: .newItem) {
                Button(localization.t("common.refresh")) {
                    Task { await monitor.pollAll() }
                }
                .keyboardShortcut("r")
            }
        }

        // Always-on presence: the app keeps polling with its window closed and
        // reports status from the menu bar, which is the point of a native
        // client over a web page.
        MenuBarExtra {
            MenuBarPanel()
                .environmentObject(monitor)
                .environmentObject(localization)
        } label: {
            MenuBarLabel()
                .environmentObject(monitor)
        }
        .menuBarExtraStyle(.window)

        Settings {
            SettingsView()
                .environmentObject(settings)
                .environmentObject(alerts)
                .environmentObject(localization)
        }
    }
}
