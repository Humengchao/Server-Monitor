import Charts
import SwiftUI

// MARK: - Storage

/// Every mount with its own bar, plus the disk I/O rates.
struct StatusStorageCard: View {
    let snapshot: MetricSnapshot
    @EnvironmentObject private var loc: Localization

    var body: some View {
        StatusCard(title: loc.t("card.storage"), systemImage: "internaldrive", tint: .orange) {
            VStack(alignment: .leading, spacing: 10) {
                if snapshot.filesystems.isEmpty {
                    Text(loc.t("card.noFilesystems"))
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                } else {
                    ForEach(snapshot.filesystems) { filesystem in
                        VStack(alignment: .leading, spacing: 3) {
                            HStack(spacing: 8) {
                                Text(filesystem.mount)
                                    .font(.caption.weight(.medium))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                Spacer(minLength: 8)
                                Text(Format.usage(used: filesystem.used, total: filesystem.total))
                                    .font(.caption)
                                    .monospacedDigit()
                                    .foregroundStyle(.secondary)
                            }
                            StatusBar(fraction: filesystem.percent / 100, height: 5)
                        }
                    }
                }
                Divider()
                HStack(spacing: 18) {
                    rate("R", snapshot.diskReadRate, .teal)
                    rate("W", snapshot.diskWriteRate, .pink)
                    Spacer(minLength: 0)
                }
            }
        }
    }

    private func rate(_ label: String, _ value: Double, _ tint: Color) -> some View {
        HStack(spacing: 5) {
            Text(label)
                .font(.system(size: 9, weight: .bold, design: .rounded))
                .foregroundStyle(tint)
            Text(Format.rate(value))
                .font(.caption)
                .monospacedDigit()
        }
    }
}

// MARK: - Network

/// Per-interface traffic. Bridges and veths are folded away by default: a
/// Docker host has dozens and they bury the NIC that matters.
struct StatusNetworkCard: View {
    let snapshot: MetricSnapshot
    @EnvironmentObject private var loc: Localization
    @State private var showAll = false

    private var primary: [NetInterface] {
        snapshot.interfaces.filter { !Self.isVirtual($0.name) }
    }

    private var shown: [NetInterface] {
        showAll ? snapshot.interfaces : (primary.isEmpty ? snapshot.interfaces : primary)
    }

    private var hiddenCount: Int { snapshot.interfaces.count - shown.count }

    var body: some View {
        StatusCard(title: loc.t("card.network"), systemImage: "network", tint: .green) {
            if hiddenCount > 0 || (showAll && snapshot.interfaces.count > primary.count) {
                Button(showAll
                       ? loc.t("common.collapse")
                       : loc.t("card.showVirtual") + " (\(hiddenCount))") {
                    showAll.toggle()
                }
                .buttonStyle(.borderless)
                .font(.caption)
            }
        } content: {
            VStack(alignment: .leading, spacing: 9) {
                if shown.isEmpty {
                    Text(loc.t("card.noInterfaces"))
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                } else {
                    ForEach(shown) { interface in
                        interfaceRow(interface)
                    }
                }
            }
        }
    }

    private func interfaceRow(_ interface: NetInterface) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 8) {
                Text(interface.name)
                    .font(.caption.weight(.medium))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 8)
                Label(Format.rate(interface.rxRate), systemImage: "arrow.down")
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundStyle(.green)
                Label(Format.rate(interface.txRate), systemImage: "arrow.up")
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundStyle(.orange)
            }
            Text(verbatim: "↓ \(Format.bytes(interface.rxTotal))   ↑ \(Format.bytes(interface.txTotal))")
                .font(.system(size: 9))
                .monospacedDigit()
                .foregroundStyle(.tertiary)
        }
    }

    /// Container and tunnel plumbing rather than a real link.
    static func isVirtual(_ name: String) -> Bool {
        let prefixes = ["veth", "br-", "docker", "virbr", "cni", "flannel", "tun", "tap", "utun", "kube"]
        return prefixes.contains { name.hasPrefix($0) }
    }
}

// MARK: - Processes

/// The `ps` rows, filterable the way SwiftServer's process card is.
struct StatusProcessCard: View {
    let snapshot: MetricSnapshot
    let isWindows: Bool
    @EnvironmentObject private var loc: Localization
    @State private var filter = ""

    /// Enough to see what is busy without turning the card into a wall. The
    /// filter is how you reach the rest — a scroll view here would sit inside
    /// the page's own scroll view and swallow the wheel.
    private static let visibleRows = 12

