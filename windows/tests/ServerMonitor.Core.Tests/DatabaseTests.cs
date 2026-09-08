using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class DatabaseTests
{
    private static Server NewServer(string name = "web-1", int sortIndex = 0) => new()
    {
        Name = name,
        Host = "10.0.0.1",
        Username = "root",
        AuthKind = AuthKind.Agent,
        SortIndex = sortIndex,
    };

    [Fact]
    public void MigrationsRunOnAFreshStore()
    {
        var database = Database.InMemory();
        Assert.Empty(database.AllServers());
        Assert.Empty(database.AllIdentities());
        Assert.Empty(database.AllGroups());
        Assert.Empty(database.AllSnippets());
        Assert.Empty(database.RecentSessions());
    }

    [Fact]
    public void MigratingTwiceIsANoOp()
    {
        // Opening the same file twice must not try to recreate the tables:
        // that is every launch after the first.
        var path = Path.Combine(Path.GetTempPath(), $"sm-test-{Guid.NewGuid():N}.sqlite");
        try
        {
            var first = Database.Open(path);
            first.Save(NewServer());
            var second = Database.Open(path);
            Assert.Single(second.AllServers());
        }
        finally
        {
            foreach (var suffix in new[] { "", "-wal", "-shm" })
            {
                try { File.Delete(path + suffix); } catch (IOException) { /* WAL may linger */ }
            }
        }
    }

    [Fact]
    public void AServerRoundTripsEveryColumn()
    {
        var database = Database.InMemory();
        var server = NewServer();
        server.Port = 2222;
        server.AuthKind = AuthKind.IdentityFile;
        server.IdentityFile = @"C:\Users\me\.ssh\id_ed25519";
        server.SshAlias = "web";
        server.OsKind = OSKind.Linux;
        server.CountryCode = "JP";
        server.TagList = "prod,db";
        server.Notes = "note";
        server.CpuThreshold = 85;
        server.DiskThreshold = 0;
        server.Cores = 8;
        server.MemoryTotal = 16_000_000_000;
        server.DiskTotal = 500_000_000_000;
        server.DockerVersion = "24.0.7";
        database.Save(server);

        var loaded = Assert.Single(database.AllServers());
        Assert.Equal(server.Id, loaded.Id);
        Assert.Equal(2222, loaded.Port);
        Assert.Equal(AuthKind.IdentityFile, loaded.AuthKind);
        Assert.Equal(server.IdentityFile, loaded.IdentityFile);
        Assert.Equal("web", loaded.SshAlias);
        Assert.Equal(OSKind.Linux, loaded.OsKind);
        Assert.Equal("JP", loaded.CountryCode);
        Assert.Equal(["prod", "db"], loaded.Tags);
        Assert.Equal(85, loaded.CpuThreshold);
        Assert.Equal(8, loaded.Cores);
        Assert.Equal("24.0.7", loaded.DockerVersion);
    }

    [Fact]
    public void ANullThresholdMeansInheritAndZeroMeansOff()
    {
        // Two different things, and the difference is load-bearing: null
        // follows the global setting, 0 disables the alert for that metric.
        var database = Database.InMemory();
        var server = NewServer();
        server.CpuThreshold = null;
        server.DiskThreshold = 0;
        database.Save(server);

        var loaded = Assert.Single(database.AllServers());
        Assert.Null(loaded.CpuThreshold);
        Assert.Equal(0, loaded.DiskThreshold);
    }

    [Fact]
    public void AnUnknownStoredAuthKindFallsBackRatherThanFailingTheFetch()
    {
        // Row decoding is all-or-nothing: without the fallback, one row from a
        // future build makes the whole fetch fail and the app shows no servers
        // at all, with nothing to explain why.
        Assert.Equal(AuthKind.SshConfigAlias, EnumNames.ToAuthKind("somethingNew"));
        Assert.Equal(AuthKind.SshConfigAlias, EnumNames.ToAuthKind(null));
        Assert.Equal(OSKind.Auto, EnumNames.ToOSKind("bsd"));
    }

    [Fact]
    public void SavingAnExistingServerUpdatesRatherThanDuplicates()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        server.Name = "renamed";
        database.Save(server);

        var loaded = Assert.Single(database.AllServers());
        Assert.Equal("renamed", loaded.Name);
    }

    [Fact]
    public void DeletingAServerTakesItsHistoryWithIt()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        database.Insert(new MetricSample(server.Id, new MetricSnapshot { CpuPercent = 10 }));
        Assert.NotNull(database.LatestSample(server.Id));

        database.DeleteServer(server.Id);
        Assert.Empty(database.AllServers());
        // The cascade, which only fires because Connect() turns foreign keys
        // on — SQLite has them off by default, per connection.
        Assert.Null(database.LatestSample(server.Id));
    }

    [Fact]
    public void DeletingAGroupKeepsItsMachines()
    {
        var database = Database.InMemory();
        var group = new MachineGroup { Name = "prod" };
        database.Save(group);
        var server = NewServer();
        server.GroupId = group.Id;
        database.Save(server);

        database.DeleteGroup(group.Id);
        var loaded = Assert.Single(database.AllServers());
        Assert.Null(loaded.GroupId);
    }

    [Fact]
    public void DeletingAnIdentityLeavesItsServersOnTheirOwnSettings()
    {
        var database = Database.InMemory();
        var identity = new Identity { Name = "deploy", Username = "deploy" };
        database.Save(identity);
        var server = NewServer();
        server.IdentityId = identity.Id;
        database.Save(server);
        Assert.Equal(1, database.ServerCountUsingIdentity(identity.Id));

        database.DeleteIdentity(identity.Id);
        Assert.Null(Assert.Single(database.AllServers()).IdentityId);
    }

    [Fact]
    public void SessionHistoryOutlivesTheServerItRefersTo()
    {
        // Nulled rather than cascaded: the name column carries the label.
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        database.Save(new SessionRecord
        {
            ServerId = server.Id,
            ServerName = server.Name,
            Kind = SessionKind.Terminal,
        });

        database.DeleteServer(server.Id);
        var record = Assert.Single(database.RecentSessions());
        Assert.Null(record.ServerId);
        Assert.Equal("web-1", record.ServerName);
    }

    [Fact]
    public void DanglingSessionsAreClosedOnRelaunch()
    {
        var database = Database.InMemory();
        var started = DateTime.UtcNow.AddMinutes(-5);
        database.Save(new SessionRecord
        {
            ServerId = null,
            ServerName = "gone",
            Kind = SessionKind.Sftp,
            StartedAt = started,
        });
        Assert.True(Assert.Single(database.RecentSessions()).IsOpen);

        database.CloseDanglingSessions();
        var closed = Assert.Single(database.RecentSessions());
        Assert.False(closed.IsOpen);
        Assert.Equal(TimeSpan.Zero, closed.Duration);
    }

    [Fact]
    public void ReorderWritesZeroToNMinusOne()
    {
        var database = Database.InMemory();
        var a = NewServer("a", 5);
        var b = NewServer("b", 9);
        var c = NewServer("c", 1);
        foreach (var server in new[] { a, b, c }) database.Save(server);

        database.ReorderServers([b.Id, c.Id, a.Id]);
        Assert.Equal(["b", "c", "a"], database.AllServers().Select(s => s.Name));
        Assert.Equal([0, 1, 2], database.AllServers().Select(s => s.SortIndex));
    }

    [Fact]
    public void SnippetUseIsCounted()
    {
        var database = Database.InMemory();
        var snippet = new Snippet { Name = "df", Command = "df -h" };
        database.Save(snippet);
        database.MarkSnippetUsed(snippet.Id);
        database.MarkSnippetUsed(snippet.Id);

        var loaded = Assert.Single(database.AllSnippets());
        Assert.Equal(2, loaded.UseCount);
        Assert.NotNull(loaded.LastUsedAt);
    }

    [Fact]
    public async Task RecordPollWritesTheSampleAndTheFactsInOneGo()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);

        var snapshot = new MetricSnapshot
        {
            CpuPercent = 42,
            MemoryUsed = 4_000_000_000,
            MemoryTotal = 16_000_000_000,
            Cores = 8,
            DiskTotal = 500_000_000_000,
            DockerVersion = "24.0.7",
        };
        await database.RecordPollAsync(server.Id, snapshot, OSKind.Linux, updateFacts: true);

        var sample = database.LatestSample(server.Id);
        Assert.NotNull(sample);
        Assert.Equal(42, sample.CpuPercent);
        var loaded = Assert.Single(database.AllServers());
        Assert.Equal(8, loaded.Cores);
        Assert.Equal("24.0.7", loaded.DockerVersion);
        Assert.Equal(OSKind.Linux, loaded.OsKind);
    }

    [Fact]
    public async Task AProbedOsDoesNotOverrideOneTheUserSetByHand()
    {
        // The row is re-read inside the transaction rather than saved from the
        // poll's copy, precisely so an edit made while the poll was in flight
        // survives.
        var database = Database.InMemory();
        var server = NewServer();
        server.OsKind = OSKind.Windows;
        database.Save(server);

        await database.RecordPollAsync(server.Id, new MetricSnapshot(), OSKind.Linux, updateFacts: false);
        Assert.Equal(OSKind.Windows, Assert.Single(database.AllServers()).OsKind);
    }

    [Fact]
    public async Task RecordPollForADeletedServerDoesNotResurrectIt()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        database.DeleteServer(server.Id);

        // The insert fails its foreign key; the whole transaction rolls back
        // rather than half-applying.
        await Assert.ThrowsAnyAsync<Exception>(() =>
            database.RecordPollAsync(server.Id, new MetricSnapshot(), null, updateFacts: true));
        Assert.Empty(database.AllServers());
    }

    [Fact]
    public void PruneDropsOnlyWhatIsOlderThanRetention()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);

        var now = DateTime.UtcNow;
        database.Insert(new MetricSample(server.Id, new MetricSnapshot(), now.AddDays(-10)));
        database.Insert(new MetricSample(server.Id, new MetricSnapshot(), now.AddHours(-1)));

        var removed = database.PruneHistory(TimeSpan.FromDays(7));
        Assert.Equal(1, removed);
        Assert.Single(database.Samples(server.Id, now.AddDays(-30)));
    }

    [Fact]
    public void ReducedSamplesBucketsToTheRequestedCount()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);

        var since = DateTime.UtcNow.AddHours(-1);
        // 600 samples across the hour: six per bucket at 100 buckets.
        for (var i = 0; i < 600; i++)
        {
            database.Insert(new MetricSample(
                server.Id,
                new MetricSnapshot { CpuPercent = 50, NetRxTotal = i, MemoryTotal = 16_000_000_000 },
                since.AddSeconds(i * 6)));
        }

        var reduced = database.ReducedSamples(server.Id, since, maxPoints: 100);
        Assert.InRange(reduced.Count, 90, 101);
        // Levels are averaged.
        Assert.All(reduced, sample => Assert.Equal(50, sample.CpuPercent, 3));
        // Cumulative totals take the bucket maximum: a mean of running totals
        // is a number that never happened, and it would make the traffic
        // counter appear to go backwards mid-chart.
        Assert.True(
            reduced.Zip(reduced.Skip(1)).All(pair => pair.Second.NetRxTotal >= pair.First.NetRxTotal),
            "bucket totals must not decrease");
    }

    [Fact]
    public void ReducedSamplesFallsBackToRawForAZeroSpan()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        var at = DateTime.UtcNow;
        database.Insert(new MetricSample(server.Id, new MetricSnapshot { CpuPercent = 7 }, at));

        var reduced = database.ReducedSamples(server.Id, at, at, maxPoints: 240);
        Assert.Equal(7, Assert.Single(reduced).CpuPercent);
    }

    [Fact]
    public void TimestampsAreStoredSoLexicographicOrderIsChronological()
    {
        // What makes the range indexes work, and what SQLite's
        // strftime('%s', …) in the bucketing query needs.
        var early = Database.Stamp(new DateTime(2026, 1, 2, 3, 4, 5, DateTimeKind.Utc));
        var late = Database.Stamp(new DateTime(2026, 11, 2, 3, 4, 5, DateTimeKind.Utc));
        Assert.True(string.CompareOrdinal(early, late) < 0);
        Assert.Equal("2026-01-02 03:04:05.000", early);
    }

    [Fact]
    public void ALocalTimestampIsStoredAsUtc()
    {
        // Two clients reading one file must agree on what a row's time means.
        var local = new DateTime(2026, 6, 1, 12, 0, 0, DateTimeKind.Local);
        Assert.Equal(Database.Stamp(local.ToUniversalTime()), Database.Stamp(local));
    }

    [Fact]
    public void GuidsRoundTripThroughTheBlobColumn()
    {
        var database = Database.InMemory();
        var server = NewServer();
        database.Save(server);
        Assert.Equal(server.Id, Assert.Single(database.AllServers()).Id);
    }
}

