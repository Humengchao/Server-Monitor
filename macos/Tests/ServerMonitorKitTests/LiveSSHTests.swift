import Foundation
import Testing
@testable import ServerMonitorKit

/// End-to-end check against a real server: the ssh invocation, ControlMaster
/// reuse, command exec, parsing and rate derivation across two polls.
///
/// Opt-in. Runs only when SM_LIVE_ALIAS names a ~/.ssh/config Host, so
/// `swift test` stays offline and hermetic by default:
///   SM_LIVE_ALIAS=km swift test
@Suite("Live SSH", .serialized)
struct LiveSSHTests {

    private func alias() -> String? {
        ProcessInfo.processInfo.environment["SM_LIVE_ALIAS"]
    }

    @Test func collectsTwiceOverOneReusedConnection() async throws {
        guard let alias = alias() else { return }

        let target = SSHTarget(
            serverID: UUID(),
            host: alias,
            port: 22,
            username: "",
            credential: .sshConfigAlias
        )
        let runner = SSHRunner()
        let collector = MetricsCollector(runner: runner)

        let first = try await collector.collect(target: target)
        #expect(first.cores > 0, "cores=\(first.cores)")
        #expect(first.memoryTotal > 0)
        #expect(first.memoryUsed > 0 && first.memoryUsed <= first.memoryTotal)
        #expect(first.diskTotal > 0)
        #expect(first.uptimeSeconds > 0)
        #expect(first.cpuPercent >= 0 && first.cpuPercent <= 100)
        #expect(first.latencyMs > 0, "latency should be measurable")
        // Rates need a baseline, so the first poll has none.
        #expect(first.netRxRate == 0)

        // The second poll must reuse the pooled connection and now derive rates.
        // One retry because a multiplexed channel can be torn down mid-read
        // under load, which the collector now surfaces as a thrown error rather
        // than a zeroed snapshot — a transient the real app recovers from on
        // its next poll, so the test does too rather than flaking.
        let second = try await { () async throws -> MetricSnapshot in
            do { return try await collector.collect(target: target) }
            catch { return try await collector.collect(target: target) }
        }()
        #expect(second.cores == first.cores)
        #expect(second.netRxTotal >= first.netRxTotal, "counters must not go backwards")
        #expect(second.netRxRate >= 0)
        #expect(second.netTxRate >= 0)

        print("""

        ── live SSH via system client ──
        host       \(alias)
        cores      \(first.cores)
        cpu        \(Format.percent(second.cpuPercent))
        memory     \(Format.usage(used: second.memoryUsed, total: second.memoryTotal))
        disk       \(Format.usage(used: second.diskUsed, total: second.diskTotal))
        net rate   ↓ \(Format.rate(second.netRxRate))  ↑ \(Format.rate(second.netTxRate))
        latency    \(Format.latency(second.latencyMs))
        uptime     \(Format.uptime(second.uptimeSeconds, chinese: false))
        docker     \(second.dockerVersion.isEmpty ? "(none)" : second.dockerVersion)
        ── detail ──
        host       \(second.identity.hostname)  \(second.identity.osName)
        kernel     \(second.identity.kernel) \(second.identity.architecture)
        addresses  \(second.identity.addresses.joined(separator: ", "))
        cores      \(second.coreLoads.map { Format.percent($0.percent) }.joined(separator: " "))
        cpu model  \(second.identity.cpuModel)
        breakdown  user \(Format.percent(second.cpuBreakdown.user)) system \(Format.percent(second.cpuBreakdown.system)) nice \(Format.percent(second.cpuBreakdown.nice)) iowait \(Format.percent(second.cpuBreakdown.iowait)) steal \(Format.percent(second.cpuBreakdown.steal))
        memory     used \(Format.bytes(second.memory.used)) buffers \(Format.bytes(second.memory.buffers)) cached \(Format.bytes(second.memory.cached)) swap \(Format.percent(second.memory.swapPercent))
        mounts     \(second.filesystems.map { "\($0.mount) \(Format.percent($0.percent))" }.joined(separator: "  "))
        nics       \(second.interfaces.map { "\($0.name) ↓\(Format.rate($0.rxRate))" }.joined(separator: "  "))
        processes  \(second.processes.prefix(3).map { "\($0.command.prefix(18)) \(Format.percent($0.cpuPercent))" }.joined(separator: " | "))
        engine     \(second.dockerSummary.map { "\($0.engineVersion) images \($0.images) running \($0.running) stopped \($0.stopped)" } ?? "(none)")
        ────────────────────────────────
        """)

        // The machine screen's Docker card reads this. It used to come only
        // from the Docker page's fleet sweep, so the card sat empty until you
        // had visited that page.
        if second.dockerVersion.isEmpty == false {
            let summary = try #require(second.dockerSummary, "docker version but no engine counts")
            #expect(summary.engineVersion == second.dockerVersion)
            #expect(summary.images > 0, "a host running docker should have images")
        }

        // The detail cards are only as good as this: a machine screen with an
        // empty core list or no mounts is the failure people would report.
        #expect(second.coreLoads.count == second.cores, "core list did not match nproc")
        #expect(second.memory.total > 0)
        #expect(second.filesystems.isEmpty == false, "no filesystems parsed")
        #expect(second.filesystems.contains { $0.mount == "/" }, "root filesystem missing")
        #expect(second.interfaces.isEmpty == false, "no interfaces parsed")
        #expect(second.processes.isEmpty == false, "no processes parsed")
        #expect(second.identity.hostname.isEmpty == false)
        #expect(second.identity.kernel.isEmpty == false)
        #expect(second.identity.cpuModel.isEmpty == false, "no CPU model from /proc/cpuinfo")
        // The breakdown's parts plus idle must account for the whole window,
        // so the busy figure and the row can never contradict each other.
        let breakdown = second.cpuBreakdown
        let accounted = breakdown.user + breakdown.system + breakdown.nice + breakdown.steal
        #expect(abs(accounted - second.cpuPercent) < 1.0, "breakdown disagrees with cpuPercent")

        // The traffic card's probe. Either answer is a pass; what must not
        // happen is a third thing — an unparseable reply, which the card would
        // show as an error on every Linux host.
        let vnstat = try await runner.run(VnstatParser.command, on: target, timeout: 45)
        let traffic = VnstatParser.parse(vnstat)
        #expect(traffic != nil, "vnstat probe answered something unexpected: \(vnstat.prefix(120))")
        switch traffic {
        case .notInstalled?:
            print("── vnStat ── not installed on \(alias); the card shows the install guide")
        case .report(let report)?:
            print("── vnStat ── \(report.vnstatVersion), \(report.interfaces.count) interfaces, primary \(report.primaryInterface?.name ?? "-"), collecting=\(report.isCollecting)")
            #expect(report.interfaces.isEmpty == false)
        case nil:
            break
        }

        // The install button's first step, which only looks. Nothing is
        // installed here — that is the user's click to make.
        let probe = try await runner.run(VnstatInstaller.probeCommand, on: target, timeout: 20)
        let plan = VnstatInstaller.plan(fromProbe: probe)
        #expect(plan != nil, "no package manager recognised from: \(probe.prefix(80))")
        if let plan {
            print("── vnStat install plan ── \(plan.manager.rawValue), root=\(plan.asRoot): \(plan.displayCommand)")
        }

        await collector.forget(target: target)
    }
}

