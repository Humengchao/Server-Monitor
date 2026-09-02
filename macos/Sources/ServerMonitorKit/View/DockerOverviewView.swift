import SwiftUI

/// Docker across every host, as a grid of engine cards.
///
/// Mirrors the dashboard's shape on purpose: the same header and facts row, so
/// a host reads the same way whichever page you are on, with engine counts
/// replacing the CPU gauges.
struct DockerOverviewView: View {
    @Binding var search: String
    let onOpen: (UUID) -> Void

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @State private var loading = false

    private var hosts: [Server] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        let all = monitor.servers.filter(\.hasDocker)
        guard !query.isEmpty else { return all }
        return all.filter { $0.name.lowercased().contains(query) }
    }

    var body: some View {
        ScrollView {
            if hosts.isEmpty {
                ContentUnavailableView(
                    loc.t("nav.docker"),
                    systemImage: "shippingbox",
                    description: Text(loc.t("docker.noHosts"))
                )
                .padding(.top, 90)
            } else {
                LazyVGrid(
                    columns: [GridItem(.adaptive(minimum: 360, maximum: 560), spacing: 16)],
                    spacing: 16
                ) {
                    ForEach(hosts) { host in
                        DockerHostCard(server: host, onOpen: { onOpen(host.id) })
                    }
                }
                .padding(16)
            }
        }
        .navigationTitle(loc.t("nav.docker"))
        .task { await reload() }
        .onChange(of: monitor.servers.count) { _, _ in
            Task { await reload() }
        }
    }

    private func reload() async {
        guard !loading else { return }
        loading = true
        await monitor.refreshDockerSummaries()
        loading = false
    }
}

/// One Docker host: identity, host facts, then engine counts.
struct DockerHostCard: View {
    let server: Server
    let onOpen: () -> Void

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @State private var hovering = false

    private var summary: DockerSummary? { monitor.dockerSummaries[server.id] }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ServerCardHeader(server: server)
            ServerFactsRow(server: server)
            Divider()
            counts
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

    /// Same height loaded or not — this is a lazy grid; see DashboardView.
    private var counts: some View {
        countsContent.frame(minHeight: 52, alignment: .topLeading)
    }

    @ViewBuilder
    private var countsContent: some View {
        if let summary {
            HStack(alignment: .top, spacing: 0) {
                stat(loc.t("docker.engine"), summary.engineVersion.isEmpty ? "—" : summary.engineVersion)
                stat(loc.t("docker.images"), "\(summary.images)")
                stat(loc.t("docker.running"), "\(summary.running)", tint: summary.running > 0 ? .green : .secondary)
                stat(loc.t("docker.stopped"), "\(summary.stopped)", tint: summary.stopped > 0 ? .orange : .secondary)
            }
        } else {
            HStack { Spacer(); ProgressView().controlSize(.small); Spacer() }
                .frame(maxWidth: .infinity, minHeight: 52)
        }
    }

    private func stat(_ title: String, _ value: String, tint: Color = .primary) -> some View {
        VStack(spacing: 6) {
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.system(.title3, design: .rounded, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(tint)
                .lineLimit(1)
                .minimumScaleFactor(0.6)
        }
        .frame(maxWidth: .infinity)
    }
}
