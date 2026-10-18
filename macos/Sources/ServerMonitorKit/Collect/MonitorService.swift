import AppKit
import Foundation
import Network
import Observation
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
///
/// `@Observable` rather than `ObservableObject`: a view depends only on the
/// properties its body actually reads. With `ObservableObject`, one change
/// notification per poll re-evaluated every view holding the service — the
/// window frame, the toolbar, panes that only ever *call* it — and keeping
/// them quiet took a second, un-observed injection channel and a comment in
/// each file. Now the compiler does it: a view that reads `servers` is left
/// alone when `latest` changes, and one that reads nothing never re-runs.
@Observable
@MainActor
public final class MonitorService {
    public private(set) var servers: [Server] = []
    /// Newest snapshot per server, for the dashboard tiles.
    public private(set) var latest: [UUID: MetricSnapshot] = [:]
    public private(set) var status: [UUID: ServerStatus] = [:]
    public let live = MonitorSummary()

    public let database: Database
    public let settings: AppSettings
    public let docker: DockerClient
    /// Only ever used from the IP card's button; see `GeoLookup`.
    public let geo = GeoLookup()
    public let sftp: SFTPClient
    /// Set by the app so poll results can raise notifications.
    @ObservationIgnored public weak var alerts: AlertService?
    /// Shared logins, refreshed alongside the server list.
    public private(set) var identities: [Identity] = []
    public private(set) var groups: [MachineGroup] = []
    /// Engine summary per Docker host, for the overview cards.
    public private(set) var dockerSummaries: [UUID: DockerSummary] = [:]

    private let runner: SSHRunner
    private let collector: MetricsCollector
    /// Injection for lifecycle tests; production always uses the collector.
    @ObservationIgnored var collectOverride: (@Sendable (SSHTarget, OSKind, Bool) async throws -> MetricSnapshot)?
    @ObservationIgnored private var pollTasks: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var resetTasks: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var generations: [UUID: UInt64] = [:]
    @ObservationIgnored private var writeValidity: [UUID: PollWriteValidity] = [:]
    /// Application wiring owns session cleanup; collection does not depend on UI sessions.
    @ObservationIgnored public var serverDidDelete: ((UUID) -> Void)?
    @ObservationIgnored private var pollTask: Task<Void, Never>?
    @ObservationIgnored private var maintenanceTask: Task<Void, Never>?
    @ObservationIgnored private var pathMonitor: NWPathMonitor?
    @ObservationIgnored private var wakeObserver: NSObjectProtocol?
    /// Nil until the first path update, so starting up is not mistaken for a
    /// network that just came back.
    @ObservationIgnored private var networkWasSatisfied: Bool?
    /// Held while polling, to keep macOS App Nap from freezing the poll loop's
    /// timer when the window is in the background. Exposed only so a test can
    /// assert the start/stop contract.
    @ObservationIgnored private(set) var pollingActivity: NSObjectProtocol?

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
            reloadSnippets()
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

    /// The stored server with this id, if it still exists.
    public func server(id: UUID) -> Server? { servers.first { $0.id == id } }

    func hasServer(_ id: UUID) -> Bool { servers.contains { $0.id == id } }

    public func addServer(_ server: Server) throws {
        try database.save(server)
        reload()
    }

    public func updateServer(_ server: Server) throws {
        let previous = self.server(id: server.id).flatMap { try? target(for: $0) }
        // Address or credential may have changed; drop the multiplexed
        // connection so the next poll reconnects with the new settings.
        invalidate(serverID: server.id, previous: previous)
        try database.save(server)
        // Editing a failing host is usually the fix for it. Sitting out a
        // five-minute backoff afterwards would look like the edit did nothing.
        noteSuccess(serverID: server.id)
        reload()
    }

    /// Records a looked-up country without touching any other column. The
    /// card that asks holds a copy of the row that may be a poll behind, and
    /// saving that copy back undid the poll's facts and probed OS. Nor is a
    /// lookup a sign the host answered, so the backoff is left alone.
    public func updateCountryCode(serverID: UUID, countryCode: String) throws {
        try database.updateCountryCode(serverID: serverID, countryCode: countryCode)
        reload()
    }

