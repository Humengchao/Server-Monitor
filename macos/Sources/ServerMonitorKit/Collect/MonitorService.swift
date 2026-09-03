import Foundation
import Network
import SwiftUI
import os

/// Whether the last collection attempt for a server succeeded.
public enum ServerStatus: Equatable, Sendable {
    case unknown
    case polling
    case online(at: Date)
    case offline(reason: String)

    public var isOnline: Bool {
        if case .online = self { return true }
        return false
    }
}

/// Owns the polling loop and all UI-facing state.
///
/// Everything published here is touched only on the main actor; the SSH and
/// SQLite work happens in the collector actor and on the database queue, so the
/// UI never blocks on a slow or unreachable host.
@MainActor
public final class MonitorService: ObservableObject {
    @Published public private(set) var servers: [Server] = []
    /// Newest snapshot per server, for the dashboard tiles.
    @Published public private(set) var latest: [UUID: MetricSnapshot] = [:]
    @Published public private(set) var status: [UUID: ServerStatus] = [:]

    public let database: Database
    public let settings: AppSettings
    public let docker: DockerClient
    /// Only ever used from the IP card's button; see `GeoLookup`.
    public let geo = GeoLookup()
    public let sftp: SFTPClient
    /// Set by the app so poll results can raise notifications.
    public weak var alerts: AlertService?
    /// Shared logins, refreshed alongside the server list.
    @Published public private(set) var identities: [Identity] = []
    @Published public private(set) var groups: [MachineGroup] = []
    /// Engine summary per Docker host, for the overview cards.
    @Published public private(set) var dockerSummaries: [UUID: DockerSummary] = [:]

    private let runner: SSHRunner
    private let collector: MetricsCollector
    private var pollTask: Task<Void, Never>?
    private var maintenanceTask: Task<Void, Never>?
    private var pathMonitor: NWPathMonitor?
    /// Nil until the first path update, so starting up is not mistaken for a
    /// network that just came back.
    private var networkWasSatisfied: Bool?
    /// Held while polling, to keep macOS App Nap from freezing the poll loop's
    /// timer when the window is in the background. Exposed only so a test can
    /// assert the start/stop contract.
    private(set) var pollingActivity: NSObjectProtocol?

    public init(database: Database, settings: AppSettings) {
        self.database = database
        self.settings = settings
        let runner = SSHRunner()
        self.runner = runner
        self.collector = MetricsCollector(runner: runner)
        self.docker = DockerClient(runner: runner)
        self.sftp = SFTPClient(runner: runner)
        reload()
    }

    // MARK: - Server list

    public func reload() {
        do {
            identities = (try? database.allIdentities()) ?? []
            groups = (try? database.allGroups()) ?? []
            servers = try database.allServers()
            // Seed tiles from history so a relaunch shows the last known values
            // instead of empty cards until the first poll lands.
            for server in servers where latest[server.id] == nil {
                if let sample = try database.latestSample(serverID: server.id) {
                    latest[server.id] = sample.snapshot
                }
            }
        } catch {
            servers = []
        }
    }

    public func addServer(_ server: Server) throws {
        try database.save(server)
        reload()
    }

    public func updateServer(_ server: Server) throws {
        let previous = try? target(for: server)
        try database.save(server)
        // Address or credential may have changed; drop the multiplexed
        // connection so the next poll reconnects with the new settings.
        if let previous {
            Task { await collector.forget(target: previous) }
        }
        // Editing a failing host is usually the fix for it. Sitting out a
        // five-minute backoff afterwards would look like the edit did nothing.
        noteSuccess(serverID: server.id)
        reload()
    }

    public func deleteServer(_ server: Server) throws {
        let target = try? target(for: server)
        try database.deleteServer(id: server.id)
        if let target {
            Task { await collector.forget(target: target) }
        }
        alerts?.forget(serverID: server.id)
        latest.removeValue(forKey: server.id)
        status.removeValue(forKey: server.id)
        failureStreak.removeValue(forKey: server.id)
        retryAfter.removeValue(forKey: server.id)
        pending.removeAll { $0.serverID == server.id }
        reload()
    }

