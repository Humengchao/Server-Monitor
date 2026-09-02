import Charts
import SwiftUI

/// History window shown in the detail charts.
public enum TimeRange: String, CaseIterable, Identifiable {
    case m15, h1, h6, h24
    public var id: String { rawValue }

    var seconds: TimeInterval {
        switch self {
        case .m15: return 15 * 60
        case .h1: return 3600
        case .h6: return 6 * 3600
        case .h24: return 24 * 3600
        }
    }

    var labelKey: String {
        switch self {
        case .m15: return "range.15m"
        case .h1: return "range.1h"
        case .h6: return "range.6h"
        case .h24: return "range.24h"
        }
    }
}

public struct ServerDetailView: View {
    private let server: Server

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    @State private var tab: Tab = .overview
    @State private var range: TimeRange = .m15
    @State private var samples: [MetricSample] = []

    enum Tab: Hashable { case overview, terminal, docker }

    public init(server: Server) {
        self.server = server
    }

    public var body: some View {
        VStack(spacing: 0) {
            Picker("", selection: $tab) {
                Text(loc.t("nav.overview")).tag(Tab.overview)
                Text(loc.t("nav.terminal")).tag(Tab.terminal)
                if server.hasDocker {
                    Text(loc.t("nav.docker")).tag(Tab.docker)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(10)
            .frame(maxWidth: 420)

            Divider()

            switch tab {
            case .overview:
                overview
            case .terminal:
                // Identity keyed on the server so switching hosts tears the old
                // session down rather than reusing its connection.
                TerminalPane(server: server).id(server.id)
            case .docker:
                DockerPane(server: server).id(server.id)
            }
        }
        .navigationTitle(server.name)
        .navigationSubtitle(server.displayTarget)
        // A task keyed on the server, rather than a Timer: the timer's closure
        // captured a copy of this struct — and with it the server it was made
        // for — so it kept reloading that host's history after the view had
        // moved on. The task is cancelled and restarted with the new id.
        .task(id: server.id) {
            loadHistory()
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(15))
                guard !Task.isCancelled else { break }
                loadHistory()
            }
        }
        .onChange(of: range) { _, _ in loadHistory() }
    }

    /// ≤240 points, bucketed in SQLite off the main thread. The charts re-lay
    /// out on every frame of a resize and a day of raw polls is ~17,000 points
    /// (measured at 1.6 s per frame); 240 is more than a 700pt-wide chart can
    /// show anyway, and fetching them raw was itself a 120 ms main-thread hitch.
    private func loadHistory() {
        let requested = range
        let since = Date().addingTimeInterval(-requested.seconds)
        let serverID = server.id
        Task {
            let reduced = await monitor.chartHistory(serverID: serverID, since: since)
            // The picker may have moved while the query ran; a stale answer
            // would flash the old range's shape before the right one lands.
            guard range == requested else { return }
            samples = reduced
        }
    }

    // MARK: - Overview

    /// A grid of status cards rather than one column of tiles, matching
    /// SwiftServer's machine screen: on a wide window CPU, memory and network
    /// sit side by side; on a narrow one they stack, with no second layout.
    private var overview: some View {
        // The width is read outside the ScrollView: the card layout needs it to
        // pick a column count, and a GeometryReader inside a scroll view
        // measures the scrolling content rather than the pane.
        GeometryReader { proxy in
            ScrollView {
                overviewContent(width: proxy.size.width - 32)
            }
        }
    }

    @ViewBuilder
    private func overviewContent(width: CGFloat) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            headerCard
            if let snapshot = monitor.latest[server.id] {
                cards(snapshot, width: width)
            } else if case .offline = status {
                // The reason is already on the header card; no point
                // showing a grid of zeroes underneath it.
                EmptyView()
            } else {
                HStack {
                    Spacer()
                    ProgressView().controlSize(.small)
                    Spacer()
                }
                .padding(.vertical, 40)
            }

            Picker("", selection: $range) {
                ForEach(TimeRange.allCases) { value in
                    Text(loc.t(value.labelKey)).tag(value)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(maxWidth: 360)

            // Same quantised width as the card grid, so a drag relayouts the
            // charts only on the frames the cards relayout too.
            HistoryCharts(samples: samples)
                .frame(width: GridLayout.quantise(width))
                .frame(maxWidth: .infinity, alignment: .center)
        }
        .padding(16)
    }

    private var status: ServerStatus { monitor.status[server.id] ?? .unknown }

    private var isWindows: Bool {
        monitor.latest[server.id]?.detectedOS == .windows || server.osKind == .windows
    }

    private var headerCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            ServerCardHeader(server: server)
            ServerFactsRow(server: server)
            if case .offline(let reason) = status {
                Label(reason, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground)
    }

    private func cards(_ snapshot: MetricSnapshot, width: CGFloat) -> some View {
        MachineCardsLayout(
            server: server, snapshot: snapshot, samples: samples, isWindows: isWindows, width: width
        )
    }

}
