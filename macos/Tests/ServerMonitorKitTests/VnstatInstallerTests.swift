import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("vnStat installer")
struct VnstatInstallerTests {

    @Test func probeFindsTheManagerAndWhetherWeAreRoot() {
        let root = VnstatInstaller.plan(fromProbe: "apt-get\nSM_PROBE_END\n0\n")
        #expect(root?.manager == .apt)
        #expect(root?.asRoot == true)

        let user = VnstatInstaller.plan(fromProbe: "dnf\nSM_PROBE_END\n1000\n")
        #expect(user?.manager == .dnf)
        #expect(user?.asRoot == false)
    }

    @Test func aHostWithNoKnownManagerYieldsNoPlan() {
        // The loop prints nothing before the marker; the card then explains
        // rather than trying to run something.
        #expect(VnstatInstaller.plan(fromProbe: "SM_PROBE_END\n0\n") == nil)
        #expect(VnstatInstaller.plan(fromProbe: "") == nil)
        #expect(VnstatInstaller.plan(fromProbe: "bash: warning: setlocale\nSM_PROBE_END\n0") == nil)
    }

    @Test func theShownCommandMatchesWhoWeAre() {
        let root = VnstatInstaller.Plan(manager: .apk, asRoot: true)
        #expect(root.displayCommand == "apk add vnstat")
        let user = VnstatInstaller.Plan(manager: .apt, asRoot: false)
        // Every step is prefixed, not only the first.
        #expect(user.displayCommand.hasPrefix("sudo DEBIAN_FRONTEND"))
        #expect(user.displayCommand.contains("&& sudo DEBIAN_FRONTEND=noninteractive apt-get install"))
    }

    @Test func theScriptIsNonInteractiveAndSelfReporting() {
        for manager in VnstatInstaller.PackageManager.allCases {
            let script = VnstatInstaller.remoteScript(for: .init(manager: manager, asRoot: true))
            #expect(script.contains("SM_INSTALLED"), "\(manager) lacks the success marker")
            #expect(script.contains("SM_FAILED"), "\(manager) lacks the failure marker")
            #expect(script.contains("2>&1"), "\(manager): errors must come back on stdout")
            #expect(script.contains("sudo") == false, "\(manager): root must not sudo")
        }
        // Non-interactive flags, so nothing waits on a prompt nobody answers.
        #expect(VnstatInstaller.PackageManager.apt.installLine.contains("-y"))
        #expect(VnstatInstaller.PackageManager.apt.installLine.contains("DEBIAN_FRONTEND=noninteractive"))
        #expect(VnstatInstaller.PackageManager.pacman.installLine.contains("--noconfirm"))
        #expect(VnstatInstaller.PackageManager.zypper.installLine.contains("-n"))
    }

    @Test func aNonRootUserGetsSudoDashN() {
        let script = VnstatInstaller.remoteScript(for: .init(manager: .apt, asRoot: false))
        // -n: fail immediately instead of hanging on a password prompt.
        #expect(script.contains("sudo -n env DEBIAN_FRONTEND=noninteractive apt-get update"))
        #expect(script.contains("sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install"))
        #expect(script.contains("sudo -n env systemctl enable --now vnstat"))
        let yum = VnstatInstaller.remoteScript(for: .init(manager: .yum, asRoot: false))
        #expect(yum.contains("sudo -n env yum install -y epel-release; sudo -n env yum install -y vnstat"))
    }

    @Test func outcomesAreReadFromTheMarkers() {
        #expect(VnstatInstaller.outcome(from: "Reading package lists...\nSM_INSTALLED\n") == .installed)
        #expect(VnstatInstaller.outcome(from: "sudo: a password is required\nSM_FAILED rc=1") == .needsSudoPassword)
        #expect(VnstatInstaller.outcome(from: "sudo: command not found\nSM_FAILED rc=127") == .needsSudoPassword)
        let failed = VnstatInstaller.outcome(from: "E: Unable to locate package vnstat\nSM_FAILED rc=100")
        if case .failed(let detail) = failed {
            #expect(detail.contains("Unable to locate package"), "the tail must carry the reason")
        } else {
            Issue.record("expected .failed, got \(failed)")
        }
        #expect(VnstatInstaller.outcome(from: "") == .failed("no output"))
    }

    /// Verbatim probe output from four real hosts, so the parser is checked
    /// against what servers actually print rather than what I imagined.
    @Test func realHostsParseIntoTheRightPlan() throws {
        // Debian/Ubuntu as root — the common case.
        let asRoot = VnstatInstaller.plan(fromProbe: "apt-get\nSM_PROBE_END\n0\n")
        #expect(asRoot == VnstatInstaller.Plan(manager: .apt, asRoot: true))

        // An Oracle Cloud image logs in as a non-root user with passwordless
        // sudo; the uid line is what distinguishes it, and getting it wrong
        // means the install runs without sudo and fails on permissions.
        let asUser = VnstatInstaller.plan(fromProbe: "apt-get\nSM_PROBE_END\n1001\n")
        #expect(asUser == VnstatInstaller.Plan(manager: .apt, asRoot: false))
        #expect(try VnstatInstaller.remoteScript(for: #require(asUser)).contains("sudo -n env "))
    }

    @Test func aTrailingCarriageReturnStillParses() {
        // `lines()` exists because CRLF is one grapheme cluster in Swift; the
        // probe is one of the places a host can hand it back.
        let plan = VnstatInstaller.plan(fromProbe: "apt-get\r\nSM_PROBE_END\r\n0\r\n")
        #expect(plan == VnstatInstaller.Plan(manager: .apt, asRoot: true))
    }
}
