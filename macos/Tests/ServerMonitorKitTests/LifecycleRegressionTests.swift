import AppKit
import Foundation
import GRDB
import Testing
@testable import ServerMonitorKit

/// A collection that deliberately ignores cancellation until the test releases
/// it, modelling an SSH response already queued when a target is edited.
private actor HeldPoll {
    private var result: CheckedContinuation<MetricSnapshot, Never>?
    private var started: CheckedContinuation<Void, Never>?

    func collect() async -> MetricSnapshot {
        await withCheckedContinuation { continuation in
            result = continuation
            started?.resume()
            started = nil
        }
    }

    func waitUntilStarted() async {
        if result != nil { return }
        await withCheckedContinuation { started = $0 }
    }

    func finish(cpu: Double) {
        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = cpu
        snapshot.cores = 2
        snapshot.memoryTotal = 100
        result?.resume(returning: snapshot)
        result = nil
    }
}

@Suite("Monitor lifecycle regressions", .serialized)
@MainActor
struct LifecycleRegressionTests {
    private func service() throws -> MonitorService {
        MonitorService(database: try Database(inMemory: true), settings: AppSettings())
    }

    @Test func menuBarUpdatesWhileTheMainWindowRemainsUnpublished() async throws {
        let monitor = try service()
        let server = Server(name: "host", host: "127.0.0.1", username: "u", authKind: .agent)
        try monitor.addServer(server)
        monitor.setUIVisible(false)
        monitor.collectOverride = { _, _, _ in
            var sample = MetricSnapshot()
            sample.cpuPercent = 91
            sample.memoryTotal = 100
            sample.memoryUsed = 42
            return sample
        }
        await monitor.poll(server)
        #expect(monitor.latest[server.id] == nil)
        #expect(monitor.status[server.id] == nil)
        #expect(monitor.live.peakCPU == 91)
        #expect(monitor.live.readings[server.id]?.memoryPercent == 42)

        monitor.collectOverride = { _, _, _ in throw SSHError.commandFailed("offline") }
        await monitor.poll(server)
        #expect(monitor.live.offlineCount == 1)
        #expect(monitor.live.peakCPU == nil)
        #expect(monitor.status[server.id] == nil, "menu updates must not wake the hidden dashboard")
        monitor.setUIVisible(true)
        if case .offline = monitor.status[server.id] {} else { Issue.record("latest failure was not flushed") }
    }

    @Test func editingATargetDiscardsOldResultsWithoutFinishingTheNewPoll() async throws {
        let monitor = try service()
        var server = Server(name: "host", host: "old.example", username: "u", authKind: .agent)
        try monitor.addServer(server)
        let old = HeldPoll(), fresh = HeldPoll()
        monitor.collectOverride = { target, _, _ in
            if target.host == "old.example" { return await old.collect() }
            return await fresh.collect()
        }
        let first = try #require(monitor.pollNow(server.id))
        await old.waitUntilStarted()
        server.host = "new.example"
        try monitor.updateServer(server)
        let second = try #require(monitor.pollNow(server.id))
        await fresh.waitUntilStarted()
        await old.finish(cpu: 99)
        await first.value
        #expect(monitor.inFlight.contains(server.id), "an obsolete completion must not finish the replacement")
        #expect(monitor.latest[server.id] == nil)
        #expect(try monitor.database.latestSample(serverID: server.id) == nil)
        await fresh.finish(cpu: 12)
        await second.value
        #expect(monitor.latest[server.id]?.cpuPercent == 12)
        #expect(monitor.live.peakCPU == 12)
        #expect(try monitor.database.samples(serverID: server.id, since: .distantPast).count == 1)
    }

    @Test func stoppingDiscardsAnAlreadyReturningCollection() async throws {
        let monitor = try service()
        let server = Server(name: "host", host: "127.0.0.1", username: "u", authKind: .agent)
        try monitor.addServer(server)
        let held = HeldPoll()
        monitor.collectOverride = { _, _, _ in await held.collect() }
        let task = try #require(monitor.pollNow(server.id))
        await held.waitUntilStarted()
        monitor.stop()
        await held.finish(cpu: 99)
        await task.value
        #expect(monitor.latest[server.id] == nil)
        #expect(monitor.live.readings[server.id] == nil)
        #expect(try monitor.database.latestSample(serverID: server.id) == nil)
    }

