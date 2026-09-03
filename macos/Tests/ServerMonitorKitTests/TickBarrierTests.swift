import Foundation
import Testing
@testable import ServerMonitorKit

/// One tick of polls becomes one published change, however far apart the
/// hosts answer. Measured before: 3.4 full dashboard passes per tick, because
/// hosts finish about a second apart and a 250 ms window kept re-opening.
///
/// The barrier is a small state machine over a set and a timer, so most of
/// these drive `openTick`/`pollFinished` directly; one test keeps the wiring
/// from `pollDue` honest with real (instantly refused) polls.
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

    /// A server with a verdict on screen, so its results are held rather than
    /// let through as a first result. `pollAll` is what gives it the verdict.
    private func settled(_ name: String, in monitor: MonitorService) async throws -> UUID {
        try monitor.addServer(refusing(name))
        let id = try #require(monitor.servers.last?.id)
        await monitor.pollAll()
        return id
    }

    @Test func pollDueOpensATickForExactlyWhatItLaunched() async throws {
        let monitor = try service()
        try monitor.addServer(refusing("a"))
        try monitor.addServer(refusing("b"))
        let resting = try await settled("resting", in: monitor)
        for _ in 1...3 { monitor.noteFailure(serverID: resting) }   // sits out this tick

        let tasks = monitor.pollDue()
        #expect(monitor.tickOpen, "launching polls opens the tick")
        #expect(monitor.tickMembers.count == 2, "the backed-off host was not launched, so it is not waited for")

        for task in tasks { await task.value }
        #expect(!monitor.tickOpen, "the last poll closes the tick")
        #expect(monitor.tickMembers.isEmpty)
        #expect(monitor.status.count == 3)
    }

    @Test func aTickHoldsResultsUntilItsLastPollReturns() async throws {
        let monitor = try service()
        let a = try await settled("a", in: monitor)
        let b = try await settled("b", in: monitor)
        var applied = 0

        monitor.openTick(members: [a, b])
        monitor.commit(for: a) { applied += 1 }
        monitor.pollFinished(a)
        #expect(applied == 0, "one poll back, one to go: still held")
        monitor.commit(for: b) { applied += 1 }
        monitor.pollFinished(b)
        #expect(applied == 2, "the last poll back publishes both together")
        #expect(!monitor.tickOpen)
    }

    @Test func aHostsFirstEverResultIsNotHeld() async throws {
        // Launch: every host is on a cold SSH handshake and the first tick runs
        // to its cap, so holding first results kept the dashboard on spinners
        // for the whole cap. Nothing-to-something is worth its own pass, once.
        let monitor = try service()
        try monitor.addServer(refusing("fresh"))
        let fresh = try #require(monitor.servers.first?.id)
        var applied = 0

        monitor.openTick(members: [UUID()])
        monitor.commit(for: fresh) { applied += 1 }
        #expect(applied == 1, "a host with only a spinner on screen publishes at once, mid-tick")
        monitor.stop()

        // With a verdict on screen, its updates wait for the tick.
        await monitor.pollAll()
        guard case .offline = monitor.status[fresh] else {
            Issue.record("expected the refused poll to have published its verdict"); return
        }
        monitor.openTick(members: [UUID()])
        monitor.commit(for: fresh) { applied += 1 }
        #expect(applied == 1, "with a verdict on screen, the next result is held for the tick")
    }

    @Test func theCapReleasesATickAStragglerIsHoldingUp() async throws {
        let monitor = try service()
        monitor.tickCap = .milliseconds(50)
        let live = try await settled("live", in: monitor)
        var applied = 0

        monitor.openTick(members: [UUID()])                 // a host that never answers
        monitor.commit(for: live) { applied += 1 }
        #expect(applied == 0)

        try await Task.sleep(for: .milliseconds(200))
        #expect(!monitor.tickOpen, "the cap closed the tick")
        #expect(applied == 1, "held results were published at the cap")
    }

    @Test func aCapClosedTickDoesNotCarryItsStragglerIntoTheNext() async throws {
        // The bug this guards: the straggler stayed a member after the cap, so
        // every later tick — whose own polls all came back in a second — could
        // only close by the cap too. Eight live hosts waiting 2 s a round for
        // the one that was gone.
        let monitor = try service()
        monitor.tickCap = .milliseconds(50)
        let live = try await settled("live", in: monitor)
        let dead = UUID()

        monitor.openTick(members: [live, dead])
        monitor.pollFinished(live)
        try await Task.sleep(for: .milliseconds(200))
        #expect(!monitor.tickOpen)
        #expect(monitor.tickMembers.isEmpty, "the cap must let go of the straggler")

        var applied = 0
        monitor.openTick(members: [live])                   // next tick: only the live host
        monitor.commit(for: live) { applied += 1 }
        monitor.pollFinished(live)
        #expect(applied == 1, "closed as complete the moment its own polls returned")
        #expect(!monitor.tickOpen)

        monitor.pollFinished(dead)                          // the straggler, much later
        #expect(monitor.tickMembers.isEmpty)
    }

    @Test func aManualRefreshMidTickJoinsIt() throws {
        let monitor = try service()
        let a = UUID(), b = UUID()
        monitor.openTick(members: [a])
        monitor.openTick(members: [b])
        #expect(monitor.tickMembers == [a, b], "the second opener joins; the tick is not restarted")
        #expect(monitor.tickOpen)
        monitor.stop()
    }

    @Test func aManualRefreshWithEverythingInFlightDoesNotSplitTheTick() async throws {
        let monitor = try service()
        let live = try await settled("live", in: monitor)
        var applied = 0
        monitor.openTick(members: [UUID()])
        monitor.commit(for: live) { applied += 1 }

        // Nothing to launch (all in flight), so this joins the open tick and
        // must not force what it holds out early.
        monitor.stop()   // release the cap timer before pollDue would re-arm it below
        monitor.openTick(members: [UUID()])
        monitor.commit(for: live) { applied += 1 }
        let launched = monitor.pollDue(ignoringBackoff: true)
        for task in launched { await task.value }
        // `live` was launched by that pollDue and returned, so the tick it
        // joined is still held open by the unknown member: pollAll's tail
        // condition is `!tickOpen`.
        #expect(monitor.tickOpen)
        #expect(applied == 0, "a refresh that joins a tick lets the tick publish")
        monitor.stop()
    }

    @Test func hiddenTicksStayQueuedForTheWindow() async throws {
        let monitor = try service()
        let live = try await settled("live", in: monitor)
        monitor.setUIVisible(false)
        var applied = 0

        let member = UUID()
        monitor.openTick(members: [member])
        monitor.commit(for: live) { applied += 1 }
        monitor.pollFinished(member)
        #expect(!monitor.tickOpen)
        #expect(applied == 0, "hidden: the tick closing must not publish")
        #expect(monitor.pending.count == 1)

        monitor.setUIVisible(true)
        #expect(applied == 1, "showing the window applies what the tick held")
    }

    @Test func stopClearsAnOpenTickAndWhatItHeld() async throws {
        let monitor = try service()
        let live = try await settled("live", in: monitor)
        var applied = 0
        monitor.openTick(members: [UUID()])
        monitor.commit(for: live) { applied += 1 }
        monitor.stop()
        #expect(!monitor.tickOpen)
        #expect(monitor.tickMembers.isEmpty)
        #expect(monitor.pending.isEmpty, "a stopped service publishes nothing further")
        #expect(applied == 0)
    }
}
