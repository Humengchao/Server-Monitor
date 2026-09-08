using Microsoft.Data.Sqlite;
using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Store;

/// <summary>
/// The app's SQLite store: servers, credential <em>references</em>, and metric
/// history.
/// </summary>
/// <remarks>
/// The schema is the macOS build's, deliberately: the same six tables, the
/// same column names and types, and UUIDs stored as 16-byte BLOBs, so a
/// database can be carried between the two clients (P8's import/export is then
/// a copy, not a translation). The macOS migrations v1–v9 are collapsed into
/// one <c>v1</c> here — there is no Windows database in the wild that predates
/// this, so replaying nine steps to reach the same shape would be pure
/// ceremony. New migrations are appended from <c>v2</c>.
///
/// Every connection is opened per operation from a shared connection string
/// rather than kept as one object: <c>SqliteConnection</c> is not thread-safe,
/// and pooling is what <c>Microsoft.Data.Sqlite</c> already does behind the
/// string. WAL means the background history read does not make a poll's insert
/// wait, which is the same reason the macOS build uses a pool rather than a
/// queue.
/// </remarks>
public sealed class Database : IDisposable
{
    private readonly string _connectionString;
    /// <summary>Serialises writers, so two polls cannot collide on a BUSY.</summary>
    private readonly SemaphoreSlim _writeLock = new(1, 1);

    public string Path { get; }

    private Database(string path, string connectionString, bool keepAlive)
    {
        Path = path;
        _connectionString = connectionString;
        if (keepAlive)
        {
            // Opened *before* migrating, not after. A shared-cache in-memory
            // database lives only as long as some connection to it is open, so
            // with this the other way round the migration's own connection was
            // the only one, the schema went away when it closed, and every
            // later call opened a fresh empty database — "no such table:
            // server" on a store that had just been created.
            _keepAlive = new SqliteConnection(connectionString);
            _keepAlive.Open();
        }
        Migrate();
    }

