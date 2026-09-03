import Foundation
import GRDB

/// The app's SQLite store: servers, credential *references*, and metric history.
///
/// One `DatabaseQueue` serialises every access, so the collector can write from
/// a background task while the UI reads, without either seeing a partial state.
/// `@unchecked Sendable`: the only state is the `DatabaseQueue`, which
/// serialises every access itself, so handing the object to a background task
/// for a read is safe — and that is how the history query stays off the main
/// thread.
public final class Database: @unchecked Sendable {
    /// A `DatabasePool` for the on-disk store, so the background history read
    /// never makes a poll's insert — and with it the main thread — wait: WAL
    /// lets readers run alongside the writer. In-memory stores (tests) are a
    /// `DatabaseQueue`, which the pool cannot be; both are `DatabaseWriter`.
    public let queue: any DatabaseWriter

    /// Where the store lives: ~/Library/Application Support/ServerMonitor.
    /// A scratch store that cannot fail, for the launch path: if the on-disk
    /// database will not open, the app still needs *a* store so the window can
    /// render and explain why. Opening an in-memory SQLite and running the
    /// migrations on it has no external dependency, but the initialiser is
    /// throwing, and `try!` there would turn "your database is unreadable"
    /// into "the app crashes on launch with no message".
    ///
    /// Returns nil only if even that fails, which nothing short of a broken
    /// SQLite would cause — the caller then has a real problem to report.
    public static func scratch() -> Database? {
        try? Database(inMemory: true)
    }

    public static func defaultURL() throws -> URL {
        let base = try FileManager.default.url(
            for: .applicationSupportDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        ).appendingPathComponent("ServerMonitor", isDirectory: true)
        try FileManager.default.createDirectory(at: base, withIntermediateDirectories: true)
        return base.appendingPathComponent("monitor.sqlite")
    }

    public init(url: URL) throws {
        var configuration = Configuration()
        // Metrics writes are frequent and individually worthless if lost on a
        // hard crash, so trade durability for far less disk churn.
        configuration.prepareDatabase { db in
            try db.execute(sql: "PRAGMA journal_mode = WAL")
            try db.execute(sql: "PRAGMA synchronous = NORMAL")
        }
        queue = try DatabasePool(path: url.path, configuration: configuration)
        try Self.migrator.migrate(queue)
    }

    /// In-memory store, for tests.
    public init(inMemory: Bool) throws {
        queue = try DatabaseQueue()
        try Self.migrator.migrate(queue)
    }

