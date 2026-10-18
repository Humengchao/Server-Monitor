import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("SFTP listing")
@MainActor
struct SFTPClientTests {
    private func listing(_ rows: [[String]]) -> String {
        (["SM-FILES-V1"] + rows.flatMap { $0 } + ["SM-FILES-END", ""]).joined(separator: "\0")
    }

    private func entry(_ name: String, type: String = "f") -> [String] {
        [name, type, "1024", "1787656500.25", type == "d" ? "drwxr-xr-x" : "-rw-r--r--", "root"]
    }

    @Test func parsesStructuredListingAndKeepsDirectoriesFirst() throws {
        let files = try SFTPClient.parseListing(listing([
            entry("notes.txt"), entry("apps", type: "d"), entry("current -> release", type: "l"),
        ]), parent: "/srv")
        #expect(files.count == 3)
        #expect(files[0].name == "apps")
        #expect(files[0].isDirectory)
        #expect(files[0].path == "/srv/apps")
        let link = try #require(files.first { $0.isSymlink })
        #expect(link.name == "current -> release")
        let notes = try #require(files.first { $0.name == "notes.txt" })
        #expect(notes.size == 1024)
        #expect(notes.owner == "root")
        #expect(notes.modified?.timeIntervalSince1970 == 1787656500.25)
    }

    @Test func exactNamesRemainDistinctThroughDeletionCommand() throws {
        let names = ["keep backup", "keep  backup", " leading and trailing ", "line\nbreak", "tab\tname", "it's a -> file", ".hidden", "\u{76EE}\u{5F55}"]
        let files = try SFTPClient.parseListing(listing(names.map { entry($0) }), parent: "/data")
        #expect(Set(files.map(\.name)) == Set(names))
        #expect(Set(files.map(\.id)).count == names.count)
        for file in files {
            #expect(file.path == "/data/" + file.name)
            #expect(try SFTPClient.removeCommand(file) == "rm -f -- " + SFTPClient.quote("/data/" + file.name))
        }
    }

    @Test func rootAndEmptyDirectoryAreRepresentedExactly() throws {
        let files = try SFTPClient.parseListing(listing([entry("a")]), parent: "/")
        #expect(files[0].path == "/a")
        #expect(try SFTPClient.parseListing(listing([]), parent: "/empty").isEmpty)
    }

    @Test func ambiguousOrTruncatedListingsFailClosed() {
        let valid = listing([entry("ok")])
        for output in [
            "-rw-r--r-- 1 root root 5 2026-08-25 09:15 ok.txt",
            String(valid.dropLast()),
            "SM-FILES-V1\0name\0SM-FILES-END\0",
            listing([entry("bad\u{FFFD}name")]),
            listing([entry("../other")]),
            listing([entry("same"), entry("same")]),
        ] {
            #expect(throws: SFTPClient.Failure.self) { try SFTPClient.parseListing(output, parent: "/tmp") }
        }
    }

    @Test func refusesUnsafeMutationPaths() {
        let file = RemoteFile(name: "..", path: "/data/..", size: 0, modified: nil,
                              isDirectory: true, isSymlink: false, mode: "drwxr-xr-x", owner: "root")
        #expect(throws: SFTPClient.Failure.self) { try SFTPClient.removeCommand(file) }
        #expect(!SFTPClient.isFileName("../../target"))
        #expect(SFTPClient.isFileName(" name \n"))
    }

    @Test func quotingEscapesSingleQuotes() {
        #expect(SFTPClient.quote("it's a file; rm -rf /") == "'it'\\''s a file; rm -rf /'")
    }
}

@Suite("SSH key scanner")
@MainActor
struct SSHKeyScannerTests {

    @Test func parsesKeygenOutput() {
        let line = "4096 SHA256:Vgou1sHBhuUl4zXYf23e0pM0Q6JGyngo+qgf4OxieGo hmc@mac (RSA)"
        let parsed = SSHKeyScanner.parseKeygenLine(line)
        #expect(parsed?.bits == 4096)
        #expect(parsed?.type == "RSA")
        #expect(parsed?.comment == "hmc@mac")
        #expect(parsed?.fingerprint.hasPrefix("SHA256:") == true)
    }

    @Test func parsesCommentWithSpaces() {
        let line = "256 SHA256:abc my laptop key (ED25519)"
        let parsed = SSHKeyScanner.parseKeygenLine(line)
        #expect(parsed?.comment == "my laptop key")
        #expect(parsed?.type == "ED25519")
    }

    @Test func rejectsGarbage() {
        #expect(SSHKeyScanner.parseKeygenLine("") == nil)
        #expect(SSHKeyScanner.parseKeygenLine("not a key file") == nil)
    }
}

