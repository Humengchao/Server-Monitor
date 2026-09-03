import Foundation
import Testing
@testable import ServerMonitorKit

/// A Windows host answers `uname -s` by failing, and so does a host that is
/// simply unreachable. Telling them apart is what keeps a down host from being
/// run through the whole Windows collection as well.
@Suite("OS probe classification")
struct OSProbeTests {

    @Test func aShellWithoutUnameIsWindows() {
        // What `ssh windowsbox uname -s` really does: cmd.exe answers, exits
        // non-zero, and says so on stderr.
        let failure = SSHRunner.Failure.commandFailed(
            status: 1,
            stderr: "'uname' is not recognized as an internal or external command"
        )
        #expect(MetricsCollector.osKind(fromProbeFailure: failure) == .windows)
    }

    @Test func sshOwnErrorSaysNothingAboutTheOS() {
        // 255 is ssh's own exit code — the host was never reached, so there is
        // no evidence for either OS.
        let failure = SSHRunner.Failure.commandFailed(
            status: 255,
            stderr: "Connection timed out during banner exchange"
        )
        #expect(MetricsCollector.osKind(fromProbeFailure: failure) == nil)
    }

    @Test func timeoutAndLaunchFailureSayNothingEither() {
        #expect(MetricsCollector.osKind(fromProbeFailure: .timedOut(seconds: 15)) == nil)
        #expect(MetricsCollector.osKind(fromProbeFailure: .launchFailed("no such file")) == nil)
    }

    @Test func aSuccessfulProbeStillReadsItsOutput() {
        #expect(MetricsCollector.osKind(fromUname: "Linux\n") == .linux)
        #expect(MetricsCollector.osKind(fromUname: "  linux  ") == .linux)
        #expect(MetricsCollector.osKind(fromUname: "MINGW64_NT-10.0") == .windows)
        // An empty answer from a shell that did exit zero is odd, but it is an
        // answer; only the throwing paths above mean "no evidence".
        #expect(MetricsCollector.osKind(fromUname: "") == .windows)
    }

    @Test func dockerIsSampledOnASlowCadence() {
        let now = Date()
        #expect(MetricsCollector.shouldSampleDocker(lastSampled: nil, now: now), "nothing cached yet")
        #expect(!MetricsCollector.shouldSampleDocker(lastSampled: now.addingTimeInterval(-5), now: now))
        #expect(MetricsCollector.shouldSampleDocker(lastSampled: now.addingTimeInterval(-31), now: now))
        #expect(MetricsCollector.dockerInterval == 30)
    }
}
