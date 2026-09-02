import AppKit
import SwiftUI

/// The panel behind the menu bar icon.
///
/// Deliberately read-only and compact: it answers "is anything wrong?" at a
/// glance without opening the app, and hands off to the window for anything
/// that needs interaction.
public struct MenuBarPanel: View {
    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @Environment(\.openWindow) private var openWindow

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()

            if monitor.servers.isEmpty {
                Text(loc.t("dashboard.empty"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(12)
            } else if monitor.servers.count <= visibleWithoutScrolling {
                // A menu bar window proposes no height, so a ScrollView here
                // collapses to nothing. Short lists lay out naturally instead.
                serverRows
            } else {
                ScrollView { serverRows }
                    .frame(height: CGFloat(visibleWithoutScrolling) * rowHeight)
            }

            Divider()
            footer
        }
        .frame(width: 300)
    }

    /// Beyond this many hosts the list scrolls instead of growing the panel.
    private let visibleWithoutScrolling = 8
    private let rowHeight: CGFloat = 40

    private var serverRows: some View {
        VStack(spacing: 0) {
            ForEach(monitor.servers) { server in
                row(server)
                if server.id != monitor.servers.last?.id { Divider() }
            }
        }
    }

    private var header: some View {
        let offline = monitor.offlineServers.count
        return HStack(spacing: 8) {
            Text(loc.t("app.title")).font(.headline)
            Spacer()
            if offline > 0 {
                Label("\(offline)", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.red)
            } else {
                Label(loc.t("common.online"), systemImage: "checkmark.circle.fill")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }

    private func row(_ server: Server) -> some View {
        let status = monitor.status[server.id] ?? .unknown
        let snapshot = monitor.latest[server.id]
        return HStack(spacing: 8) {
            StatusDot(status: status)
            if !server.flag.isEmpty { Text(server.flag).font(.caption) }
            Text(server.name)
                .font(.callout)
                .lineLimit(1)
            Spacer(minLength: 6)
            if let snapshot, status.isOnline {
                HStack(spacing: 8) {
                    metric("CPU", snapshot.cpuPercent)
                    metric(loc.t("metric.memory"), snapshot.memoryPercent)
                }
            } else if case .offline = status {
                Text(loc.t("common.offline"))
                    .font(.caption2)
                    .foregroundStyle(.red)
            } else {
                ProgressView().controlSize(.small)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .contentShape(Rectangle())
        .onTapGesture { openMainWindow() }
    }

    private func metric(_ label: String, _ value: Double) -> some View {
        VStack(alignment: .trailing, spacing: 0) {
            Text(label).font(.system(size: 8)).foregroundStyle(.tertiary)
            Text(Format.percent(value))
                .font(.caption2.weight(.medium))
                .monospacedDigit()
                .foregroundStyle(MetricTile.tint(for: value / 100))
        }
        .frame(width: 42, alignment: .trailing)
    }

    private var footer: some View {
        HStack(spacing: 12) {
            Button(loc.t("menubar.open")) { openMainWindow() }
                .buttonStyle(.link)
            Button(loc.t("common.refresh")) {
                Task { await monitor.pollAll() }
            }
            .buttonStyle(.link)
            Spacer()
            Button(loc.t("menubar.quit")) { NSApplication.shared.terminate(nil) }
                .buttonStyle(.link)
        }
        .font(.caption)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    /// Brings the main window forward, reopening it if it was closed.
    private func openMainWindow() {
        NSApplication.shared.activate(ignoringOtherApps: true)
        if let window = NSApplication.shared.windows.first(where: { $0.canBecomeMain }) {
            window.makeKeyAndOrderFront(nil)
        } else {
            openWindow(id: "main")
        }
    }
}

/// Compact status for the menu bar itself.
public struct MenuBarLabel: View {
    @EnvironmentObject private var monitor: MonitorService

    public init() {}

    public var body: some View {
        let offline = monitor.offlineServers.count
        HStack(spacing: 3) {
            Image(systemName: offline > 0 ? "bolt.trianglebadge.exclamationmark" : "bolt.horizontal")
            if offline > 0 {
                Text("\(offline)").font(.caption2.weight(.bold))
            } else if let peak = monitor.peakCPU {
                // Showing the busiest host means the icon carries information
                // even when everything is healthy.
                Text("\(Int(peak.percent.rounded()))%")
                    .font(.caption2)
                    .monospacedDigit()
            }
        }
    }
}
