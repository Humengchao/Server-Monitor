import Foundation
import Testing
@testable import ServerMonitorKit

/// One tick of polls becomes one published change, however far apart the
/// hosts answer. Measured before: 3.4 full dashboard passes per tick, because
/// hosts finish about a second apart and the 250 ms window kept re-opening.
@Suite("Tick barrier")
@MainActor
struct TickBarrierTests {

    private func service() throws -> MonitorService {
        MonitorService(database: try Database(inMemory: true), settings: AppSettings())
    }

    /// Connection refused at once: a poll that finishes in milliseconds.
    private func refusing(_ name: String) -> Server {
        Server(name: name, host: "127.0.0.1", port: 1, username: "u", authKind: .agent)
    }

    /// Unroutable: a poll that hangs on the connect timeout.
    private func hanging(_ name: String) -> Server {
        Server(name: name, host: "10.255.255.1", port: 22, username: "u", authKind: .agent)
    }

    /// A server `pollDue` will skip: it exists, but is sitting out a backoff.
    /// Its results are held like any other and survive the per-server
    /// de-duplication, which a polled server's would not.
    private func bystander(in monitor: MonitorService, _ name: String = "bystander") throws -> UUID {
        try monitor.addServer(refusing(name))
        let id = try #require(monitor.servers.last?.id)
        for _ in 1...3 { monitor.noteFailure(serverID: id) }
        return id
    }

    @Test func aTickHoldsResultsUntilItsLastPollReturns() async throws {
        let monitor = try service()
        try monitor.addServer(refusing("a"))
        try monitor.addServer(refusing("b"))
        let bystander = try bystander(in: monitor)
        var applied = 0

        let tasks = monitor.pollDue()
        #expect(monitor.tickOpen, "launching polls opens the tick")
        #expect(monitor.tickMembers.count == 2, "the backed-off host was not launched")

        // The tasks have not run yet — nothing has suspended — so this result
        // arrives mid-tick and must wait for the tick, not publish alone.
        monitor.commit(for: bystander) { applied += 1 }
        #expect(applied == 0, "mid-tick results are held")

        for task in tasks { await task.value }
        #expect(!monitor.tickOpen, "the last poll closes the tick")
        #expect(monitor.tickMembers.isEmpty)
        #expect(applied == 1, "published when the tick closed")
        #expect(monitor.status.count == 2, "both polled results landed in the one publish")
    }

    @Test func theCapReleasesATickAStragglerIsHoldingUp() async throws {
        let monitor = try service()
        monitor.tickCap = .milliseconds(150)
        try monitor.addServer(hanging("slow"))
        let bystander = try bystander(in: monitor)
        var applied = 0

        let tasks = monitor.pollDue()
        defer { for task in tasks { task.cancel() } }
        monitor.commit(for: bystander) { applied += 1 }
        #expect(applied == 0)

        // The hanging host will sit on its 10 s connect timeout. Eight live
        // hosts must not wait for it: the cap publishes what has arrived.
        try await Task.sleep(for: .milliseconds(400))
        #expect(!monitor.tickOpen, "the cap closed the tick")
        #expect(applied == 1, "held results were published at the cap")
        #expect(monitor.inFlight.count == 1, "the straggler is still polling")
    }

    @Test func theWindowExpiringMidTickDoesNotSplitTheTick() async throws {
        // Before the barrier: a result outside a tick applied at once and
        // opened a 250 ms window; the window's expiry then published whatever
        // had queued — even if a tick was now in progress, splitting it.
        let monitor = try service()
        monitor.tickCap = .milliseconds(700)
        try monitor.addServer(hanging("slow"))
        let bystander = try bystander(in: monitor)
        var applied = 0

        monitor.commit(for: bystander) { applied += 1 }
        #expect(applied == 1, "a lone result outside any tick is not delayed")

        let tasks = monitor.pollDue()
        defer { for task in tasks { task.cancel() } }
        monitor.commit(for: bystander) { applied += 1 }

        try await Task.sleep(for: .milliseconds(400))      // the 250 ms window has expired
        #expect(applied == 1, "the window expiring mid-tick must not publish")
        #expect(monitor.tickOpen)

        try await Task.sleep(for: .milliseconds(500))      // now past the cap
        #expect(applied == 2, "the tick's close published it")
    }

    @Test func aManualRefreshMidTickJoinsIt() throws {
        let monitor = try service()
        try monitor.addServer(hanging("slow"))
        let first = monitor.pollDue()
        defer { for task in first { task.cancel() } }
        #expect(monitor.tickOpen)

        // Everything is already in flight: nothing new launches, and the open
        // tick is left as it is rather than restarted.
        let second = monitor.pollDue(ignoringBackoff: true)
        #expect(second.isEmpty)
        #expect(monitor.tickMembers.count == 1)
    }

    @Test func hiddenTicksStayQueuedForTheWindow() async throws {
        let monitor = try service()
        try monitor.addServer(refusing("a"))
        monitor.setUIVisible(false)

        for task in monitor.pollDue() { await task.value }
        #expect(!monitor.tickOpen)
        #expect(monitor.status.isEmpty, "hidden: the tick closing must not publish")
        #expect(!monitor.pending.isEmpty)

        monitor.setUIVisible(true)
        #expect(monitor.status.count == 1, "showing the window applies what the tick held")
    }

    @Test func stopClearsAnOpenTick() throws {
        let monitor = try service()
        try monitor.addServer(hanging("slow"))
        let tasks = monitor.pollDue()
        defer { for task in tasks { task.cancel() } }
        #expect(monitor.tickOpen)
        monitor.stop()
        #expect(!monitor.tickOpen)
        #expect(monitor.tickMembers.isEmpty)
    }
}
