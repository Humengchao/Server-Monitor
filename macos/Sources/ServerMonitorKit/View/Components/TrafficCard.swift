import Charts
import SwiftUI

/// Upload/download history from vnStat, the way SwiftServer's network section
/// shows it: a range picker over hours, days, months and years, bars per
/// bucket, and totals for the range and all time.
///
/// Three states, because most hosts start in the first: vnStat not installed
/// (with a pointer to the install guide), installed but still collecting, and
/// data. Fetched on the card's own cadence rather than in the metrics poll —
/// `vnstat --json` is a few hundred KB on a busy host and changes by the
/// minute, not the second.
struct StatusTrafficCard: View {
    let server: Server

    @Environment(MonitorService.self) private var monitor
    @EnvironmentObject private var loc: Localization

    @State private var outcome: VnstatParser.Outcome?
    @State private var failure: String?
    @State private var loading = false
    @State private var granularity: TrafficGranularity = .hour
    @State private var selectedInterface: String?

    @State private var probing = false
    @State private var installing = false
    @State private var installPlan: VnstatInstaller.Plan?
    @State private var confirmingInstall = false
    /// Text, whether it is an error, and a command the user can copy when we
    /// could not run it for them.
    @State private var installMessage: (text: String, isError: Bool, command: String?)?

    /// A result to show without fetching — how the render check gets at the
    /// three states without a host in each one.
    private let preloaded: VnstatParser.Outcome?

    init(server: Server, preloaded: VnstatParser.Outcome? = nil) {
        self.server = server
        self.preloaded = preloaded
        _outcome = State(initialValue: preloaded)
    }

    private var report: TrafficReport? {
        if case .report(let report)? = outcome { return report }
        return nil
    }

    private var interface: TrafficInterface? {
        guard let report else { return nil }
        if let selectedInterface, let chosen = report.interfaces.first(where: { $0.name == selectedInterface }) {
            return chosen
        }
        return report.primaryInterface
    }

    var body: some View {
        StatusCard(title: loc.t("traffic.title"), systemImage: "chart.bar.xaxis", tint: .green) {
            accessory
        } content: {
            content
        }
        .task(id: server.id) { await load() }
    }