    /// Reorders the sidebar and the dashboard together: both draw `servers` in
    /// `sortIndex` order, and that order is persisted so it survives a relaunch.
    public func moveServers(fromOffsets source: IndexSet, toOffset destination: Int) throws {
        var reordered = servers
        reordered.move(fromOffsets: source, toOffset: destination)
        try database.reorderServers(reordered.map(\.id))
        reload()
    }

    public func deleteServer(_ server: Server) throws {
        let target = try? target(for: server)
        invalidate(serverID: server.id, previous: target)
        try database.deleteServer(id: server.id)
        serverDidDelete?(server.id)
        alerts?.forget(serverID: server.id)
        latest.removeValue(forKey: server.id)
        status.removeValue(forKey: server.id)
        detailedServers.remove(server.id)
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

    /// Saved commands. A stored, observed property rather than a query, so the
    /// terminal's snippet menu sees a save or a delete the moment it happens.
    public private(set) var snippets: [Snippet] = []

    public func save(_ snippet: Snippet) throws {
        try database.save(snippet)
        reloadSnippets()
    }

    public func deleteSnippet(id: UUID) throws {
        try database.deleteSnippet(id: id)
        reloadSnippets()
    }

    private func reloadSnippets() {
        snippets = (try? database.allSnippets()) ?? []
    }

    /// Runs a snippet on a host and returns its combined output.
    public func run(snippet: Snippet, on server: Server) async throws -> String {
        let target = try target(for: server)
        try? database.markSnippetUsed(id: snippet.id)
        reloadSnippets()
        return try await runner.run("\(snippet.command) 2>&1", on: target, timeout: 120)
    }

    /// One command over the server's pooled connection, for cards that fetch
    /// something the metrics poll does not carry.
    public func run(_ command: String, on server: Server, timeout: Int = 30) async throws -> String {
        try await runner.run(command, on: target(for: server), timeout: timeout)
    }

    public func save(_ identity: Identity) throws {
        let previous = servers.filter { $0.identityID == identity.id }.compactMap { try? target(for: $0) }
        for target in previous { invalidate(serverID: target.serverID, previous: target) }
        try database.save(identity)
        reload()
    }

    public func deleteIdentity(id: UUID) throws {
        let previous = servers.filter { $0.identityID == id }.compactMap { try? target(for: $0) }
        for target in previous { invalidate(serverID: target.serverID, previous: target) }
        try database.deleteIdentity(id: id)
        reload()
    }

    /// Invalidate before allowing another poll to start. A cancelled SSH task
    /// can still finish, so its generation is checked at every publish boundary.
    private func invalidate(serverID: UUID, previous: SSHTarget?) {
        // Revoke queued writes before saving edited connection fields. A write
        // already executing completes before this returns; a queued one cannot
        // populate the new target with the previous target's facts.
        writeValidity.removeValue(forKey: serverID)?.invalidate()
        generations[serverID, default: 0] &+= 1
        pollTasks.removeValue(forKey: serverID)?.cancel()
        inFlight.remove(serverID)
        tickMembers.remove(serverID)
        pending.removeAll { $0.serverID == serverID }
        latest.removeValue(forKey: serverID)
        status.removeValue(forKey: serverID)
        dockerSummaries.removeValue(forKey: serverID)
        live.forget(serverID)
        noteSuccess(serverID: serverID)
        if let previous {
            let preceding = resetTasks[serverID]
            resetTasks[serverID] = Task { [collector] in
                await preceding?.value
                await collector.forget(target: previous)
            }
        }
        if tickOpen && tickMembers.isEmpty { closeTick(reason: "invalidated") }
    }

    private func isCurrent(_ serverID: UUID, generation: UInt64) -> Bool {
        hasServer(serverID) && generations[serverID, default: 0] == generation && !Task.isCancelled
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
        let generation = generations[server.id, default: 0]
        guard let summary = try? await docker.summary(target: target) else { return }
        guard isCurrent(server.id, generation: generation) else { return }
        dockerSummaries[server.id] = summary
    }

    /// Refreshes the engine summary for every host that reports Docker.
    public func refreshDockerSummaries() async {
        let hosts = servers.filter(\.hasDocker)
        await withTaskGroup(of: (UUID, UInt64, DockerSummary?).self) { group in
            for host in hosts {
                let generation = generations[host.id, default: 0]
                group.addTask { [weak self] in
                    guard let self, let target = try? await self.target(for: host) else {
                        return (host.id, generation, nil)
                    }
                    return (host.id, generation, try? await self.docker.summary(target: target))
                }
            }
            for await (id, generation, summary) in group where isCurrent(id, generation: generation) {
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
        watchWake()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                // Fire and move on. Awaiting the whole fleet here coupled every
                // host's cadence to the slowest: one unreachable host (10 s
                // connect timeout) stretched everyone's 5 s interval to 15 s,
                // and a hung one (30 s watchdog) to 35 s.
                _ = self.pollDue()
                let interval = Self.effectiveInterval(
                    self.settings.pollInterval,
                    lowPower: ProcessInfo.processInfo.isLowPowerModeEnabled
                )
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

    /// The gap between ticks. In Low Power Mode the user has asked the whole
    /// Mac to do less; nine ssh round trips every five seconds is exactly the
    /// kind of background work that request is about, so the cadence drops
    /// to a third. Read on every tick, so flipping the switch takes effect at
    /// the next one.
    static func effectiveInterval(_ configured: TimeInterval, lowPower: Bool) -> TimeInterval {
        lowPower ? configured * 3 : configured
    }

    public func stop() {
        for validity in writeValidity.values { validity.invalidate() }
        writeValidity.removeAll()
        for server in servers { generations[server.id, default: 0] &+= 1 }
        for task in pollTasks.values { task.cancel() }
        pollTasks.removeAll()
        inFlight.removeAll()
        pollTask?.cancel()
        maintenanceTask?.cancel()
        pollTask = nil
        maintenanceTask = nil
        pathMonitor?.cancel()
        pathMonitor = nil
        networkWasSatisfied = nil
        if let wakeObserver { Self.wakeNotificationCenter.removeObserver(wakeObserver) }
        wakeObserver = nil
        tickCapTask?.cancel()
        tickCapTask = nil
        tickMembers.removeAll()
        // A stopped service publishes nothing further, so what an open tick
        // was holding goes with it; and a restart begins with a clean backoff.
        pending.removeAll()
        failureStreak.removeAll()
        retryAfter.removeAll()
        if let pollingActivity {
            ProcessInfo.processInfo.endActivity(pollingActivity)
            self.pollingActivity = nil
        }
        // Multiplexed connections close themselves once ControlPersist lapses.
    }

    /// Servers whose previous poll has not returned yet. A host that is slow
    /// to answer is polled once, not once per tick, so a stall cannot pile up
    /// a queue of ssh processes behind it.
    @ObservationIgnored private(set) var inFlight: Set<UUID> = []

    /// Servers whose machine screen is on screen. Only they get the process
    /// list, the one part of a poll the dashboard never shows and the host
    /// pays ~30 ms of CPU for.
    @ObservationIgnored private(set) var detailedServers: Set<UUID> = []

    /// Called by the machine screen as it appears and disappears. Opening one
    /// polls the host at once, so the process card fills in about a second
    /// rather than waiting out the rest of the current interval.
    @discardableResult
    public func setDetailVisible(_ serverID: UUID, _ visible: Bool) -> Task<Void, Never>? {
        if visible {
            detailedServers.insert(serverID)
            return pollNow(serverID)
        }
        detailedServers.remove(serverID)
        return nil
    }

    /// One host, now, outside the tick — for a screen that has just opened.
    /// Nothing if it is already being polled; that poll is seconds from done.
    @discardableResult
    func pollNow(_ serverID: UUID) -> Task<Void, Never>? {
        guard let server = server(id: serverID), !inFlight.contains(serverID) else { return nil }
        return launchPoll(server)
    }

    private func launchPoll(_ server: Server) -> Task<Void, Never> {
        let generation = generations[server.id, default: 0]
        inFlight.insert(server.id)
        let task = Task { [weak self] in
            guard let self else { return }
            await self.poll(server, generation: generation)
            guard self.generations[server.id, default: 0] == generation else { return }
            self.pollTasks[server.id] = nil
            self.pollFinished(server.id)
        }
        pollTasks[server.id] = task
        return task
    }

    static let log = Logger(subsystem: "com.hmchxd.ServerMonitor", category: "polling")

    // MARK: - One tick, one publish

    /// Servers launched by the tick in progress whose polls have not returned.
    @ObservationIgnored private(set) var tickMembers: Set<UUID> = []
    /// Fires `tickCap` after the tick opened; nil whenever no tick is open,
    /// which is what `tickOpen` reads.
    @ObservationIgnored private var tickCapTask: Task<Void, Never>?
    /// True from the moment a tick launches its polls until the last of them
    /// returns or `tickCap` elapses. Results arriving meanwhile are held.
    var tickOpen: Bool { tickCapTask != nil }

    /// Longest a tick's results are held for a straggler before what has
    /// arrived is shown anyway. Measured spread between the first and last
    /// host finishing one tick: p50 1.0 s, p95 1.6 s. A host that has gone
    /// away takes the full 10 s connect timeout, so the cap is what keeps one
    /// dead host from delaying eight live ones.
    @ObservationIgnored var tickCap: Duration = .seconds(2)

    /// Holds publishing until every poll this tick launched has answered.
    ///
    /// A 250 ms window on its own gave 3.4 publishes per tick, measured over
    /// 221 ticks: hosts finish about a second apart (latency differs by
    /// 200 ms and the script itself samples twice, 0.5 s apart), so the first
    /// result opened a window, the next few closed it, and the stragglers each
    /// opened another. Every publish is a full dashboard pass, so that was
    /// three layouts to show one tick's numbers. The tick knows exactly which
    /// polls it started; waiting for those turns it into one.
    func openTick(members: Set<UUID>) {
        tickMembers.formUnion(members)
        guard !tickOpen else { return }
        let cap = tickCap
        tickCapTask = Task { [weak self] in
            try? await Task.sleep(for: cap)
            guard let self, !Task.isCancelled else { return }
            self.closeTick(reason: "cap")
        }
    }

    func pollFinished(_ serverID: UUID) {
        inFlight.remove(serverID)
        tickMembers.remove(serverID)
        if tickOpen && tickMembers.isEmpty { closeTick(reason: "complete") }
    }

    private func closeTick(reason: StaticString) {
        tickCapTask?.cancel()
        tickCapTask = nil
        // A straggler the cap gave up on must not be carried into the next
        // tick: it stays in flight for its whole timeout, and with it still a
        // member every later tick could only close by the cap — eight live
        // hosts waiting 2 s each round for the one that is gone.
        tickMembers.removeAll()
        Self.log.debug("tick closed (\(reason, privacy: .public)), \(self.pending.count) results held")
        // Hidden: leave them queued for `setUIVisible(true)`, exactly as a
        // result arriving outside a tick would be.
        if uiIsVisible { flushPending() }
    }

    // MARK: - Failure backoff

    /// Consecutive failed polls per server, reset by any success.
    @ObservationIgnored private(set) var failureStreak: [UUID: Int] = [:]
    /// Earliest time `pollDue` will try a server again.
    @ObservationIgnored private(set) var retryAfter: [UUID: Date] = [:]

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

    /// Watches for the Mac waking from sleep.
    ///
    /// The network path is often "satisfied" before anything actually routes
    /// — the link is up, DHCP and DNS are not — so the first ticks after a
    /// lid-open fail without the path monitor ever seeing a transition to
    /// clear the backoff they build. Waking is its own signal: the fleet
    /// almost certainly did not go down while the lid was shut.
    private func watchWake() {
        guard wakeObserver == nil else { return }
        wakeObserver = Self.wakeNotificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: nil
        ) { [weak self] _ in
            Task { @MainActor in
                guard let self, self.wakeObserver != nil else { return }
                self.systemDidWake()
            }
        }
    }

    static var wakeNotificationCenter: NotificationCenter { NSWorkspace.shared.notificationCenter }

    /// Forgets every backoff and polls at once, so the dashboard is current a
    /// few seconds after the lid opens rather than after the longest backoff
    /// runs out.
    func systemDidWake() {
        Self.log.info("woke from sleep; retrying the whole fleet")
        failureStreak.removeAll()
        retryAfter.removeAll()
        _ = pollDue()
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
            launched.insert(server.id)
            return launchPoll(server)
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
        // returns — and regardless of visibility, since tests and the manual
        // refresh both rely on it. Unless everything was already in flight:
        // then this refresh joined a tick still in progress, and flushing here
        // would publish half of it; the tick's own close publishes the rest.
        if !tickOpen { flushPending() }
    }

    public func poll(_ server: Server) async {
        await poll(server, generation: generations[server.id, default: 0])
    }

    private func poll(_ server: Server, generation: UInt64) async {
        // A task created by `pollDue` starts at the next suspension point, so a
        // server can be deleted between the tick and this line; without the
        // check it came back as a `.polling` entry for a row that no longer
        // exists.
        guard isCurrent(server.id, generation: generation) else { return }
        let validity = writeValidity[server.id] ?? PollWriteValidity()
        writeValidity[server.id] = validity
        await resetTasks[server.id]?.value
        guard isCurrent(server.id, generation: generation) else { return }
        if status[server.id] == nil || status[server.id] == .unknown {
            commit(for: server.id) { self.status[server.id] = .polling }
        }
        do {
            let target = try target(for: server)
            let snapshot: MetricSnapshot
            if let collectOverride {
                snapshot = try await collectOverride(target, server.osKind, detailedServers.contains(server.id))
            } else {
                snapshot = try await collector.collect(
                    target: target, osKind: server.osKind, processes: detailedServers.contains(server.id)
                )
            }
            // Deleted while this poll was in flight: nothing to record, and the
            // sample insert would fail its foreign key anyway.
            guard isCurrent(server.id, generation: generation) else { return }

            // Remember a probed OS so later polls skip straight to the right
            // script instead of trying Linux every time. Decided against the
            // row as it is now, not the copy this poll started with: the user
            // may have set the OS by hand while the poll ran.
            let current = self.server(id: server.id) ?? server
            let detected = current.osKind == .auto ? snapshot.detectedOS : nil
            // Facts change once (the first poll) or never, so the row is only
            // touched when one did; the common case is a single INSERT.
            let factsChanged = current.cores != snapshot.cores
                || current.memoryTotal != snapshot.memoryTotal
                || current.diskTotal != snapshot.diskTotal
                || current.dockerVersion != snapshot.dockerVersion
            // Durable writes go straight to the store; they publish nothing.
            // One transaction, awaited on GRDB's own queue rather than on this
            // actor: three separate writes per host per poll were running on
            // the thread that draws.
            try await database.recordPoll(
                serverID: server.id, snapshot: snapshot, detectedOS: detected, updateFacts: factsChanged,
                validity: validity
            )
            // The write suspended us; the server may be gone by now.
            guard isCurrent(server.id, generation: generation) else { return }

            // Everything the UI observes, in one batch.
            let now = Date()
            noteSuccess(serverID: server.id)
            live.update(serverID: server.id, status: .online(at: now), snapshot: snapshot)
            commit(for: server.id) {
                guard self.generations[server.id, default: 0] == generation else { return }
                self.latest[server.id] = snapshot
                self.status[server.id] = .online(at: now)
                // Free with the poll, so the machine screen and the Docker page
                // both stay live without their own round trip.
                if let summary = snapshot.dockerSummary {
                    self.dockerSummaries[server.id] = summary
                } else {
                    // Docker can disappear, lose permissions, or stop between
                    // polls. Do not leave the previous engine card visible as
                    // if it were still current.
                    self.dockerSummaries.removeValue(forKey: server.id)
                }
                // Only when something moved: assigning identical values still
                // publishes `servers` on every poll.
                if factsChanged || detected != nil,
                   let index = self.servers.firstIndex(where: { $0.id == server.id }) {
                    if let detected, self.servers[index].osKind == .auto {
                        self.servers[index].osKind = detected
                    }
                    self.servers[index].cores = snapshot.cores
                    self.servers[index].memoryTotal = snapshot.memoryTotal
                    self.servers[index].diskTotal = snapshot.diskTotal
                    self.servers[index].dockerVersion = snapshot.dockerVersion
                }
            }
            // The user may have edited the name or per-host thresholds while
            // this SSH round was in flight. Notifications must use the row as
            // it is now, not the stale value captured when polling started.
            let currentForAlert = self.server(id: server.id) ?? server
            alerts?.evaluate(server: currentForAlert, status: .online(at: now), snapshot: snapshot)
        } catch {
            // A cancelled poll says nothing about the host.
            if error is CancellationError { return }
            guard isCurrent(server.id, generation: generation) else { return }
            noteFailure(serverID: server.id)
            Self.log.info("\(server.name, privacy: .public): \(error.localizedDescription, privacy: .public)")
            let failure = ServerStatus.offline(reason: error.localizedDescription)
            live.update(serverID: server.id, status: failure, snapshot: nil)
            commit(for: server.id) {
                guard self.generations[server.id, default: 0] == generation else { return }
                self.status[server.id] = failure
            }
            let currentForAlert = self.server(id: server.id) ?? server
            alerts?.evaluate(server: currentForAlert, status: failure, snapshot: nil)
        }
    }

    // MARK: - Publishing

    /// Results waiting to be applied together — at most one per server, the
    /// newest. Replaced on arrival rather than de-duplicated at flush so this
    /// stays bounded by the fleet: with the window closed (the app's normal
    /// menu-bar mode) an append-only queue grew by one closure holding a full
    /// snapshot per host per poll — tens of megabytes an hour — until the
    /// window was next shown.
    @ObservationIgnored private(set) var pending: [(serverID: UUID, apply: () -> Void)] = []

    /// False while every window is hidden or fully covered.
    ///
    /// SwiftUI keeps evaluating and laying out views when a window is occluded
    /// — AppKit only skips the drawing. Profiling nine hosts with the window
    /// behind Finder put ~11% of a core in `sizeThatFits` and
    /// `StackLayout.placeChildren` for a dashboard nobody could see. Holding
    /// results back while nothing is visible removes that; collection, the
    /// database and alerts are unaffected, and `setUIVisible(true)` applies
    /// whatever accumulated so the first frame shown is current.
    @ObservationIgnored private(set) var uiIsVisible = true

    /// Called from the app when window occlusion changes.
    public func setUIVisible(_ visible: Bool) {
        guard visible != uiIsVisible else { return }
        uiIsVisible = visible
        if visible { flushPending() }
    }

    /// Publishes a result now, or holds it for the tick.
    ///
    /// Every dashboard card observes this object, so each published change is
    /// a full pass over the dashboard. Results are held while a tick is in
    /// progress and applied together when it closes (see `openTick`). Two
    /// kinds go straight through: a result for a host with nothing on screen
    /// yet — a spinner turning into numbers is worth its own pass, once — and
    /// one arriving with no tick open, a straggler whose tick the cap already
    /// closed. Nothing is applied while every window is hidden.
    func commit(for serverID: UUID, _ apply: @escaping @MainActor () -> Void) {
        guard uiIsVisible else { hold(serverID, apply); return }
        guard tickOpen, hasContent(serverID) else { apply(); return }
        hold(serverID, apply)
    }

    private func hold(_ serverID: UUID, _ apply: @escaping @MainActor () -> Void) {
        if let index = pending.firstIndex(where: { $0.serverID == serverID }) {
            pending[index].apply = apply
        } else {
            pending.append((serverID, apply))
        }
    }

    /// Whether the card for this server shows anything but a spinner: a
    /// snapshot, or a verdict. A host that keeps failing has a verdict on
    /// screen, so its next failure waits for the tick like any other update.
    private func hasContent(_ serverID: UUID) -> Bool {
        if latest[serverID] != nil { return true }
        switch status[serverID] {
        case .online, .offline: return true
        case .polling, .unknown, nil: return false
        }
    }

    /// Applies everything held, as one change. Results for a server deleted in
    /// the meantime are dropped rather than resurrecting it.
    func flushPending() {
        guard !pending.isEmpty else { return }
        let batch = pending
        pending.removeAll()
        for item in batch where hasServer(item.serverID) { item.apply() }
        Self.log.debug("published \(batch.count) results")
    }

    /// Servers that failed their last poll, for the menu bar summary.
    public var offlineServers: [Server] {
        servers.filter {
            if case .offline = live.readings[$0.id]?.status ?? .unknown { return true }
            return false
        }
    }

    /// Highest CPU currently reported, or nil when nothing has reported yet.
    public var peakCPU: (server: Server, percent: Double)? {
        servers
            .compactMap { server -> (server: Server, percent: Double)? in
                guard let percent = live.readings[server.id]?.cpuPercent else { return nil }
                return (server, percent)
            }
            .max { $0.1 < $1.1 }
    }

    /// One-shot connectivity check for the editor's "test connection" button.
    public func testConnection(osKind: OSKind = .auto, target: SSHTarget, password: String? = nil) async throws {
        try await runner.testConnection(on: target, password: password)
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
