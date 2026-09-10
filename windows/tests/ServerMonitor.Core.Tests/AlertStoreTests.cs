using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The two tables the rule engine reads and writes.
/// </summary>
/// <remarks>
/// The engine is tested against delegates, which is what keeps its state
/// machine testable without SQLite — so these are the other half: that what it
/// hands the store comes back the same, and that the restore-on-launch query
/// really finds what a crash would have left behind.
/// </remarks>
public class AlertStoreTests
{
    private static AlertRule Rule(
        string name = "cpu high",
        AlertMetric metric = AlertMetric.Cpu,
        Guid? serverId = null) => new()
    {
        Name = name,
        Metric = metric,
        Comparator = ">",
        Threshold = 90,
        DurationSeconds = 300,
        ServerId = serverId,
    };

    private static AlertEvent Event(AlertRule rule, Guid serverId, DateTime? started = null) => new()
    {
        RuleId = rule.Id,
        RuleName = rule.Name,
        Metric = rule.Metric,
        ServerId = serverId,
        ServerName = "web-1",
        Value = 97.5,
        Comparator = rule.Comparator,
        Threshold = rule.Threshold,
        DurationSeconds = rule.DurationSeconds,
        Message = "web-1 CPU 97.5% > 90%",
        StartedAt = started ?? DateTime.UtcNow,
    };

    [Fact]
    public void ARuleRoundTrips()
    {
        using var database = Database.InMemory();
        var rule = Rule(metric: AlertMetric.Latency);
        rule.Threshold = 250;
        rule.Comparator = "<";
        rule.DurationSeconds = 45;
        rule.Enabled = false;
        database.Save(rule);

        var back = Assert.Single(database.AllAlertRules());
        Assert.Equal(rule.Id, back.Id);
        Assert.Equal("cpu high", back.Name);
        Assert.Equal(AlertMetric.Latency, back.Metric);
        Assert.Equal("<", back.Comparator);
        Assert.Equal(250, back.Threshold);
        Assert.Equal(45, back.DurationSeconds);
        Assert.False(back.Enabled);
        Assert.Null(back.ServerId);
    }

    [Fact]
    public void SavingTheSameRuleTwiceUpdatesItRatherThanDuplicating()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        rule.Threshold = 70;
        database.Save(rule);

