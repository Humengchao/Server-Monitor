import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Database")
struct DatabaseTests {

    @Test func saveAndFetchServer() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .sshConfigAlias)
        try database.save(server)

        let fetched = try database.allServers()
        #expect(fetched.count == 1)
        #expect(fetched.first?.name == "web-1")
        #expect(fetched.first?.port == 22)
    }

    @Test func samplesAreScopedByServerAndRange() throws {
        let database = try Database(inMemory: true)
        let a = Server(name: "a", host: "1.1.1.1", username: "root", authKind: .sshConfigAlias)
        let b = Server(name: "b", host: "2.2.2.2", username: "root", authKind: .sshConfigAlias)
        try database.save(a)
        try database.save(b)

        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = 42
        let now = Date()
        try database.insert(MetricSample(serverID: a.id, timestamp: now.addingTimeInterval(-30), snapshot: snapshot))
        try database.insert(MetricSample(serverID: a.id, timestamp: now.addingTimeInterval(-7200), snapshot: snapshot))
        try database.insert(MetricSample(serverID: b.id, timestamp: now, snapshot: snapshot))

        let recent = try database.samples(serverID: a.id, since: now.addingTimeInterval(-600))
        #expect(recent.count == 1, "the two-hour-old sample and server b must be excluded")
        #expect(recent.first?.cpuPercent == 42)
    }

    @Test func deletingServerCascadesItsHistory() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .sshConfigAlias)
        try database.save(server)
        try database.insert(MetricSample(serverID: server.id, snapshot: MetricSnapshot()))

        try database.deleteServer(id: server.id)

        #expect(try database.allServers().isEmpty)
        #expect(try database.samples(serverID: server.id, since: .distantPast).isEmpty)
    }

    @Test func pruneDropsOnlyOldSamples() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .sshConfigAlias)
        try database.save(server)
        let now = Date()
        try database.insert(MetricSample(serverID: server.id, timestamp: now, snapshot: MetricSnapshot()))
        try database.insert(MetricSample(
            serverID: server.id,
            timestamp: now.addingTimeInterval(-86_400 * 10),
            snapshot: MetricSnapshot()
        ))

        let deleted = try database.pruneHistory(retention: 86_400 * 7)

        #expect(deleted == 1)
        #expect(try database.samples(serverID: server.id, since: .distantPast).count == 1)
    }

    @Test func aPollRecordsItsSampleAndOnlyTheFactsItWasToldMoved() async throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .sshConfigAlias)
        try database.save(server)

        var snapshot = MetricSnapshot()
        snapshot.cores = 4
        snapshot.memoryTotal = 1024
        snapshot.diskTotal = 2048
        snapshot.dockerVersion = "24.0.7"
        try await database.recordPoll(serverID: server.id, snapshot: snapshot, detectedOS: nil, updateFacts: true)
        let stored = try #require(try database.allServers().first)
        #expect(stored.cores == 4)
        #expect(stored.dockerVersion == "24.0.7")
        #expect(try database.samples(serverID: server.id, since: .distantPast).count == 1)

        // The common case: the caller saw nothing move, so the row is not
        // touched — only the sample lands.
        snapshot.cores = 8
        try await database.recordPoll(serverID: server.id, snapshot: snapshot, detectedOS: nil, updateFacts: false)
        #expect(try database.allServers().first?.cores == 4, "facts are the caller's call")
        #expect(try database.samples(serverID: server.id, since: .distantPast).count == 2)
    }

    @Test func snapshotRoundTripsThroughStorage() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .sshConfigAlias)
        try database.save(server)

        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = 12.5
        snapshot.memoryUsed = 2_048
        snapshot.memoryTotal = 8_192
        snapshot.netRxRate = 1_500.5
        snapshot.uptimeSeconds = 98_765
        try database.insert(MetricSample(serverID: server.id, snapshot: snapshot))

        let stored = try database.latestSample(serverID: server.id)?.snapshot
        #expect(stored?.cpuPercent == 12.5)
        #expect(stored?.memoryPercent == 25)
        #expect(stored?.netRxRate == 1_500.5)
        #expect(stored?.uptimeSeconds == 98_765)
    }
}

@Suite("Legacy data")
struct LegacyDataTests {

