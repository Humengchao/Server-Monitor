import Foundation
import Testing
@testable import ServerMonitorKit

/// Opens a *copy* of a real, older database and checks the migrations bring it
/// forward without losing anything.
///
/// An in-memory store always runs every migration at once, so it can never
/// catch a migration that breaks an existing file — which is exactly the class
/// of bug that once blanked the whole server list.
///
/// Opt-in:  SM_MIGRATE_DB=~/Library/.../monitor.sqlite swift test --filter upgradesARealDatabase
@Suite("Migration upgrade")
struct MigrationUpgradeTests {

    @Test func upgradesARealDatabase() throws {
        guard let source = ProcessInfo.processInfo.environment["SM_MIGRATE_DB"],
              FileManager.default.fileExists(atPath: source)
        else { return }

        let copy = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("sm-migrate-\(UUID().uuidString).sqlite")
        try FileManager.default.copyItem(at: URL(fileURLWithPath: source), to: copy)
        defer { try? FileManager.default.removeItem(at: copy) }

        let database = try Database(url: copy)
        let servers = try database.allServers()

        print("""

        ── migration upgrade ── \(servers.count) servers survived
        \(servers.map { "  \($0.name)  \($0.displayTarget)  tags=\($0.tags)" }.joined(separator: "\n"))

        """)

        // The failure that matters is silent: one undecodable row makes
        // allServers() throw and the UI shows an empty list with no error.
        #expect(servers.isEmpty == false, "the copied database had servers; none survived")
        for server in servers {
            #expect(server.name.isEmpty == false)
            #expect(server.tags.isEmpty, "an upgraded row starts with no tags, not junk")
        }

        // And the new column has to be usable straight away.
        var first = try #require(servers.first)
        first.tags = ["migrated"]
        try database.save(first)
        let reloaded = try #require(try database.allServers().first { $0.id == first.id })
        #expect(reloaded.tags == ["migrated"])

        // Everything else the app reads must still open.
        _ = try database.allGroups()
        _ = try database.allIdentities()
    }
}