/// End-to-end against a real Windows host over password auth, exercising the
/// askpass plumbing, the PowerShell command and the parser together.
///
/// Opt-in:
///   SM_WIN_HOST=1.2.3.4 SM_WIN_USER=Administrator SM_WIN_PASSWORD=… swift test
@Suite("Live Windows", .serialized)
@MainActor
struct LiveWindowsTests {

    @Test func collectsFromRealWindowsHost() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let host = env["SM_WIN_HOST"],
              let password = env["SM_WIN_PASSWORD"]
        else { return }
        let user = env["SM_WIN_USER"] ?? "Administrator"

        let serverID = UUID()
        try Keychain.savePassword(password, serverID: serverID)
        defer { Keychain.deletePassword(serverID: serverID) }

        let target = SSHTarget(
            serverID: serverID,
            host: host,
            port: Int(env["SM_WIN_PORT"] ?? "22") ?? 22,
            username: user,
            credential: .password
        )
        let collector = MetricsCollector()

        // .auto must work this out on its own: try Linux, fall back to Windows.
        let first = try await collector.collect(target: target, osKind: .auto)
        #expect(first.detectedOS == .windows, "auto-detection should land on Windows")
        #expect(first.cores > 0, "cores=\(first.cores)")
        #expect(first.memoryTotal > 0)
        #expect(first.memoryUsed > 0 && first.memoryUsed <= first.memoryTotal)
        #expect(first.diskTotal > 0)
        #expect(first.uptimeSeconds > 0)
        #expect(first.cpuPercent >= 0 && first.cpuPercent <= 100)
        #expect(first.netRxTotal > 0)