    /// A row written by an older build must not take the whole list with it.
    @Test func unknownAuthKindDecodesToAFallback() throws {
        let database = try Database(inMemory: true)
        let good = Server(name: "new", host: "10.0.0.1", username: "root", authKind: .agent)
        try database.save(good)

        // Simulate a row left behind by a previous schema.
        try database.queue.write { db in
            try db.execute(sql: """
                INSERT INTO server (id, name, host, port, username, authKind, notes, countryCode,
                                    createdAt, sortIndex, cores, memoryTotal, diskTotal,
                                    dockerVersion, sshAlias, identityFile)
                VALUES (randomblob(16), 'legacy', '10.0.0.2', 22, 'root', 'privateKey', '', '',
                        '2026-01-01 00:00:00.000', 1, 0, 0, 0, '', '', '')
                """)
        }

        let servers = try database.allServers()
        #expect(servers.count == 2, "the legacy row must not break the fetch")
        #expect(servers.contains { $0.name == "legacy" })
        #expect(servers.first { $0.name == "legacy" }?.authKind == .sshConfigAlias)
    }
}

@Suite("Localization table")
struct LocalizationTableTests {

    @Test func everyKeyResolvesInBothLanguages() {
        // A duplicate key in the table traps in Dictionary's literal
        // initialiser — not at build time, and not on the first *test*, but on
        // the app's first string lookup, which is during launch. Touching the
        // table at all is what makes that a test failure instead of a crash on
        // the user's machine.
        let localization = Localization()
        for language in [AppLanguage.zh, .en] {
            localization.language = language
            for key in Localization.knownKeys {
                #expect(
                    localization.t(key).isEmpty == false,
                    "\(key) is empty in \(language.rawValue)"
                )
            }
        }
    }

    @Test func chineseAndEnglishAreActuallyDifferentTables() {
        // A copy-paste that left both columns identical would make the language
        // switch look broken; a handful of keys are legitimately the same
        // ("CPU", "Docker"), so this only asserts the tables are not wholesale
        // identical.
        let localization = Localization()
        localization.language = .zh
        let chinese = Localization.knownKeys.map { localization.t($0) }
        localization.language = .en
        let english = Localization.knownKeys.map { localization.t($0) }
        let differing = zip(chinese, english).count { $0 != $1 }
        #expect(differing > Localization.knownKeys.count / 2)
    }
}

@Suite("Background polling")
@MainActor
struct PollingActivityTests {

    private func service() throws -> MonitorService {
        MonitorService(database: try Database(inMemory: true), settings: AppSettings())
    }

    @Test func startHoldsAnActivityAndStopReleasesIt() throws {
        // The activity is what keeps App Nap from freezing the poll timer in
        // the background; without the token there is no protection. With no
        // servers loaded the poll loop does nothing, so this exercises only the
        // start/stop lifecycle.
        let monitor = try service()
        #expect(monitor.pollingActivity == nil)
        monitor.start()
        #expect(monitor.pollingActivity != nil, "polling must hold an activity")
        monitor.stop()
        #expect(monitor.pollingActivity == nil, "stopping must release it, or the Mac never naps")
    }

    @Test func startIsIdempotentAndDoesNotLeakActivities() throws {
        // A second start() while already running must not replace the token and
        // orphan the first — that would leave an activity that stop() cannot
        // end.
        let monitor = try service()
        monitor.start()
        let first = monitor.pollingActivity
        monitor.start()
        #expect(monitor.pollingActivity === first, "start() while running must be a no-op")
        monitor.stop()
    }

    @Test func stopWithoutStartIsHarmless() throws {
        let monitor = try service()
        monitor.stop()
        #expect(monitor.pollingActivity == nil)
    }

    /// A host nothing listens on: ssh fails with "connection refused" in
    /// milliseconds, so a poll completes fast and offline.
    private func refusingServer() -> Server {
        Server(name: "refused", host: "127.0.0.1", port: 1, username: "nobody", authKind: .agent)
    }

    @Test func aHostIsPolledOnceWhileItsPreviousPollIsStillRunning() async throws {
        // The whole point of the per-host cadence: a tick that arrives while a
        // slow host is still answering must not start a second ssh behind the
        // first. Before this, every tick awaited the slowest host instead.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let id = monitor.servers[0].id

        let first = monitor.pollDue()
        #expect(first.count == 1)
        #expect(monitor.inFlight.contains(id))

        let second = monitor.pollDue()
        #expect(second.isEmpty, "the host is already being polled; the tick must skip it")

        for task in first { await task.value }
        #expect(monitor.inFlight.isEmpty, "a finished poll must release its slot")
        // `pollDue` results are coalesced (see `commit`); only `pollAll` promises
        // they are visible on return. Flush the way it does before looking.
        monitor.flushPending()
        if case .offline = monitor.status[id] ?? .unknown {} else {
            Issue.record("expected the refused host to be offline, got \(String(describing: monitor.status[id]))")
        }

        #expect(monitor.pollDue().count == 1, "and the next tick polls it again")
        for task in monitor.pollDue() { await task.value }
    }