    static var migrator: DatabaseMigrator {
        var migrator = DatabaseMigrator()

        migrator.registerMigration("v1") { db in
            try db.create(table: "server") { table in
                table.primaryKey("id", .blob)
                table.column("name", .text).notNull()
                table.column("host", .text).notNull()
                table.column("port", .integer).notNull().defaults(to: 22)
                table.column("username", .text).notNull()
                table.column("authKind", .text).notNull()
                table.column("notes", .text).notNull().defaults(to: "")
                table.column("createdAt", .datetime).notNull()
                table.column("sortIndex", .integer).notNull().defaults(to: 0)
                table.column("cores", .integer).notNull().defaults(to: 0)
                table.column("memoryTotal", .integer).notNull().defaults(to: 0)
                table.column("diskTotal", .integer).notNull().defaults(to: 0)
                table.column("dockerVersion", .text).notNull().defaults(to: "")
            }

            try db.create(table: "metricSample") { table in
                table.autoIncrementedPrimaryKey("id")
                // Deleting a server takes its history with it.
                table.column("serverID", .blob).notNull()
                    .references("server", column: "id", onDelete: .cascade)
                table.column("timestamp", .datetime).notNull()
                table.column("cpuPercent", .double).notNull().defaults(to: 0)
                table.column("load1", .double).notNull().defaults(to: 0)
                table.column("load5", .double).notNull().defaults(to: 0)
                table.column("load15", .double).notNull().defaults(to: 0)
                table.column("memoryUsed", .integer).notNull().defaults(to: 0)
                table.column("memoryTotal", .integer).notNull().defaults(to: 0)
                table.column("diskUsed", .integer).notNull().defaults(to: 0)
                table.column("diskTotal", .integer).notNull().defaults(to: 0)
                table.column("netRxRate", .double).notNull().defaults(to: 0)
                table.column("netTxRate", .double).notNull().defaults(to: 0)
                table.column("diskReadRate", .double).notNull().defaults(to: 0)
                table.column("diskWriteRate", .double).notNull().defaults(to: 0)
                table.column("netRxTotal", .integer).notNull().defaults(to: 0)
                table.column("netTxTotal", .integer).notNull().defaults(to: 0)
                table.column("uptimeSeconds", .integer).notNull().defaults(to: 0)
                table.column("latencyMs", .double).notNull().defaults(to: 0)
            }
            // Every history read is "one server over a time range".
            try db.create(
                index: "metricSample_server_time",
                on: "metricSample",
                columns: ["serverID", "timestamp"]
            )
        }

        migrator.registerMigration("v2-country") { db in
            try db.alter(table: "server") { table in
                table.add(column: "countryCode", .text).notNull().defaults(to: "")
            }
        }

        migrator.registerMigration("v3-ssh-config") { db in
            try db.alter(table: "server") { table in
                table.add(column: "sshAlias", .text).notNull().defaults(to: "")
                table.add(column: "identityFile", .text).notNull().defaults(to: "")
            }
        }

        // Rows written before the SSH layer moved to the system client carry
        // the old auth kinds. Every one of them came from the ~/.ssh/config
        // importer, where the server name *is* the Host alias, so that is what
        // they become.
        migrator.registerMigration("v4-legacy-auth") { db in
            try db.execute(sql: """
                UPDATE server
                   SET authKind = 'sshConfigAlias',
                       sshAlias = CASE WHEN sshAlias = '' THEN name ELSE sshAlias END
                 WHERE authKind NOT IN ('sshConfigAlias', 'identityFile', 'agent')
                """)
        }

        migrator.registerMigration("v5-toolbox") { db in
            try db.create(table: "snippet") { table in
                table.primaryKey("id", .blob)
                table.column("name", .text).notNull()
                table.column("command", .text).notNull()
                table.column("notes", .text).notNull().defaults(to: "")
                table.column("category", .text).notNull().defaults(to: "")
                table.column("createdAt", .datetime).notNull()
                table.column("useCount", .integer).notNull().defaults(to: 0)
                table.column("lastUsedAt", .datetime)
            }

            try db.create(table: "identity") { table in
                table.primaryKey("id", .blob)
                table.column("name", .text).notNull()
                table.column("username", .text).notNull()
                table.column("authKind", .text).notNull()
                table.column("identityFile", .text).notNull().defaults(to: "")
                table.column("createdAt", .datetime).notNull()
            }

            try db.create(table: "sessionRecord") { table in
                table.primaryKey("id", .blob)
                // Nulled rather than cascaded: history should outlive the
                // server it refers to, so the name column carries the label.
                table.column("serverID", .blob)
                    .references("server", column: "id", onDelete: .setNull)
                table.column("serverName", .text).notNull()
                table.column("kind", .text).notNull()
                table.column("startedAt", .datetime).notNull()
                table.column("endedAt", .datetime)
            }
            try db.create(
                index: "sessionRecord_startedAt",
                on: "sessionRecord",
                columns: ["startedAt"]
            )

            // A server may delegate its login to a shared identity.
            try db.alter(table: "server") { table in
                table.add(column: "identityID", .blob)
                    .references("identity", column: "id", onDelete: .setNull)
            }
        }

        migrator.registerMigration("v6-groups") { db in
            try db.create(table: "machineGroup") { table in
                table.primaryKey("id", .blob)
                table.column("name", .text).notNull()
                table.column("colorName", .text).notNull().defaults(to: "blue")
                table.column("sortIndex", .integer).notNull().defaults(to: 0)
                table.column("createdAt", .datetime).notNull()
            }
            try db.alter(table: "server") { table in
                // Deleting a group must not take its machines with it.
                table.add(column: "groupID", .blob)
                    .references("machineGroup", column: "id", onDelete: .setNull)
            }
        }

        migrator.registerMigration("v7-os-and-thresholds") { db in
            try db.alter(table: "server") { table in
                table.add(column: "osKind", .text).notNull().defaults(to: "auto")
                // Nullable on purpose: null means "inherit the global limit",
                // which is different from 0, meaning "no alert for this metric".
                table.add(column: "cpuThreshold", .integer)
                table.add(column: "memoryThreshold", .integer)
                table.add(column: "diskThreshold", .integer)
            }
        }

        migrator.registerMigration("v8-tags") { db in
            try db.alter(table: "server") { table in
                // Comma-separated rather than a join table: tags are free text
                // a single user types, the whole set is derived by scanning the
                // servers anyway, and a second table would buy nothing but a
                // join on every read.
                table.add(column: "tagList", .text).notNull().defaults(to: "")
            }
        }

        migrator.registerMigration("v9-prune-index") { db in
            // The retention prune deletes by timestamp alone, which the
            // (serverID, timestamp) index cannot serve: it was a full scan of
            // the table every minute — 8 ms at 87k rows and growing with it.
            try db.create(
                index: "metricSample_time",
                on: "metricSample",
                columns: ["timestamp"],
                ifNotExists: true
            )
        }

        return migrator
    }

