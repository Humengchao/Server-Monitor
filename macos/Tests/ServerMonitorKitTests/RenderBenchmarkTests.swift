import Charts
import Foundation
import SwiftUI
import Testing
@testable import ServerMonitorKit

/// Times one full layout-and-draw of the views that re-render on every resize
/// frame. `ImageRenderer` does the same layout pass a live resize does, so
/// this is a usable proxy for "how expensive is one frame" without a window
/// server. A resize at 60 fps has ~16 ms per frame for *everything*.
///
/// Opt-in:  SM_BENCH=1 swift test --filter RenderBenchmarkTests
@Suite("Render cost", .serialized)
@MainActor
struct RenderBenchmarkTests {

    static var enabled: Bool { ProcessInfo.processInfo.environment["SM_BENCH"] != nil }

    /// Synthetic history at the app's real poll cadence.
    static func samples(seconds: TimeInterval, every step: TimeInterval = 5) -> [MetricSample] {
        let serverID = UUID()
        let start = Date().addingTimeInterval(-seconds)
        return stride(from: 0, to: seconds, by: step).map { offset in
            var snapshot = MetricSnapshot()
            snapshot.cpuPercent = 30 + 25 * sin(offset / 600)
            snapshot.memoryUsed = Int64(8_000_000_000 + 2_000_000_000 * cos(offset / 900))
            snapshot.memoryTotal = 32_000_000_000
            snapshot.netRxRate = 20_000 + 15_000 * sin(offset / 300)
            snapshot.netTxRate = 8_000 + 6_000 * cos(offset / 450)
            snapshot.diskReadRate = 1_000_000 * abs(sin(offset / 700))
            snapshot.diskWriteRate = 2_000_000 * abs(cos(offset / 500))
            snapshot.load1 = 1.2 + sin(offset / 800)
            return MetricSample(serverID: serverID, timestamp: start.addingTimeInterval(offset), snapshot: snapshot)
        }
    }

    /// A fresh renderer at a different width each run: `ImageRenderer` caches
    /// its image, so re-reading it would measure nothing, and a resize is
    /// exactly "same content, new width" anyway.
    @discardableResult
    static func time<V: View>(_ label: String, width: CGFloat = 900, _ make: () -> V) -> Double {
        _ = ImageRenderer(content: make().frame(width: width)).nsImage   // warm up
        let runs = 4
        let started = Date()
        for run in 0..<runs {
            let renderer = ImageRenderer(content: make().frame(width: width + CGFloat(run % 2) * 40))
            renderer.scale = 2
            _ = renderer.nsImage
        }
        let milliseconds = Date().timeIntervalSince(started) / Double(runs) * 1000
        print(String(format: "  %-44@ %8.1f ms/frame", label as NSString, milliseconds))
        return milliseconds
    }

    @Test func historyChartsAtEachRange() {
        guard Self.enabled else { return }
        let localization = Localization()
        print("\n── history charts (4 charts) ──")
        for (label, seconds) in [("15m", 900.0), ("1h", 3600.0), ("6h", 21600.0), ("24h", 86400.0)] {
            let samples = Self.samples(seconds: seconds)
            Self.time("\(label): \(samples.count) raw samples") {
                HistoryCharts(samples: samples).environmentObject(localization)
            }
        }
    }

    @Test func historyChartsAfterReduction() {
        guard Self.enabled else { return }
        let localization = Localization()
        print("\n── history charts, reduced to ≤240 points ──")
        for (label, seconds) in [("6h", 21600.0), ("24h", 86400.0)] {
            let reduced = HistoryReducer.reduce(Self.samples(seconds: seconds), maxPoints: 240)
            Self.time("\(label): \(reduced.count) points") {
                HistoryCharts(samples: reduced).environmentObject(localization)
            }
        }
    }