    @Test func pollDueWithNoServersStartsNothing() throws {
        let monitor = try service()
        #expect(monitor.pollDue().isEmpty)
        #expect(monitor.inFlight.isEmpty)
    }

    @Test func aPollThatFinishesAfterItsServerWasDeletedRecordsNothing() async throws {
        // The poll started with a copy of the server; if it wrote its result
        // back, the deleted host reappeared in `status`/`latest` and the sample
        // insert failed its foreign key.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let server = monitor.servers[0]
        let tasks = monitor.pollDue()
        try monitor.deleteServer(server)
        for task in tasks { await task.value }
        #expect(monitor.status[server.id] == nil, "a deleted server must not get a status")
        #expect(monitor.latest[server.id] == nil)
        #expect(monitor.inFlight.isEmpty)
    }

    @Test func aLoneResultIsNeverDelayedAndABurstAppliesTogether() async throws {
        // The whole point: a burst of N results must become one published
        // change, not N — while a result with no tick open goes straight out.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let id = monitor.servers[0].id
        await monitor.pollAll()                              // a verdict on screen
        var applied = 0

        monitor.commit(for: id) { applied += 1 }
        #expect(applied == 1, "no tick open: the result must not wait")

        monitor.openTick(members: [UUID()])                 // a tick another host holds open
        for _ in 0..<5 { monitor.commit(for: id) { applied += 1 } }
        #expect(applied == 1, "results inside a tick are held")
        #expect(monitor.pending.count == 1, "held results collapse to the newest per server on arrival")

        monitor.flushPending()
        // Five snapshots of one host are five layout passes to show the last.
        #expect(applied == 2, "the flush applied the newest held result, once")
        #expect(monitor.pending.isEmpty)
    }

    @Test func flushingByHandAppliesWhatIsHeld() async throws {
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let id = monitor.servers[0].id
        await monitor.pollAll()
        var applied = 0
        monitor.openTick(members: [UUID()])
        monitor.commit(for: id) { applied += 1 }
        monitor.commit(for: id) { applied += 1 }
        #expect(applied == 0)
        monitor.flushPending()
        #expect(applied == 1, "pollAll relies on this to be deterministic")
    }

    @Test func heldResultsForADeletedServerAreDropped() async throws {
        let monitor = try service()
        try monitor.addServer(refusingServer())
        try monitor.addServer(Server(name: "other", host: "127.0.0.1", port: 1, username: "u", authKind: .agent))
        await monitor.pollAll()
        let doomed = monitor.servers[0]
        let kept = monitor.servers[1].id
        var doomedApplied = 0, keptApplied = 0
        monitor.openTick(members: [UUID()])
        monitor.commit(for: kept) { keptApplied += 1 }
        monitor.commit(for: doomed.id) { doomedApplied += 1 }
        monitor.commit(for: kept) { keptApplied += 1 }
        try monitor.deleteServer(doomed)
        monitor.flushPending()
        #expect(doomedApplied == 0, "a deleted server's held result must not resurrect it")
        #expect(keptApplied == 1)
    }

    @Test func nothingIsPublishedWhileEveryWindowIsHidden() async throws {
        // SwiftUI lays views out even when the window is covered; the profile
        // showed ~11% of a core in sizeThatFits for a dashboard nobody could
        // see. Results must queue instead.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let id = monitor.servers[0].id
        var applied = 0

        monitor.setUIVisible(false)
        for _ in 0..<4 { monitor.commit(for: id) { applied += 1 } }
        #expect(applied == 0, "hidden: nothing should reach the view tree")
        // Bounded by the fleet, not by how long the window has been closed: an
        // append-only queue grew by a snapshot-holding closure per host per
        // poll for as long as the app sat in the menu bar.
        #expect(monitor.pending.count == 1)

        // And no timer running in the background for nothing.
        try await Task.sleep(for: .milliseconds(400))
        #expect(applied == 0, "a flush fired while hidden")

        monitor.setUIVisible(true)
        #expect(applied == 1, "showing the window applies the latest per server, not all four")
        #expect(monitor.pending.isEmpty)
    }

    @Test func onlyTheNewestResultPerServerSurvivesTheQueue() throws {
        // A minute behind Finder queues a dozen snapshots per host; laying the
        // dashboard out a dozen times to arrive at the last one is pointless.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        try monitor.addServer(Server(name: "b", host: "127.0.0.1", port: 1, username: "u", authKind: .agent))
        let first = monitor.servers[0].id, second = monitor.servers[1].id
        var order: [String] = []

        monitor.setUIVisible(false)
        monitor.commit(for: first) { order.append("a1") }
        monitor.commit(for: second) { order.append("b1") }
        monitor.commit(for: first) { order.append("a2") }
        monitor.commit(for: second) { order.append("b2") }
        monitor.setUIVisible(true)
        #expect(order == ["a2", "b2"], "got \(order): newest per server, in first-seen order")
    }

