import Foundation
import SwiftUI
import Testing
@testable import ServerMonitorKit

/// Renders the machine screen's cards to a PNG so their layout can actually be
/// looked at. `ImageRenderer` needs no window server, which is what makes this
/// usable where a screenshot is not.
///
/// It only reaches views SwiftUI draws itself. Anything AppKit-backed — `Table`,
/// `Button`, `TextField` — comes out as a flat yellow placeholder, so the Docker
/// listings cannot be checked this way and are covered by `LiveDockerTests`
/// against a real engine instead.
///
/// Opt-in:  SM_RENDER_CARDS=/tmp/cards.png swift test --filter rendersTheMachineScreen
@Suite("Card rendering")
@MainActor
struct CardRenderTests {

    @Test func rendersDockerTilesAlone() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_RENDER_DOCKER"] else { return }
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        var server = Self.sampleServer
        server.dockerVersion = "29.1.3"
        let all = Self.sampleContainers + Self.moreContainers
        print("preloaded ids:", all.map { String($0.0.id.prefix(1)) })
        let view = VStack(spacing: 20) {
            Text(verbatim: "cardWidth = 900 (3 per row)").font(.caption)
            StatusDockerCard(server: server, snapshot: nil, preloaded: all)
                .frame(width: 900)
                .environment(\.cardWidth, 900)
            Text(verbatim: "cardWidth = nil (fallback column)").font(.caption)
            StatusDockerCard(server: server, snapshot: nil, preloaded: all)
                .frame(width: 400)
        }
        .padding(16)
        .background(Color(nsColor: .windowBackgroundColor))
        .environmentObject(localization)
        .environmentObject(monitor)
        let renderer = ImageRenderer(content: view)
        renderer.scale = 1
        let image = try #require(renderer.nsImage)
        try #require(Self.png(from: image)).write(to: URL(fileURLWithPath: path))
        print("rendered docker \(image.size.width)x\(image.size.height) -> \(path)")
    }

    @Test func rendersTheWholeLayout() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_RENDER_LAYOUT"] else { return }
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        var server = Self.sampleServer
        server.dockerVersion = "29.1.3"
        let view = MachineCardsLayout(
            server: server, snapshot: Self.sampleSnapshot(),
            samples: [], isWindows: false, width: 900,
            previewContainers: Self.sampleContainers + Self.moreContainers
        )
        .padding(16)
        .frame(width: 932)
        .background(Color(nsColor: .windowBackgroundColor))
        .environmentObject(localization)
        .environmentObject(monitor)
        let renderer = ImageRenderer(content: view)
        renderer.scale = 1
        let image = try #require(renderer.nsImage)
        try #require(Self.png(from: image)).write(to: URL(fileURLWithPath: path))
        print("rendered layout \(image.size.width)x\(image.size.height) -> \(path)")
    }

    @Test func rendersTheMachineScreen() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_RENDER_CARDS"] else { return }

        let snapshot = Self.sampleSnapshot()
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())

        let view = VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .top, spacing: 14) {
                StatusCPUCard(snapshot: snapshot).frame(width: 360)
                StatusMemoryCard(snapshot: snapshot).frame(width: 360)
            }
            HStack(alignment: .top, spacing: 14) {
                StatusStorageCard(snapshot: snapshot).frame(width: 360)
                StatusNetworkCard(snapshot: snapshot).frame(width: 360)
            }
            HStack(alignment: .top, spacing: 14) {
                StatusMachineCard(server: Self.sampleServer, snapshot: snapshot).frame(width: 360)
                IPLocationCard(server: Self.locatedServer, snapshot: snapshot)
                    .frame(width: 360)
                    .environmentObject(monitor)
            }
            StatusGPUCard(status: Self.sampleGPU()).frame(width: 480)
            // The traffic card in all three of its states.
            HStack(alignment: .top, spacing: 14) {
                StatusTrafficCard(server: Self.sampleServer, preloaded: .notInstalled)
                    .frame(width: 360)
                    .environmentObject(monitor)
                StatusTrafficCard(server: Self.sampleServer, preloaded: Self.sampleTraffic())
                    .frame(width: 400)
                    .environmentObject(monitor)
            }
            // The per-container tiles, which is what the Docker card is made of.
            HStack(alignment: .top, spacing: 10) {
                ForEach(Self.sampleContainers, id: \.0.id) { pair in
                    DockerContainerTile(container: pair.0, stats: pair.1)
                        .frame(width: 250)
                }
            }
        }
        .padding(20)
        .background(Color(nsColor: .windowBackgroundColor))
        .environmentObject(localization)
        .environmentObject(monitor)

        let renderer = ImageRenderer(content: view)
        renderer.scale = 2
        let image = try #require(renderer.nsImage, "ImageRenderer produced nothing")
        let data = try #require(Self.png(from: image))
        try data.write(to: URL(fileURLWithPath: path))
        print("rendered \(image.size.width)x\(image.size.height) -> \(path)")
    }

    /// The grid as the machine screen composes it, at a width that leaves the
    /// quantised columns with slack to spread — checks margins and gaps look
    /// deliberate rather than off-by-a-few-pixels.
    @Test func rendersTheQuantisedGrid() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_RENDER_GRID"] else { return }
        let localization = Localization()
        localization.language = .zh
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())
        let snapshot = Self.sampleSnapshot()
        let items: [StaticGrid<AnyView>.Item] = [
            .init(id: "cpu", view: AnyView(StatusCPUCard(snapshot: snapshot))),
            .init(id: "memory", view: AnyView(StatusMemoryCard(snapshot: snapshot))),
            .init(id: "storage", view: AnyView(StatusStorageCard(snapshot: snapshot))),
            .init(id: "network", view: AnyView(StatusNetworkCard(snapshot: snapshot))),
        ]
        // 750 − 32 = 718: two columns of 352 exact → quantised 352 (a multiple
        // of 16 already)… so use 762 → 730 → 358 exact → 352, slack 6 → 2pt per slot.
        let view = StaticGrid(items: items, availableWidth: 730)
            .padding(16)
            .frame(width: 762)
            .background(Color(nsColor: .windowBackgroundColor))
            .environmentObject(localization)
            .environmentObject(monitor)
        let renderer = ImageRenderer(content: view)
        renderer.scale = 2
        let image = try #require(renderer.nsImage)
        try #require(Self.png(from: image)).write(to: URL(fileURLWithPath: path))
        print("rendered grid \(image.size) -> \(path)")
    }

    static func png(from image: NSImage) -> Data? {
        guard let tiff = image.tiffRepresentation,
              let bitmap = NSBitmapImageRep(data: tiff)
        else { return nil }
        return bitmap.representation(using: .png, properties: [:])
    }

    static let sampleServer: Server = {
        var server = Server(
            name: "km", host: "192.168.9.132", username: "root", authKind: .agent
        )
        server.dockerVersion = "29.1.3"
        return server
    }()

    static func sampleTraffic() -> VnstatParser.Outcome {
        // 24 hourly buckets with a daytime bump, so the chart has a shape.
        let hours = (0..<24).map { hour -> String in
            let load = hour >= 9 && hour <= 18 ? 4.0 : 1.0
            let rx = Int64(38_000_000 * load) + Int64(hour * 700_000)
            let tx = Int64(12_000_000 * load) + Int64(hour * 300_000)
            return """
            {"id":\(hour + 1),"date":{"year":2026,"month":8,"day":26},"time":{"hour":\(hour),"minute":0},"rx":\(rx),"tx":\(tx)}
            """
        }.joined(separator: ",")
        let json = """
        {"vnstatversion":"2.10","jsonversion":"2","interfaces":[{"name":"eth0","alias":"",
         "traffic":{"total":{"rx":918000000000,"tx":421000000000},"fiveminute":[],
          "hour":[\(hours)],"day":[],"month":[],"year":[],"top":[]}}]}
        """
        return VnstatParser.parse(json)!
    }

    static func sampleGPU() -> GPUStatus {
        ProcParsers.gpuStatus("""
        driver=550.90.07
        cuda=12.4
        gpu=0, NVIDIA A100-SXM4-40GB, 87, 40960, 32768, 61, [N/A], 245.31, 400.00
        gpu=1, NVIDIA GeForce RTX 4090, 12, 24564, 1024, 44, 31, 55.12, 450.00
        proc=3241, /usr/bin/python3, 30720
        """)
    }

    /// A host reached over a routable address, with its country already known.
    static let locatedServer: Server = {
        var server = Server(
            name: "hk-1", host: "8.153.77.5", username: "root", authKind: .agent
        )
        server.countryCode = "CN"
        return server
    }()

    /// Shaped after what SwiftServer shows: a busy container, an idle one, and
    /// one the engine could not sample.
    static let sampleContainers: [(DockerContainer, DockerContainerStats?)] = [
        (
            DockerContainer(
                id: String(repeating: "a", count: 64), name: "uos_instance_0",
                image: "uos", state: "running", status: "Up 3 months"
            ),
            DockerContainerStats(
                cpuPercent: 54.31, memoryPercent: 80.93, memoryUsage: "1.6GiB / 2GiB",
                netRx: "18.5GB", netTx: "53.3GB", blockRead: "17.4TB", blockWrite: "75.9GB"
            )
        ),
        (
            DockerContainer(
                id: String(repeating: "b", count: 64),
                name: "postgresql_iinc-postgresql_iinc-1",
                image: "postgres:16", state: "running", status: "Up 2 weeks (healthy)"
            ),
            DockerContainerStats(
                cpuPercent: 0.3, memoryPercent: 2.56, memoryUsage: "52MiB / 2GiB",
                netRx: "22.6GB", netTx: "6.63GB", blockRead: "17.5GB", blockWrite: "104GB"
            )
        ),
        (
            DockerContainer(
                id: String(repeating: "c", count: 64), name: "meilisearch",
                image: "getmeili/meilisearch", state: "running", status: "Up 4 months"
            ),
            nil
        ),
    ]

    /// Two more, with their own ids: `ForEach` over duplicate ids drops rows.
    static let moreContainers: [(DockerContainer, DockerContainerStats?)] = [
        (
            DockerContainer(
                id: String(repeating: "d", count: 64), name: "wowhub-db",
                image: "postgres:16", state: "running", status: "Up 5 days"
            ),
            DockerContainerStats(
                cpuPercent: 0.0, memoryPercent: 7.5, memoryUsage: "150MiB / 2GiB",
                netRx: "2.66GB", netTx: "7.89GB", blockRead: "5.62GB", blockWrite: "149GB"
            )
        ),
        (
            DockerContainer(
                id: String(repeating: "e", count: 64), name: "stock-a-backend-1",
                image: "stock-a-backend", state: "running", status: "Up 3 days"
            ),
            DockerContainerStats(
                cpuPercent: 0.2, memoryPercent: 0.4, memoryUsage: "8MiB / 2GiB",
                netRx: "92.2MB", netTx: "67.1MB", blockRead: "102MB", blockWrite: "668MB"
            )
        ),
    ]

    static func emptyEngineSnapshot() -> MetricSnapshot {
        var snapshot = MetricSnapshot()
        snapshot.dockerVersion = "27.0.3"
        snapshot.dockerSummary = DockerSummary(
            engineVersion: "27.0.3", images: 0, running: 0, stopped: 0
        )
        return snapshot
    }

    static func sampleSnapshot() -> MetricSnapshot {
        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = 37.5
        snapshot.cores = 16
        snapshot.coreLoads = (0..<16).map {
            CoreLoad(index: $0, percent: [4.0, 92, 18, 61, 7, 33, 88, 12, 5, 47, 2, 71, 9, 25, 3, 55][$0])
        }
        var breakdown = CPUBreakdown()
        breakdown.user = 28.4
        breakdown.system = 6.1
        breakdown.nice = 0
        breakdown.iowait = 2.4
        breakdown.steal = 0.6
        snapshot.cpuBreakdown = breakdown
        snapshot.load1 = 1.82
        snapshot.load5 = 2.14
        snapshot.load15 = 1.05
        var memory = MemoryBreakdown()
        memory.total = 33_700_000_000
        memory.free = 1_200_000_000
        memory.buffers = 1_300_000_000
        memory.cached = 18_800_000_000
        memory.swapTotal = 2_000_000_000
        memory.swapUsed = 460_000_000
        snapshot.memory = memory
        snapshot.memoryTotal = memory.total
        snapshot.memoryUsed = memory.used
        snapshot.filesystems = [
            FilesystemUsage(mount: "/", device: "/dev/vda2", used: 30_799_843_328, total: 84_421_599_232),
            FilesystemUsage(mount: "/data", device: "/dev/vdb1", used: 52_343_545_856, total: 210_301_161_472),
        ]
        snapshot.diskUsed = 30_799_843_328
        snapshot.diskTotal = 84_421_599_232
        snapshot.diskReadRate = 1_240_000
        snapshot.diskWriteRate = 3_800_000
        snapshot.interfaces = [
            NetInterface(name: "eth0", rxTotal: 918_000_000_000, txTotal: 421_000_000_000, rxRate: 15_600, txRate: 36_100),
            NetInterface(name: "docker0", rxTotal: 4_100_000, txTotal: 9_200_000, rxRate: 0, txRate: 0),
        ]
        snapshot.processes = [
            HostProcess(pid: 1234, user: "root", cpuPercent: 12.5, memPercent: 3.2, residentBytes: 524_288_000, command: "/usr/bin/meilisearch --db-path /data/ms"),
            HostProcess(pid: 5678, user: "www-data", cpuPercent: 4.5, memPercent: 1.1, residentBytes: 20_971_520, command: "nginx: worker process"),
            HostProcess(pid: 9012, user: "postgres", cpuPercent: 2.1, memPercent: 8.4, residentBytes: 1_073_741_824, command: "postgres: checkpointer"),
            HostProcess(pid: 3456, user: "root", cpuPercent: 0.7, memPercent: 0.3, residentBytes: 8_388_608, command: "sshd: root@notty"),
        ]
        var identity = HostIdentity()
        identity.hostname = "km"
        identity.osName = "Ubuntu 24.04.4 LTS"
        identity.kernel = "6.8.0-134-generic"
        identity.architecture = "x86_64"
        identity.cpuModel = "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz"
        identity.addresses = ["192.168.9.132", "172.17.0.1"]
        snapshot.identity = identity
        snapshot.dockerVersion = "29.1.3"
        snapshot.dockerSummary = DockerSummary(
            engineVersion: "29.1.3", images: 14, running: 5, stopped: 3, paused: 1
        )
        return snapshot
    }
}

