import Charts
import SwiftUI

// MARK: - Machine information

/// Who this machine is: the facts that do not move between polls.
struct StatusMachineCard: View {
    let server: Server
    let snapshot: MetricSnapshot?
    @EnvironmentObject private var loc: Localization

    var body: some View {
        StatusCard(title: loc.t("card.machineInfo"), systemImage: "desktopcomputer", tint: .indigo) {
            VStack(spacing: 7) {
                StatusFactRow(label: loc.t("card.hostname"), value: identity.hostname.isEmpty ? server.name : identity.hostname)
                StatusFactRow(label: loc.t("server.host"), value: server.displayTarget)
                if !identity.osName.isEmpty {
                    StatusFactRow(label: loc.t("card.os"), value: identity.osName)
                }
                if !identity.kernel.isEmpty {
                    StatusFactRow(label: loc.t("card.kernel"), value: identity.kernel)
                }
                if !identity.architecture.isEmpty {
                    StatusFactRow(label: loc.t("card.arch"), value: identity.architecture)
                }
                StatusFactRow(label: loc.t("card.cores"), value: cores)
                if !identity.addresses.isEmpty {
                    StatusFactRow(
                        label: loc.t("card.addresses"),
                        value: identity.addresses.prefix(4).joined(separator: "\n")
                    )
                }
            }
        }
    }

    private var identity: HostIdentity { snapshot?.identity ?? HostIdentity() }

    private var cores: String {
        let count = snapshot?.cores ?? server.cores
        return count > 0 ? "\(count)" : ""
    }
}

// MARK: - CPU

/// Overall usage plus every logical core, the way SwiftServer's CPU card does
/// it — one busy core on a 16-core box is invisible in the average alone.
struct StatusCPUCard: View {
    let snapshot: MetricSnapshot
    @EnvironmentObject private var loc: Localization
    @Environment(\.cardWidth) private var cardWidth
    @State private var showAllCores = false

    private var cores: [CoreLoad] { snapshot.coreLoads }
    private var collapsedLimit: Int { 8 }

    /// Width of one core cell when the container told us the card's width:
    /// card padding (14 each side), the 74pt ring plus its 16pt gap, then two
    /// cells and the 8pt between them. Fixed cells lay out in one pass.
    private var coreCellWidth: CGFloat? {
        guard let cardWidth else { return nil }
        let available = cardWidth - 28 - 74 - 16
        return available > 120 ? (available - 8) / 2 : nil
    }

    var body: some View {
        StatusCard(title: loc.t("card.cpuUsage"), systemImage: "cpu", tint: .blue) {
            if cores.count > collapsedLimit {
                // A labelled button rather than a bare switch: an unlabelled
                // toggle in a card header says nothing about what it toggles.
                Button(showAllCores ? loc.t("common.collapse") : loc.t("card.showAllCores")) {
                    showAllCores.toggle()
                }
                .buttonStyle(.borderless)
                .font(.caption)
            }
        } content: {
            VStack(alignment: .leading, spacing: 10) {
                HStack(alignment: .top, spacing: 16) {
                    VStack(spacing: 4) {
                        RingGauge(value: snapshot.cpuPercent, diameter: 74, lineWidth: 8)
                        if snapshot.cores > 0 {
                            Text("\(snapshot.cores) \(loc.t("card.cores"))")
                                .font(.system(size: 9))
                                .foregroundStyle(.secondary)
                        }
                    }
                    if cores.isEmpty {
                        Text(loc.t("card.awaitingData"))
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    } else {
                        coreGrid
                    }
                }
                if !snapshot.identity.cpuModel.isEmpty {
                    Text(snapshot.identity.cpuModel)
                        .font(.system(size: 9))
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
                // Left out entirely on hosts that do not report the split,
                // rather than drawn as five zeroes.
                if snapshot.cpuBreakdown.isReported {
                    Divider()
                    breakdownRow(snapshot.cpuBreakdown)
                }
            }
        }
    }

    /// User / System / Nice / IOWait / Steal — the split that says whether a
    /// busy box is working, waiting on disk, or being robbed by its host.
    private func breakdownRow(_ breakdown: CPUBreakdown) -> some View {
        // Five equal cells; fixed when the width is known, for one-pass layout.
        let cellWidth = cardWidth.map { ($0 - 28) / 5 }
        return HStack(spacing: 0) {
            ForEach(Self.slices(breakdown, loc: loc), id: \.0) { slice in
                let cell = VStack(spacing: 1) {
                    Text(Format.percent(slice.1))
                        .font(.system(size: 11, weight: .medium, design: .rounded))
                        .monospacedDigit()
                        .foregroundStyle(slice.2)
                    Text(slice.0)
                        .font(.system(size: 9))
                        .foregroundStyle(.secondary)
                }
                if let cellWidth {
                    cell.frame(width: cellWidth)
                } else {
                    cell.frame(maxWidth: .infinity)
                }
            }
        }
    }

    static func slices(_ breakdown: CPUBreakdown, loc: Localization) -> [(String, Double, Color)] {
        [
            ("User", breakdown.user, .blue),
            ("System", breakdown.system, .purple),
            ("Nice", breakdown.nice, .secondary),
            // The two worth noticing: time lost waiting on disk, and time the
            // hypervisor gave to somebody else.
            ("IOWait", breakdown.iowait, breakdown.iowait > 10 ? .orange : .secondary),
            ("Steal", breakdown.steal, breakdown.steal > 5 ? .red : .secondary),
        ]
    }

    private var coreGrid: some View {
        let shown = showAllCores ? cores : Array(cores.prefix(collapsedLimit))
        // Fixed rows rather than a lazy grid: a lazy grid inside a card inside
        // a scroll view only measures what is visible, so the card's height
        // changes as it scrolls — see `StaticGrid`.
        return VStack(spacing: 7) {
            ForEach(Array(shown.chunked(into: 2).enumerated()), id: \.offset) { row in
                HStack(spacing: 8) {
                    ForEach(row.element) { core in
                        coreBar(core)
                    }
                    // Keeps a lone core on the last row the same width as the
                    // others instead of stretching it across the card.
                    if row.element.count == 1, coreCellWidth == nil {
                        Color.clear.frame(maxWidth: .infinity)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func coreBar(_ core: CoreLoad) -> some View {
        if let coreCellWidth {
            coreBarContent(core).frame(width: coreCellWidth)
        } else {
            coreBarContent(core).frame(maxWidth: .infinity)
        }
    }

    private func coreBarContent(_ core: CoreLoad) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 4) {
                Text("#\(core.index)")
                    .font(.system(size: 9, design: .rounded))
                    .foregroundStyle(.tertiary)
                Spacer(minLength: 0)
                Text(Format.percent(core.percent))
                    .font(.system(size: 9, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            StatusBar(fraction: core.percent / 100, height: 4)
        }
    }

}

/// Load average, which on Windows is the processor queue length instead.
struct StatusLoadCard: View {
    let snapshot: MetricSnapshot
    let samples: [MetricSample]
    let isWindows: Bool
    @EnvironmentObject private var loc: Localization

    var body: some View {
        StatusCard(title: loc.t("card.cpuLoad"), systemImage: "chart.line.uptrend.xyaxis", tint: .teal) {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 18) {
                    loadValue("1m", snapshot.load1)
                    if !isWindows {
                        loadValue("5m", snapshot.load5)
                        loadValue("15m", snapshot.load15)
                    }
                    Spacer(minLength: 0)
                }
                Chart {
                    AreaPlot(samples, x: .value("t", \.timestamp), y: .value("load", \.load1))
                        .foregroundStyle(.teal.opacity(0.16))
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("load", \.load1))
                        .foregroundStyle(.teal)
                }
                .chartLegend(.hidden)
                .chartXAxis { AxisMarks(values: .automatic(desiredCount: 4)) }
                .chartYAxis { AxisMarks(values: .automatic(desiredCount: 3)) }
                .frame(height: 74)
            }
        }
    }

    private func loadValue(_ label: String, _ value: Double) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.system(size: 9)).foregroundStyle(.tertiary)
            Text(Format.load(value))
                .font(.system(size: 15, weight: .semibold, design: .rounded))
                .monospacedDigit()
        }
    }
}

