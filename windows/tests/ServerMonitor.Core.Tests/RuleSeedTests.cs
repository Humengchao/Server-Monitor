using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The migration from three fixed thresholds to rules.
/// </summary>
/// <remarks>
/// The seed is the part of the port that can silently take away alerts a user
/// is relying on: it runs once, on a database that already existed, and if it
/// gets the old settings wrong nobody finds out until the alert that should
/// have fired does not. So the cases here are the ones where "obviously
/// equivalent" is not obviously equivalent — a per-host override, a metric
/// switched off for one host, a limit of 0.
/// </remarks>
public class RuleSeedTests
{
    private static AppSettings Settings(int cpu = 0, int memory = 0, int disk = 0, bool offline = false)
    {
        var settings = new AppSettings
        {
            CpuThreshold = cpu,
            MemoryThreshold = memory,
            DiskThreshold = disk,
            NotifyOnOffline = offline,
        };
        return settings;
    }

    private static Server Host(string name, int? cpu = null, int? memory = null, int? disk = null) => new()
    {
        Name = name,
        Host = "10.0.0.1",
        Username = "root",
        AuthKind = AuthKind.Agent,
        CpuThreshold = cpu,
        MemoryThreshold = memory,
        DiskThreshold = disk,
    };

    [Fact]
    public void AGlobalLimitBecomesOneRuleForAllHosts()
    {
        var rules = RuleSeed.From(Settings(cpu: 85), [Host("web-1"), Host("web-2")]);

        var rule = Assert.Single(rules);
        Assert.Equal(AlertMetric.Cpu, rule.Metric);
        Assert.Equal(85, rule.Threshold);
        Assert.Equal(">", rule.Comparator);
        Assert.Null(rule.ServerId);
        Assert.True(rule.Enabled);
    }

    [Fact]
    public void AMetricSetToZeroSeedsNothing()
    {
        // 0 was "no alert for this metric". A rule at 0 would fire on every
        // poll of every host, which is the opposite.
        Assert.Empty(RuleSeed.From(Settings(cpu: 0, memory: 0, disk: 0), [Host("web-1")]));
    }

    [Fact]
    public void EveryConfiguredMetricGetsItsOwnRule()
    {
        var rules = RuleSeed.From(Settings(cpu: 80, memory: 85, disk: 90, offline: true), [Host("web-1")]);

        Assert.Equal(4, rules.Count);
        Assert.Contains(rules, r => r.Metric == AlertMetric.Cpu && r.Threshold == 80);
        Assert.Contains(rules, r => r.Metric == AlertMetric.Memory && r.Threshold == 85);
        Assert.Contains(rules, r => r.Metric == AlertMetric.Disk && r.Threshold == 90);
        Assert.Contains(rules, r => r.Metric == AlertMetric.Offline);
    }

    [Fact]
    public void AnOfflineRuleIsSeededOnlyWhenTheSettingWasOn()
    {
        Assert.Empty(RuleSeed.From(Settings(offline: false), [Host("web-1")]));
        Assert.Single(RuleSeed.From(Settings(offline: true), [Host("web-1")]));
    }

    [Fact]
    public void OneHostOverridingAMetricExpandsItIntoPerHostRules()
    {
        // The old service read `server.CpuThreshold ?? settings.CpuThreshold`,
        // so an override *replaced* the global limit for that host. A rule is
        // scoped to one host or to all — there is no "all except" — so the
        // only faithful spelling is one rule per host.
        var rules = RuleSeed.From(
            Settings(cpu: 90),
            [Host("web-1", cpu: 50), Host("web-2"), Host("web-3")]);

        Assert.Equal(3, rules.Count);
        Assert.All(rules, r => Assert.NotNull(r.ServerId));
        Assert.Single(rules, r => r.Threshold == 50);
        Assert.Equal(2, rules.Count(r => r.Threshold == 90));
    }

    [Fact]
    public void AHostThatOptedOutGetsNoRuleWhileTheOthersKeepTheGlobalLimit()
    {
        // 0 on a host meant "off for this metric here", which the expansion
        // has to preserve — otherwise the migration would start alerting on
        // exactly the host the user silenced.
        var rules = RuleSeed.From(Settings(cpu: 50), [Host("noisy", cpu: 0), Host("web-2")]);

        var rule = Assert.Single(rules);
        Assert.Equal(50, rule.Threshold);
        Assert.NotNull(rule.ServerId);
    }

    [Fact]
    public void AnOverrideOnOneMetricLeavesTheOthersGlobal()
    {
        // The expansion is per metric, not per settings file: overriding CPU
        // on one host should not turn the disk rule into three rows.
        var rules = RuleSeed.From(
            Settings(cpu: 90, disk: 95),
            [Host("web-1", cpu: 50), Host("web-2")]);

        Assert.Equal(2, rules.Count(r => r.Metric == AlertMetric.Cpu));
        var disk = Assert.Single(rules, r => r.Metric == AlertMetric.Disk);
        Assert.Null(disk.ServerId);
    }

    [Fact]
    public void EverySeededRuleSurvivesTheValidator()
    {
        // A seeded rule the engine refuses would be worse than no seed: the
        // user would see rows in the list that never fire.
        var rules = RuleSeed.From(
            Settings(cpu: 80, memory: 85, disk: 90, offline: true),
            [Host("web-1", cpu: 50), Host("web-2", disk: 0)]);

        Assert.NotEmpty(rules);
        foreach (var rule in rules) RuleValidation.Normalize(rule);
    }

    [Fact]
    public void TheSeededDurationIsTheShortestTheLanguageAllows()
    {
        // The old service fired after three polls (~15 s) and announced an
        // offline host at once. The engine's own default is 300 s, and using
        // it here would quietly make every existing alert five minutes later.
        var rules = RuleSeed.From(Settings(cpu: 80, offline: true), [Host("web-1")]);

        Assert.All(rules, r => Assert.Equal(RuleValidation.MinDurationSeconds, r.DurationSeconds));
        Assert.Equal(RuleValidation.MinDurationSeconds, RuleSeed.SeededDurationSeconds);
    }

    [Fact]
    public void ARuleNameSaysWhatItWatchesAndWhere()
    {
        using var _ = new LanguageScope(AppLanguage.En);
        var rules = RuleSeed.From(Settings(cpu: 90), [Host("web-1", cpu: 50), Host("web-2")]);

        Assert.Contains(rules, r => r.Name == "CPU above 50% · web-1");
        Assert.Contains(rules, r => r.Name == "CPU above 90% · web-2");
    }

    [Fact]
    public void AFreshInstallSeedsTheDefaultsRatherThanNothing()
    {
        // A new user has no servers and the shipped defaults: disk at 90 and
        // offline on. Seeding nothing would leave the rules page empty and the
        // app silent until they went looking.
        var rules = RuleSeed.From(new AppSettings(), []);

        Assert.Contains(rules, r => r.Metric == AlertMetric.Disk && r.Threshold == 90);
        Assert.Contains(rules, r => r.Metric == AlertMetric.Offline);
    }
}