    /// <summary>
    /// <c>%LOCALAPPDATA%\ServerMonitor\monitor.sqlite</c> (D6).
    /// </summary>
    /// <remarks>
    /// LocalApplicationData rather than Roaming: this is a machine's own
    /// monitoring history, and syncing a SQLite file between machines over a
    /// roaming profile is a good way to corrupt it.
    /// </remarks>
    public static string DefaultDirectory =>
        System.IO.Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "ServerMonitor");

    public static string DefaultPath => System.IO.Path.Combine(DefaultDirectory, "monitor.sqlite");

    public static Database Open(string? path = null)
    {
        path ??= DefaultPath;
        Directory.CreateDirectory(System.IO.Path.GetDirectoryName(path)!);
        var builder = new SqliteConnectionStringBuilder
        {
            DataSource = path,
            Mode = SqliteOpenMode.ReadWriteCreate,
            Cache = SqliteCacheMode.Default,
            Pooling = true,
        };
        return new Database(path, builder.ToString(), keepAlive: false);
    }

    /// <summary>
    /// An in-memory store, for tests.
    /// </summary>
    /// <remarks>
    /// <c>Cache=Shared</c> plus a name is load-bearing: a plain
    /// <c>:memory:</c> database is private to one connection, so this class's
    /// open-per-operation model would create a fresh empty database on every
    /// call. The name makes every connection in the process reach the same
    /// one, and it lives until the last connection closes — which is why
    /// <see cref="_keepAlive"/> exists.
    /// </remarks>
    public static Database InMemory()
    {
        var name = $"sm-{Guid.NewGuid():N}";
        var builder = new SqliteConnectionStringBuilder
        {
            DataSource = name,
            Mode = SqliteOpenMode.Memory,
            Cache = SqliteCacheMode.Shared,
        };
        return new Database(name, builder.ToString(), keepAlive: true);
    }

    /// <summary>
    /// Holds an in-memory database open. Null for a file-backed store.
    /// </summary>
    private readonly SqliteConnection? _keepAlive;

    /// <summary>
    /// A scratch store that cannot fail, for the launch path.
    /// </summary>
    /// <remarks>
    /// If the on-disk database will not open, the app still needs
    /// <em>a</em> store so the window can render and explain why. Opening an
    /// in-memory SQLite and migrating it has no external dependency; returning
    /// null only if even that fails, which nothing short of a broken SQLite
    /// would cause. Without this, "your database is unreadable" becomes "the
    /// app crashes on launch with no message".
    /// </remarks>
    public static Database? Scratch()
    {
        try
        {
            return InMemory();
        }
        catch (Exception)
        {
            return null;
        }
    }

    private SqliteConnection Connect()
    {
        var connection = new SqliteConnection(_connectionString);
        connection.Open();
        using var pragma = connection.CreateCommand();
        // Metrics writes are frequent and individually worthless if lost to a
        // hard crash, so trade durability for far less disk churn. WAL is
        // meaningless for an in-memory database and setting it there is a
        // no-op, not an error.
        pragma.CommandText = """
            PRAGMA journal_mode = WAL;
            PRAGMA synchronous = NORMAL;
            PRAGMA foreign_keys = ON;
            PRAGMA busy_timeout = 5000;
            """;
        pragma.ExecuteNonQuery();
        return connection;
    }

    // MARK: - Migrations

    private void Migrate()
    {
        using var connection = Connect();
        using var transaction = connection.BeginTransaction();

        Execute(connection, transaction, "CREATE TABLE IF NOT EXISTS schemaVersion (version INTEGER NOT NULL)");
        var current = ScalarLong(connection, transaction, "SELECT MAX(version) FROM schemaVersion") ?? 0;

        if (current < 1)
        {
            foreach (var statement in V1) Execute(connection, transaction, statement);
            Execute(connection, transaction, "INSERT INTO schemaVersion (version) VALUES (1)");
        }

        transaction.Commit();
    }

    /// <summary>
    /// The macOS build's v1–v9 as one schema.
    /// </summary>
    /// <remarks>
    /// Column order and types match the Swift definitions so both clients read
    /// the same file. Two things that look like mistakes and are not:
    /// <c>cpuThreshold</c> and friends are nullable, because null means
    /// "inherit the global limit" — a different thing from 0, meaning "no
    /// alert for this metric"; and <c>tagList</c> is comma-separated rather
    /// than a join table, because tags are free text one user types, the whole
    /// set is derived by scanning the servers anyway, and a second table would
    /// buy nothing but a join on every read.
    /// </remarks>
    private static readonly string[] V1 =
    [
        """
        CREATE TABLE server (
            id BLOB PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            host TEXT NOT NULL,
            port INTEGER NOT NULL DEFAULT 22,
            username TEXT NOT NULL,
            authKind TEXT NOT NULL,
            notes TEXT NOT NULL DEFAULT '',
            createdAt TEXT NOT NULL,
            sortIndex INTEGER NOT NULL DEFAULT 0,
            cores INTEGER NOT NULL DEFAULT 0,
            memoryTotal INTEGER NOT NULL DEFAULT 0,
            diskTotal INTEGER NOT NULL DEFAULT 0,
            dockerVersion TEXT NOT NULL DEFAULT '',
            countryCode TEXT NOT NULL DEFAULT '',
            sshAlias TEXT NOT NULL DEFAULT '',
            identityFile TEXT NOT NULL DEFAULT '',
            identityID BLOB REFERENCES identity(id) ON DELETE SET NULL,
            groupID BLOB REFERENCES machineGroup(id) ON DELETE SET NULL,
            osKind TEXT NOT NULL DEFAULT 'auto',
            cpuThreshold INTEGER,
            memoryThreshold INTEGER,
            diskThreshold INTEGER,
            tagList TEXT NOT NULL DEFAULT ''
        )
        """,
        """
        CREATE TABLE metricSample (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            serverID BLOB NOT NULL REFERENCES server(id) ON DELETE CASCADE,
            timestamp TEXT NOT NULL,
            cpuPercent REAL NOT NULL DEFAULT 0,
            load1 REAL NOT NULL DEFAULT 0,
            load5 REAL NOT NULL DEFAULT 0,
            load15 REAL NOT NULL DEFAULT 0,
            memoryUsed INTEGER NOT NULL DEFAULT 0,
            memoryTotal INTEGER NOT NULL DEFAULT 0,
            diskUsed INTEGER NOT NULL DEFAULT 0,
            diskTotal INTEGER NOT NULL DEFAULT 0,
            netRxRate REAL NOT NULL DEFAULT 0,
            netTxRate REAL NOT NULL DEFAULT 0,
            diskReadRate REAL NOT NULL DEFAULT 0,
            diskWriteRate REAL NOT NULL DEFAULT 0,
            netRxTotal INTEGER NOT NULL DEFAULT 0,
            netTxTotal INTEGER NOT NULL DEFAULT 0,
            uptimeSeconds INTEGER NOT NULL DEFAULT 0,
            latencyMs REAL NOT NULL DEFAULT 0
        )
        """,
        // Every history read is "one server over a time range".
        "CREATE INDEX metricSample_server_time ON metricSample (serverID, timestamp)",
        // The retention prune deletes by timestamp alone, which the composite
        // index above cannot serve: it was a full scan of the table every
        // minute — 8 ms at 87k rows on the macOS build, and growing with it.
        "CREATE INDEX metricSample_time ON metricSample (timestamp)",
        """
        CREATE TABLE snippet (
            id BLOB PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            command TEXT NOT NULL,
            notes TEXT NOT NULL DEFAULT '',
            category TEXT NOT NULL DEFAULT '',
            createdAt TEXT NOT NULL,
            useCount INTEGER NOT NULL DEFAULT 0,
            lastUsedAt TEXT
        )
        """,
        """
        CREATE TABLE identity (
            id BLOB PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            username TEXT NOT NULL,
            authKind TEXT NOT NULL,
            identityFile TEXT NOT NULL DEFAULT '',
            createdAt TEXT NOT NULL
        )
        """,
        """
        CREATE TABLE sessionRecord (
            id BLOB PRIMARY KEY NOT NULL,
            serverID BLOB REFERENCES server(id) ON DELETE SET NULL,
            serverName TEXT NOT NULL,
            kind TEXT NOT NULL,
            startedAt TEXT NOT NULL,
            endedAt TEXT
        )
        """,
        "CREATE INDEX sessionRecord_startedAt ON sessionRecord (startedAt)",
        """
        CREATE TABLE machineGroup (
            id BLOB PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            colorName TEXT NOT NULL DEFAULT 'blue',
            sortIndex INTEGER NOT NULL DEFAULT 0,
            createdAt TEXT NOT NULL
        )
        """,
    ];

    // MARK: - Machine groups

    public List<MachineGroup> AllGroups() => Query(
        "SELECT * FROM machineGroup ORDER BY sortIndex ASC, name ASC",
        reader => new MachineGroup
        {
            Id = ReadGuid(reader, "id"),
            Name = reader.GetString(reader.GetOrdinal("name")),
            ColorName = reader.GetString(reader.GetOrdinal("colorName")),
            SortIndex = reader.GetInt32(reader.GetOrdinal("sortIndex")),
            CreatedAt = ReadDate(reader, "createdAt") ?? DateTime.UtcNow,
        });

    public void Save(MachineGroup group) => Write(
        """
        INSERT INTO machineGroup (id, name, colorName, sortIndex, createdAt)
        VALUES ($id, $name, $colorName, $sortIndex, $createdAt)
        ON CONFLICT(id) DO UPDATE SET
            name = excluded.name,
            colorName = excluded.colorName,
            sortIndex = excluded.sortIndex
        """,
        command =>
        {
            command.Parameters.AddWithValue("$id", group.Id.ToByteArray());
            command.Parameters.AddWithValue("$name", group.Name);
            command.Parameters.AddWithValue("$colorName", group.ColorName);
            command.Parameters.AddWithValue("$sortIndex", group.SortIndex);
            command.Parameters.AddWithValue("$createdAt", Stamp(group.CreatedAt));
        });

    public void DeleteGroup(Guid id) => Write(
        "DELETE FROM machineGroup WHERE id = $id",
        command => command.Parameters.AddWithValue("$id", id.ToByteArray()));

    public int NextGroupSortIndex() =>
        (int)(ScalarLong("SELECT MAX(sortIndex) FROM machineGroup") ?? 0) + 1;

    // MARK: - Snippets

    public List<Snippet> AllSnippets() => Query(
        "SELECT * FROM snippet ORDER BY category ASC, name ASC",
        reader => new Snippet
        {
            Id = ReadGuid(reader, "id"),
            Name = reader.GetString(reader.GetOrdinal("name")),
            Command = reader.GetString(reader.GetOrdinal("command")),
            Notes = reader.GetString(reader.GetOrdinal("notes")),
            Category = reader.GetString(reader.GetOrdinal("category")),
            CreatedAt = ReadDate(reader, "createdAt") ?? DateTime.UtcNow,
            UseCount = reader.GetInt32(reader.GetOrdinal("useCount")),
            LastUsedAt = ReadDate(reader, "lastUsedAt"),
        });

    public void Save(Snippet snippet) => Write(
        """
        INSERT INTO snippet (id, name, command, notes, category, createdAt, useCount, lastUsedAt)
        VALUES ($id, $name, $command, $notes, $category, $createdAt, $useCount, $lastUsedAt)
        ON CONFLICT(id) DO UPDATE SET
            name = excluded.name,
            command = excluded.command,
            notes = excluded.notes,
            category = excluded.category
        """,
        command =>
        {
            command.Parameters.AddWithValue("$id", snippet.Id.ToByteArray());
            command.Parameters.AddWithValue("$name", snippet.Name);
            command.Parameters.AddWithValue("$command", snippet.Command);
            command.Parameters.AddWithValue("$notes", snippet.Notes);
            command.Parameters.AddWithValue("$category", snippet.Category);
            command.Parameters.AddWithValue("$createdAt", Stamp(snippet.CreatedAt));
            command.Parameters.AddWithValue("$useCount", snippet.UseCount);
            command.Parameters.AddWithValue("$lastUsedAt", Nullable(Stamp(snippet.LastUsedAt)));
        });

    public void DeleteSnippet(Guid id) => Write(
        "DELETE FROM snippet WHERE id = $id",
        command => command.Parameters.AddWithValue("$id", id.ToByteArray()));

    /// <summary>Records a use, for the "most used" ordering in the picker.</summary>
    public void MarkSnippetUsed(Guid id) => Write(
        "UPDATE snippet SET useCount = useCount + 1, lastUsedAt = $now WHERE id = $id",
        command =>
        {
            command.Parameters.AddWithValue("$now", Stamp(DateTime.UtcNow));
            command.Parameters.AddWithValue("$id", id.ToByteArray());
        });

    // MARK: - Identities

    public List<Identity> AllIdentities() => Query(
        "SELECT * FROM identity ORDER BY name ASC",
        reader => new Identity
        {
            Id = ReadGuid(reader, "id"),
            Name = reader.GetString(reader.GetOrdinal("name")),
            Username = reader.GetString(reader.GetOrdinal("username")),
            AuthKind = EnumNames.ToAuthKind(reader.GetString(reader.GetOrdinal("authKind"))),
            IdentityFile = reader.GetString(reader.GetOrdinal("identityFile")),
            CreatedAt = ReadDate(reader, "createdAt") ?? DateTime.UtcNow,
        });

    public void Save(Identity identity) => Write(
        """
        INSERT INTO identity (id, name, username, authKind, identityFile, createdAt)
        VALUES ($id, $name, $username, $authKind, $identityFile, $createdAt)
        ON CONFLICT(id) DO UPDATE SET
            name = excluded.name,
            username = excluded.username,
            authKind = excluded.authKind,
            identityFile = excluded.identityFile
        """,
        command =>
        {
            command.Parameters.AddWithValue("$id", identity.Id.ToByteArray());
            command.Parameters.AddWithValue("$name", identity.Name);
            command.Parameters.AddWithValue("$username", identity.Username);
            command.Parameters.AddWithValue("$authKind", identity.AuthKind.Store());
            command.Parameters.AddWithValue("$identityFile", identity.IdentityFile);
            command.Parameters.AddWithValue("$createdAt", Stamp(identity.CreatedAt));
        });

    public void DeleteIdentity(Guid id) => Write(
        "DELETE FROM identity WHERE id = $id",
        command => command.Parameters.AddWithValue("$id", id.ToByteArray()));

    /// <summary>How many servers point at an identity, so deletion can warn first.</summary>
    public int ServerCountUsingIdentity(Guid id) => (int)(ScalarLong(
        "SELECT COUNT(*) FROM server WHERE identityID = $id",
        command => command.Parameters.AddWithValue("$id", id.ToByteArray())) ?? 0);

    // MARK: - Session history

    public void Save(SessionRecord record) => Write(
        """
        INSERT INTO sessionRecord (id, serverID, serverName, kind, startedAt, endedAt)
        VALUES ($id, $serverID, $serverName, $kind, $startedAt, $endedAt)
        ON CONFLICT(id) DO UPDATE SET endedAt = excluded.endedAt
        """,
        command =>
        {
            command.Parameters.AddWithValue("$id", record.Id.ToByteArray());
            command.Parameters.AddWithValue("$serverID", Nullable(record.ServerId?.ToByteArray()));
            command.Parameters.AddWithValue("$serverName", record.ServerName);
            command.Parameters.AddWithValue("$kind", SessionRecord.Store(record.Kind));
            command.Parameters.AddWithValue("$startedAt", Stamp(record.StartedAt));
            command.Parameters.AddWithValue("$endedAt", Nullable(Stamp(record.EndedAt)));
        });

    public List<SessionRecord> RecentSessions(int limit = 200) => Query(
        "SELECT * FROM sessionRecord ORDER BY startedAt DESC LIMIT $limit",
        reader => new SessionRecord
        {
            Id = ReadGuid(reader, "id"),
            ServerId = ReadGuidOrNull(reader, "serverID"),
            ServerName = reader.GetString(reader.GetOrdinal("serverName")),
            Kind = SessionRecord.ToKind(reader.GetString(reader.GetOrdinal("kind"))),
            StartedAt = ReadDate(reader, "startedAt") ?? DateTime.UtcNow,
            EndedAt = ReadDate(reader, "endedAt"),
        },
        command => command.Parameters.AddWithValue("$limit", limit));

    public void ClearSessionHistory() => Write("DELETE FROM sessionRecord", _ => { });

    /// <summary>
    /// Closes sessions left open by a crash, so history has no dangling rows.
    /// </summary>
    public void CloseDanglingSessions() => Write(
        "UPDATE sessionRecord SET endedAt = startedAt WHERE endedAt IS NULL", _ => { });

    // MARK: - Servers

    public List<Server> AllServers() => Query(
        "SELECT * FROM server ORDER BY sortIndex ASC, createdAt ASC",
        reader => new Server
        {
            Id = ReadGuid(reader, "id"),
            Name = reader.GetString(reader.GetOrdinal("name")),
            Host = reader.GetString(reader.GetOrdinal("host")),
            Port = reader.GetInt32(reader.GetOrdinal("port")),
            Username = reader.GetString(reader.GetOrdinal("username")),
            AuthKind = EnumNames.ToAuthKind(reader.GetString(reader.GetOrdinal("authKind"))),
            SshAlias = reader.GetString(reader.GetOrdinal("sshAlias")),
            IdentityFile = reader.GetString(reader.GetOrdinal("identityFile")),
            IdentityId = ReadGuidOrNull(reader, "identityID"),
            GroupId = ReadGuidOrNull(reader, "groupID"),
            OsKind = EnumNames.ToOSKind(reader.GetString(reader.GetOrdinal("osKind"))),
            CpuThreshold = ReadIntOrNull(reader, "cpuThreshold"),
            MemoryThreshold = ReadIntOrNull(reader, "memoryThreshold"),
            DiskThreshold = ReadIntOrNull(reader, "diskThreshold"),
            Notes = reader.GetString(reader.GetOrdinal("notes")),
            CountryCode = reader.GetString(reader.GetOrdinal("countryCode")),
            TagList = reader.GetString(reader.GetOrdinal("tagList")),
            CreatedAt = ReadDate(reader, "createdAt") ?? DateTime.UtcNow,
            SortIndex = reader.GetInt32(reader.GetOrdinal("sortIndex")),
            Cores = reader.GetInt32(reader.GetOrdinal("cores")),
            MemoryTotal = reader.GetInt64(reader.GetOrdinal("memoryTotal")),
            DiskTotal = reader.GetInt64(reader.GetOrdinal("diskTotal")),
            DockerVersion = reader.GetString(reader.GetOrdinal("dockerVersion")),
        });

    private const string SaveServerSql =
        """
        INSERT INTO server (
            id, name, host, port, username, authKind, notes, createdAt, sortIndex,
            cores, memoryTotal, diskTotal, dockerVersion, countryCode, sshAlias,
            identityFile, identityID, groupID, osKind,
            cpuThreshold, memoryThreshold, diskThreshold, tagList)
        VALUES (
            $id, $name, $host, $port, $username, $authKind, $notes, $createdAt, $sortIndex,
            $cores, $memoryTotal, $diskTotal, $dockerVersion, $countryCode, $sshAlias,
            $identityFile, $identityID, $groupID, $osKind,
            $cpuThreshold, $memoryThreshold, $diskThreshold, $tagList)
        ON CONFLICT(id) DO UPDATE SET
            name = excluded.name, host = excluded.host, port = excluded.port,
            username = excluded.username, authKind = excluded.authKind,
            notes = excluded.notes, sortIndex = excluded.sortIndex,
            cores = excluded.cores, memoryTotal = excluded.memoryTotal,
            diskTotal = excluded.diskTotal, dockerVersion = excluded.dockerVersion,
            countryCode = excluded.countryCode, sshAlias = excluded.sshAlias,
            identityFile = excluded.identityFile, identityID = excluded.identityID,
            groupID = excluded.groupID, osKind = excluded.osKind,
            cpuThreshold = excluded.cpuThreshold,
            memoryThreshold = excluded.memoryThreshold,
            diskThreshold = excluded.diskThreshold, tagList = excluded.tagList
        """;

    private static void BindServer(SqliteCommand command, Server server)
    {
        command.Parameters.AddWithValue("$id", server.Id.ToByteArray());
        command.Parameters.AddWithValue("$name", server.Name);
        command.Parameters.AddWithValue("$host", server.Host);
        command.Parameters.AddWithValue("$port", server.Port);
        command.Parameters.AddWithValue("$username", server.Username);
        command.Parameters.AddWithValue("$authKind", server.AuthKind.Store());
        command.Parameters.AddWithValue("$notes", server.Notes);
        command.Parameters.AddWithValue("$createdAt", Stamp(server.CreatedAt));
        command.Parameters.AddWithValue("$sortIndex", server.SortIndex);
        command.Parameters.AddWithValue("$cores", server.Cores);
        command.Parameters.AddWithValue("$memoryTotal", server.MemoryTotal);
        command.Parameters.AddWithValue("$diskTotal", server.DiskTotal);
        command.Parameters.AddWithValue("$dockerVersion", server.DockerVersion);
        command.Parameters.AddWithValue("$countryCode", server.CountryCode);
        command.Parameters.AddWithValue("$sshAlias", server.SshAlias);
        command.Parameters.AddWithValue("$identityFile", server.IdentityFile);
        command.Parameters.AddWithValue("$identityID", Nullable(server.IdentityId?.ToByteArray()));
        command.Parameters.AddWithValue("$groupID", Nullable(server.GroupId?.ToByteArray()));
        command.Parameters.AddWithValue("$osKind", server.OsKind.Store());
        command.Parameters.AddWithValue("$cpuThreshold", Nullable(server.CpuThreshold));
        command.Parameters.AddWithValue("$memoryThreshold", Nullable(server.MemoryThreshold));
        command.Parameters.AddWithValue("$diskThreshold", Nullable(server.DiskThreshold));
        command.Parameters.AddWithValue("$tagList", server.TagList);
    }

    public void Save(Server server) => Write(SaveServerSql, command => BindServer(command, server));

    /// <summary>
    /// Deletes a server and its history (via the cascade).
    /// </summary>
    /// <remarks>
    /// The stored password is <em>not</em> deleted here: the credential store
    /// is the App's, and Core must not depend on it.
    /// <c>MonitorService.DeleteServer</c> does both.
    /// </remarks>
    public void DeleteServer(Guid id) => Write(
        "DELETE FROM server WHERE id = $id",
        command => command.Parameters.AddWithValue("$id", id.ToByteArray()));

    /// <summary>Writes <c>sortIndex</c> 0…n-1 in the given order, in one transaction.</summary>
    public void ReorderServers(IReadOnlyList<Guid> ids) => InTransaction((connection, transaction) =>
    {
        for (var index = 0; index < ids.Count; index++)
        {
            using var command = connection.CreateCommand();
            command.Transaction = transaction;
            command.CommandText = "UPDATE server SET sortIndex = $index WHERE id = $id";
            command.Parameters.AddWithValue("$index", index);
            command.Parameters.AddWithValue("$id", ids[index].ToByteArray());
            command.ExecuteNonQuery();
        }
    });

    public int NextSortIndex() => (int)(ScalarLong("SELECT MAX(sortIndex) FROM server") ?? 0) + 1;

    // MARK: - Samples

    public void Insert(MetricSample sample) => Write(InsertSampleSql,
        command => BindSample(command, sample.ServerId, sample.Timestamp, sample.ToSnapshot()));

    private const string InsertSampleSql =
        """
        INSERT INTO metricSample (
            serverID, timestamp, cpuPercent, load1, load5, load15,
            memoryUsed, memoryTotal, diskUsed, diskTotal,
            netRxRate, netTxRate, diskReadRate, diskWriteRate,
            netRxTotal, netTxTotal, uptimeSeconds, latencyMs)
        VALUES (
            $serverID, $timestamp, $cpuPercent, $load1, $load5, $load15,
            $memoryUsed, $memoryTotal, $diskUsed, $diskTotal,
            $netRxRate, $netTxRate, $diskReadRate, $diskWriteRate,
            $netRxTotal, $netTxTotal, $uptimeSeconds, $latencyMs)
        """;

    private static void BindSample(
        SqliteCommand command, Guid serverId, DateTime timestamp, MetricSnapshot snapshot)
    {
        command.Parameters.AddWithValue("$serverID", serverId.ToByteArray());
        command.Parameters.AddWithValue("$timestamp", Stamp(timestamp));
        command.Parameters.AddWithValue("$cpuPercent", snapshot.CpuPercent);
        command.Parameters.AddWithValue("$load1", snapshot.Load1);
        command.Parameters.AddWithValue("$load5", snapshot.Load5);
        command.Parameters.AddWithValue("$load15", snapshot.Load15);
        command.Parameters.AddWithValue("$memoryUsed", snapshot.MemoryUsed);
        command.Parameters.AddWithValue("$memoryTotal", snapshot.MemoryTotal);
        command.Parameters.AddWithValue("$diskUsed", snapshot.DiskUsed);
        command.Parameters.AddWithValue("$diskTotal", snapshot.DiskTotal);
        command.Parameters.AddWithValue("$netRxRate", snapshot.NetRxRate);
        command.Parameters.AddWithValue("$netTxRate", snapshot.NetTxRate);
        command.Parameters.AddWithValue("$diskReadRate", snapshot.DiskReadRate);
        command.Parameters.AddWithValue("$diskWriteRate", snapshot.DiskWriteRate);
        command.Parameters.AddWithValue("$netRxTotal", snapshot.NetRxTotal);
        command.Parameters.AddWithValue("$netTxTotal", snapshot.NetTxTotal);
        command.Parameters.AddWithValue("$uptimeSeconds", snapshot.UptimeSeconds);
        command.Parameters.AddWithValue("$latencyMs", snapshot.LatencyMs);
    }

    /// <summary>
    /// Everything a successful poll writes, as one transaction: the sample,
    /// and — only when the caller saw a fact move — the host facts and a
    /// probed OS.
    /// </summary>
    /// <remarks>
    /// One commit per host per poll rather than three. The row is re-read
    /// inside the transaction rather than saved from the poll's copy, so a
    /// name, tags, or an OS the user set while the poll ran are kept: the
    /// probed OS only lands on a row still marked automatic.
    /// </remarks>
    public Task RecordPollAsync(
        Guid serverId, MetricSnapshot snapshot, OSKind? detectedOS, bool updateFacts) =>
        Task.Run(() => InTransaction((connection, transaction) =>
        {
            using (var insert = connection.CreateCommand())
            {
                insert.Transaction = transaction;
                insert.CommandText = InsertSampleSql;
                BindSample(insert, serverId, DateTime.UtcNow, snapshot);
                insert.ExecuteNonQuery();
            }

            if (!updateFacts && detectedOS is null) return;

            using var read = connection.CreateCommand();
            read.Transaction = transaction;
            read.CommandText = "SELECT osKind FROM server WHERE id = $id";
            read.Parameters.AddWithValue("$id", serverId.ToByteArray());
            var storedOS = read.ExecuteScalar() as string;
            // Deleted while the poll was in flight: nothing to update.
            if (storedOS is null) return;

            var sets = new List<string>();
            if (updateFacts)
            {
                sets.AddRange([
                    "cores = $cores",
                    "memoryTotal = $memoryTotal",
                    "diskTotal = $diskTotal",
                    "dockerVersion = $dockerVersion",
                ]);
            }
            // Only onto a row still marked automatic — the user may have set
            // it by hand while this poll was in flight.
            var applyOS = detectedOS is not null && EnumNames.ToOSKind(storedOS) == OSKind.Auto;
            if (applyOS) sets.Add("osKind = $osKind");
            if (sets.Count == 0) return;

            using var update = connection.CreateCommand();
            update.Transaction = transaction;
            update.CommandText = $"UPDATE server SET {string.Join(", ", sets)} WHERE id = $id";
            update.Parameters.AddWithValue("$id", serverId.ToByteArray());
            if (updateFacts)
            {
                update.Parameters.AddWithValue("$cores", snapshot.Cores);
                update.Parameters.AddWithValue("$memoryTotal", snapshot.MemoryTotal);
                update.Parameters.AddWithValue("$diskTotal", snapshot.DiskTotal);
                update.Parameters.AddWithValue("$dockerVersion", snapshot.DockerVersion);
            }
            if (applyOS) update.Parameters.AddWithValue("$osKind", detectedOS!.Value.Store());
            update.ExecuteNonQuery();
        }));

    /// <summary>
    /// One column, so a lookup finishing a poll behind cannot undo the poll.
    /// </summary>
    public void UpdateCountryCode(Guid serverId, string countryCode) => Write(
        "UPDATE server SET countryCode = $countryCode WHERE id = $id",
        command =>
        {
            command.Parameters.AddWithValue("$countryCode", countryCode);
            command.Parameters.AddWithValue("$id", serverId.ToByteArray());
        });

    /// <summary>
    /// History thinned to at most <paramref name="maxPoints"/> time buckets,
    /// aggregated inside SQLite so only the buckets cross into C#.
    /// </summary>
    /// <remarks>
    /// The same query the macOS build runs, for the same reason: fetching a
    /// day of raw polls (~17,000 rows at a 5 s interval) and thinning them in
    /// managed code cost ~120 ms of main-thread time every 15 s at the 24 h
    /// range — a visible hitch — and held the database for all of it.
    ///
    /// Levels and rates are averaged; cumulative totals take the bucket's
    /// maximum, because a mean of running totals is a number that never
    /// happened. Same semantics as <see cref="Collect.HistoryReducer"/>, which
    /// remains the in-memory equivalent.
    /// </remarks>
    public List<MetricSample> ReducedSamples(
        Guid serverId, DateTime since, DateTime? until = null, int maxPoints = 240)
    {
        var end = until ?? DateTime.UtcNow;
        var span = (end - since).TotalSeconds;
        if (maxPoints <= 0 || span <= 0) return Samples(serverId, since, end);
        var bucketSeconds = Math.Max(1.0, span / maxPoints);

        const string Sql =
            """
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
                WHERE serverID = $serverID AND timestamp >= $since AND timestamp <= $until
            )
            GROUP BY CAST((epoch - $sinceEpoch) / $bucket AS INTEGER)
            ORDER BY ts
            """;

        return Query(Sql, reader =>
        {
            var snapshot = new MetricSnapshot
            {
                CpuPercent = Double(reader, "cpu"),
                Load1 = Double(reader, "l1"),
                Load5 = Double(reader, "l5"),
                Load15 = Double(reader, "l15"),
                MemoryUsed = (long)Double(reader, "memUsed"),
                MemoryTotal = (long)Double(reader, "memTotal"),
                DiskUsed = (long)Double(reader, "diskUsed"),
                DiskTotal = (long)Double(reader, "diskTotal"),
                NetRxRate = Double(reader, "rxRate"),
                NetTxRate = Double(reader, "txRate"),
                DiskReadRate = Double(reader, "rRate"),
                DiskWriteRate = Double(reader, "wRate"),
                NetRxTotal = (long)Double(reader, "rxTotal"),
                NetTxTotal = (long)Double(reader, "txTotal"),
                UptimeSeconds = (long)Double(reader, "uptime"),
                LatencyMs = Double(reader, "latency"),
            };
            var epoch = Double(reader, "ts");
            return new MetricSample(
                serverId, snapshot, DateTimeOffset.FromUnixTimeSeconds((long)epoch).UtcDateTime);
        },
        command =>
        {
            command.Parameters.AddWithValue("$serverID", serverId.ToByteArray());
            command.Parameters.AddWithValue("$since", Stamp(since));
            command.Parameters.AddWithValue("$until", Stamp(end));
            command.Parameters.AddWithValue("$sinceEpoch", new DateTimeOffset(since, TimeSpan.Zero).ToUnixTimeSeconds());
            command.Parameters.AddWithValue("$bucket", bucketSeconds);
        });
    }

    public List<MetricSample> Samples(Guid serverId, DateTime since, DateTime? until = null) => Query(
        """
        SELECT * FROM metricSample
        WHERE serverID = $serverID AND timestamp >= $since AND timestamp <= $until
        ORDER BY timestamp ASC
        """,
        ReadSample,
        command =>
        {
            command.Parameters.AddWithValue("$serverID", serverId.ToByteArray());
            command.Parameters.AddWithValue("$since", Stamp(since));
            command.Parameters.AddWithValue("$until", Stamp(until ?? DateTime.UtcNow));
        });

    public MetricSample? LatestSample(Guid serverId) => Query(
        "SELECT * FROM metricSample WHERE serverID = $serverID ORDER BY timestamp DESC LIMIT 1",
        ReadSample,
        command => command.Parameters.AddWithValue("$serverID", serverId.ToByteArray()))
        .FirstOrDefault();

    private static MetricSample ReadSample(SqliteDataReader reader) => new()
    {
        Id = reader.GetInt64(reader.GetOrdinal("id")),
        ServerId = ReadGuid(reader, "serverID"),
        Timestamp = ReadDate(reader, "timestamp") ?? DateTime.UtcNow,
        CpuPercent = Double(reader, "cpuPercent"),
        Load1 = Double(reader, "load1"),
        Load5 = Double(reader, "load5"),
        Load15 = Double(reader, "load15"),
        MemoryUsed = reader.GetInt64(reader.GetOrdinal("memoryUsed")),
        MemoryTotal = reader.GetInt64(reader.GetOrdinal("memoryTotal")),
        DiskUsed = reader.GetInt64(reader.GetOrdinal("diskUsed")),
        DiskTotal = reader.GetInt64(reader.GetOrdinal("diskTotal")),
        NetRxRate = Double(reader, "netRxRate"),
        NetTxRate = Double(reader, "netTxRate"),
        DiskReadRate = Double(reader, "diskReadRate"),
        DiskWriteRate = Double(reader, "diskWriteRate"),
        NetRxTotal = reader.GetInt64(reader.GetOrdinal("netRxTotal")),
        NetTxTotal = reader.GetInt64(reader.GetOrdinal("netTxTotal")),
        UptimeSeconds = reader.GetInt64(reader.GetOrdinal("uptimeSeconds")),
        LatencyMs = Double(reader, "latencyMs"),
    };

    /// <summary>
    /// Drops history older than <paramref name="retention"/>.
    /// </summary>
    /// <remarks>
    /// Raw samples only, no rollups: the server build's 1m/15m aggregates
    /// existed to serve many users from one database, which does not apply to
    /// a single-user store where a bounded window is enough.
    /// </remarks>
    public int PruneHistory(TimeSpan retention)
    {
        var cutoff = DateTime.UtcNow - retention;
        return WriteReturning(
            "DELETE FROM metricSample WHERE timestamp < $cutoff",
            command => command.Parameters.AddWithValue("$cutoff", Stamp(cutoff)));
    }

    // MARK: - Plumbing

    /// <summary>
    /// The stored form of a timestamp.
    /// </summary>
    /// <remarks>
    /// ISO 8601 in UTC with millisecond precision, matching what GRDB writes
    /// on the macOS side so both clients read each other's rows — and so
    /// SQLite's <c>strftime('%s', …)</c> in the bucketing query understands
    /// them. Lexicographic order equals chronological order, which is what
    /// makes the range indexes work.
    /// </remarks>
    internal static string Stamp(DateTime value) =>
        value.ToUniversalTime().ToString("yyyy-MM-dd HH:mm:ss.fff", System.Globalization.CultureInfo.InvariantCulture);

    private static string? Stamp(DateTime? value) => value is null ? null : Stamp(value.Value);

    private static object Nullable(object? value) => value ?? DBNull.Value;

    internal static DateTime? ReadDate(SqliteDataReader reader, string column)
    {
        var index = reader.GetOrdinal(column);
        if (reader.IsDBNull(index)) return null;
        var text = reader.GetString(index);
        return DateTime.TryParse(
            text,
            System.Globalization.CultureInfo.InvariantCulture,
            System.Globalization.DateTimeStyles.AdjustToUniversal | System.Globalization.DateTimeStyles.AssumeUniversal,
            out var parsed)
            ? parsed
            : null;
    }

    private static Guid ReadGuid(SqliteDataReader reader, string column) =>
        ReadGuidOrNull(reader, column) ?? Guid.Empty;

    /// <summary>
    /// A UUID stored as a 16-byte BLOB, the way GRDB writes it.
    /// </summary>
    /// <remarks>
    /// <c>Guid.ToByteArray()</c> and the matching constructor both use .NET's
    /// mixed-endian layout, so a round trip through this pair is stable and a
    /// row written here reads back identically. It is <em>not</em> RFC 4122
    /// byte order, so the 16 bytes will not match what a Swift
    /// <c>UUID</c> writes for the same textual id — which does not matter,
    /// because ids are only ever generated and compared by one client at a
    /// time, and P8's exchange goes through the textual form.
    /// </remarks>
    private static Guid? ReadGuidOrNull(SqliteDataReader reader, string column)
    {
        var index = reader.GetOrdinal(column);
        if (reader.IsDBNull(index)) return null;
        var bytes = new byte[16];
        var read = reader.GetBytes(index, 0, bytes, 0, 16);
        return read == 16 ? new Guid(bytes) : null;
    }

    private static int? ReadIntOrNull(SqliteDataReader reader, string column)
    {
        var index = reader.GetOrdinal(column);
        return reader.IsDBNull(index) ? null : reader.GetInt32(index);
    }

    private static double Double(SqliteDataReader reader, string column)
    {
        var index = reader.GetOrdinal(column);
        return reader.IsDBNull(index) ? 0 : reader.GetDouble(index);
    }

    private List<T> Query<T>(
        string sql, Func<SqliteDataReader, T> read, Action<SqliteCommand>? bind = null)
    {
        using var connection = Connect();
        using var command = connection.CreateCommand();
        command.CommandText = sql;
        bind?.Invoke(command);
        using var reader = command.ExecuteReader();
        var result = new List<T>();
        while (reader.Read()) result.Add(read(reader));
        return result;
    }

    private void Write(string sql, Action<SqliteCommand> bind) => WriteReturning(sql, bind);

    private int WriteReturning(string sql, Action<SqliteCommand> bind)
    {
        _writeLock.Wait();
        try
        {
            using var connection = Connect();
            using var command = connection.CreateCommand();
            command.CommandText = sql;
            bind(command);
            return command.ExecuteNonQuery();
        }
        finally
        {
            _writeLock.Release();
        }
    }

    private void InTransaction(Action<SqliteConnection, SqliteTransaction> body)
    {
        _writeLock.Wait();
        try
        {
            using var connection = Connect();
            using var transaction = connection.BeginTransaction();
            body(connection, transaction);
            transaction.Commit();
        }
        finally
        {
            _writeLock.Release();
        }
    }

    private long? ScalarLong(string sql, Action<SqliteCommand>? bind = null)
    {
        using var connection = Connect();
        return ScalarLong(connection, null, sql, bind);
    }

    private static long? ScalarLong(
        SqliteConnection connection,
        SqliteTransaction? transaction,
        string sql,
        Action<SqliteCommand>? bind = null)
    {
        using var command = connection.CreateCommand();
        command.Transaction = transaction;
        command.CommandText = sql;
        bind?.Invoke(command);
        var value = command.ExecuteScalar();
        return value is null or DBNull ? null : Convert.ToInt64(value, System.Globalization.CultureInfo.InvariantCulture);
    }

    private static void Execute(
        SqliteConnection connection, SqliteTransaction transaction, string sql)
    {
        using var command = connection.CreateCommand();
        command.Transaction = transaction;
        command.CommandText = sql;
        command.ExecuteNonQuery();
    }

    /// <summary>
    /// Closes the store: the in-memory keep-alive, and the connection pool.
    /// </summary>
    /// <remarks>
    /// The app opens one store for the life of the process and does not need
    /// this; the tests do, and what they were missing is why two unrelated
    /// things were wrong.
    ///
    /// A file-backed store is opened with <c>Pooling=true</c>, so disposing
    /// the per-operation connection returns its handle to the pool rather than
    /// to the OS and the file stays open. Test cleanup that deleted the file
    /// therefore always threw, and the <c>catch (IOException)</c> around it
    /// swallowed the failure — 36 scratch databases had accumulated in
    /// <c>%TEMP%</c>. The in-memory case leaked differently: nothing ever
    /// closed <see cref="_keepAlive"/>, so every <see cref="InMemory"/> store
    /// a test made stayed open for the whole run.
    ///
    /// Which is also what made <c>HandleTests</c> flaky. It measures the
    /// process's handle count across a hundred failed connections, and those
    /// two leaks are handles appearing in the same process from whatever else
    /// xUnit is running in parallel — enough of them inside the measurement
    /// window and the delta cleared the threshold. Emptying the pool is the
    /// fix for the test as much as for the files.
    /// </remarks>
    public void Dispose()
    {
        _keepAlive?.Dispose();
        // ClearPool identifies the pool by connection string; the connection
        // handed to it is never opened.
        using (var key = new SqliteConnection(_connectionString))
        {
            SqliteConnection.ClearPool(key);
        }
        _writeLock.Dispose();
    }
}
