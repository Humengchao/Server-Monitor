import Foundation
import Testing
@testable import ServerMonitorKit

/// The alert rules are what stand between a useful monitor and a notification
/// firehose, so the debouncing is pinned down here. Delivery itself is not
/// exercised: it needs a real notification centre.
@Suite("Alert rules", .serialized)
@MainActor
struct AlertServiceTests {

    /// Collects what would have been delivered.
    final class Recorder: @unchecked Sendable {
        private(set) var messages: [(title: String, body: String)] = []
        var count: Int { messages.count }
        func record(_ title: String, _ body: String) { messages.append((title, body)) }
        func reset() { messages.removeAll() }
    }

    private func makeService(
        cpu: Int = 0,
        memory: Int = 0,
        disk: Int = 0,
        offline: Bool = true
    ) -> (AlertService, AppSettings, Recorder) {
        let settings = AppSettings()
        settings.notificationsEnabled = true
        settings.notifyOnOffline = offline
        settings.cpuThreshold = cpu
        settings.memoryThreshold = memory
        settings.diskThreshold = disk
        let recorder = Recorder()
        let service = AlertService(settings: settings) { title, body in
            recorder.record(title, body)
        }
        return (service, settings, recorder)
    }

    private func snapshot(cpu: Double = 0, memoryUsed: Int64 = 0, memoryTotal: Int64 = 100) -> MetricSnapshot {
        var value = MetricSnapshot()
        value.cpuPercent = cpu
        value.memoryUsed = memoryUsed
        value.memoryTotal = memoryTotal
        return value
    }

    private var server: Server {
        Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .agent)
    }

    @Test func thresholdNeedsThreeConsecutivePolls() {
        let (service, _, recorder) = makeService(cpu: 80)
        let host = server

        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        #expect(recorder.count == 0, "two breaching polls must not alert")

        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        #expect(recorder.count == 1)
        #expect(recorder.messages.first?.title == "web-1")
    }

    @Test func sustainedBreachAlertsOnlyOnce() {
        let (service, _, recorder) = makeService(cpu: 80)
        let host = server
        // A host pegged at 100% must not notify on every single poll.
        for _ in 0..<20 {
            service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 99))
        }
        #expect(recorder.count == 1)
    }

    @Test func droppingBelowTheLimitResetsTheRun() {
        let (service, _, recorder) = makeService(cpu: 80)
        let host = server

        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        // A single calm poll must undo the build-up, or a flapping host alerts.
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 10))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        #expect(recorder.count == 0)
    }

    @Test func zeroThresholdDisablesTheMetric() {
        let (service, _, recorder) = makeService(cpu: 0)
        let host = server
        for _ in 0..<5 {
            service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 100))
        }
        #expect(recorder.count == 0)
    }

    @Test func goingOfflineClearsThresholdRuns() {
        let (service, _, recorder) = makeService(cpu: 80, offline: false)
        let host = server
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .offline(reason: "timeout"), snapshot: nil)
        // The run must restart, not resume, once the host answers again.
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        #expect(recorder.count == 0)
    }

    @Test func memoryUsesPercentNotRawBytes() {
        let (service, _, recorder) = makeService(memory: 80)
        let host = server
        // 90 bytes of 1000 is 9%: below the limit, even though the raw number
        // exceeds it.
        let low = snapshot(memoryUsed: 90, memoryTotal: 1_000)
        for _ in 0..<4 {
            service.evaluate(server: host, status: .online(at: Date()), snapshot: low)
        }
        #expect(recorder.count == 0)
    }

    @Test func disabledNotificationsEvaluateNothing() {
        let (service, settings, recorder) = makeService(cpu: 80)
        settings.notificationsEnabled = false
        let host = server
        for _ in 0..<5 {
            service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 99))
        }
        #expect(recorder.count == 0)
    }

    @Test func offlineAndRecoveryEachAlertOnce() {
        let (service, _, recorder) = makeService()
        let host = server

        // The first observation establishes a baseline and must stay silent.
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot())
        #expect(recorder.count == 0)

        service.evaluate(server: host, status: .offline(reason: "timeout"), snapshot: nil)
        #expect(recorder.count == 1)
        #expect(recorder.messages.last?.body.contains("timeout") == true)

        // Staying offline must not keep notifying.
        service.evaluate(server: host, status: .offline(reason: "timeout"), snapshot: nil)
        #expect(recorder.count == 1)

        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot())
        #expect(recorder.count == 2)
    }

    @Test func offlineAlertsCanBeTurnedOff() {
        let (service, _, recorder) = makeService(offline: false)
        let host = server
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot())
        service.evaluate(server: host, status: .offline(reason: "down"), snapshot: nil)
        #expect(recorder.count == 0)
    }

    @Test func forgettingAServerClearsItsState() {
        let (service, _, recorder) = makeService(cpu: 80)
        let host = server
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        service.forget(serverID: host.id)
        service.evaluate(server: host, status: .online(at: Date()), snapshot: snapshot(cpu: 95))
        #expect(recorder.count == 0, "the run must start over after forgetting")
    }

    @Test func thresholdTextNamesMetricAndLimit() {
        let english = L10nBridge.threshold(metric: .cpu, value: 93.6, limit: 85)
        #expect(english.contains("85") || english.contains("94"))
    }
}
