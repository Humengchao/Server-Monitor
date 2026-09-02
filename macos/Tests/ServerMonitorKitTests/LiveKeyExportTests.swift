import Foundation
import Testing
@testable import ServerMonitorKit

/// Exports a throwaway key to a real host, checks it landed, then removes it
/// again, so the host's `authorized_keys` is exactly as it started.
///
/// Opt-in:  SM_LIVE_ALIAS=myhost swift test --filter exportsAPublicKeyIdempotently
@Suite("Live key export", .serialized)
struct LiveKeyExportTests {

    @Test func exportsAPublicKeyIdempotently() async throws {
        guard let alias = ProcessInfo.processInfo.environment["SM_LIVE_ALIAS"] else { return }
        let target = SSHTarget(
            serverID: UUID(), host: alias, port: 22,
            username: "", credential: .sshConfigAlias
        )
        let runner = SSHRunner()

        // Generated into a temp directory and never written to ~/.ssh, so this
        // cannot touch the user's real keys.
        let directory = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("sm-keytest-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let keyPath = directory.appendingPathComponent("id_ed25519").path

        let keygen = Process()
        keygen.executableURL = URL(fileURLWithPath: "/usr/bin/ssh-keygen")
        keygen.arguments = ["-t", "ed25519", "-N", "", "-C", "server-monitor-selftest", "-f", keyPath]
        keygen.standardOutput = FileHandle.nullDevice
        keygen.standardError = FileHandle.nullDevice
        try keygen.run()
        keygen.waitUntilExit()
        let publicKey = try String(contentsOfFile: keyPath + ".pub", encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let body = String(publicKey.split(separator: " ")[1])

        let installer = PublicKeyInstaller(runner: runner)

        // Results are gathered first and asserted *after* the key is removed.
        // `defer` cannot await, so a fire-and-forget cleanup task might not
        // finish before the process exits — which would strand a test key in a
        // real person's authorized_keys.
        let first = try? await installer.install(publicKey: publicKey, on: target)
        let second = try? await installer.install(publicKey: publicKey, on: target)
        // Re-exporting under a different comment must not duplicate it either.
        let renamed = publicKey.replacingOccurrences(
            of: "server-monitor-selftest", with: "other-comment"
        )
        let third = try? await installer.install(publicKey: renamed, on: target)

        func remote(_ command: String) async -> String? {
            (try? await runner.run(command, on: target, timeout: 20))?
                .trimmingCharacters(in: .whitespacesAndNewlines)
        }
        let count = await remote("grep -cF '\(body)' ~/.ssh/authorized_keys")
        let permissions = await remote("stat -c %a ~/.ssh/authorized_keys")

        _ = await remote(
            "grep -vF '\(body)' ~/.ssh/authorized_keys > ~/.ssh/.sm_tmp "
                + "&& mv ~/.ssh/.sm_tmp ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
        )
        let leftOver = await remote("grep -cF '\(body)' ~/.ssh/authorized_keys || true")

        #expect(first == .added)
        #expect(second == .alreadyPresent, "pressing the button twice must not append a second copy")
        #expect(third == .alreadyPresent, "a new comment on the same key is still the same key")
        #expect(count == "1", "the key should appear exactly once, saw \(count ?? "nil")")
        #expect(permissions == "600", "sshd ignores a loose authorized_keys; saw \(permissions ?? "nil")")
        #expect(leftOver == "0", "the test key must be gone from the host, saw \(leftOver ?? "nil")")

        print("""

        ── live key export ── added, idempotent under a new comment, \
        mode \(permissions ?? "?"), left over \(leftOver ?? "?")

        """)

        await MetricsCollector(runner: runner).forget(target: target)
    }
}