    @ViewBuilder
    private var accessory: some View {
        if loading {
            ProgressView().controlSize(.mini)
        } else if let report, report.interfaces.count > 1 {
            // Only when there is a choice to make.
            Menu {
                ForEach(report.interfaces) { candidate in
                    Button(candidate.displayName) { selectedInterface = candidate.name }
                }
            } label: {
                Text(interface?.displayName ?? "").font(.caption)
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
        } else if let interface {
            Text(interface.displayName).font(.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch outcome {
        case nil:
            if let failure {
                Text(failure).font(.caption).foregroundStyle(.red).lineLimit(2)
            } else {
                Text(loc.t("card.awaitingData")).font(.caption).foregroundStyle(.tertiary)
            }
        case .notInstalled?:
            notInstalled
        case .report(let report)?:
            if report.isCollecting {
                explanation(
                    symbol: "hourglass",
                    title: loc.t("traffic.collecting"),
                    body: loc.t("traffic.collectingHint")
                )
            } else if let interface {
                chart(for: interface)
            }
        }
    }

    private var notInstalled: some View {
        VStack(alignment: .leading, spacing: 10) {
            explanation(
                symbol: "shippingbox.and.arrow.backward",
                title: loc.t("traffic.notInstalled"),
                body: loc.t("traffic.installHint")
            )
            HStack(spacing: 12) {
                if installing {
                    ProgressView().controlSize(.small)
                    Text(loc.t("traffic.installing")).font(.caption).foregroundStyle(.secondary)
                } else {
                    Button(loc.t("traffic.install"), systemImage: "arrow.down.circle") {
                        Task { await prepareInstall() }
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(probing)
                    if probing { ProgressView().controlSize(.mini) }
                }
                Link(destination: URL(string: "https://humdi.net/vnstat")!) {
                    Label(loc.t("traffic.installLink"), systemImage: "arrow.up.right.square")
                        .font(.caption)
                }
            }
            if let installMessage {
                VStack(alignment: .leading, spacing: 4) {
                    Text(installMessage.text)
                        .font(.caption)
                        .foregroundStyle(installMessage.isError ? .red : .secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    if let command = installMessage.command {
                        HStack(alignment: .top, spacing: 6) {
                            Text(command)
                                .font(.system(size: 10, design: .monospaced))
                                .textSelection(.enabled)
                                .fixedSize(horizontal: false, vertical: true)
                            Button(loc.t("common.copy")) {
                                NSPasteboard.general.clearContents()
                                NSPasteboard.general.setString(command, forType: .string)
                            }
                            .buttonStyle(.borderless)
                            .font(.caption)
                        }
                    }
                }
            }
        }
        // Nothing runs until this is confirmed; the message is the exact command.
        .alert(
            loc.t("traffic.installConfirmTitle", server.name),
            isPresented: $confirmingInstall,
            presenting: installPlan
        ) { plan in
            Button(loc.t("common.install")) { Task { await install(plan) } }
            Button(loc.t("common.cancel"), role: .cancel) {}
        } message: { plan in
            Text(loc.t("traffic.installConfirmBody") + "\n\n" + plan.displayCommand)
        }
    }

    // MARK: - Install

    /// Step one: find out what would run, then ask.
    private func prepareInstall() async {
        probing = true
        installMessage = nil
        defer { probing = false }
        do {
            let output = try await monitor.run(VnstatInstaller.probeCommand, on: server, timeout: 20)
            guard let plan = VnstatInstaller.plan(fromProbe: output) else {
                installMessage = (loc.t("traffic.installNoPackageManager"), true, nil)
                return
            }
            installPlan = plan
            confirmingInstall = true
        } catch {
            if error is CancellationError { return }
            installMessage = (error.localizedDescription, true, nil)
        }
    }

    /// Step two, after confirmation.
    private func install(_ plan: VnstatInstaller.Plan) async {
        installing = true
        defer { installing = false }
        do {
            // apt-get update alone can take a minute on a slow mirror.
            let output = try await monitor.run(VnstatInstaller.remoteScript(for: plan), on: server, timeout: 300)
            switch VnstatInstaller.outcome(from: output) {
            case .installed:
                installMessage = (loc.t("traffic.installed"), false, nil)
                await load()
            case .needsSudoPassword:
                installMessage = (loc.t("traffic.installNeedsSudo"), true, plan.displayCommand)
            case .failed(let detail):
                installMessage = (loc.t("traffic.installFailed") + " " + detail, true, plan.displayCommand)
            }
        } catch {
            if error is CancellationError { return }
            installMessage = (error.localizedDescription, true, plan.displayCommand)
        }
    }

    private func explanation(symbol: String, title: String, body: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(.secondary)
                .frame(width: 24)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.caption.weight(.medium))
                Text(body)
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: - Chart

    private func chart(for interface: TrafficInterface) -> some View {
        let buckets = Array(interface.buckets(granularity).suffix(granularity.shownCount))
        return VStack(alignment: .leading, spacing: 10) {
            Picker("", selection: $granularity) {
                ForEach(TrafficGranularity.allCases) { value in
                    Text(loc.t("traffic.\(value.rawValue)")).tag(value)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .controlSize(.small)

            if buckets.isEmpty {
                Text(loc.t("traffic.collecting"))
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                    .frame(height: 120)
            } else {
                bars(buckets)
            }

            totals(interface: interface, shown: buckets)
        }
    }

    /// Two stacked series per bucket. The x axis is categorical — a label per
    /// bucket — because five-minute slots have no Swift Charts calendar unit
    /// and mixed-width bars would misrepresent equal intervals.
    private func bars(_ buckets: [TrafficBucket]) -> some View {
        let labels = buckets.map { Self.label(for: $0.date, granularity: granularity) }
        let step = max(1, labels.count / 6)
        return Chart {
            ForEach(Array(buckets.enumerated()), id: \.offset) { index, bucket in
                BarMark(x: .value("t", labels[index]), y: .value("rx", bucket.rx))
                    .foregroundStyle(by: .value("dir", loc.t("metric.download")))
                BarMark(x: .value("t", labels[index]), y: .value("tx", bucket.tx))
                    .foregroundStyle(by: .value("dir", loc.t("metric.upload")))
            }
        }
        .chartForegroundStyleScale([
            loc.t("metric.download"): Color.green,
            loc.t("metric.upload"): Color.orange,
        ])
        .chartXAxis {
            AxisMarks(values: Array(stride(from: 0, to: labels.count, by: step)).map { labels[$0] }) { _ in
                AxisValueLabel().font(.system(size: 9))
            }
        }
        .chartYAxis {
            AxisMarks { value in
                AxisGridLine()
                AxisValueLabel {
                    if let bytes = value.as(Int64.self) {
                        Text(Format.bytes(bytes)).font(.system(size: 9))
                    }
                }
            }
        }
        // Room for the first and last labels: a categorical axis puts its first
        // label flush against the plot edge and truncates it to "0…".
        .chartXScale(range: .plotDimension(startPadding: 18, endPadding: 18))
        .chartLegend(position: .top, alignment: .trailing)
        .frame(height: 130)
    }

    private func totals(interface: TrafficInterface, shown: [TrafficBucket]) -> some View {
        let rx = shown.reduce(0) { $0 + $1.rx }
        let tx = shown.reduce(0) { $0 + $1.tx }
        return HStack(spacing: 0) {
            total(loc.t("traffic.shown"), rx: rx, tx: tx)
            Divider().frame(height: 22)
            total(loc.t("traffic.allTime"), rx: interface.totalRx, tx: interface.totalTx)
        }
    }

    private func total(_ title: String, rx: Int64, tx: Int64) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.system(size: 9)).foregroundStyle(.tertiary)
            HStack(spacing: 8) {
                Label(Format.bytes(rx), systemImage: "arrow.down")
                    .foregroundStyle(.green)
                Label(Format.bytes(tx), systemImage: "arrow.up")
                    .foregroundStyle(.orange)
            }
            .font(.system(size: 11, design: .rounded))
            .monospacedDigit()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Axis labels in the host's own clock — see `VnstatParser.calendar`.
    static func label(for date: Date, granularity: TrafficGranularity) -> String {
        formatters[granularity]!.string(from: date)
    }

    /// Built once. A `DateFormatter` costs ~0.15 ms to create, and the chart
    /// labels every bar on every frame of a resize — 24 fresh formatters per
    /// frame were a third of this card's cost.
    private static let formatters: [TrafficGranularity: DateFormatter] = {
        var result: [TrafficGranularity: DateFormatter] = [:]
        for granularity in TrafficGranularity.allCases {
            let formatter = DateFormatter()
            formatter.timeZone = VnstatParser.calendar.timeZone
            switch granularity {
            case .fiveMinute, .hour: formatter.dateFormat = "HH:mm"
            case .day: formatter.dateFormat = "M/d"
            case .month: formatter.dateFormat = "yyyy-MM"
            case .year: formatter.dateFormat = "yyyy"
            }
            result[granularity] = formatter
        }
        return result
    }()

    private func load() async {
        guard preloaded == nil else { return }
        loading = true
        failure = nil
        defer { loading = false }
        // A new server must not show the previous one's chart while its own
        // query is in flight.
        outcome = nil
        do {
            // vnstat --json can be a few hundred KB; give it longer than a poll.
            let output = try await monitor.run(VnstatParser.command, on: server, timeout: 45)
            if let parsed = VnstatParser.parse(output) {
                outcome = parsed
            } else {
                failure = loc.t("traffic.unavailable")
            }
        } catch {
            // The view moved on (server switched); not an error to show.
            if error is CancellationError { return }
            failure = error.localizedDescription
        }
    }
}