    // MARK: - Machine groups

    public func allGroups() throws -> [MachineGroup] {
        try queue.read { db in
            try MachineGroup.order(Column("sortIndex").asc, Column("name").asc).fetchAll(db)
        }
    }

    public func save(_ group: MachineGroup) throws {
        try queue.write { db in try group.save(db) }
    }

    public func deleteGroup(id: UUID) throws {
        _ = try queue.write { db in try MachineGroup.deleteOne(db, key: ["id": id]) }
    }

    public func nextGroupSortIndex() throws -> Int {
        try queue.read { db in
            (try Int.fetchOne(db, sql: "SELECT MAX(sortIndex) FROM machineGroup") ?? 0) + 1
        }
    }

    // MARK: - Snippets

    public func allSnippets() throws -> [Snippet] {
        try queue.read { db in
            try Snippet.order(Column("category").asc, Column("name").asc).fetchAll(db)
        }
    }

    public func save(_ snippet: Snippet) throws {
        try queue.write { db in try snippet.save(db) }
    }

    public func deleteSnippet(id: UUID) throws {
        _ = try queue.write { db in try Snippet.deleteOne(db, key: ["id": id]) }
    }

    /// Records a use, for the "most used" ordering in the picker.
    public func markSnippetUsed(id: UUID) throws {
        try queue.write { db in
            try db.execute(
                sql: "UPDATE snippet SET useCount = useCount + 1, lastUsedAt = ? WHERE id = ?",
                arguments: [Date(), id]
            )
        }
    }

    // MARK: - Identities

    public func allIdentities() throws -> [Identity] {
        try queue.read { db in
            try Identity.order(Column("name").asc).fetchAll(db)
        }
    }

    public func save(_ identity: Identity) throws {
        try queue.write { db in try identity.save(db) }
    }

    public func deleteIdentity(id: UUID) throws {
        _ = try queue.write { db in try Identity.deleteOne(db, key: ["id": id]) }
    }

    /// How many servers point at an identity, so deletion can warn first.
    public func serverCount(usingIdentity id: UUID) throws -> Int {
        try queue.read { db in
            try Int.fetchOne(
                db,
                sql: "SELECT COUNT(*) FROM server WHERE identityID = ?",
                arguments: [id]
            ) ?? 0
        }
    }

    // MARK: - Session history

    public func save(_ record: SessionRecord) throws {
        try queue.write { db in try record.save(db) }
    }

    public func recentSessions(limit: Int = 200) throws -> [SessionRecord] {
        try queue.read { db in
            try SessionRecord
                .order(Column("startedAt").desc)
                .limit(limit)
                .fetchAll(db)
        }
    }

    public func clearSessionHistory() throws {
        _ = try queue.write { db in try SessionRecord.deleteAll(db) }
    }

