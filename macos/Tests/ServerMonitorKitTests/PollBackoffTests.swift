import Foundation
import Testing
@testable import ServerMonitorKit

/// A host that is simply gone costs a 10 s connect timeout and one held ssh
/// process per attempt. Before the backoff, the app paid that forever.
@Suite("Poll backoff")
@MainActor
struct PollBackoffTests {

    private func service() throws -> MonitorService {
        let settings = AppSettings()
        settings.pollInterval = 5
        return MonitorService(database: try Database(inMemory: true), settings: settings)
    }

    @Test func firstFailureRetriesImmediately() throws {
        let monitor = try service()
        // One blip should not delay anything: a five-second gap is already the
        // normal cadence, and a single dropped poll is unremarkable.
        #expect(monitor.backoffDelay(streak: 1) == 0)
    }

    @Test func delayGrowsGeometricallyThenStops() throws {
        let monitor = try service()
        #expect(monitor.backoffDelay(streak: 2) == 10)
        #expect(monitor.backoffDelay(streak: 3) == 20)
        #expect(monitor.backoffDelay(streak: 4) == 40)
        #expect(monitor.backoffDelay(streak: 8) == 300, "capped, not unbounded")
        #expect(monitor.backoffDelay(streak: 40) == MonitorService.maxBackoff)
    }

    @Test func theCapKeepsRecoveryNoticeable() throws {
        let monitor = try service()
        // The point of the cap: a host that comes back is seen within minutes,
        // not hours. Anything much larger turns the monitor into a stale panel.
        #expect(MonitorService.maxBackoff <= 300)
    }

    @Test func aWaitingServerIsSkippedUntilItsTimeComes() throws {
        let monitor = try service()
        let server = Server(name: "gone", host: "10.255.255.1", username: "root", authKind: .agent)
        try monitor.addServer(server)
        let stored = try #require(monitor.servers.first)

        let now = Date()
        // Three failures => a 20 s wait, through the same call the poll's
        // catch block makes.
        for _ in 1...3 { monitor.noteFailure(serverID: stored.id, now: now) }
        #expect(monitor.pollDue(now: now).isEmpty, "still inside its backoff window")

        let later = monitor.pollDue(now: now.addingTimeInterval(21))
        #expect(later.count == 1, "polled again once the window passes")
        for task in later { task.cancel() }
    }

    @Test func aBackwardClockJumpDoesNotStrandAHost() throws {
        let monitor = try service()
        let now = Date()
        let due = now.addingTimeInterval(MonitorService.maxBackoff)
        #expect(monitor.isStillWaiting(until: due, now: now), "a legitimate full-cap wait")

        // The clock moved back an hour under us. Nothing the backoff can do
        // produces a wait that long, so it must not be honoured — otherwise
        // the host never polls again until the app is relaunched.
        let after = now.addingTimeInterval(-3600)
        #expect(!monitor.isStillWaiting(until: due, now: after))

        #expect(!monitor.isStillWaiting(until: now.addingTimeInterval(-1), now: now), "already due")
    }

    @Test func manualRefreshOverridesTheBackoff() throws {
        let monitor = try service()
        let server = Server(name: "gone", host: "10.255.255.1", username: "root", authKind: .agent)
        try monitor.addServer(server)
        let stored = try #require(monitor.servers.first)

        let now = Date()
        for _ in 1...10 { monitor.noteFailure(serverID: stored.id, now: now) }
        // Clicking refresh means "try now" — skipping the host the user is
        // most likely asking about would make the button look broken.
        let tasks = monitor.pollDue(ignoringBackoff: true, now: now)
        #expect(tasks.count == 1)
        for task in tasks { task.cancel() }
    }

    @Test func editingAServerClearsItsBackoff() throws {
        let monitor = try service()
        let server = Server(name: "typo", host: "10.255.255.1", username: "root", authKind: .agent)
        try monitor.addServer(server)
        var stored = try #require(monitor.servers.first)
        for _ in 1...10 { monitor.noteFailure(serverID: stored.id) }

        stored.host = "10.0.0.9"
        try monitor.updateServer(stored)
        #expect(monitor.retryAfter[stored.id] == nil, "the edit is the fix; retry at once")
        #expect(monitor.failureStreak[stored.id] == nil)
    }

    @Test func recoveryClearsTheBackoffAtOnce() throws {
        let monitor = try service()
        let id = UUID()
        for _ in 1...10 { monitor.noteFailure(serverID: id) }
        #expect(monitor.retryAfter[id] != nil)

        monitor.noteSuccess(serverID: id)
        // A host that answers is back on the normal cadence immediately —
        // it must not serve out the rest of a five-minute penalty.
        #expect(monitor.retryAfter[id] == nil)
        #expect(monitor.failureStreak[id] == nil)
        #expect(monitor.backoffDelay(streak: 1) == 0)
    }

    @Test func theNetworkComingBackClearsEveryBackoff() throws {
        let monitor = try service()
        let a = UUID(), b = UUID()
        for _ in 1...10 { monitor.noteFailure(serverID: a); monitor.noteFailure(serverID: b) }

        // The first update is the current state, not a transition: it must not
        // be mistaken for a recovery.
        monitor.networkPathChanged(satisfied: true)
        #expect(monitor.retryAfter[a] != nil, "startup is not a recovery")

        monitor.networkPathChanged(satisfied: false)
        monitor.networkPathChanged(satisfied: true)
        // Nine hosts failing at once is almost always this machine — a lid, a
        // train, a different Wi-Fi — so the whole fleet is retried, not just
        // whichever host happened to come due first.
        #expect(monitor.retryAfter.isEmpty)
        #expect(monitor.failureStreak.isEmpty)
    }

    @Test func stayingOfflineDoesNotClearAnything() throws {
        let monitor = try service()
        let id = UUID()
        monitor.networkPathChanged(satisfied: true)   // establish a baseline
        for _ in 1...10 { monitor.noteFailure(serverID: id) }

        monitor.networkPathChanged(satisfied: false)
        monitor.networkPathChanged(satisfied: false)
        // Losing the network, or hearing about it twice, is not a recovery.
        #expect(monitor.retryAfter[id] != nil)
    }

    @Test func wakingFromSleepClearsEveryBackoff() throws {
        let monitor = try service()
        let a = UUID(), b = UUID()
        for _ in 1...10 { monitor.noteFailure(serverID: a); monitor.noteFailure(serverID: b) }
        // Unlike the network path, a wake needs no prior state: the lid
        // opening is itself the transition.
        monitor.systemDidWake()
        #expect(monitor.retryAfter.isEmpty)
        #expect(monitor.failureStreak.isEmpty)
    }

    @Test func lowPowerModeSlowsTheCadence() {
        // Not off — a monitor that stops monitoring on battery is worse than
        // none — just a third as often.
        #expect(MonitorService.effectiveInterval(5, lowPower: false) == 5)
        #expect(MonitorService.effectiveInterval(5, lowPower: true) == 15)
        #expect(MonitorService.effectiveInterval(60, lowPower: true) == 180)
    }

    @Test func deletingAServerForgetsItsBackoff() throws {
        let monitor = try service()
        let server = Server(name: "gone", host: "10.255.255.1", username: "root", authKind: .agent)
        try monitor.addServer(server)
        let stored = try #require(monitor.servers.first)
        for _ in 1...10 { monitor.noteFailure(serverID: stored.id) }

        try monitor.deleteServer(stored)
        // A re-added host with the same UUID would otherwise inherit a
        // stranger's backoff.
        #expect(monitor.retryAfter[stored.id] == nil)
        #expect(monitor.failureStreak[stored.id] == nil)
    }
}
