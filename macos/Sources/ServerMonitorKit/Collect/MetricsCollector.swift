import Foundation

/// Runs one collection round against a host and turns raw counters into rates.
///
/// Off the main actor, and holds no SwiftData types: it takes a target and
/// returns a value, leaving persistence to the caller.
public actor MetricsCollector {
    private let runner: SSHRunner
    private let ping = PingProbe()
    /// Latency changes far more slowly than load, so ICMP runs on its own
    /// slower cadence and the reading is reused between polls. At a 5-second
    /// interval, pinging every round would spawn 12 processes a minute per
    /// host for a number that barely moves.
    private var pingCache: [UUID: (reading: PingProbe.Reading, takenAt: Date)] = [:]
    private let pingInterval: TimeInterval = 30
    /// Previous cumulative counters per server, needed to derive per-second
    /// rates. Lost on relaunch, which costs exactly one poll of rate data.
    private var baselines: [UUID: CounterBaseline] = [:]
    private var interfaceBaselines: [UUID: InterfaceBaseline] = [:]
    /// CPU model per server, for hosts whose /proc/cpuinfo does not name it.
    /// Looked up with `lscpu` once — it is a fixed fact, and `lscpu` is not
    /// cheap on an 80-core ARM box — then carried into every later snapshot.
    private var cpuModelCache: [UUID: String] = [:]
    /// Hosts already asked, so one that cannot answer is not asked every poll.
    private var cpuModelLookedUp: Set<UUID> = []

    public init(runner: SSHRunner = SSHRunner()) {
        self.runner = runner
    }

    public func collect(target: SSHTarget, osKind: OSKind = .auto) async throws -> MetricSnapshot {
        // Kick the ICMP probe off alongside the SSH round trip when it is due,
        // so latency costs no extra wall-clock time.
        async let icmp = pingIfDue(target: target)

        var snapshot: MetricSnapshot
        var elapsed: TimeInterval = 0
        var clocks: (start: String, end: String) = ("", "")

        switch osKind {
        case .windows:
            snapshot = try await collectWindows(target: target)
        case .linux:
            (snapshot, elapsed, clocks) = try await collectLinux(target: target)
        case .auto:
            // Probe with one cheap command rather than speculatively running a
            // whole collection. The Linux batch contains `sleep` and several
            // `cat`s; against a Windows shell that does not fail fast, it hung
            // until the timeout — a detection should cost a moment, not 30s.
            let detected = try await detectOS(target: target)
            if detected == .linux {
                (snapshot, elapsed, clocks) = try await collectLinux(target: target)
            } else {
                snapshot = try await collectWindows(target: target)
            }
            snapshot.detectedOS = detected
        }

        // Prefer real ICMP over the physical NIC: it is the actual network
        // round trip. The remote-clock figure is the fallback for hosts that
        // filter ICMP — it always works, but it also carries the local ssh
        // process launch and the proxy hop, so it reads higher.
        let clockLatency = ProcParsers.networkLatency(
            elapsed: elapsed,
            startClock: clocks.start,
            endClock: clocks.end
        )
        snapshot.latencyMs = await icmp?.averageMs ?? clockLatency
        return snapshot
    }

    /// Asks the host what it is, with a short timeout.
    ///
    /// A Windows host answers by *failing* — it has no `uname` — so a non-zero
    /// exit from the remote shell is the Windows signal rather than an error.
    /// ssh's own failures are a different thing entirely: 255, a timeout, or a
    /// failed launch all mean no shell was ever reached. Swallowing those and
    /// guessing `.windows` sent an unreachable host through the whole Windows
    /// collection as well, so every poll of a down host paid two 10 s connect
    /// timeouts instead of one, and reported the failure in Windows terms.
    private func detectOS(target: SSHTarget) async throws -> OSKind {
        do {
            let output = try await runner.run("uname -s", on: target, timeout: 15)
            return Self.osKind(fromUname: output)
        } catch let failure as SSHRunner.Failure {
            guard let kind = Self.osKind(fromProbeFailure: failure) else { throw failure }
            return kind
        }
    }

    /// What a failed `uname -s` says about the host, or `nil` when it says
    /// nothing — in which case the caller must rethrow rather than guess.
    ///
    /// ssh reserves status 255 for its own errors, so a different status means
    /// a shell answered and simply had no `uname`: that is Windows. A 255, a
    /// timeout, or a launch failure means we never reached a shell.
    static func osKind(fromProbeFailure failure: SSHRunner.Failure) -> OSKind? {
        if case .commandFailed(let status, _) = failure, status != 255 { return .windows }
        return nil
    }

    /// A Linux host prints "Linux"; a Windows shell prints an error, or nothing
    /// at all, so anything else is treated as Windows.
    static func osKind(fromUname output: String) -> OSKind {
        let text = output.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        return text.contains("linux") ? .linux : .windows
    }

    /// The parsed model when the host gave one; otherwise the cached answer,
    /// or one `lscpu` round trip the first time.
    private func cpuModel(parsed: String, target: SSHTarget) async -> String {
        if !parsed.isEmpty { return parsed }
        if let cached = cpuModelCache[target.serverID] { return cached }
        guard !cpuModelLookedUp.contains(target.serverID) else { return "" }
        cpuModelLookedUp.insert(target.serverID)
        let answer = ((try? await runner.run(ProcParsers.cpuModelFallbackCommand, on: target, timeout: 15)) ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if !answer.isEmpty { cpuModelCache[target.serverID] = answer }
        return answer
    }

    /// Returns the snapshot plus what the latency calculation needs.
    private func collectLinux(
        target: SSHTarget
    ) async throws -> (MetricSnapshot, TimeInterval, (start: String, end: String)) {
        let started = Date()
        let output = try await runner.run(ProcParsers.linuxMetricsCommand, on: target)
        let elapsed = Date().timeIntervalSince(started)

        let sections = ProcParsers.splitSections(output, want: ProcParsers.Section.allCases.count)
        func section(_ which: ProcParsers.Section) -> String { sections[which.rawValue] }

        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = ProcParsers.cpuPercent(
            first: section(.statFirst),
            second: section(.statSecond)
        )
        (snapshot.load1, snapshot.load5, snapshot.load15) = ProcParsers.loadAverage(section(.loadAvg))
        (snapshot.memoryUsed, snapshot.memoryTotal) = ProcParsers.memInfo(section(.memInfo))
        (snapshot.diskUsed, snapshot.diskTotal) = ProcParsers.diskUsage(section(.diskUsage))
        snapshot.uptimeSeconds = ProcParsers.uptime(section(.uptime))
        snapshot.cores = ProcParsers.cores(section(.nproc))
        snapshot.dockerVersion = ProcParsers.dockerVersion(section(.docker))
        if !snapshot.dockerVersion.isEmpty {
            let summary = DockerClient.parseSummary(section(.docker))
            // parseSummary needs the pipe-separated form; a host that answered
            // with a bare version leaves the counts at zero, which would read
            // as "no containers" rather than "not reported".
            if !summary.engineVersion.isEmpty { snapshot.dockerSummary = summary }
        }

        // Detail for the machine screen. Most of this comes from output the
        // batch already fetched and used to throw away.
        snapshot.coreLoads = ProcParsers.coreLoads(
            first: section(.statFirst),
            second: section(.statSecond)
        )
        snapshot.cpuBreakdown = ProcParsers.cpuBreakdown(
            first: section(.statFirst),
            second: section(.statSecond)
        )
        snapshot.memory = ProcParsers.memoryBreakdown(section(.memInfo))
        snapshot.filesystems = ProcParsers.filesystems(section(.diskUsage))
        snapshot.processes = ProcParsers.processes(section(.processes))
        snapshot.identity = ProcParsers.hostIdentity(section(.hostInfo))
        snapshot.identity.cpuModel = await cpuModel(
            parsed: snapshot.identity.cpuModel, target: target
        )
        snapshot.gpu = ProcParsers.gpuStatus(section(.gpu))
        snapshot.interfaces = ProcParsers.netInterfaces(section(.netDev))

        let net = ProcParsers.netDev(section(.netDev))
        let disk = ProcParsers.diskStats(section(.diskStats))
        snapshot.netRxTotal = net.rx
        snapshot.netTxTotal = net.tx
        applyRates(serverID: target.serverID, snapshot: &snapshot, net: net, disk: disk)
        applyInterfaceRates(serverID: target.serverID, snapshot: &snapshot)

        // A live Linux host always has memory and at least one CPU. Both zero
        // means the batch came back empty or truncated — ssh exited 0 with no
        // usable stdout, which `run` does not treat as an error. Returning this
        // would paint a real host as a zeroed-out machine on the dashboard
        // (0 cores, 0 memory, no filesystems); a thrown error marks it offline
        // and the next poll recovers. Seen intermittently under load, when a
        // multiplexed channel is torn down mid-read.
        guard snapshot.memoryTotal > 0 || snapshot.cores > 0 else {
            throw SSHError.commandFailed("empty metrics from host (truncated collection)")
        }

        return (snapshot, elapsed, (section(.startClock), section(.endClock)))
    }

    private func collectWindows(target: SSHTarget) async throws -> MetricSnapshot {
        let output = try await runner.run(WindowsMetrics.command, on: target)
        guard var snapshot = WindowsMetrics.parse(output) else {
            // Carry the output: "no metrics" alone says nothing about whether
            // the shell rejected the command, PowerShell is missing, or the
            // host answered with something else entirely.
            let sample = output.trimmingCharacters(in: .whitespacesAndNewlines).prefix(300)
            throw SSHError.commandFailed(
                sample.isEmpty ? "Windows host returned no output" : "unparsed Windows output: \(sample)"
            )
        }
        let counters = WindowsMetrics.counters(output)
        applyRates(
            serverID: target.serverID,
            snapshot: &snapshot,
            net: counters.net,
            disk: counters.disk
        )
        return snapshot
    }

    /// Per-interface rates, kept separate from `applyRates` because an
    /// interface can appear or disappear between polls (a container bridge
    /// coming up, a VPN going down) and must not be compared against a
    /// baseline that belonged to a different NIC.
    private func applyInterfaceRates(serverID: UUID, snapshot: inout MetricSnapshot) {
        let now = Date()
        let previous = interfaceBaselines[serverID]
        defer {
            interfaceBaselines[serverID] = InterfaceBaseline(
                counters: Dictionary(
                    snapshot.interfaces.map { ($0.name, (rx: $0.rxTotal, tx: $0.txTotal)) },
                    uniquingKeysWith: { first, _ in first }
                ),
                takenAt: now
            )
        }
        guard let previous else { return }
        let seconds = now.timeIntervalSince(previous.takenAt)
        guard seconds > 0.5 else { return }
        for index in snapshot.interfaces.indices {
            guard let before = previous.counters[snapshot.interfaces[index].name] else { continue }
            let rx = snapshot.interfaces[index].rxTotal - before.rx
            let tx = snapshot.interfaces[index].txTotal - before.tx
            // A counter that went backwards means the NIC was reset; report
            // nothing rather than a nonsense spike.
            snapshot.interfaces[index].rxRate = rx >= 0 ? Double(rx) / seconds : 0
            snapshot.interfaces[index].txRate = tx >= 0 ? Double(tx) / seconds : 0
        }
    }

    /// Converts cumulative counters into per-second rates using the previous
    /// poll as the baseline. A counter that went backwards (host reboot, NIC
    /// reset) yields zero instead of a spike.
    private func applyRates(
        serverID: UUID,
        snapshot: inout MetricSnapshot,
        net: (rx: Int64, tx: Int64),
        disk: (read: Int64, written: Int64)
    ) {
        let now = Date()
        defer {
            baselines[serverID] = CounterBaseline(
                netRx: net.rx, netTx: net.tx,
                diskRead: disk.read, diskWrite: disk.written,
                takenAt: now
            )
        }
        guard let previous = baselines[serverID] else { return }
        let seconds = now.timeIntervalSince(previous.takenAt)
        guard seconds > 0 else { return }

        func rate(_ current: Int64, _ earlier: Int64) -> Double {
            let delta = current - earlier
            return delta >= 0 ? Double(delta) / seconds : 0
        }
        snapshot.netRxRate = rate(net.rx, previous.netRx)
        snapshot.netTxRate = rate(net.tx, previous.netTx)
        snapshot.diskReadRate = rate(disk.read, previous.diskRead)
        snapshot.diskWriteRate = rate(disk.written, previous.diskWrite)
    }

    /// Returns a fresh ICMP reading when one is due, otherwise the cached one.
    private func pingIfDue(target: SSHTarget) async -> PingProbe.Reading? {
        if let cached = pingCache[target.serverID],
           Date().timeIntervalSince(cached.takenAt) < pingInterval {
            return cached.reading
        }
        guard let address = await Self.address(for: target) else { return nil }
        guard let reading = await ping.measure(host: address) else {
            // Leave any previous reading in place rather than blanking the
            // display on one dropped probe.
            return pingCache[target.serverID]?.reading
        }
        pingCache[target.serverID] = (reading, Date())
        return reading
    }

    /// Resolved addresses for ~/.ssh/config aliases, which rarely change.
    private static let resolvedHosts = HostResolutionCache()

    /// The address to ping. For a config alias the stored host may be empty or
    /// stale, so ask ssh what it would actually connect to.
    static func address(for target: SSHTarget) async -> String? {
        guard case .sshConfigAlias = target.credential else {
            return target.host.isEmpty ? nil : target.host
        }
        if let cached = await resolvedHosts.value(for: target.host) { return cached }
        guard let output = try? await SSHRunner.execute(
            executable: "/usr/bin/ssh",
            arguments: ["-G", target.host],
            timeout: 5
        ) else { return target.host.isEmpty ? nil : target.host }
        let resolved = Self.parseHostName(output) ?? target.host
        await resolvedHosts.store(resolved, for: target.host)
        return resolved.isEmpty ? nil : resolved
    }

    /// `ssh -G` prints the effective config, one "key value" per line.
    static func parseHostName(_ output: String) -> String? {
        for line in output.lines() {
            let parts = line.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
            guard parts.count == 2, parts[0].lowercased() == "hostname" else { continue }
            return String(parts[1]).trimmingCharacters(in: .whitespaces)
        }
        return nil
    }

    /// Forgets a host's rate baseline and drops its multiplexed connection.
    public func forget(target: SSHTarget) async {
        baselines.removeValue(forKey: target.serverID)
        interfaceBaselines.removeValue(forKey: target.serverID)
        pingCache.removeValue(forKey: target.serverID)
        await runner.disconnect(target)
    }
}