    /// Closes sessions left open by a crash, so history has no dangling rows.
    public func closeDanglingSessions() throws {
        try queue.write { db in
            try db.execute(sql: "UPDATE sessionRecord SET endedAt = startedAt WHERE endedAt IS NULL")
        }
    }

    // MARK: - Servers

    public func allServers() throws -> [Server] {
        try queue.read { db in
            try Server
                .order(Column("sortIndex").asc, Column("createdAt").asc)
                .fetchAll(db)
        }
    }

    public func save(_ server: Server) throws {
        try queue.write { db in try server.save(db) }
    }

    /// Deletes a server, its history (via the cascade) and any stored password.
    public func deleteServer(id: UUID) throws {
        try queue.write { db in
            _ = try Server.deleteOne(db, key: ["id": id])
        }
        Keychain.deletePassword(serverID: id)
    }

    /// Writes `sortIndex` 0…n-1 in the given order, in one transaction.
    public func reorderServers(_ ids: [UUID]) throws {
        try queue.write { db in
            for (index, id) in ids.enumerated() {
                try db.execute(sql: "UPDATE server SET sortIndex = ? WHERE id = ?", arguments: [index, id])
            }
        }
    }

    public func nextSortIndex() throws -> Int {
        try queue.read { db in
            let maximum = try Int.fetchOne(db, sql: "SELECT MAX(sortIndex) FROM server") ?? 0
            return maximum + 1
        }
    }

    // MARK: - Samples

    public func insert(_ sample: MetricSample) throws {
        var value = sample
        try queue.write { db in try value.insert(db) }
    }

    /// Everything a successful poll writes, as one transaction: the sample,
    /// and — only when the caller saw a fact move — the host facts and a
    /// probed OS.
    ///
    /// These were three separate `write`s, three commits per host per poll,
    /// and they ran on the main actor. GRDB's async `write` suspends until
    /// its own queue is free rather than parking a thread. The row is fetched
    /// fresh inside the transaction rather than saved from the poll's copy, so
    /// a name, tags, or an OS the user set while the poll ran are kept: the
    /// probed OS only lands on a row still marked automatic.
    public func recordPoll(
        serverID: UUID,
        snapshot: MetricSnapshot,
        detectedOS: OSKind?,
        updateFacts: Bool
    ) async throws {
        try await queue.write { db in
            _ = try MetricSample(serverID: serverID, snapshot: snapshot).inserted(db)
            guard updateFacts || detectedOS != nil,
                  var server = try Server.fetchOne(db, key: ["id": serverID])
            else { return }
            var changed = false
            if updateFacts {
                server.cores = snapshot.cores
                server.memoryTotal = snapshot.memoryTotal
                server.diskTotal = snapshot.diskTotal
                server.dockerVersion = snapshot.dockerVersion
                changed = true
            }
            if let detectedOS, server.osKind == .auto {
                server.osKind = detectedOS
                changed = true
            }
            if changed { try server.update(db) }
        }
    }

    /// One column, so a lookup finished a poll behind cannot undo the poll.
    public func updateCountryCode(serverID: UUID, countryCode: String) throws {
        try queue.write { db in
            try db.execute(
                sql: "UPDATE server SET countryCode = ? WHERE id = ?",
                arguments: [countryCode, serverID]
            )
        }
    }

