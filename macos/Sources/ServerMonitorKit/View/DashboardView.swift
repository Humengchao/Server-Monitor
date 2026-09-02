import SwiftUI

/// All hosts at a glance: three counters over a grid of server cards.
public struct DashboardView: View {
    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    private let onSelect: (UUID) -> Void

    /// Filter text from the toolbar search field.
    @Binding private var search: String

    public init(search: Binding<String>, onSelect: @escaping (UUID) -> Void) {
        self._search = search
        self.onSelect = onSelect
    }

    private var visibleServers: [Server] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return monitor.servers }
        return monitor.servers.filter {
            $0.name.lowercased().contains(query)
                || $0.host.lowercased().contains(query)
                || $0.username.lowercased().contains(query)
                // Tags are only useful if searching for one finds the machines
                // wearing it.
                || $0.tags.contains { $0.lowercased().contains(query) }
        }
    }

    public var body: some View {
        ScrollView {
            if monitor.servers.isEmpty {
                ContentUnavailableView(
                    loc.t("dashboard.emptyTitle"),
                    systemImage: "server.rack",
                    description: Text(loc.t("dashboard.empty"))
                )
                .padding(.top, 90)
            } else {
                VStack(spacing: 16) {
                    counters
                    LazyVGrid(
                        columns: [GridItem(.adaptive(minimum: 380, maximum: 620), spacing: 16)],
                        spacing: 16
                    ) {
                        ForEach(visibleServers) { server in
                            ServerCard(server: server, onOpen: { onSelect(server.id) })
                        }
                    }
                }
                .padding(16)
            }
        }
        .navigationTitle(loc.t("dashboard.title"))
    }

    private var counters: some View {
        let statuses = monitor.servers.map { monitor.status[$0.id] ?? .unknown }
        let online = statuses.filter(\.isOnline).count
        let offline = statuses.filter { if case .offline = $0 { return true } else { return false } }.count
        return HStack(spacing: 16) {
            CounterCard(title: loc.t("dashboard.total"), value: monitor.servers.count, tint: .blue)
            CounterCard(title: loc.t("dashboard.online"), value: online, tint: .green)
            CounterCard(title: loc.t("dashboard.offline"), value: offline, tint: .red)
        }
    }
}

/// One of the three headline counters.
struct CounterCard: View {
    let title: String
    let value: Int
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(title)
                .font(.subheadline)
                .foregroundStyle(.secondary)
            HStack(spacing: 8) {
                Circle().fill(tint).frame(width: 9, height: 9)
                Text("\(value)")
                    .font(.system(.title, design: .rounded, weight: .semibold))
                    .monospacedDigit()
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground)
    }
}

/// One host: identity and latency, host facts, then the live metrics block.
struct ServerCard: View {
    let server: Server
    let onOpen: () -> Void

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @State private var hovering = false