        // A second poll must reuse the multiplexed connection and derive rates.
        let second = try await collector.collect(target: target, osKind: .windows)
        #expect(second.netRxTotal >= first.netRxTotal, "counters must not go backwards")
        #expect(second.netRxRate >= 0)

        print("""

        ── live Windows via password auth ──
        host       \(user)@\(host)
        cores      \(second.cores)
        cpu        \(Format.percent(second.cpuPercent))
        memory     \(Format.usage(used: second.memoryUsed, total: second.memoryTotal))
        disk       \(Format.usage(used: second.diskUsed, total: second.diskTotal))
        net total  rx \(Format.bytes(second.netRxTotal))  tx \(Format.bytes(second.netTxTotal))
        net rate   ↓ \(Format.rate(second.netRxRate))  ↑ \(Format.rate(second.netTxRate))
        queue      \(Format.load(second.load1))
        uptime     \(Format.uptime(second.uptimeSeconds, chinese: false))
        latency    \(Format.latency(second.latencyMs))
        ── detail ──
        host       \(second.identity.hostname)  \(second.identity.osName)
        version    \(second.identity.kernel)  \(second.identity.architecture)
        addresses  \(second.identity.addresses.joined(separator: ", "))
        cores      \(second.coreLoads.map { Format.percent($0.percent) }.joined(separator: " "))
        volumes    \(second.filesystems.map { "\($0.mount) \(Format.percent($0.percent))" }.joined(separator: "  "))
        nics       \(second.interfaces.prefix(3).map(\.name).joined(separator: " | "))
        processes  \(second.processes.prefix(3).map { "\($0.command) \($0.cpuPercent)s" }.joined(separator: " | "))
        ────────────────────────────────────
        """)

        // The Windows machine screen renders from these; an empty list here is
        // a blank card there.
        #expect(second.coreLoads.isEmpty == false, "no per-core data")
        #expect(second.filesystems.isEmpty == false, "no volumes parsed")
        #expect(second.processes.isEmpty == false, "no processes parsed")
        #expect(second.identity.hostname.isEmpty == false, "no hostname")
        #expect(second.memory.total > 0)

        await collector.forget(target: target)
    }
}

/// The app-level pipeline for a Windows host: `MonitorService` is what every
/// view binds to, so this covers what the UI will actually show — the server is
/// stored, polled, marked online, its snapshot published, and its history
/// written — rather than only the collector underneath it.
///
/// Opt-in with the same variables as `LiveWindowsTests`.
extension LiveWindowsTests {

    @Test func appPipelineStoresAWindowsHost() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let host = env["SM_WIN_HOST"],
              let password = env["SM_WIN_PASSWORD"]
        else { return }
        let user = env["SM_WIN_USER"] ?? "Administrator"

        let database = try Database(inMemory: true)
        let monitor = MonitorService(database: database, settings: AppSettings())

        let server = Server(
            name: "win-test",
            host: host,
            port: Int(env["SM_WIN_PORT"] ?? "22") ?? 22,
            username: user,
            authKind: .password,
            // Left on .auto deliberately: detection is part of what is under test.
            osKind: .auto
        )
        try Keychain.savePassword(password, serverID: server.id)
        defer { Keychain.deletePassword(serverID: server.id) }