    /// History thinned to at most `maxPoints` time buckets, aggregated inside
    /// SQLite so only the buckets cross into Swift.
    ///
    /// Fetching a day of raw polls and thinning them in Swift cost ~120 ms of
    /// main-thread time every 15 s at the 24 h range — a visible hitch — and
    /// held the serialised database queue for all of it, so a poll's insert
    /// landing in that window blocked the main thread too. Levels and rates are
    /// averaged; cumulative totals take the bucket's maximum, because a mean of
    /// running totals is a number that never happened. Same semantics as
    /// `HistoryReducer`, which remains the in-memory equivalent.
    public func reducedSamples(
        serverID: UUID, since: Date, until: Date = Date(), maxPoints: Int
    ) throws -> [MetricSample] {
        let span = until.timeIntervalSince(since)
        guard maxPoints > 0, span > 0 else {
            return try samples(serverID: serverID, since: since, until: until)
        }
        let bucketSeconds = max(1.0, span / Double(maxPoints))
        let sql = """
            SELECT
                AVG(epoch)          AS ts,
                AVG(cpuPercent)     AS cpu,
                AVG(load1)          AS l1,
                AVG(load5)          AS l5,
                AVG(load15)         AS l15,
                AVG(memoryUsed)     AS memUsed,
                MAX(memoryTotal)    AS memTotal,
                AVG(diskUsed)       AS diskUsed,
                MAX(diskTotal)      AS diskTotal,
                AVG(netRxRate)      AS rxRate,
                AVG(netTxRate)      AS txRate,
                AVG(diskReadRate)   AS rRate,
                AVG(diskWriteRate)  AS wRate,
                MAX(netRxTotal)     AS rxTotal,
                MAX(netTxTotal)     AS txTotal,
                MAX(uptimeSeconds)  AS uptime,
                AVG(latencyMs)      AS latency
            FROM (
                SELECT *, strftime('%s', timestamp) AS epoch
                FROM metricSample
                WHERE serverID = :serverID AND timestamp >= :since AND timestamp <= :until
            )
            GROUP BY CAST((epoch - :sinceEpoch) / :bucket AS INTEGER)
            ORDER BY ts
            """
        return try queue.read { db in
            try Row.fetchAll(db, sql: sql, arguments: [
                "serverID": serverID,
                "since": since,
                "until": until,
                "sinceEpoch": since.timeIntervalSince1970,
                "bucket": bucketSeconds,
            ]).map { row in
                var snapshot = MetricSnapshot()
                snapshot.cpuPercent = row["cpu"] ?? 0
                snapshot.load1 = row["l1"] ?? 0
                snapshot.load5 = row["l5"] ?? 0
                snapshot.load15 = row["l15"] ?? 0
                snapshot.memoryUsed = Int64((row["memUsed"] as Double?) ?? 0)
                snapshot.memoryTotal = row["memTotal"] ?? 0
                snapshot.diskUsed = Int64((row["diskUsed"] as Double?) ?? 0)
                snapshot.diskTotal = row["diskTotal"] ?? 0
                snapshot.netRxRate = row["rxRate"] ?? 0
                snapshot.netTxRate = row["txRate"] ?? 0
                snapshot.diskReadRate = row["rRate"] ?? 0
                snapshot.diskWriteRate = row["wRate"] ?? 0
                snapshot.netRxTotal = row["rxTotal"] ?? 0
                snapshot.netTxTotal = row["txTotal"] ?? 0
                snapshot.uptimeSeconds = row["uptime"] ?? 0
                snapshot.latencyMs = row["latency"] ?? 0
                let epoch: Double = row["ts"] ?? since.timeIntervalSince1970
                return MetricSample(
                    serverID: serverID,
                    timestamp: Date(timeIntervalSince1970: epoch),
                    snapshot: snapshot
                )
            }
        }
    }

    public func samples(serverID: UUID, since: Date, until: Date = Date()) throws -> [MetricSample] {
        try queue.read { db in
            try MetricSample
                .filter(Column("serverID") == serverID)
                .filter(Column("timestamp") >= since && Column("timestamp") <= until)
                .order(Column("timestamp").asc)
                .fetchAll(db)
        }
    }

    public func latestSample(serverID: UUID) throws -> MetricSample? {
        try queue.read { db in
            try MetricSample
                .filter(Column("serverID") == serverID)
                .order(Column("timestamp").desc)
                .fetchOne(db)
        }
    }

    /// Drops history older than `retention`.
    ///
    /// The local app keeps raw samples only; the server build's 1m/15m rollups
    /// existed to serve many users from one database, which does not apply to a
    /// single-user store where a bounded window is enough.
    @discardableResult
    public func pruneHistory(retention: TimeInterval) throws -> Int {
        let cutoff = Date().addingTimeInterval(-retention)
        return try queue.write { db in
            try MetricSample
                .filter(Column("timestamp") < cutoff)
                .deleteAll(db)
        }
    }
}