    @Test func workspaceNotificationActuallyClearsBackoff() async throws {
        let monitor = try service()
        monitor.start()
        defer { monitor.stop() }
        let id = UUID()
        for _ in 0..<10 { monitor.noteFailure(serverID: id) }
        NSWorkspace.shared.notificationCenter.post(name: NSWorkspace.didWakeNotification, object: NSWorkspace.shared)
        // The observer hops to MainActor. Give that queued delivery a turn.
        try await Task.sleep(for: .milliseconds(30))
        #expect(monitor.retryAfter.isEmpty)
        #expect(monitor.failureStreak.isEmpty)
    }

    @Test func sidebarTerminalStateSurvivesSelectionAndClosesExplicitly() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "host", host: "127.0.0.1", username: "u", authKind: .agent)
        try database.save(server)
        let sessions = SessionManager(database: database)
        let first = sessions.open(server: server, kind: .terminal)
        let terminal = sessions.terminalSession(for: first.id)
        let second = sessions.open(server: server, kind: .terminal)
        _ = sessions.terminalSession(for: second.id)
        #expect(sessions.terminalSession(for: first.id) === terminal)
        #expect(!terminal.isClosed)
        sessions.close(first.id)
        #expect(terminal.isClosed)
        #expect(sessions.active.map(\.id) == [second.id])
        #expect(try database.recentSessions().first { $0.id == first.id }?.endedAt != nil)
        sessions.close(second.id)
    }

    @Test func deletingAHostClosesItsSessionsAndApplicationExitClosesTheRest() throws {
        let monitor = try service()
        let a = Server(name: "a", host: "a.invalid", username: "u", authKind: .agent)
        let b = Server(name: "b", host: "b.invalid", username: "u", authKind: .agent)
        try monitor.addServer(a)
        try monitor.addServer(b)
        let sessions = SessionManager(database: monitor.database)
        monitor.serverDidDelete = { [weak sessions] id in sessions?.close(serverID: id) }
        let aShell = sessions.open(server: a, kind: .terminal)
        let aFiles = sessions.open(server: a, kind: .sftp)
        let bShell = sessions.open(server: b, kind: .terminal)
        let aTerminal = sessions.terminalSession(for: aShell.id)
        let bTerminal = sessions.terminalSession(for: bShell.id)

        try monitor.deleteServer(a)
        #expect(aTerminal.isClosed)
        #expect(!bTerminal.isClosed)
        #expect(sessions.active.map(\.id) == [bShell.id])
        let ended = try monitor.database.recentSessions().filter { $0.endedAt != nil }.map(\.id)
        #expect(Set(ended) == Set([aShell.id, aFiles.id]))
        #expect(try monitor.database.recentSessions().filter { $0.id == aShell.id || $0.id == aFiles.id }
            .allSatisfy { $0.serverID == nil })

        sessions.closeAll()
        #expect(bTerminal.isClosed)
        #expect(sessions.active.isEmpty)
        #expect(try monitor.database.recentSessions().allSatisfy { $0.endedAt != nil })
    }

    @Test func aRevokedWriteQueuedBehindAnotherTransactionCannotPersistOldFacts() async throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "host", host: "new.invalid", username: "u", authKind: .agent,
                            osKind: .auto, cores: 4, memoryTotal: 1024)
        try database.save(server)
        let validity = PollWriteValidity()
        var obsolete = MetricSnapshot()
        obsolete.cores = 99
        obsolete.memoryTotal = 9999
        obsolete.cpuPercent = 99

        // Hold the real GRDB writer queue. Revocation must be checked when
        // the queued transaction executes, not when recordPoll is called.
        let entered = AsyncStream<Void>.makeStream()
        let release = DispatchSemaphore(value: 0)
        let holder = Task {
            try await withCheckedThrowingContinuation { (done: CheckedContinuation<Void, Error>) in
                DispatchQueue.global().async {
                    do {
                        try database.queue.write { _ in
                            entered.continuation.yield(())
                            release.wait()
                        }
                        done.resume()
                    } catch {
                        entered.continuation.finish()
                        done.resume(throwing: error)
                    }
                }
            }
        }
        defer { release.signal() }
        for await _ in entered.stream { break }
        let pending = Task {
            try await database.recordPoll(serverID: server.id, snapshot: obsolete,
                                          detectedOS: .linux, updateFacts: true, validity: validity)
        }
        await Task.yield()
        validity.invalidate()
        release.signal()
        try await holder.value
        do {
            try await pending.value
            Issue.record("an obsolete queued write unexpectedly committed")
        } catch is CancellationError {
            // The transaction must roll back without touching either table.
        }
        #expect(try database.latestSample(serverID: server.id) == nil)
        let stored = try #require(database.allServers().first)
        #expect(stored.cores == 4)
        #expect(stored.memoryTotal == 1024)
        #expect(stored.osKind == .auto)
    }
}