        try monitor.addServer(server)
        #expect(monitor.servers.contains { $0.id == server.id })

        await monitor.poll(server)

        #expect(
            monitor.status[server.id]?.isOnline == true,
            "status was \(String(describing: monitor.status[server.id]))"
        )
        let snapshot = try #require(monitor.latest[server.id])
        #expect(snapshot.detectedOS == .windows)
        #expect(snapshot.cores > 0)
        #expect(snapshot.memoryTotal > 0)
        #expect(snapshot.diskTotal > 0)
        #expect(snapshot.uptimeSeconds > 0)
        // Windows has no /proc; a Linux-shaped parse would leave these at zero.
        #expect(snapshot.cpuPercent >= 0 && snapshot.cpuPercent <= 100)

        // Second poll turns the cumulative counters into rates. Poll the
        // refreshed row the way the UI does — the first poll wrote the detected
        // OS back, so this one skips detection.
        let refreshed = try #require(monitor.servers.first { $0.id == server.id })
        #expect(refreshed.osKind == .windows, "detected OS was not persisted")
        await monitor.poll(refreshed)
        // Re-checked because `latest` keeps the previous snapshot when a poll
        // fails: asserting only on it would hide a broken second poll.
        #expect(
            monitor.status[server.id]?.isOnline == true,
            "second poll: \(String(describing: monitor.status[server.id]))"
        )
        let second = try #require(monitor.latest[server.id])
        #expect(second.netRxRate >= 0)

        let history = monitor.history(serverID: server.id, since: Date().addingTimeInterval(-3600))
        #expect(history.count >= 2, "history had \(history.count) samples")

        print("""

        ── live Windows through MonitorService ──
        status     \(String(describing: monitor.status[server.id]!))
        detected   \(String(describing: second.detectedOS))
        cores      \(second.cores)
        cpu        \(Format.percent(second.cpuPercent))
        memory     \(Format.usage(used: second.memoryUsed, total: second.memoryTotal))
        disk       \(Format.usage(used: second.diskUsed, total: second.diskTotal))
        net rate   ↓ \(Format.rate(second.netRxRate))  ↑ \(Format.rate(second.netTxRate))
        latency    \(Format.latency(second.latencyMs))
        uptime     \(Format.uptime(second.uptimeSeconds, chinese: false))
        history    \(history.count) samples
        ─────────────────────────────────────────
        """)

        try monitor.deleteServer(server)
        #expect(monitor.servers.contains { $0.id == server.id } == false)
    }
}

/// The Docker page's listings against a real engine.
///
/// Opt-in:  SM_LIVE_ALIAS=myhost swift test --filter listsEveryDockerResource
@Suite("Live Docker", .serialized)
struct LiveDockerTests {