        var back = Assert.Single(database.AllAlertRules());
        Assert.Equal(70, back.Threshold);
    }

    [Fact]
    public void AScopedRuleRemembersItsHost()
    {
        using var database = Database.InMemory();
        var server = new Server { Name = "web-1", Host = "10.0.0.1", Username = "root" };
        database.Save(server);
        database.Save(Rule(serverId: server.Id));

        Assert.Equal(server.Id, Assert.Single(database.AllAlertRules()).ServerId);
    }

    [Fact]
    public void DeletingAHostTakesItsScopedRulesWithIt()
    {
        // ON DELETE CASCADE. A rule pointing at a server that no longer exists
        // would show "rule deleted" as its scope and could never fire.
        using var database = Database.InMemory();
        var server = new Server { Name = "web-1", Host = "10.0.0.1", Username = "root" };
        database.Save(server);
        database.Save(Rule(serverId: server.Id));
        database.Save(Rule(name: "global"));

        database.DeleteServer(server.Id);

        var remaining = Assert.Single(database.AllAlertRules());
        Assert.Equal("global", remaining.Name);
    }

    [Fact]
    public void AnEventRoundTripsWithEveryFieldTheHistoryShows()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        var serverId = Guid.NewGuid();
        database.OpenAlertEvent(Event(rule, serverId));

        var back = Assert.Single(database.AlertEvents());
        Assert.Equal(rule.Id, back.RuleId);
        Assert.Equal("cpu high", back.RuleName);
        Assert.Equal(AlertMetric.Cpu, back.Metric);
        Assert.Equal(serverId, back.ServerId);
        Assert.Equal("web-1", back.ServerName);
        Assert.Equal(97.5, back.Value);
        Assert.Equal(">", back.Comparator);
        Assert.Equal(90, back.Threshold);
        Assert.Equal(300, back.DurationSeconds);
        Assert.Equal("web-1 CPU 97.5% > 90%", back.Message);
        Assert.Null(back.ResolvedAt);
    }

    [Fact]
    public void ResolvingClosesTheOpenEventForThatRuleAndHost()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        var serverId = Guid.NewGuid();
        database.OpenAlertEvent(Event(rule, serverId));

        var close = Event(rule, serverId);
        close.Value = 41;
        close.ResolvedAt = DateTime.UtcNow;
        database.ResolveAlertEvent(close);

        var back = Assert.Single(database.AlertEvents());
        Assert.NotNull(back.ResolvedAt);
        // The closing reading replaces the opening one, so the row says what
        // it came back to rather than what it went out at.
        Assert.Equal(41, back.Value);
        Assert.Empty(database.UnresolvedAlertEvents());
    }

    [Fact]
    public void ResolvingOneHostLeavesTheOtherHostsEventOpen()
    {
        // One rule, two breaching hosts: the engine keys its state on the pair,
        // and the UPDATE has to match on the pair too or recovering anywhere
        // would close the alert everywhere.
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        var first = Guid.NewGuid();
        var second = Guid.NewGuid();
        database.OpenAlertEvent(Event(rule, first));
        database.OpenAlertEvent(Event(rule, second));

        var close = Event(rule, first);
        close.ResolvedAt = DateTime.UtcNow;
        database.ResolveAlertEvent(close);

        var open = Assert.Single(database.UnresolvedAlertEvents());
        Assert.Equal(second, open.ServerId);
    }

    [Fact]
    public void UnresolvedEventsAreWhatTheEngineRestoresFrom()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        var serverId = Guid.NewGuid();
        database.OpenAlertEvent(Event(rule, serverId));

        var restored = Assert.Single(database.UnresolvedAlertEvents());
        Assert.Equal(rule.Id, restored.RuleId);
        Assert.Equal(serverId, restored.ServerId);
    }

    [Fact]
    public void HistoryIsNewestFirst()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        var serverId = Guid.NewGuid();
        var older = Event(rule, serverId, DateTime.UtcNow.AddHours(-2));
        older.Message = "older";
        var newer = Event(rule, serverId, DateTime.UtcNow);
        newer.Message = "newer";
        database.OpenAlertEvent(older);
        database.OpenAlertEvent(newer);

        Assert.Equal("newer", database.AlertEvents()[0].Message);
    }

    [Fact]
    public void DeletingARuleKeepsItsHistory()
    {
        // ON DELETE SET NULL, not CASCADE. What happened, happened, and the
        // event carries its own copy of the rule's name and numbers so the row
        // still reads correctly afterwards.
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        database.OpenAlertEvent(Event(rule, Guid.NewGuid()));

        database.DeleteAlertRule(rule.Id);

        var back = Assert.Single(database.AlertEvents());
        Assert.Equal("cpu high", back.RuleName);
        Assert.Equal(90, back.Threshold);
        Assert.Equal("web-1 CPU 97.5% > 90%", back.Message);
    }

    [Fact]
    public void ClearingTheHistoryKeepsTheRules()
    {
        using var database = Database.InMemory();
        var rule = Rule();
        database.Save(rule);
        database.OpenAlertEvent(Event(rule, Guid.NewGuid()));

        database.ClearAlertHistory();

        Assert.Empty(database.AlertEvents());
        Assert.Single(database.AllAlertRules());
    }

    [Fact]
    public void AFreshStoreSaysItAdvancedTheSchemaAndAReopenedOneDoesNot()
    {
        // This flag is what makes the threshold seed run exactly once. If a
        // reopen also reported true, every launch would re-seed and a user who
        // deleted the default rules would find them back.
        var path = Path.Combine(Path.GetTempPath(), $"sm-alerts-{Guid.NewGuid():N}.sqlite");
        try
        {
            using (var first = Database.Open(path)) Assert.True(first.SchemaAdvanced);
            using (var second = Database.Open(path)) Assert.False(second.SchemaAdvanced);
        }
        finally
        {
            // Same cleanup as DatabaseTests: a file-backed store pools its
            // connections, so the deletes only land once both are disposed.
            foreach (var suffix in new[] { "", "-wal", "-shm" })
            {
                try { File.Delete(path + suffix); } catch (IOException) { /* WAL may linger */ }
            }
        }
    }
}