    private var rows: [HostProcess] {
        let query = filter.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return snapshot.processes }
        return snapshot.processes.filter {
            $0.command.lowercased().contains(query)
                || $0.user.lowercased().contains(query)
                || "\($0.pid)".contains(query)
        }
    }

    var body: some View {
        StatusCard(title: loc.t("card.processes"), systemImage: "list.bullet.rectangle", tint: .pink) {
            Text(verbatim: "\(snapshot.processes.count)")
                .font(.caption)
                .monospacedDigit()
                .foregroundStyle(.secondary)
        } content: {
            VStack(alignment: .leading, spacing: 8) {
                if snapshot.processes.isEmpty {
                    Text(loc.t("card.noProcesses"))
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                } else {
                    TextField(loc.t("card.processFilter"), text: $filter)
                        .textFieldStyle(.roundedBorder)
                        .controlSize(.small)
                    if isWindows {
                        Text(loc.t("card.processCPUSeconds"))
                            .font(.system(size: 9))
                            .foregroundStyle(.tertiary)
                    }
                    VStack(spacing: 5) {
                        ForEach(rows.prefix(Self.visibleRows), id: \.pid) { process in
                            processRow(process)
                        }
                    }
                    if rows.count > Self.visibleRows {
                        Text(loc.t("card.moreProcesses") + " \(rows.count - Self.visibleRows)")
                            .font(.system(size: 9))
                            .foregroundStyle(.tertiary)
                    }
                }
            }
        }
    }

    private func processRow(_ process: HostProcess) -> some View {
        HStack(spacing: 8) {
            // verbatim: Text("\(anInt)") goes through LocalizedStringKey and
            // groups digits, which turns pid 1234 into "1,234".
            Text(verbatim: String(process.pid))
                .font(.system(size: 10, design: .monospaced))
                .foregroundStyle(.tertiary)
                .frame(width: 46, alignment: .trailing)
            VStack(alignment: .leading, spacing: 0) {
                Text(process.command)
                    .font(.caption)
                    .lineLimit(1)
                    .truncationMode(.middle)
                if !process.user.isEmpty {
                    Text(process.user)
                        .font(.system(size: 9))
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer(minLength: 6)
            Text(isWindows ? "\(Format.load(process.cpuPercent))s" : Format.percent(process.cpuPercent))
                .font(.system(size: 10, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(.blue)
                .frame(width: 50, alignment: .trailing)
            Text(Format.bytes(process.residentBytes))
                .font(.system(size: 10, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .frame(width: 62, alignment: .trailing)
        }
    }
}

// MARK: - Docker

/// One card per running container, the way SwiftServer's machine screen shows
/// them: name, age, a CPU and a memory ring, then cumulative network and block
/// I/O. An engine-wide donut said nothing about *which* container was busy,
/// which is the only question this card is asked.
struct StatusDockerCard: View {
    let server: Server
    let snapshot: MetricSnapshot?
    @Environment(\.monitorService) private var monitor
    @EnvironmentObject private var loc: Localization
    @Environment(\.cardWidth) private var cardWidth

    @State private var containers: [DockerContainer] = []
    @State private var stats: [String: DockerContainerStats] = [:]
    @State private var loading = false
    @State private var failure: String?

    /// Containers to show without fetching — how the render check sees tiles
    /// without a Docker host.
    private let preloaded: [(DockerContainer, DockerContainerStats?)]?

    init(server: Server, snapshot: MetricSnapshot?, preloaded: [(DockerContainer, DockerContainerStats?)]? = nil) {
        self.server = server
        self.snapshot = snapshot
        self.preloaded = preloaded
        if let preloaded {
            _containers = State(initialValue: preloaded.map(\.0))
            _stats = State(initialValue: Dictionary(
                preloaded.compactMap { pair in pair.1.map { (pair.0.shortID, $0) } },
                uniquingKeysWith: { first, _ in first }
            ))
        }
    }

    private var running: [DockerContainer] { containers.filter(\.isRunning) }

    static let tileMinimumWidth: CGFloat = 250
    static let tileSpacing: CGFloat = 10

    /// How many tiles fit in a row of a card this wide (28 = card padding).
    static func tileColumns(cardWidth: CGFloat) -> Int {
        let content = cardWidth - 28
        return max(1, Int((content + tileSpacing) / (tileMinimumWidth + tileSpacing)))
    }

    var body: some View {
        StatusCard(title: "Docker", systemImage: "shippingbox", tint: .cyan) {
            HStack(spacing: 6) {
                if loading { ProgressView().controlSize(.mini) }
                Text(loc.t("docker.runningContainers"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Text(verbatim: String(running.count))
                    .font(.caption.weight(.semibold))
                    .monospacedDigit()
            }
        } content: {
            content
        }
        // `docker stats` samples twice and takes a couple of seconds, so it is
        // deliberately not part of the 3-second metrics poll; this card asks
        // for it on its own slower cadence instead.
        .task(id: server.id) { await load() }
    }

    @ViewBuilder
    private var content: some View {
        if let failure {
            // This card only appears on a host where Docker was detected, so a
            // failure here is real — and reporting it as "no containers" is the
            // one answer that sends someone looking in the wrong place.
            Label(failure, systemImage: "exclamationmark.triangle.fill")
                .font(.caption)
                .foregroundStyle(.orange)
                .textSelection(.enabled)
        } else if running.isEmpty {
            Text(loc.t(loading ? "card.awaitingData" : "docker.noContainers"))
                .font(.caption)
                .foregroundStyle(.tertiary)
        } else {
            // Fixed rows of fixed-width tiles rather than a lazy grid: this sits
            // inside the page's scroll view, where a lazy grid makes the card's
            // height depend on how far it has been scrolled, and fixed widths
            // lay out in one pass (see StaticGrid).
            tileRows
        }
    }

    @ViewBuilder
    private var tileRows: some View {
        if let cardWidth {
            let columns = Self.tileColumns(cardWidth: cardWidth)
            let tileWidth = (cardWidth - 28 - Self.tileSpacing * CGFloat(columns - 1)) / CGFloat(columns)
            VStack(spacing: Self.tileSpacing) {
                ForEach(Array(running.chunked(into: columns).enumerated()), id: \.offset) { row in
                    HStack(alignment: .top, spacing: Self.tileSpacing) {
                        ForEach(row.element) { container in
                            DockerContainerTile(
                                container: container, stats: stats[container.shortID], width: tileWidth
                            )
                        }
                        if row.element.count < columns { Spacer(minLength: 0) }
                    }
                }
            }
        } else {
            VStack(spacing: Self.tileSpacing) {
                ForEach(running) { container in
                    DockerContainerTile(container: container, stats: stats[container.shortID])
                }
            }
        }
    }

    private func load() async {
        guard preloaded == nil else { return }
        loading = true
        failure = nil
        defer { loading = false }
        containers = []
        stats = [:]
        do {
            let target = try monitor.required.target(for: server)
            containers = try await monitor.required.docker.listContainers(target: target)
            // Tolerated failing: without it the tiles still name every container
            // and simply show no figures, which beats an empty card.
            stats = (try? await monitor.required.docker.stats(target: target)) ?? [:]
        } catch {
            // Switching machines cancels this task; that is not a Docker fault
            // and must not leave the next host's card showing an error.
            if error is CancellationError { return }
            failure = error.localizedDescription
        }
    }
}

/// One running container.
struct DockerContainerTile: View {
    let container: DockerContainer
    let stats: DockerContainerStats?
    /// The tile's own width when its row fixed it; nil lets it fill its column.
    var width: CGFloat? = nil
    @EnvironmentObject private var loc: Localization

    /// Width of the I/O column when the tile's width is known: tile padding
    /// (20), two 46pt gauges and their two 10pt gaps. Fixed so the tile lays
    /// out in one pass — a host with eight running containers has eight of
    /// these, redrawn on every resize frame.
    ///
    /// Derived from the *tile* width. It used to read the card's width from the
    /// environment, which was right while a tile spanned the card; once tiles
    /// sat three to a row the I/O column was sized for the whole card, the
    /// content overflowed its 284pt frame, the background stretched across the
    /// row, and the first tile in each row was pushed out and covered.
    private var ioColumnWidth: CGFloat? {
        guard let width else { return nil }
        let remaining = width - 20 - 46 * 2 - 10 * 2
        return remaining > 80 ? remaining : nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            VStack(alignment: .leading, spacing: 1) {
                Text(container.name)
                    .font(.caption.weight(.medium))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(Self.age(container.status))
                    .font(.system(size: 9))
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
            }
            HStack(alignment: .top, spacing: 10) {
                gauge(loc.t("metric.cpu"), stats?.cpuPercent)
                gauge(loc.t("metric.memory"), stats?.memoryPercent)
                ioColumn
            }
        }
        .padding(10)
        .frame(width: width, alignment: .leading)
        .frame(maxWidth: width == nil ? .infinity : nil, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 10).fill(Color.primary.opacity(0.045))
        )
    }

    @ViewBuilder
    private var ioColumn: some View {
        let column = VStack(alignment: .leading, spacing: 5) {
            ioRow(loc.t("card.network"), "arrow.down", stats?.netRx, "arrow.up", stats?.netTx)
            ioRow("Block IO", "r.circle", stats?.blockRead, "w.circle", stats?.blockWrite)
        }
        if let ioColumnWidth {
            column.frame(width: ioColumnWidth, alignment: .leading)
        } else {
            column.frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    @ViewBuilder
    private func gauge(_ title: String, _ value: Double?) -> some View {
        VStack(spacing: 3) {
            if let value {
                RingGauge(value: value, diameter: 46, lineWidth: 5)
            } else {
                // A dimmed "0%" reads as an idle container rather than one the
                // engine could not sample.
                ZStack {
                    Circle()
                        .stroke(Color.primary.opacity(0.10), lineWidth: 5)
                        .frame(width: 46, height: 46)
                    Text("—").font(.system(size: 13)).foregroundStyle(.tertiary)
                }
            }
            Text(title)
                .font(.system(size: 9))
                .foregroundStyle(.secondary)
        }
    }

    private func ioRow(
        _ title: String,
        _ firstSymbol: String, _ first: String?,
        _ secondSymbol: String, _ second: String?
    ) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(title).font(.system(size: 9)).foregroundStyle(.tertiary)
            HStack(spacing: 8) {
                figure(firstSymbol, first)
                figure(secondSymbol, second)
            }
        }
    }

    private func figure(_ symbol: String, _ value: String?) -> some View {
        HStack(spacing: 2) {
            Image(systemName: symbol).font(.system(size: 8)).foregroundStyle(.secondary)
            Text(value.map { $0.isEmpty ? "—" : $0 } ?? "—")
                .font(.system(size: 10, design: .rounded))
                .monospacedDigit()
                .lineLimit(1)
        }
    }

    /// `docker ps` prints "Up 2 weeks (healthy)"; the card wants the age and
    /// the health, not the word "Up".
    static func age(_ status: String) -> String {
        var text = status.trimmingCharacters(in: .whitespaces)
        if text.hasPrefix("Up ") { text = String(text.dropFirst(3)) }
        return text
    }
}

// MARK: - GPU

/// NVIDIA cards, when the host has any. Left out entirely otherwise — an empty
/// GPU card on the many machines without one is just noise.
struct StatusGPUCard: View {
    let status: GPUStatus
    @EnvironmentObject private var loc: Localization

    var body: some View {
        StatusCard(title: "GPU", systemImage: "cpu.fill", tint: .mint) {
            if !status.driverVersion.isEmpty {
                Text(verbatim: driverLine)
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
        } content: {
            VStack(alignment: .leading, spacing: 10) {
                ForEach(status.gpus) { gpu in
                    gpuRow(gpu)
                }
                if !status.processes.isEmpty {
                    Divider()
                    ForEach(status.processes.prefix(5)) { process in
                        HStack(spacing: 8) {
                            Text(verbatim: String(process.pid))
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundStyle(.tertiary)
                            Text(process.name)
                                .font(.caption)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer(minLength: 6)
                            Text(Format.bytes(process.memoryUsed))
                                .font(.system(size: 10, design: .rounded))
                                .monospacedDigit()
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
    }

    private var driverLine: String {
        status.cudaVersion.isEmpty
            ? status.driverVersion
            : "\(status.driverVersion) · CUDA \(status.cudaVersion)"
    }

    private func gpuRow(_ gpu: GPUInfo) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 6) {
                Text(verbatim: "#\(gpu.index)")
                    .font(.system(size: 9, design: .rounded))
                    .foregroundStyle(.tertiary)
                Text(gpu.name).font(.caption.weight(.medium)).lineLimit(1)
                Spacer(minLength: 6)
                // Only what this card actually reports: a fanless datacentre
                // part showing "0 RPM" would be a lie, not a reading.
                ForEach(readings(gpu), id: \.0) { reading in
                    Text(verbatim: reading.1)
                        .font(.system(size: 10, design: .rounded))
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                }
            }
            HStack(spacing: 10) {
                labelledBar(loc.t("gpu.utilization"), gpu.utilizationPercent)
                labelledBar(
                    Format.usage(used: gpu.memoryUsed, total: gpu.memoryTotal),
                    gpu.memoryPercent
                )
            }
        }
    }

    private func readings(_ gpu: GPUInfo) -> [(String, String)] {
        var out: [(String, String)] = []
        if let temperature = gpu.temperatureC { out.append(("t", "\(Int(temperature))°C")) }
        if let fan = gpu.fanPercent { out.append(("f", "\(Int(fan))%")) }
        if let draw = gpu.powerDrawW {
            let limit = gpu.powerLimitW.map { " / \(Int($0))W" } ?? "W"
            out.append(("p", "\(Int(draw))\(limit)"))
        }
        return out
    }

    private func labelledBar(_ caption: String, _ percent: Double) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 4) {
                Text(caption).font(.system(size: 9)).foregroundStyle(.secondary)
                Spacer(minLength: 4)
                Text(Format.percent(percent))
                    .font(.system(size: 9, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            StatusBar(fraction: percent / 100, height: 5)
        }
        .frame(maxWidth: .infinity)
    }
}