// MARK: - Memory

/// Where the RAM went, not just how much is gone.
struct StatusMemoryCard: View {
    let snapshot: MetricSnapshot
    @EnvironmentObject private var loc: Localization

    private var memory: MemoryBreakdown { snapshot.memory }

    var body: some View {
        StatusCard(title: loc.t("card.memoryUsage"), systemImage: "memorychip", tint: .purple) {
            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .center, spacing: 16) {
                    donut
                    legend
                }
                if memory.hasSwap {
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Text(loc.t("card.swap"))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Spacer()
                            Text(Format.usage(used: memory.swapUsed, total: memory.swapTotal))
                                .font(.caption)
                                .monospacedDigit()
                                .foregroundStyle(.secondary)
                        }
                        StatusBar(fraction: memory.swapPercent / 100, height: 5)
                    }
                }
            }
        }
    }

    /// Falls back to the flat used/total split on hosts that do not report a
    /// buffers/cache breakdown — Windows, mainly.
    private var bands: [(key: String, bytes: Int64, color: Color)] {
        guard memory.total > 0 else { return [] }
        if memory.buffers == 0 && memory.cached == 0 {
            return [
                (loc.t("card.used"), memory.used, .purple),
                (loc.t("card.free"), max(0, memory.total - memory.used), Color.primary.opacity(0.12)),
            ]
        }
        return [
            (loc.t("card.used"), memory.used, .purple),
            (loc.t("card.buffers"), memory.buffers, .blue),
            (loc.t("card.cached"), memory.cached, .teal),
            (loc.t("card.free"), memory.free, Color.primary.opacity(0.12)),
        ]
    }

    private var donut: some View {
        Chart(bands, id: \.key) { band in
            SectorMark(
                angle: .value("bytes", max(band.bytes, 0)),
                innerRadius: .ratio(0.66),
                angularInset: 1
            )
            .foregroundStyle(band.color)
        }
        .chartLegend(.hidden)
        .frame(width: 92, height: 92)
        .overlay {
            VStack(spacing: 0) {
                Text(Format.percent(memory.usedPercent))
                    .font(.system(size: 15, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                Text(Format.bytes(memory.total))
                    .font(.system(size: 9))
                    .foregroundStyle(.tertiary)
            }
        }
    }

    private var legend: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(bands, id: \.key) { band in
                HStack(spacing: 6) {
                    RoundedRectangle(cornerRadius: 2)
                        .fill(band.color)
                        .frame(width: 8, height: 8)
                    Text(band.key).font(.caption)
                    Spacer(minLength: 10)
                    Text(Format.bytes(band.bytes))
                        .font(.caption)
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                }
            }
        }
    }
}
