import ServerMonitorKit
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
        var database: Database
        do {
            database = try Database(url: try Database.defaultURL())
        } catch {
            failure = error.localizedDescription
            // Fall back to a scratch store so the UI can still render and
            // explain the problem.
            database = try! Database(inMemory: true)
        }
        _settings = StateObject(wrappedValue: settings)
        _monitor = StateObject(wrappedValue: MonitorService(database: database, settings: settings))
        _sessions = StateObject(wrappedValue: SessionManager(database: database))
        let alertService = AlertService(settings: settings)
        _alerts = StateObject(wrappedValue: alertService)
        startupFailure = failure
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