@Suite("Session history")
@MainActor
struct SessionHistoryTests {

    @Test func durationFormatting() {
        #expect(SessionHistoryView.formatDuration(45) == "45s")
        #expect(SessionHistoryView.formatDuration(125) == "2m 5s")
        #expect(SessionHistoryView.formatDuration(7_320) == "2h 2m")
    }

    @Test func recordsRoundTripAndCloseDangling() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "web-1", host: "10.0.0.1", username: "root", authKind: .agent)
        try database.save(server)

        let open = SessionRecord(serverID: server.id, serverName: "web-1", kind: .terminal)
        try database.save(open)
        #expect(try database.recentSessions().first?.isOpen == true)

        // A crash leaves rows open; startup must tidy them.
        try database.closeDanglingSessions()
        #expect(try database.recentSessions().first?.isOpen == false)
    }

    @Test func historyOutlivesItsServer() throws {
        let database = try Database(inMemory: true)
        let server = Server(name: "gone", host: "10.0.0.9", username: "root", authKind: .agent)
        try database.save(server)
        try database.save(SessionRecord(serverID: server.id, serverName: "gone", kind: .sftp))

        try database.deleteServer(id: server.id)

        let records = try database.recentSessions()
        #expect(records.count == 1, "deleting a server must not erase its history")
        #expect(records[0].serverName == "gone")
        #expect(records[0].serverID == nil, "the link is nulled, not cascaded")
    }
}

@Suite("Toolbox storage")
@MainActor
struct ToolboxStorageTests {

    @Test func snippetUseCountIncrements() throws {
        let database = try Database(inMemory: true)
        let snippet = Snippet(name: "disk", command: "df -h")
        try database.save(snippet)

        try database.markSnippetUsed(id: snippet.id)
        try database.markSnippetUsed(id: snippet.id)

        let stored = try database.allSnippets().first
        #expect(stored?.useCount == 2)
        #expect(stored?.lastUsedAt != nil)
    }

    @Test func identityUsageCountsServers() throws {
        let database = try Database(inMemory: true)
        let identity = Identity(name: "ops", username: "root", identityFile: "/k")
        try database.save(identity)
        var server = Server(name: "a", host: "1.1.1.1", username: "root", authKind: .identityFile)
        server.identityID = identity.id
        try database.save(server)

        #expect(try database.serverCount(usingIdentity: identity.id) == 1)

        // Deleting the identity must not delete the server, only unlink it.
        try database.deleteIdentity(id: identity.id)
        let servers = try database.allServers()
        #expect(servers.count == 1)
        #expect(servers[0].identityID == nil)
    }
}

@Suite("SSH key encryption detection")
@MainActor
struct SSHKeyEncryptionTests {

    private func write(_ contents: String) throws -> String {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("smtest-\(UUID().uuidString)")
        try contents.write(to: url, atomically: true, encoding: .utf8)
        return url.path
    }

    /// An unencrypted OpenSSH key names its cipher "none" in the decoded body.
    @Test func unencryptedOpenSSHKeyIsNotFlagged() throws {
        var body = Data("openssh-key-v1\u{0}".utf8)
        body.append(contentsOf: [0, 0, 0, 4])
        body.append(contentsOf: Array("none".utf8))
        let armoured = """
        -----BEGIN OPENSSH PRIVATE KEY-----
        \(body.base64EncodedString())
        -----END OPENSSH PRIVATE KEY-----
        """
        let path = try write(armoured)
        defer { try? FileManager.default.removeItem(atPath: path) }
        #expect(SSHKeyScanner.isEncrypted(path: path) == false)
    }

    @Test func encryptedOpenSSHKeyIsFlagged() throws {
        var body = Data("openssh-key-v1\u{0}".utf8)
        body.append(contentsOf: [0, 0, 0, 10])
        body.append(contentsOf: Array("aes256-ctr".utf8))
        let armoured = """
        -----BEGIN OPENSSH PRIVATE KEY-----
        \(body.base64EncodedString())
        -----END OPENSSH PRIVATE KEY-----
        """
        let path = try write(armoured)
        defer { try? FileManager.default.removeItem(atPath: path) }
        #expect(SSHKeyScanner.isEncrypted(path: path) == true)
    }

    @Test func classicPEMEncryptionIsFlagged() throws {
        let path = try write("""
        -----BEGIN RSA PRIVATE KEY-----
        Proc-Type: 4,ENCRYPTED
        DEK-Info: AES-128-CBC,0123456789ABCDEF

        aGVsbG8=
        -----END RSA PRIVATE KEY-----
        """)
        defer { try? FileManager.default.removeItem(atPath: path) }
        #expect(SSHKeyScanner.isEncrypted(path: path) == true)
    }
}