/// A host that has never connected, next to one that has — the difference the
/// em dash is there to make visible.
/// Opt-in: SM_RENDER_FACTS=/tmp/facts.png swift test --filter rendersFactsForAnUnreachedHost
@Suite("Facts row rendering")
@MainActor
struct FactsRowRenderTests {

    @Test func rendersFactsForAnUnreachedHost() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_RENDER_FACTS"] else { return }
        let monitor = MonitorService(database: try Database(inMemory: true), settings: AppSettings())

        func row(_ label: String, _ language: AppLanguage, connected: Bool) -> some View {
            let localization = Localization()
            localization.language = language
            var server = Server(name: "host", host: "h", username: "u", authKind: .agent)
            if connected {
                server.cores = 8
                server.memoryTotal = 16 * 1024 * 1024 * 1024
                server.diskTotal = 512 * 1024 * 1024 * 1024
            }
            return VStack(alignment: .leading, spacing: 4) {
                Text(verbatim: label).font(.caption2).foregroundStyle(.secondary)
                ServerFactsRow(server: server)
                    .environmentObject(localization)
                    .environmentObject(monitor)
            }
        }

        let view = VStack(alignment: .leading, spacing: 16) {
            row("zh · polled", .zh, connected: true)
            row("zh · never reached", .zh, connected: false)
            row("en · polled", .en, connected: true)
            row("en · never reached", .en, connected: false)
        }
        .padding(16)
        .frame(width: 420, alignment: .leading)
        .background(Color(nsColor: .windowBackgroundColor))

        let renderer = ImageRenderer(content: view)
        renderer.scale = 2
        let image = try #require(renderer.nsImage)
        try #require(CardRenderTests.png(from: image)).write(to: URL(fileURLWithPath: path))
        print("wrote", path)
    }
}