    @Test func visibilityChangesAreIdempotent() throws {
        let monitor = try service()
        try monitor.addServer(refusingServer())
        let id = monitor.servers[0].id
        var applied = 0
        monitor.setUIVisible(true)                 // already visible
        monitor.commit(for: id) { applied += 1 }
        #expect(applied == 1)
        monitor.setUIVisible(false)
        monitor.setUIVisible(false)                // repeated: no flush
        monitor.commit(for: id) { applied += 1 }
        #expect(applied == 1)
        monitor.setUIVisible(true)
        #expect(applied == 2)
        monitor.setUIVisible(true)                 // repeated: nothing left
        #expect(applied == 2)
    }

    @Test func manualRefreshAppliesEvenWhileHidden() async throws {
        // The menu bar's refresh works with no window on screen.
        let monitor = try service()
        try monitor.addServer(refusingServer())
        monitor.setUIVisible(false)
        await monitor.pollAll()
        #expect(monitor.status[monitor.servers[0].id] != nil, "a manual refresh must still land")
    }

    @Test func manualRefreshWaitsForWhatItStarted() async throws {
        let monitor = try service()
        try monitor.addServer(refusingServer())
        await monitor.pollAll()
        #expect(monitor.inFlight.isEmpty, "pollAll returned before its own polls finished")
        #expect(monitor.status[monitor.servers[0].id] != nil)
    }
}

@Suite("Targeted OS update")
struct UpdateOSKindTests {
    @Test func recordingAProbedOSLeavesTheUsersEditsAlone() async throws {
        // The scenario: a poll started with a copy of the row, the user renamed
        // and tagged the server while it ran, and the poll then persisted what it
        // learned. Saving its stale copy back would undo the edits.
        let database = try Database(inMemory: true)
        var server = Server(name: "old", host: "h", username: "u", authKind: .agent, osKind: .auto)
        try database.save(server)

        server.name = "renamed"
        server.tags = ["prod"]
        try database.save(server)                       // the user's edit lands first

        try await database.recordPoll(                  // then the poll's
            serverID: server.id, snapshot: MetricSnapshot(), detectedOS: .linux, updateFacts: false
        )

        let stored = try #require(try database.allServers().first { $0.id == server.id })
        #expect(stored.osKind == .linux)
        #expect(stored.name == "renamed", "the poll clobbered the rename")
        #expect(stored.tags == ["prod"], "the poll clobbered the tags")
    }

    @Test func aProbedOSNeverOverridesOneTheUserSet() async throws {
        // The poll started while the row said automatic; the user chose
        // Windows before it finished. The probe's opinion loses.
        let database = try Database(inMemory: true)
        var server = Server(name: "box", host: "h", username: "u", authKind: .agent, osKind: .auto)
        try database.save(server)
        server.osKind = .windows
        try database.save(server)

        try await database.recordPoll(
            serverID: server.id, snapshot: MetricSnapshot(), detectedOS: .linux, updateFacts: false
        )
        #expect(try database.allServers().first?.osKind == .windows)
    }
}

@Suite("Startup fallback")
struct StartupFallbackTests {

    @Test func aScratchStoreOpensAndIsUsable() throws {
        // The launch path falls back to this when the on-disk store will not
        // open. It used to be reached with `try!`, so a failure here crashed
        // the app before it could say anything.
        let scratch = try #require(Database.scratch())
        // Fully migrated, not just opened: the window still renders lists.
        var server = Server(name: "s", host: "h", username: "u", authKind: .agent)
        server.tags = ["t"]
        try scratch.save(server)
        #expect(try scratch.allServers().count == 1)
        #expect(try scratch.allGroups().isEmpty)
        #expect(try scratch.allIdentities().isEmpty)
    }

    @Test func scratchStoresAreIndependent() throws {
        // Each is its own in-memory database; one must not see the other's rows.
        let a = try #require(Database.scratch())
        let b = try #require(Database.scratch())
        try a.save(Server(name: "only-in-a", host: "h", username: "u", authKind: .agent))
        #expect(try a.allServers().count == 1)
        #expect(try b.allServers().isEmpty)
    }

    @Test func openingAnUnwritablePathFailsRatherThanTrapping() {
        // The condition the fallback exists for: the real store cannot open.
        // It must surface as a thrown error, not a crash.
        #expect(throws: (any Error).self) {
            _ = try Database(url: URL(fileURLWithPath: "/dev/null/nope/monitor.sqlite"))
        }
    }
}