    private var status: ServerStatus { monitor.status[server.id] ?? .unknown }
    private var snapshot: MetricSnapshot? { monitor.latest[server.id] }
    private var chinese: Bool { loc.resolved == .zh }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ServerCardHeader(server: server)
            ServerFactsRow(server: server)
            Divider()
            metrics
        }
        .padding(16)
        .background(cardBackground)
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .strokeBorder(hovering ? Color.accentColor.opacity(0.5) : .clear)
        )
        .contentShape(Rectangle())
        .onTapGesture(perform: onOpen)
        .onHover { hovering = $0 }
    }

    /// Every state is pinned to the same height. The dashboard is a lazy grid,
    /// and a lazy grid only stays scrollable when its cells are uniform: an
    /// offline card that collapsed to two lines of text was ~55pt shorter than
    /// its neighbours, which is exactly the height mismatch that made the
    /// machine screen snap back to the bottom before it was made non-lazy.
    private var metrics: some View {
        metricsContent
            .frame(minHeight: 86, alignment: .topLeading)
    }

    @ViewBuilder
    private var metricsContent: some View {
        // Offline first: `latest` keeps the last good snapshot, and gauges drawn
        // from it look like a live reading of a host that is not answering.
        if case .offline(let reason) = status {
            VStack(alignment: .leading, spacing: 4) {
                Text(reason)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(2)
                if let snapshot {
                    Text(loc.t("dashboard.lastSeen", Format.uptime(snapshot.uptimeSeconds, chinese: loc.resolved == .zh)))
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } else if let snapshot {
            HStack(alignment: .top, spacing: 0) {
                gaugeColumn(loc.t("metric.cpu"), snapshot.cpuPercent)
                gaugeColumn(loc.t("metric.memory"), snapshot.memoryPercent)
                Spacer(minLength: 12)
                RatePair(
                    title: loc.t("metric.network"),
                    firstSymbol: "arrow.up.circle",
                    firstValue: snapshot.netTxRate,
                    secondSymbol: "arrow.down.circle",
                    secondValue: snapshot.netRxRate
                )
                .frame(maxWidth: .infinity, alignment: .leading)
                RatePair(
                    title: loc.t("metric.disk"),
                    firstSymbol: "r.circle",
                    firstValue: snapshot.diskReadRate,
                    secondSymbol: "w.circle",
                    secondValue: snapshot.diskWriteRate
                )
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            HStack { Spacer(); ProgressView().controlSize(.small); Spacer() }
                .frame(maxWidth: .infinity, minHeight: 86)
        }
    }

    private func gaugeColumn(_ title: String, _ value: Double) -> some View {
        VStack(spacing: 8) {
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
            RingGauge(value: value)
        }
        .frame(width: 88)
    }
}

/// Shared card chrome, so counters and server cards sit on the same surface.
var cardBackground: some View {
    RoundedRectangle(cornerRadius: 14)
        .fill(Color(nsColor: .controlBackgroundColor))
        .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(.separator.opacity(0.6)))
}

/// Flag, name and latency — the identity line every host card shares.
struct ServerCardHeader: View {
    let server: Server

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    private var status: ServerStatus { monitor.status[server.id] ?? .unknown }

    var body: some View {
        HStack(spacing: 8) {
            if !server.flag.isEmpty {
                Text(server.flag).font(.title3)
            }
            Text(server.name)
                .font(.system(.title3, weight: .semibold))
                .lineLimit(1)
                .layoutPriority(1)
            if !server.tags.isEmpty {
                TagChips(tags: server.tags, limit: 3)
            }
            Spacer(minLength: 8)
            badge
        }
    }

    /// Green under 100 ms, amber to 300 ms, red beyond — the point where an
    /// interactive SSH session starts to feel laggy.
    static func latencyTint(_ milliseconds: Double) -> Color {
        switch milliseconds {
        case ..<100: return .green
        case ..<300: return .yellow
        default: return .orange
        }
    }

    @ViewBuilder
    private var badge: some View {
        switch status {
        case .online:
            let milliseconds = monitor.latest[server.id]?.latencyMs ?? 0
            HStack(spacing: 6) {
                Circle().fill(.green).frame(width: 8, height: 8)
                Text(Format.latency(milliseconds))
                    .font(.callout.weight(.medium))
                    .monospacedDigit()
                    .foregroundStyle(ServerCardHeader.latencyTint(milliseconds))
            }
        case .offline:
            HStack(spacing: 6) {
                Circle().fill(.red).frame(width: 8, height: 8)
                Text(loc.t("common.offline")).font(.callout).foregroundStyle(.red)
            }
        case .polling, .unknown:
            ProgressView().controlSize(.small)
        }
    }
}

/// Cores, memory, disk and uptime.
struct ServerFactsRow: View {
    let server: Server

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        HStack(spacing: 18) {
            FactChip(systemImage: "cpu", text: "\(server.cores) Cores")
            FactChip(systemImage: "memorychip", text: Format.bytes(server.memoryTotal))
            FactChip(systemImage: "internaldrive", text: Format.bytes(server.diskTotal))
            if let snapshot = monitor.latest[server.id], snapshot.uptimeSeconds > 0 {
                FactChip(
                    systemImage: "power",
                    text: Format.uptimeDays(snapshot.uptimeSeconds, chinese: loc.resolved == .zh)
                )
            }
            Spacer(minLength: 0)
        }
    }
}