    /// Resolves a stored server into something ssh can dial.
    ///
    /// A server pointing at an identity takes that identity's username and
    /// auth, so changing the shared login updates every machine using it.
    public func target(for server: Server) throws -> SSHTarget {
        var username = server.username
        var credential = server.credential
        var host = server.authKind == .sshConfigAlias ? server.sshAlias : server.host

        if let identityID = server.identityID,
           let identity = identities.first(where: { $0.id == identityID }) {
            username = identity.username
            switch identity.authKind {
            case .identityFile:
                credential = .identityFile(path: identity.identityFile)
                host = server.host
            case .agent:
                credential = .agent
                host = server.host
            case .sshConfigAlias, .password:
                break
            }
        }

        return SSHTarget(
            serverID: server.id,
            host: host,
            port: server.port,
            username: username,
            credential: credential
        )
    }

    // MARK: - Toolbox

    public func snippets() -> [Snippet] {
        (try? database.allSnippets()) ?? []
    }

    public func save(_ snippet: Snippet) throws {
        try database.save(snippet)
        objectWillChange.send()
    }

    public func deleteSnippet(id: UUID) throws {
        try database.deleteSnippet(id: id)
        objectWillChange.send()
    }

    /// Runs a snippet on a host and returns its combined output.
    public func run(snippet: Snippet, on server: Server) async throws -> String {
        let target = try target(for: server)
        try? database.markSnippetUsed(id: snippet.id)
        return try await runner.run("\(snippet.command) 2>&1", on: target, timeout: 120)
    }

    /// One command over the server's pooled connection, for cards that fetch
    /// something the metrics poll does not carry.
    public func run(_ command: String, on server: Server, timeout: Int = 30) async throws -> String {
        try await runner.run(command, on: target(for: server), timeout: timeout)
    }

    public func save(_ identity: Identity) throws {
        try database.save(identity)
        reload()
    }

    public func deleteIdentity(id: UUID) throws {
        try database.deleteIdentity(id: id)
        reload()
    }

    public func serverCount(usingIdentity id: UUID) -> Int {
        (try? database.serverCount(usingIdentity: id)) ?? 0
    }

    // MARK: - Machine groups

    public func save(_ group: MachineGroup) throws {
        try database.save(group)
        reload()
    }

    public func deleteGroup(id: UUID) throws {
        try database.deleteGroup(id: id)
        reload()
    }

    public func nextGroupSortIndex() -> Int {
        (try? database.nextGroupSortIndex()) ?? 0
    }

    public func servers(in group: MachineGroup) -> [Server] {
        servers.filter { $0.groupID == group.id }
    }

    /// Machines with no group, shown on their own below the groups.
    public var ungroupedServers: [Server] {
        servers.filter { $0.groupID == nil }
    }

    public func assign(_ server: Server, to group: MachineGroup?) throws {
        var updated = server
        updated.groupID = group?.id
        try database.save(updated)
        reload()
    }

    // MARK: - Docker

    /// Refreshes the engine summary for one host.
    ///
    /// The machine screen needs its own host's figures, and the whole-fleet
    /// sweep below is the wrong tool for that: `docker info` is a separate SSH
    /// round trip per host, so opening one machine would poll every other one
    /// too.
    public func refreshDockerSummary(for server: Server) async {
        guard server.hasDocker, let target = try? target(for: server) else { return }
        guard let summary = try? await docker.summary(target: target) else { return }
        dockerSummaries[server.id] = summary
    }

    /// Refreshes the engine summary for every host that reports Docker.
    public func refreshDockerSummaries() async {
        let hosts = servers.filter(\.hasDocker)
        await withTaskGroup(of: (UUID, DockerSummary?).self) { group in
            for host in hosts {
                group.addTask { [weak self] in
                    guard let self, let target = try? await self.target(for: host) else {
                        return (host.id, nil)
                    }
                    return (host.id, try? await self.docker.summary(target: target))
                }
            }
            for await (id, summary) in group {
                if let summary { dockerSummaries[id] = summary }
            }
        }
    }

    // MARK: - Polling

