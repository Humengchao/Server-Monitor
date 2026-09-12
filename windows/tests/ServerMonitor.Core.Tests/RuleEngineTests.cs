using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The rule engine's state machine and its input validation.
/// </summary>
/// <remarks>
/// The engine is pure — rules, webhook lookup, event records and delivery are
/// all injected — so the whole thing is testable without a store or a
/// notification centre. What is worth pinning is the behaviour the web client
/// already pins: a rule stays quiet until its whole duration has passed, then
/// opens exactly once, then resolves exactly once. The web's AlertEngineTests
/// are the source of these expectations.
/// </remarks>
public class RuleEngineTests
{
    private readonly List<(Guid Rule, Guid Server)> _fired = [];
    private readonly List<AlertEvent> _events = [];
    private readonly List<AlertRule> _rules = [];
    private readonly List<string> _delivered = [];

    private readonly Server _server = new()
    {
        Id = Guid.NewGuid(),
        Name = "web-01",
        Host = "203.0.113.10",
        Username = "root",
    };

    private RuleEngine Engine() => new(
        () => _rules,
        _ => null,
        open =>
        {
            _events.Add(open);
            _fired.Add((open.RuleId, open.ServerId));
        },
        close =>
        {
            _events.Add(close);
            _fired.Remove((close.RuleId, close.ServerId));
        },
        (serverId, title, body) => _delivered.Add($"{title}: {body}"),
        _ => { });

    private static MetricSnapshot Online(double cpu) => new() { CpuPercent = cpu };

    private static MetricSnapshot Offline() => new() { CpuPercent = double.NaN };

    private static ServerStatus Up() => ServerStatus.Online(DateTime.UtcNow);

    private static ServerStatus Down() => ServerStatus.Offline("ssh: connection refused");

    private static readonly DateTime T0 = new(2026, 9, 9, 12, 0, 0, DateTimeKind.Utc);

    private AlertRule Rule(
        string name = "cpu high", AlertMetric metric = AlertMetric.Cpu,
        string comparator = ">", double threshold = 90, int duration = 60) =>
        new()
        {
            Name = name,
            Metric = metric,
            Comparator = comparator,
            Threshold = threshold,
            DurationSeconds = duration,
        };

    [Fact]
    public void AWaitIsRequiredBeforeAnythingFires()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(97), T0);
        Assert.Empty(_fired);

        engine.Evaluate(_server, Up(), Online(98), T0.AddSeconds(30));
        Assert.Empty(_fired);
    }

    [Fact]
    public void TheWholeDurationMustPass()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(59));
        Assert.Empty(_fired);

        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(61));
        Assert.Single(_fired);
    }

    [Fact]
    public void FiringDeliversExactlyOnce()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(60));
        engine.Evaluate(_server, Up(), Online(98), T0.AddSeconds(90));
        engine.Evaluate(_server, Up(), Online(99), T0.AddSeconds(120));

        Assert.Single(_fired);
        Assert.Single(_delivered);
    }

    [Fact]
    public void AValueBelowTheThresholdResetsTheClock()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(50), T0.AddSeconds(40));
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(60));
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(90));

        // 90 - 60 = 30 s into a fresh run, not the tail of the old one.
        Assert.Empty(_fired);
    }

    [Fact]
    public void ComingBackBelowResolves()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(60));
        Assert.Single(_fired);

        engine.Evaluate(_server, Up(), Online(50), T0.AddSeconds(70));
        Assert.Empty(_fired);
        Assert.Equal(2, _events.Count);
        Assert.NotNull(_events[^1].ResolvedAt);
    }

    [Theory]
    [InlineData(AppLanguage.Zh, "web-01 离线")]
    [InlineData(AppLanguage.En, "web-01 is offline")]
    public void AnOfflineRuleFiresWhenTheHostGoesDown(AppLanguage language, string expectedMessage)
    {
        using var languageScope = new LanguageScope(language);
        _rules.Add(Rule(metric: AlertMetric.Offline, comparator: ">", threshold: 0, duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Up(), Online(50), T0);
        engine.Evaluate(_server, Down(), null, T0.AddSeconds(30));
        Assert.Empty(_fired);

        engine.Evaluate(_server, Down(), null, T0.AddSeconds(90));
        Assert.Single(_fired);
        Assert.Contains(expectedMessage, Assert.Single(_delivered), StringComparison.Ordinal);
    }

    [Fact]
    public void AThresholdRuleHasNothingToSayWhileTheHostIsDown()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Evaluate(_server, Down(), null, T0);
        engine.Evaluate(_server, Down(), null, T0.AddSeconds(300));
        Assert.Empty(_fired);
    }

    [Fact]
    public void AServerScopedRuleIgnoresOtherHosts()
    {
        var other = new Server { Id = Guid.NewGuid(), Name = "db-01", Host = "10.0.0.5" };
        _rules.Add(Rule(duration: 60).AlsoScopedTo(_server.Id));
        var engine = Engine();

        engine.Evaluate(other, Up(), Online(97), T0);
        engine.Evaluate(other, Up(), Online(97), T0.AddSeconds(300));
        Assert.Empty(_fired);

        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(60));
        Assert.Single(_fired);
        Assert.Equal(_server.Name, _events[0].ServerName);
    }

    [Fact]
    public void RestoreReopensWithoutReannouncing()
    {
        _rules.Add(Rule(duration: 60));
        var engine = Engine();

        engine.Restore(
        [
            new AlertEvent
            {
                RuleId = _rules[0].Id,
                ServerId = _server.Id,
                StartedAt = T0.AddMinutes(-10),
            },
        ]);

        Assert.Single(engine.Firing);
        Assert.Empty(_delivered);
    }

    [Fact]
    public void TheStoredMessageNeverChangesWhenTheRuleIsDeleted()
    {
        _rules.Add(Rule());
        var engine = Engine();
        engine.Evaluate(_server, Up(), Online(97), T0);
        engine.Evaluate(_server, Up(), Online(97), T0.AddSeconds(60));
        Assert.Contains("CPU 97", _events[0].Message);
    }
}

internal static class TestExtensions
{
    public static AlertRule AlsoScopedTo(this AlertRule rule, Guid serverId)
    {
        rule.ServerId = serverId;
        return rule;
    }
}