public class HistoryReducerTests
{
    private static List<MetricSample> Series(int count, DateTime start, double seconds = 5)
    {
        var id = Guid.NewGuid();
        return Enumerable.Range(0, count).Select(i => new MetricSample(
            id,
            new MetricSnapshot
            {
                CpuPercent = i % 100,
                NetRxTotal = i * 1000,
                MemoryTotal = 16_000_000_000,
                MemoryUsed = 8_000_000_000,
            },
            start.AddSeconds(i * seconds))).ToList();
    }

    [Fact]
    public void ASeriesUnderTheLimitComesBackUntouched()
    {
        var samples = Series(50, DateTime.UtcNow.AddHours(-1));
        Assert.Equal(50, HistoryReducer.Reduce(samples, 240).Count);
    }

    [Fact]
    public void ALongSeriesIsThinnedToTheBucketCount()
    {
        var samples = Series(17_000, DateTime.UtcNow.AddDays(-1));
        var reduced = HistoryReducer.Reduce(samples, 240);
        Assert.InRange(reduced.Count, 200, 241);
    }

    [Fact]
    public void CumulativeTotalsTakeTheBucketsLastValueNotItsMean()
    {
        // A mean of running totals is a number that never happened.
        var samples = Series(1000, DateTime.UtcNow.AddHours(-2));
        var reduced = HistoryReducer.Reduce(samples, 10);
        Assert.True(
            reduced.Zip(reduced.Skip(1)).All(pair => pair.Second.NetRxTotal >= pair.First.NetRxTotal),
            "totals must be monotonic");
    }

    [Fact]
    public void ABucketIsStampedAtItsMidpoint()
    {
        var start = new DateTime(2026, 1, 1, 0, 0, 0, DateTimeKind.Utc);
        var samples = Series(10, start, seconds: 10);
        var averaged = HistoryReducer.Average(samples);
        // First at 0 s, last at 90 s.
        Assert.Equal(start.AddSeconds(45), averaged.Timestamp);
    }

    [Fact]
    public void AZeroSpanSeriesCollapsesToOneSample()
    {
        var at = DateTime.UtcNow;
        var id = Guid.NewGuid();
        var samples = Enumerable.Range(0, 10)
            .Select(_ => new MetricSample(id, new MetricSnapshot { CpuPercent = 5 }, at))
            .ToList();
        Assert.Single(HistoryReducer.Reduce(samples, 5));
    }
}