    public func start() {
        guard pollTask == nil else { return }
        // Without this, App Nap suspends the poll loop's timer the moment the
        // window is occluded — the app sits at 0% CPU and stops polling, so a
        // host can go down, cross a threshold, or recover with no sample
        // written and no alert fired, and the menu bar shows stale numbers.
        // A monitor is expected to keep watching in the background; that is the
        // point of its menu-bar presence. `…AllowingIdleSystemSleep` opts out
        // of App Nap only — the Mac can still sleep when idle, and polling
        // resumes on wake.
        pollingActivity = ProcessInfo.processInfo.beginActivity(
            options: .userInitiatedAllowingIdleSystemSleep,
            reason: "Polling monitored servers"
        )
        watchNetworkPath()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                // Fire and move on. Awaiting the whole fleet here coupled every
                // host's cadence to the slowest: one unreachable host (10 s
                // connect timeout) stretched everyone's 5 s interval to 15 s,
                // and a hung one (30 s watchdog) to 35 s.
                _ = self.pollDue()
                let interval = self.settings.pollInterval
                try? await Task.sleep(for: .seconds(interval))
            }
        }
        maintenanceTask = Task { [weak self] in
            while !Task.isCancelled {
                // Give startup a moment before touching the disk, then keep a
                // slow cadence: pruning a bounded window is cheap.
                try? await Task.sleep(for: .seconds(60))
                guard let self else { return }
                let retention = self.settings.retention
                let database = self.database
                // Off the main actor: this class is @MainActor, so a plain call
                // here would run the delete on the thread that draws.
                await Task.detached(priority: .utility) {
                    _ = try? database.pruneHistory(retention: retention)
                }.value
            }
        }
    }

    public func stop() {
        pollTask?.cancel()
        maintenanceTask?.cancel()
        pollTask = nil
        maintenanceTask = nil
        pathMonitor?.cancel()
        pathMonitor = nil
        networkWasSatisfied = nil
        tickCapTask?.cancel()
        tickCapTask = nil
        tickOpen = false
        tickMembers.removeAll()
        if let pollingActivity {
            ProcessInfo.processInfo.endActivity(pollingActivity)
            self.pollingActivity = nil
        }
        // Multiplexed connections close themselves once ControlPersist lapses.
    }

    /// Servers whose previous poll has not returned yet. A host that is slow
    /// to answer is polled once, not once per tick, so a stall cannot pile up
    /// a queue of ssh processes behind it.
    private(set) var inFlight: Set<UUID> = []

    static let log = Logger(subsystem: "com.hmchxd.ServerMonitor", category: "polling")

    // MARK: - One tick, one publish

    /// Servers launched by the tick in progress whose polls have not returned.
    private(set) var tickMembers: Set<UUID> = []
    /// True from the moment a tick launches its polls until the last of them
    /// returns or `tickCap` elapses. Results arriving meanwhile are held.
    private(set) var tickOpen = false
    private var tickCapTask: Task<Void, Never>?

    /// Longest a tick's results are held for a straggler before what has
    /// arrived is shown anyway. Measured spread between the first and last
    /// host finishing one tick: p50 1.0 s, p95 1.6 s. A host that has gone
    /// away takes the full 10 s connect timeout, so the cap is what keeps one
    /// dead host from delaying eight live ones.
    var tickCap: Duration = .seconds(2)

    /// Holds publishing until every poll this tick launched has answered.
    ///
    /// The 250 ms window on its own gave 3.4 publishes per tick, measured
    /// over 221 ticks: hosts finish about a second apart (latency differs by
    /// 200 ms and the script itself samples twice, 0.5 s apart), so the first
    /// result opened a window, the next few closed it, and the stragglers each
    /// opened another. Every publish is a full dashboard pass, so that was
    /// three layouts to show one tick's numbers. The tick knows exactly which
    /// polls it started; waiting for those turns it into one.
    private func openTick(members: Set<UUID>) {
        tickMembers.formUnion(members)
        guard !tickOpen else { return }
        tickOpen = true
        tickCapTask = Task { [weak self] in
            try? await Task.sleep(for: self?.tickCap ?? .seconds(2))
            guard let self, !Task.isCancelled else { return }
            self.closeTick(reason: "cap")
        }
    }

    private func pollFinished(_ serverID: UUID) {
        inFlight.remove(serverID)
        tickMembers.remove(serverID)
        if tickOpen && tickMembers.isEmpty { closeTick(reason: "complete") }
    }

    private func closeTick(reason: StaticString) {
        tickOpen = false
        tickCapTask?.cancel()
        tickCapTask = nil
        Self.log.debug("tick closed (\(reason, privacy: .public)), \(self.pending.count) results held")
        // Hidden: leave them queued for `setUIVisible(true)`, exactly as a
        // result arriving outside a tick would be.
        if uiIsVisible { flushPending() }
    }

    // MARK: - Failure backoff

    /// Consecutive failed polls per server, reset by any success.
    private(set) var failureStreak: [UUID: Int] = [:]
    /// Earliest time `pollDue` will try a server again.
    private(set) var retryAfter: [UUID: Date] = [:]

    /// Longest a failing host waits between attempts. It still gets retried
    /// often enough that a recovery is noticed within a few minutes.
    static let maxBackoff: TimeInterval = 300

    /// A host that is simply gone costs a full 10 s connect timeout per
    /// attempt, and one held ssh process the whole time. Polling it on the
    /// normal 5 s cadence means doing that forever — on battery, for a host
    /// that has been down all day. Back off geometrically instead, and reset
    /// the moment it answers.
    func backoffDelay(streak: Int) -> TimeInterval {
        guard streak > 1 else { return 0 }
        let base = settings.pollInterval
        let grown = base * pow(2, Double(streak - 1))
        return min(grown, Self.maxBackoff)
    }

    /// Whether a server's backoff window is still open.
    ///
    /// The window is an absolute date, so a clock that moves backwards — an
    /// NTP correction on a machine with a dead RTC, a restored VM snapshot —
    /// would otherwise strand a host in a wait far longer than any backoff can
    /// legitimately produce, with no way out but relaunching. A remaining wait
    /// longer than the cap is not a backoff; it is a moved clock.
    func isStillWaiting(until due: Date, now: Date) -> Bool {
        due > now && due.timeIntervalSince(now) <= Self.maxBackoff
    }

    /// Records a failed poll and pushes the next attempt out.
    func noteFailure(serverID: UUID, now: Date = Date()) {
        let streak = (failureStreak[serverID] ?? 0) + 1
        failureStreak[serverID] = streak
        let delay = backoffDelay(streak: streak)
        retryAfter[serverID] = delay > 0 ? now.addingTimeInterval(delay) : nil
    }

    /// Any answer at all clears the backoff, so a host that comes back is
    /// polled at the normal cadence from its very next tick.
    func noteSuccess(serverID: UUID) {
        failureStreak[serverID] = nil
        retryAfter[serverID] = nil
    }

    /// Watches the local network path.
    ///
    /// On a laptop the usual reason every host fails at once is this machine,
    /// not nine servers: a closed lid, a train, a different Wi-Fi. Without
    /// this, the backoff built up during the outage keeps the fleet dark for
    /// up to five minutes after the network is already back.
    private func watchNetworkPath() {
        guard pathMonitor == nil else { return }
        let monitor = NWPathMonitor()
        pathMonitor = monitor
        monitor.pathUpdateHandler = { [weak self] path in
            // Only a Bool crosses actors; the path itself stays on its queue.
            let satisfied = path.status == .satisfied
            Task { @MainActor in self?.networkPathChanged(satisfied: satisfied) }
        }
        monitor.start(queue: DispatchQueue(label: "com.hmc.ServerMonitor.network"))
    }

    /// Clears every backoff when the network returns, and polls at once.
    func networkPathChanged(satisfied: Bool) {
        defer { networkWasSatisfied = satisfied }
        // The first update is just the current state, not a transition.
        guard let previous = networkWasSatisfied else { return }
        guard satisfied, !previous else { return }
        failureStreak.removeAll()
        retryAfter.removeAll()
        _ = pollDue()
    }

    /// Starts a poll for every server not already being polled, and returns
    /// those tasks without waiting for them. Each host runs on its own clock.
    @discardableResult
    func pollDue(ignoringBackoff: Bool = false, now: Date = Date()) -> [Task<Void, Never>] {
        var launched: Set<UUID> = []
        let tasks: [Task<Void, Never>] = servers.compactMap { server in
            guard !inFlight.contains(server.id) else { return nil }
            if !ignoringBackoff, let due = retryAfter[server.id], isStillWaiting(until: due, now: now) {
                return nil
            }
            inFlight.insert(server.id)
            launched.insert(server.id)
            return Task { [weak self] in
                await self?.poll(server)
                self?.pollFinished(server.id)
            }
        }
        // The tasks above start at the next suspension point, so the tick is
        // open before the first of them can report.
        if !launched.isEmpty { openTick(members: launched) }
        return tasks
    }

    /// One pass over every server, waited for — the manual refresh. Hosts with
    /// a poll already in flight are left to it rather than polled twice.
    public func pollAll() async {
        // A user-driven refresh is an explicit "try now", so it overrides the
        // backoff rather than silently skipping the hosts that need it most.
        for task in pollDue(ignoringBackoff: true) { await task.value }
        // Deterministic for the caller: what it started is applied when this
        // returns, not a quarter-second later — and regardless of visibility,
        // since tests and the manual refresh both rely on it.
        flushPending()
    }

    public func poll(_ server: Server) async {
        // A task created by `pollDue` starts at the next suspension point, so a
        // server can be deleted between the tick and this line; without the
        // check it came back as a `.polling` entry for a row that no longer
        // exists.
        guard servers.contains(where: { $0.id == server.id }) else { return }
        if status[server.id] == nil || status[server.id] == .unknown {
            commit(for: server.id) { self.status[server.id] = .polling }
        }
        do {
            let target = try target(for: server)
            let snapshot = try await collector.collect(target: target, osKind: server.osKind)
            // Deleted while this poll was in flight: nothing to record, and the
            // sample insert would fail its foreign key anyway.
            guard servers.contains(where: { $0.id == server.id }) else { return }

            // Remember a probed OS so later polls skip straight to the right
            // script instead of trying Linux every time.
            let detected = server.osKind == .auto ? snapshot.detectedOS : nil
            // Durable writes go straight to the store; they publish nothing.
            // One transaction, and not on this actor: three separate writes
            // per host per poll were running on the thread that draws, and
            // `Database.inTransaction` was 1% of main-thread time for it.
            let database = self.database
            let serverID = server.id
            try await Task.detached(priority: .utility) {
                try database.recordPoll(serverID: serverID, snapshot: snapshot, detectedOS: detected)
            }.value
            // The write suspended us; the server may be gone by now.
            guard servers.contains(where: { $0.id == server.id }) else { return }

            // Everything the UI observes, in one batch.
            let now = Date()
            noteSuccess(serverID: server.id)
            commit(for: server.id) {
                self.latest[server.id] = snapshot
                self.status[server.id] = .online(at: now)
                // Free with the poll, so the machine screen and the Docker page
                // both stay live without their own round trip.
                if let summary = snapshot.dockerSummary {
                    self.dockerSummaries[server.id] = summary
                }
                if let index = self.servers.firstIndex(where: { $0.id == server.id }) {
                    if let detected { self.servers[index].osKind = detected }
                    self.servers[index].cores = snapshot.cores
                    self.servers[index].memoryTotal = snapshot.memoryTotal
                    self.servers[index].diskTotal = snapshot.diskTotal
                    self.servers[index].dockerVersion = snapshot.dockerVersion
                }
            }
            alerts?.evaluate(server: server, status: .online(at: now), snapshot: snapshot)
        } catch {
            // A cancelled poll says nothing about the host.
            if error is CancellationError { return }
            guard servers.contains(where: { $0.id == server.id }) else { return }
            noteFailure(serverID: server.id)
            Self.log.info("\(server.name, privacy: .public): \(error.localizedDescription, privacy: .public)")
            let failure = ServerStatus.offline(reason: error.localizedDescription)
            commit(for: server.id) { self.status[server.id] = failure }
            alerts?.evaluate(server: server, status: failure, snapshot: nil)
        }
    }

    // MARK: - Coalesced publishing

    /// Results waiting to be applied together; see `commit`.
    private(set) var pending: [(serverID: UUID, apply: () -> Void)] = []
    private var flushScheduled = false

    /// False while every window is hidden or fully covered.
    ///
    /// SwiftUI keeps evaluating and laying out views when a window is occluded
    /// — AppKit only skips the drawing. Profiling nine hosts with the window
    /// behind Finder put ~11% of a core in `sizeThatFits` and
    /// `StackLayout.placeChildren` for a dashboard nobody could see. Holding
    /// results back while nothing is visible removes that; collection, the
    /// database and alerts are unaffected, and `windowBecameVisible()` applies
    /// whatever accumulated so the first frame shown is current.
    private(set) var uiIsVisible = true

    /// Called from the app when window occlusion changes.
    public func setUIVisible(_ visible: Bool) {
        guard visible != uiIsVisible else { return }
        uiIsVisible = visible
        if visible { flushPending() }
    }
    /// How long results are gathered before being applied as one change.
    static let publishWindow: Duration = .milliseconds(250)

    /// Applies a state change now if nothing is in flight, otherwise queues it
    /// for the next flush.
    ///
    /// Every dashboard card observes this object, so each published change
    /// re-evaluates all of them — measured at 37 ms for 30 cards. With hosts
    /// finishing their polls at different moments that was one full pass per
    /// host: at 30 hosts a fifth of a core, at 60 nearly all of it, just
    /// re-rendering. Gathering results for a quarter-second and applying them
    /// in one run-loop turn lets SwiftUI treat them as a single change, so the
    /// dashboard redraws at most four times a second however many hosts there
    /// are. The first result after a quiet spell is not delayed at all.
    func commit(for serverID: UUID, _ apply: @escaping @MainActor () -> Void) {
        // Nothing on screen: queue it and let `setUIVisible(true)` or the next
        // `pollAll` apply it. The menu bar reads `peakCPU`/`offlineServers`,
        // which are computed from this same state, so it catches up then too —
        // it is a summary of a few numbers, not a view worth 11% of a core.
        // Hidden, mid-tick, or inside a window: queue it. A lone result with
        // none of those in force is applied on the spot.
        if !uiIsVisible || tickOpen || flushScheduled {
            pending.append((serverID, apply))
            return
        }
        apply()
        scheduleFlush()
    }

    private func scheduleFlush() {
        flushScheduled = true
        Task { [weak self] in
            try? await Task.sleep(for: Self.publishWindow)
            self?.windowElapsed()
        }
    }

    /// The 250 ms window ran out. Mid-tick the tick's own close will publish,
    /// so applying here would only split one tick's results in two.
    private func windowElapsed() {
        flushScheduled = false
        if !tickOpen { flushPending() }
    }

    /// Applies everything queued, as one change. Results for a server deleted
    /// in the meantime are dropped rather than resurrecting it.
    func flushPending() {
        flushScheduled = false
        guard !pending.isEmpty else { return }
        // Only the most recent result per server matters: while hidden, a
        // minute behind Finder queues a dozen snapshots per host and applying
        // them in turn would lay the dashboard out a dozen times to show the
        // last one.
        var latestPerServer: [UUID: () -> Void] = [:]
        var order: [UUID] = []
        for item in pending {
            if latestPerServer[item.serverID] == nil { order.append(item.serverID) }
            latestPerServer[item.serverID] = item.apply
        }
        pending = order.compactMap { id in latestPerServer[id].map { (serverID: id, apply: $0) } }
        let batch = pending
        pending.removeAll()
        for item in batch where servers.contains(where: { $0.id == item.serverID }) {
            item.apply()
        }
        Self.log.debug("published \(batch.count) results")
        // A burst still arriving gets another window rather than an immediate
        // publish per result — but not while hidden, where nothing should be
        // waking up every 250 ms.
        if uiIsVisible { scheduleFlush() }
    }

    /// Servers that failed their last poll, for the menu bar summary.
    public var offlineServers: [Server] {
        servers.filter {
            if case .offline = status[$0.id] ?? .unknown { return true }
            return false
        }
    }

    /// Highest CPU currently reported, or nil when nothing has reported yet.
    public var peakCPU: (server: Server, percent: Double)? {
        servers
            .compactMap { server in latest[server.id].map { (server, $0.cpuPercent) } }
            .max { $0.1 < $1.1 }
    }

    /// One-shot connectivity check for the editor's "test connection" button.
    public func testConnection(osKind: OSKind = .auto, target: SSHTarget) async throws {
        _ = try await collector.collect(target: target, osKind: osKind)
    }

    public func history(serverID: UUID, since: Date) -> [MetricSample] {
        (try? database.samples(serverID: serverID, since: since)) ?? []
    }

    /// History for the charts: bucketed by SQLite to `maxPoints`, read on a
    /// background thread. The 24 h range used to decode a day of polls on the
    /// main thread every 15 s — a ~120 ms hitch each time. Now ~18 ms, and
    /// none of it on the thread that draws.
    public func chartHistory(serverID: UUID, since: Date, maxPoints: Int = 240) async -> [MetricSample] {
        let database = self.database
        return await Task.detached(priority: .utility) {
            (try? database.reducedSamples(serverID: serverID, since: since, maxPoints: maxPoints)) ?? []
        }.value
    }
}
