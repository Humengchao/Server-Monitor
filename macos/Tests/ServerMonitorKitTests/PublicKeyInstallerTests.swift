import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Public key export")
struct PublicKeyInstallerTests {

    static let ed25519 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJk1mZ8bQ7Xv2n9kLpQ4rT6yUwEXAMPLEexampleAAA hmc@mac"

    @Test func acceptsRealPublicKeyForms() {
        #expect(PublicKeyInstaller.isPublicKey(Self.ed25519))
        #expect(PublicKeyInstaller.isPublicKey(
            "ssh-rsa " + String(repeating: "A", count: 300) + " comment here"
        ))
        // A key with no comment is still a key.
        #expect(PublicKeyInstaller.isPublicKey("ssh-ed25519 " + String(repeating: "B", count: 68)))
    }

    @Test func refusesAPrivateKey() {
        // Pasting a private key here would copy it to a remote host, which is
        // the exact opposite of what this does.
        let private_ = """
        -----BEGIN OPENSSH PRIVATE KEY-----
        b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
        -----END OPENSSH PRIVATE KEY-----
        """
        #expect(PublicKeyInstaller.isPublicKey(private_) == false)
    }

    @Test func refusesJunk() {
        for text in ["", "hello", "ssh-ed25519", "ssh-ed25519 short", "not-an-algo AAAA…"] {
            #expect(PublicKeyInstaller.isPublicKey(text) == false, "accepted \(text.debugDescription)")
        }
    }

    @Test func theCommandFixesPermissionsAndIsIdempotent() {
        let command = PublicKeyInstaller.command(for: Self.ed25519)
        // sshd silently ignores a group-writable authorized_keys, so the export
        // has to set these or it "succeeds" and changes nothing.
        #expect(command.contains("chmod 700 ~/.ssh"))
        #expect(command.contains("chmod 600 ~/.ssh/authorized_keys"))
        // Matches on the key body, so the same key with a new comment is not
        // appended twice.
        #expect(command.contains("awk '{print $2}'"))
        #expect(command.contains("SM_ALREADY"))
        #expect(command.contains("SM_ADDED"))
    }

    @Test func aCommentContainingAQuoteCannotBreakOutOfTheShell() {
        // Comments are free text and reach a shell.
        let nasty = "ssh-ed25519 " + String(repeating: "A", count: 68) + " o'brien'; rm -rf ~; #"
        let command = PublicKeyInstaller.command(for: nasty)
        #expect(command.contains("rm -rf ~; #"), "the text is carried through")
        // …but only ever inside a quoted literal: every embedded quote is
        // closed and re-opened, never left able to terminate the string.
        #expect(command.contains("'\\''"))
        let body = command.dropFirst(command.distance(from: command.startIndex, to: command.firstIndex(of: "'")!))
        #expect(body.hasPrefix("'ssh-ed25519"))
    }

    @Test func quotingRoundTripsThroughARealShell() throws {
        // The regression that matters is whether /bin/sh agrees, so ask it.
        let nasty = "ssh-ed25519 AAAA o'brien \"quoted\" $(echo pwned) `id` \\ end"
        let quoted = PublicKeyInstaller.shellQuoted(nasty)
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/sh")
        process.arguments = ["-c", "printf '%s' \(quoted)"]
        let pipe = Pipe()
        process.standardOutput = pipe
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        #expect(String(decoding: data, as: UTF8.self) == nasty, "shell must see the text verbatim")
    }
}