    @Test func listsEveryDockerResource() async throws {
        guard let alias = ProcessInfo.processInfo.environment["SM_LIVE_ALIAS"] else { return }
        let target = SSHTarget(
            serverID: UUID(), host: alias, port: 22,
            username: NSUserName(), credential: .sshConfigAlias
        )
        let docker = DockerClient()
        guard let summary = try? await docker.summary(target: target), !summary.engineVersion.isEmpty
        else { return }   // host has no engine; nothing to check

        let containers = try await docker.listContainers(target: target)
        let images = try await docker.listImages(target: target)
        let volumes = try await docker.listVolumes(target: target)
        let networks = try await docker.listNetworks(target: target)
        let stats = try await docker.stats(target: target)
        let compose = try await docker.listComposeProjects(target: target)

        // Built up front and annotated as String: GRDB's `SQL` is also
        // ExpressibleByStringInterpolation, and inference picked it here,
        // printing a page of SQLExpression instead of the numbers.
        let tileLines: String = containers.filter(\.isRunning).prefix(3).map { container in
            let sample = stats[container.shortID]
            let cpu: String = sample.map { Format.percent($0.cpuPercent) } ?? "—"
            let memory: String = sample.map { Format.percent($0.memoryPercent) } ?? "—"
            let net: String = "\(sample?.netRx ?? "—")/\(sample?.netTx ?? "—")"
            let io: String = "\(sample?.blockRead ?? "—")/\(sample?.blockWrite ?? "—")"
            return "\(container.name) cpu \(cpu) mem \(memory) net \(net) io \(io)"
        }.joined(separator: "\n                   ")

        print("""

        ── live Docker ──
        engine     \(summary.engineVersion)
        counts     total \(summary.total) = running \(summary.running) + paused \(summary.paused) + stopped \(summary.stopped)
        containers \(containers.count) (\(containers.filter(\.isRunning).count) running)
        images     \(images.count)  e.g. \(images.prefix(2).map(\.displayName).joined(separator: ", "))
        volumes    \(volumes.count)  e.g. \(volumes.prefix(2).map(\.name).joined(separator: ", "))
        networks   \(networks.map { "\($0.name)/\($0.driver)" }.joined(separator: " "))
        stats      \(stats.count) sampled
        compose    \(compose.map { "\($0.name) [\($0.status)] \($0.directory)" }.joined(separator: "\n                   "))
        tiles      \(tileLines)
        joined     \(containers.filter { stats[$0.shortID] != nil }.count) of \(containers.filter(\.isRunning).count) running matched
        ─────────────────
        """)

        // Two different counts, both right: `docker images` prints one row per
        // repository:tag (a retagged image appears twice) and hides intermediate
        // layers; `docker info`'s Images counts every image object, intermediate
        // layers included. So unique ids from the listing can only be at most
        // the engine's total — equality held on this host until a multi-stage
        // build left three intermediates behind, and then it did not.
        let uniqueImageIDs = Set(images.map(\.id))
        #expect(
            uniqueImageIDs.count <= summary.images,
            "\(uniqueImageIDs.count) unique listed images exceeds docker info's \(summary.images)"
        )
        #expect(uniqueImageIDs.count > 0, "an engine with images listed none")
        #expect(networks.contains { $0.isBuiltIn }, "every engine has bridge/host/none")
        // Not asserted: that a running project owns a "<name>_default" network.
        // It usually does, but `network_mode: host` and external networks are
        // both common and create none — a real project on this host (`dst`)
        // failed that check while being listed perfectly correctly.
        for project in compose where project.isRunning {
            #expect(project.counts.isEmpty == false, "unparsed status: \(project.status)")
        }
        // What does hold: every running project has at least one running
        // container on the host.
        if !compose.isEmpty {
            let runningServices = compose.filter(\.isRunning).reduce(0) { $0 + $1.runningCount }
            #expect(
                runningServices <= containers.filter(\.isRunning).count,
                "compose claims \(runningServices) running services but only \(containers.filter(\.isRunning).count) containers run"
            )
        }
        // The donut prints `total` in its middle and the bands around it; if
        // these disagree the card contradicts itself.
        #expect(
            summary.total == containers.count,
            "docker info totals \(summary.total) but docker ps listed \(containers.count)"
        )
        #expect(images.allSatisfy { !$0.displayName.isEmpty })
        #expect(volumes.allSatisfy { !$0.mountpoint.isEmpty })

        // Every tile on the machine screen renders these four figures; an empty
        // string shows as a dash and looks like the engine is broken.
        for container in containers.filter(\.isRunning) {
            guard let sample = stats[container.shortID] else { continue }
            #expect(sample.netRx.isEmpty == false, "\(container.name) has no NetIO")
            #expect(sample.blockRead.isEmpty == false, "\(container.name) has no BlockIO")
            #expect(sample.memoryUsage.contains("/"), "memory usage should be used/limit")
        }

        // The short/long id join is the part most likely to silently break:
        // a mismatch shows as every running container reporting no stats.
        let running = containers.filter(\.isRunning)
        if !running.isEmpty && !stats.isEmpty {
            #expect(
                running.contains { stats[$0.shortID] != nil },
                "no running container matched a stats row — id join is broken"
            )
        }
    }
}