    /// The same four charts with the vectorised plots, to see whether they are
    /// worth switching to.
    struct VectorisedCharts: View {
        let samples: [MetricSample]
        var body: some View {
            VStack(spacing: 14) {
                Chart {
                    AreaPlot(samples, x: .value("t", \.timestamp), y: .value("cpu", \.cpuPercent))
                        .foregroundStyle(.blue.opacity(0.18))
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("cpu", \.cpuPercent))
                        .foregroundStyle(.blue)
                }
                .chartYScale(domain: 0...100)
                .frame(height: 150)
                Chart {
                    AreaPlot(samples, x: .value("t", \.timestamp), y: .value("mem", \.memoryUsed))
                        .foregroundStyle(.purple.opacity(0.18))
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("mem", \.memoryUsed))
                        .foregroundStyle(.purple)
                }
                .frame(height: 150)
                Chart {
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("rx", \.netRxRate)).foregroundStyle(.green)
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("tx", \.netTxRate)).foregroundStyle(.orange)
                }
                .frame(height: 150)
                Chart {
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("r", \.diskReadRate)).foregroundStyle(.teal)
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("w", \.diskWriteRate)).foregroundStyle(.pink)
                }
                .frame(height: 150)
            }
        }
    }

    @Test func vectorisedPlotsVersusMarks() {
        guard Self.enabled else { return }
        let localization = Localization()
        print("\n── marks vs vectorised plots ──")
        for points in [120, 240, 720] {
            let reduced = HistoryReducer.reduce(Self.samples(seconds: 86400), maxPoints: points)
            Self.time("marks, \(reduced.count) points") {
                HistoryCharts(samples: reduced).environmentObject(localization)
            }
            Self.time("plots, \(reduced.count) points") {
                VectorisedCharts(samples: reduced)
            }
        }
    }

    @Test func cardGridAtTwoWidths() throws {
        guard Self.enabled else { return }
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        let snapshot = CardRenderTests.sampleSnapshot()
        let server = CardRenderTests.sampleServer
        let samples = HistoryReducer.reduce(Self.samples(seconds: 900), maxPoints: 240)

        func grid(width: CGFloat) -> some View {
            return MachineCardsLayout(
                server: server, snapshot: snapshot, samples: samples, isWindows: false, width: width
            )
                .environmentObject(localization)
                .environment(monitor)
        }

        print("\n── card grid (10 cards) ──")
        Self.time("two columns (width 900)", width: 900) { grid(width: 900) }
        Self.time("three columns (width 1100)", width: 1100) { grid(width: 1100) }
    }

    @Test func eachCardAlone() throws {
        guard Self.enabled else { return }
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        let snapshot = CardRenderTests.sampleSnapshot()
        let server = CardRenderTests.sampleServer
        let samples = HistoryReducer.reduce(Self.samples(seconds: 900), maxPoints: 240)

        func card<V: View>(_ label: String, _ make: () -> V) {
            Self.time(label, width: 420) {
                make().environmentObject(localization).environment(monitor)
            }
        }
        print("\n── each card alone (width 420) ──")
        card("CPU") { StatusCPUCard(snapshot: snapshot) }
        card("memory") { StatusMemoryCard(snapshot: snapshot) }
        card("load") { StatusLoadCard(snapshot: snapshot, samples: samples, isWindows: false) }
        card("storage") { StatusStorageCard(snapshot: snapshot) }
        card("network") { StatusNetworkCard(snapshot: snapshot) }
        card("machine") { StatusMachineCard(server: server, snapshot: snapshot) }
        card("ip") { IPLocationCard(server: server, snapshot: snapshot) }
        card("traffic") { StatusTrafficCard(server: server, preloaded: CardRenderTests.sampleTraffic()) }
        card("processes") { StatusProcessCard(snapshot: snapshot, isWindows: false) }
        card("gpu") { StatusGPUCard(status: CardRenderTests.sampleGPU()) }
        card("docker tile ×3") {
            VStack {
                ForEach(CardRenderTests.sampleContainers, id: \.0.id) { pair in
                    DockerContainerTile(container: pair.0, stats: pair.1)
                }
            }
        }
        card("empty StatusCard (chrome only)") {
            StatusCard(title: "x", systemImage: "cpu") { Text("y") }
        }
    }

    @Test func primitives() {
        guard Self.enabled else { return }
        print("\n── primitives ──")
        Self.time("RingGauge ×1", width: 100) { RingGauge(value: 62) }
        Self.time("RingGauge ×8", width: 400) {
            HStack { ForEach(0..<8, id: \.self) { _ in RingGauge(value: 62) } }
        }
        Self.time("StatusBar ×16", width: 400) {
            VStack { ForEach(0..<16, id: \.self) { i in StatusBar(fraction: Double(i) / 16) } }
        }
        Self.time("Text ×16 (Format.percent)", width: 400) {
            VStack { ForEach(0..<16, id: \.self) { i in Text(Format.percent(Double(i) * 6)) } }
        }
        Self.time("DateFormatter ×24 fresh", width: 400) {
            VStack {
                ForEach(0..<24, id: \.self) { i in
                    Text(StatusTrafficCard.label(for: Date(timeIntervalSince1970: Double(i) * 3600), granularity: .hour))
                }
            }
        }
    }

    /// Hypothesis: the cost is in flexible layout being measured several times,
    /// not in the content. Same content, three ways of laying it out.
    @Test func layoutStrategies() {
        guard Self.enabled else { return }
        let cores = (0..<16).map { CoreLoad(index: $0, percent: Double($0) * 6) }
        func cell(_ core: CoreLoad) -> some View {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 4) {
                    Text("#\(core.index)").font(.system(size: 9))
                    Spacer(minLength: 0)
                    Text(Format.percent(core.percent)).font(.system(size: 9)).monospacedDigit()
                }
                StatusBar(fraction: core.percent / 100, height: 4)
            }
        }
        print("\n── 16 core cells, three layouts (width 300) ──")
        Self.time("nested stacks, flexible cells", width: 300) {
            VStack(spacing: 7) {
                ForEach(Array(cores.chunked(into: 2).enumerated()), id: \.offset) { row in
                    HStack(spacing: 8) {
                        ForEach(row.element) { cell($0).frame(maxWidth: .infinity) }
                    }
                }
            }
        }
        Self.time("Grid / GridRow", width: 300) {
            Grid(horizontalSpacing: 8, verticalSpacing: 7) {
                ForEach(Array(cores.chunked(into: 2).enumerated()), id: \.offset) { row in
                    GridRow { ForEach(row.element) { cell($0) } }
                }
            }
        }
        Self.time("nested stacks, fixed cell width 146", width: 300) {
            VStack(spacing: 7) {
                ForEach(Array(cores.chunked(into: 2).enumerated()), id: \.offset) { row in
                    HStack(spacing: 8) {
                        ForEach(row.element) { cell($0).frame(width: 146) }
                    }
                }
            }
        }

        print("\n── StaticGrid: flexible vs fixed column width (10 plain cards, width 900) ──")
        let items: [StaticGrid<AnyView>.Item] = (0..<10).map { i in
            .init(id: "c\(i)", view: AnyView(
                StatusCard(title: "Card \(i)", systemImage: "cpu") {
                    VStack(alignment: .leading) {
                        ForEach(0..<6, id: \.self) { j in
                            HStack { Text("row \(j)"); Spacer(); Text("\(j * 17)%") }
                            StatusBar(fraction: Double(j) / 6)
                        }
                    }
                }
            ))
        }
        Self.time("StaticGrid as shipped", width: 900) { StaticGrid(items: items, availableWidth: 900) }
        Self.time("fixed-width columns", width: 900) {
            HStack(alignment: .top, spacing: 14) {
                ForEach(0..<2, id: \.self) { column in
                    VStack(spacing: 14) {
                        ForEach(items.indices.filter { $0 % 2 == column }, id: \.self) { i in items[i].view }
                    }
                    .frame(width: 443)
                }
            }
        }
    }

    @Test func suspects() {
        guard Self.enabled else { return }
        print("\n── suspects ──")
        Self.time("Button (borderless)", width: 200) {
            Button("Show all") {}.buttonStyle(.borderless)
        }
        Self.time("segmented Picker", width: 300) {
            Picker("", selection: .constant(1)) { Text("a").tag(0); Text("b").tag(1); Text("c").tag(2) }
                .pickerStyle(.segmented)
        }
        Self.time("Menu", width: 200) {
            Menu { Button("x") {} } label: { Text("eth0") }.menuStyle(.borderlessButton)
        }
        Self.time("Link", width: 200) {
            Link(destination: URL(string: "https://example.com")!) { Text("guide") }
        }
        Self.time("RingGauge with .animation", width: 100) { RingGauge(value: 62) }
        Self.time("Divider ×4", width: 300) { VStack { ForEach(0..<4, id: \.self) { _ in Divider() } } }
        Self.time("Text(\"…\") ×16 (LocalizedStringKey)", width: 300) {
            VStack { ForEach(0..<16, id: \.self) { i in Text("#\(i)") } }
        }
        Self.time("Text(verbatim:) ×16", width: 300) {
            VStack { ForEach(0..<16, id: \.self) { i in Text(verbatim: "#\(i)") } }
        }
        Self.time("CPU card, flexible cells (no grid)", width: 420) {
            StatusCPUCard(snapshot: CardRenderTests.sampleSnapshot()).environmentObject(Localization())
        }
        Self.time("CPU card, fixed cells (cardWidth=420)", width: 420) {
            StatusCPUCard(snapshot: CardRenderTests.sampleSnapshot())
                .environmentObject(Localization())
                .environment(\.cardWidth, 420)
        }
        Self.time("docker tile ×3, flexible", width: 420) {
            VStack {
                ForEach(CardRenderTests.sampleContainers, id: \.0.id) { pair in
                    DockerContainerTile(container: pair.0, stats: pair.1)
                }
            }
            .environmentObject(Localization())
        }
        Self.time("docker tile ×3, fixed (cardWidth=420)", width: 420) {
            VStack {
                ForEach(CardRenderTests.sampleContainers, id: \.0.id) { pair in
                    DockerContainerTile(container: pair.0, stats: pair.1)
                }
            }
            .environmentObject(Localization())
            .environment(\.cardWidth, 420)
        }
    }

    struct BarRow: Identifiable {
        let id: Int
        let label: String
        let direction: String
        let bytes: Int64
    }

    @Test func barMarksVersusBarPlot() {
        guard Self.enabled else { return }
        let labels = (0..<24).map { String(format: "%02d:00", $0) }
        let rows = labels.enumerated().flatMap { index, label in
            [
                BarRow(id: index * 2, label: label, direction: "rx", bytes: Int64(40_000_000 + index * 900_000)),
                BarRow(id: index * 2 + 1, label: label, direction: "tx", bytes: Int64(12_000_000 + index * 300_000)),
            ]
        }
        print("\n── 24 stacked bars × 2 series ──")
        Self.time("BarMark per row", width: 400) {
            Chart(rows) { row in
                BarMark(x: .value("t", row.label), y: .value("b", row.bytes))
                    .foregroundStyle(by: .value("d", row.direction))
            }
            .frame(height: 130)
        }
        Self.time("BarPlot, stacked", width: 400) {
            Chart {
                BarPlot(rows, x: .value("t", \.label), y: .value("b", \.bytes), stacking: .standard)
                    .foregroundStyle(by: .value("d", \.direction))
            }
            .frame(height: 130)
        }
    }

    /// The measurement that matches the complaint: an offscreen `NSHostingView`
    /// resized one step at a time, timing each incremental layout pass — which
    /// is what happens on every frame of a live drag, layout caches included.
    /// `ImageRenderer` is a cold render each time and cannot see the caches.
    @Test func incrementalResizeOfTheWholeOverview() throws {
        guard Self.enabled else { return }
        _ = NSApplication.shared
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        let snapshot = CardRenderTests.sampleSnapshot()
        let server = CardRenderTests.sampleServer
        let raw = Self.samples(seconds: 86400)
        let reduced = HistoryReducer.reduce(raw, maxPoints: 240)

        func overview(width: CGFloat, history: [MetricSample], step: CGFloat, quantiseCharts: Bool = false) -> some View {
            var items: [StaticGrid<AnyView>.Item] = [
                .init(id: "cpu", view: AnyView(StatusCPUCard(snapshot: snapshot))),
                .init(id: "memory", view: AnyView(StatusMemoryCard(snapshot: snapshot))),
                .init(id: "load", view: AnyView(StatusLoadCard(snapshot: snapshot, samples: history, isWindows: false))),
                .init(id: "storage", view: AnyView(StatusStorageCard(snapshot: snapshot))),
                .init(id: "network", view: AnyView(StatusNetworkCard(snapshot: snapshot))),
                .init(id: "machine", view: AnyView(StatusMachineCard(server: server, snapshot: snapshot))),
                .init(id: "ip", view: AnyView(IPLocationCard(server: server, snapshot: snapshot))),
                .init(id: "traffic", view: AnyView(StatusTrafficCard(server: server, preloaded: CardRenderTests.sampleTraffic()))),
                .init(id: "processes", view: AnyView(StatusProcessCard(snapshot: snapshot, isWindows: false))),
            ]
            items.append(.init(id: "gpu", view: AnyView(StatusGPUCard(status: CardRenderTests.sampleGPU()))))
            return VStack(spacing: 14) {
                StaticGrid(items: items, availableWidth: width - 32, widthStep: step)
                HistoryCharts(samples: history)
                    .frame(width: quantiseCharts ? GridLayout.quantise(width - 32, step: step) : nil)
            }
            .padding(16)
        }

        struct Root: View {
            let make: (CGFloat) -> AnyView
            var body: some View {
                GeometryReader { proxy in
                    ScrollView { make(proxy.size.width) }
                }
            }
        }

        func measure(_ label: String, history: [MetricSample], step: CGFloat, quantiseCharts: Bool = false) {
            let root = Root(make: { AnyView(overview(width: $0, history: history, step: step, quantiseCharts: quantiseCharts)) })
                .environmentObject(localization)
                .environment(monitor)
            let hosting = NSHostingView(rootView: root)
            hosting.frame = NSRect(x: 0, y: 0, width: 900, height: 900)
            hosting.layoutSubtreeIfNeeded()

            var timings: [Double] = []
            // 2pt steps: what a real drag delivers at 60 fps.
            for width in stride(from: 900, through: 1200, by: 2) {
                let started = Date()
                hosting.frame.size.width = CGFloat(width)
                hosting.layoutSubtreeIfNeeded()
                timings.append(Date().timeIntervalSince(started) * 1000)
            }
            let sorted = timings.sorted()
            print(String(
                format: "  %-46@ median %6.1f ms   p90 %6.1f ms   max %6.1f ms",
                label as NSString, sorted[sorted.count / 2], sorted[Int(Double(sorted.count) * 0.9)], sorted.last!
            ))
        }

        print("\n── live resize, 900→1200 in 2pt steps, whole overview ──")
        measure("before: raw 24h samples, 1pt columns", history: raw, step: 1)
        measure("reduced to 240, 1pt columns", history: reduced, step: 1)
        measure("reduced to 240, 16pt quantised columns", history: reduced, step: 16)
        measure("…and quantised chart width too", history: reduced, step: 16, quantiseCharts: true)
    }

    /// Not a render cost but the same complaint: a history query that runs on
    /// the main thread every 15 s is a periodic hitch, and at the 24 h range it
    /// decodes a day of polls.
    @Test func historyQueryOnTheMainThread() throws {
        guard Self.enabled else { return }
        let database = try Database(inMemory: true)
        let server = Server(name: "bench", host: "h", username: "u", authKind: .agent)
        try database.save(server)                       // samples FK onto server
        let samples = Self.samples(seconds: 86400).map {
            MetricSample(serverID: server.id, timestamp: $0.timestamp, snapshot: $0.snapshot)
        }
        let serverID = server.id
        let insertStarted = Date()
        for sample in samples { try database.insert(sample) }
        let insertMs = Date().timeIntervalSince(insertStarted) * 1000 / Double(samples.count)

        print("\n── history query (\(samples.count) rows in the table) ──")
        print(String(format: "  one insert                                   %8.3f ms", insertMs))
        for (label, seconds) in [("15m", 900.0), ("1h", 3600.0), ("6h", 21600.0), ("24h", 86400.0)] {
            let since = Date().addingTimeInterval(-seconds)
            let started = Date()
            var rows: [MetricSample] = []
            for _ in 0..<3 { rows = try database.samples(serverID: serverID, since: since) }
            let fetchMs = Date().timeIntervalSince(started) * 1000 / 3
            let reduceStarted = Date()
            for _ in 0..<3 { _ = HistoryReducer.reduce(rows, maxPoints: 240) }
            let reduceMs = Date().timeIntervalSince(reduceStarted) * 1000 / 3
            let sqlStarted = Date()
            var bucketed: [MetricSample] = []
            for _ in 0..<3 { bucketed = try database.reducedSamples(serverID: serverID, since: since, maxPoints: 240) }
            let sqlMs = Date().timeIntervalSince(sqlStarted) * 1000 / 3
            print(String(
                format: "  %-4@ fetch+decode %6d rows %7.1f ms + reduce %5.1f ms   │  SQL-side → %3d rows %6.1f ms",
                label as NSString, rows.count, fetchMs, reduceMs, bucketed.count, sqlMs
            ))
        }
    }

    /// Does a background history read still make a poll's insert wait? With a
    /// serialised queue the insert queued behind the whole read; with the pool
    /// it should not notice.
    @Test func insertDuringAHistoryRead() async throws {
        guard Self.enabled else { return }
        let url = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("sm-pool-\(UUID().uuidString).sqlite")
        defer {
            for suffix in ["", "-wal", "-shm"] {
                try? FileManager.default.removeItem(atPath: url.path + suffix)
            }
        }
        let database = try Database(url: url)
        let server = Server(name: "bench", host: "h", username: "u", authKind: .agent)
        try database.save(server)
        for sample in Self.samples(seconds: 86400) {
            try database.insert(MetricSample(serverID: server.id, timestamp: sample.timestamp, snapshot: sample.snapshot))
        }
        let since = Date().addingTimeInterval(-86400)

        // Fire the slow read, give it a moment to start, then time an insert.
        let read = Task.detached(priority: .utility) {
            let started = Date()
            _ = try? database.reducedSamples(serverID: server.id, since: since, maxPoints: 240)
            return Date().timeIntervalSince(started) * 1000
        }
        try await Task.sleep(for: .milliseconds(2))
        let insertStarted = Date()
        try database.insert(MetricSample(serverID: server.id, snapshot: MetricSnapshot()))
        let insertMs = Date().timeIntervalSince(insertStarted) * 1000
        let readMs = await read.value
        print(String(format: "\n── insert while a 24 h read runs ── read %.1f ms, insert waited %.2f ms\n", readMs, insertMs))
    }

    @Test func stringLookupsAndFormatting() {
        guard Self.enabled else { return }
        func time(_ label: String, _ block: () -> Void) {
            let started = Date()
            block()
            print(String(format: "  %-46@ %8.2f µs/call", label as NSString, Date().timeIntervalSince(started) * 1e6 / 10_000))
        }
        let system = Localization(); system.language = .system
        let fixed = Localization(); fixed.language = .zh
        print("\n── per-call cost (10,000 calls) ──")
        time("loc.t, language = .system") { for _ in 0..<10_000 { _ = system.t("metric.cpu") } }
        time("loc.t, language = .zh") { for _ in 0..<10_000 { _ = fixed.t("metric.cpu") } }
        time("Format.bytes") { for i in 0..<10_000 { _ = Format.bytes(Int64(i) * 1_000_003) } }
        time("Format.percent") { for i in 0..<10_000 { _ = Format.percent(Double(i % 100)) } }
        time("Format.rate") { for i in 0..<10_000 { _ = Format.rate(Double(i) * 137) } }
        time("Format.uptime") { for i in 0..<10_000 { _ = Format.uptime(Int64(i) * 61, chinese: true) } }
    }

    /// Every card observes the whole MonitorService, so one host's poll landing
    /// re-evaluates all N cards. This is the cost of one such publish.
    @Test func dashboardPublishCostAtScale() async throws {
        guard Self.enabled else { return }
        let localization = Localization(); localization.language = .zh
        print("\n── dashboard: one publish = one full re-evaluation of every card ──")
        for count in [1, 10, 30, 60] {
            let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
            for i in 0..<count {
                try monitor.addServer(Server(
                    name: "srv-\(i)", host: "127.0.0.1", port: 1, username: "u", authKind: .agent
                ))
            }
            // Refused connections finish in milliseconds and leave every card in
            // its offline state — the same layout weight as a live one, minus
            // the two gauges, so this slightly *under*-states the live cost.
            await monitor.pollAll()
            let ms = Self.time("\(count) cards", width: 1200) {
                DashboardView(search: .constant(""), onSelect: { _ in })
                    .environment(monitor)
                    .environmentObject(localization)
            }
            // Publishes per second at a 5 s poll: count / 5. CPU share is what
            // fraction of each second is spent re-evaluating.
            let share = ms * Double(count) / 5 / 10
            print(String(format: "     → at 5 s polling: %.0f%% of a core just re-rendering", share))
        }
    }

    /// The local cost of one poll's parse. Nine hosts at a 5 s interval is
    /// ~1.8 of these a second, on the collector.
    @Test func parseCostOfOneRealBatch() {
        guard Self.enabled else { return }
        // Shaped like a real host: 16 cores, 40 interfaces (a docker box), a
        // full ps listing, several mounts.
        let stat: String = (["cpu  100 5 50 900 20 3 2 0 0 0"] + (0..<16).map {
            "cpu\($0) \(6 + $0) 0 3 56 1 0 0 0 0 0"
        }).joined(separator: "\n")
        let meminfo: String = (0..<48).map { "Field\($0):  \(1000 + $0) kB" }.joined(separator: "\n")
            + "\nMemTotal:  32900000 kB\nMemFree:  1200000 kB\nBuffers:  1300000 kB"
            + "\nCached:  18800000 kB\nSwapTotal: 2000000 kB\nSwapFree: 1500000 kB"
        let netdev: String = "Inter-|   Receive\n face |bytes\n" + (0..<40).map {
            "  eth\($0): \(900000 + $0) 9 0 0 0 0 0 0 \(700000 + $0) 7 0 0 0 0 0 0"
        }.joined(separator: "\n")
        let diskstats: String = (0..<12).map { index in
            let name = String(UnicodeScalar(97 + index)!)
            return String("   8  \(index) sd\(name) 1 2 \(30000 + index) 4 5 6 \(40000 + index) 8 9 10 11 12 13 14")
        }.joined(separator: "\n")
        let ps: String = (0..<30).map {
            "  \(1000 + $0) root      \(Double($0) / 2)  1.\($0 % 10)  \(50000 + $0) /usr/bin/some-daemon --flag=\($0) --path /var/lib/thing"
        }.joined(separator: "\n")
        let df: String = "Filesystem 1024-blocks Used Available Capacity Mounted on\n" + (0..<6).map { index in
            let name = String(UnicodeScalar(97 + index)!)
            return String("/dev/vd\(name) 84421599232 30799843328 49318871040 39% /mnt\(index)")
        }.joined(separator: "\n")
        let hostInfo: String = ["host=web-1", "kern=6.8.0-134-generic", "arch=x86_64",
                        "os=Ubuntu 24.04.4 LTS", "ips=10.0.0.5 172.17.0.1",
                        "cpu=Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz"].joined(separator: "\n")
        let sections: [String] = [
            "1756000000000000000", stat, stat, meminfo, "1.2 2.1 1.8 1/900 12345",
            netdev, diskstats, "123456.78 987654.32", "16", df, ps, hostInfo,
            "", "29.1.3|20|8|3|0", "1756000000500000000",
        ]
        let separator: String = "\n" + ProcParsers.sectionSeparator + "\n"
        let output: String = sections.joined(separator: separator)
        print("\n-- parse of one poll's output (\(output.utf8.count / 1024) KB) --")

        func time(_ label: String, runs: Int = 200, _ block: () -> Void) {
            block()
            let started = Date()
            for _ in 0..<runs { block() }
            let ms = Date().timeIntervalSince(started) * 1000 / Double(runs)
            print(String(format: "  %-38@ %7.2f ms  -> %4.1f%% of a core at 1.8/s",
                         label as NSString, ms, ms * 1.8 / 10))
        }

        let wanted = ProcParsers.Section.allCases.count
        time("splitSections only") { _ = ProcParsers.splitSections(output, want: wanted) }
        let parts = ProcParsers.splitSections(output, want: wanted)
        func part(_ section: ProcParsers.Section) -> String { parts[section.rawValue] }
        time("cpu: percent + cores + breakdown") {
            _ = ProcParsers.cpuPercent(first: part(.statFirst), second: part(.statSecond))
            _ = ProcParsers.coreLoads(first: part(.statFirst), second: part(.statSecond))
            _ = ProcParsers.cpuBreakdown(first: part(.statFirst), second: part(.statSecond))
        }
        time("memInfo + memoryBreakdown") {
            _ = ProcParsers.memInfo(part(.memInfo))
            _ = ProcParsers.memoryBreakdown(part(.memInfo))
        }
        time("netDev + netInterfaces") {
            _ = ProcParsers.netDev(part(.netDev))
            _ = ProcParsers.netInterfaces(part(.netDev))
        }
        time("processes") { _ = ProcParsers.processes(part(.processes)) }
        time("filesystems + diskUsage") {
            _ = ProcParsers.filesystems(part(.diskUsage))
            _ = ProcParsers.diskUsage(part(.diskUsage))
        }
        time("the rest") {
            _ = ProcParsers.diskStats(part(.diskStats))
            _ = ProcParsers.loadAverage(part(.loadAvg))
            _ = ProcParsers.hostIdentity(part(.hostInfo))
            _ = ProcParsers.gpuStatus(part(.gpu))
            _ = ProcParsers.uptime(part(.uptime))
            _ = ProcParsers.cores(part(.nproc))
        }
    }

    /// Where the steady-state CPU actually goes: each poll spawns an ssh
    /// client, and each host also gets a ping every 30 s.
    @Test func subprocessSpawnCost() async throws {
        guard Self.enabled else { return }
        func time(_ label: String, runs: Int, _ block: () async throws -> Void) async rethrows {
            try await block()
            let started = Date()
            let cpuBefore = Self.processCPUSeconds()
            for _ in 0..<runs { try await block() }
            let wall = Date().timeIntervalSince(started) / Double(runs) * 1000
            let cpu = (Self.processCPUSeconds() - cpuBefore) / Double(runs) * 1000
            print(String(format: "  %-34@ wall %6.1f ms   our CPU %5.2f ms", label as NSString, wall, cpu))
        }
        print("\n-- subprocess cost (this process's own CPU, not the child's) --")
        await time("spawn /usr/bin/true", runs: 60) {
            _ = try? await SSHRunner.execute(executable: "/usr/bin/true", arguments: [], timeout: 5)
        }
        await time("spawn ssh (usage, no connect)", runs: 40) {
            _ = try? await SSHRunner.execute(executable: "/usr/bin/ssh", arguments: ["-V"], timeout: 5)
        }
        print("  → 9 hosts / 5 s = 1.8 spawns/s, plus a ping per host per 30 s")
    }

    /// This process's consumed CPU, for attributing cost to our side rather
    /// than the child's.
    static func processCPUSeconds() -> Double {
        var usage = rusage()
        guard getrusage(RUSAGE_SELF, &usage) == 0 else { return 0 }
        let user = Double(usage.ru_utime.tv_sec) + Double(usage.ru_utime.tv_usec) / 1e6
        let system = Double(usage.ru_stime.tv_sec) + Double(usage.ru_stime.tv_usec) / 1e6
        return user + system
    }

    @Test func trafficBars() throws {
        guard Self.enabled else { return }
        let localization = Localization()
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        let server = Server(name: "b", host: "h", username: "u", authKind: .agent)
        print("\n── traffic card ──")
        Self.time("24 hourly bars", width: 420) {
            StatusTrafficCard(server: server, preloaded: CardRenderTests.sampleTraffic())
                .environmentObject(localization)
                .environment(monitor)
        }
    }
}
