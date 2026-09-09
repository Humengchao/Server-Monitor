using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// What the web's normalizeAlertRule accepts, refuses and quietly fixes.
/// </summary>
/// <remarks>
/// Ported alongside the function, because the interesting half of it is the
/// quiet fixing: a rule the user typed slightly wrong should come out usable
/// rather than rejected, and a rule that cannot mean anything should be
/// refused rather than stored to never fire.
/// </remarks>
public class RuleValidationTests
{
    private static AlertRule Rule(
        string name = "cpu", AlertMetric metric = AlertMetric.Cpu,
        string comparator = ">", double threshold = 90, int duration = 300) =>
        new()
        {
            Name = name,
            Metric = metric,
            Comparator = comparator,
            Threshold = threshold,
            DurationSeconds = duration,
        };

    [Fact]
    public void ANamelessRuleIsRefused()
    {
        // Nothing else identifies a rule in the list or in the notification.
        Assert.Throws<ArgumentException>(() => RuleValidation.Normalize(Rule(name: "   ")));
    }

    [Fact]
    public void TheNameIsTrimmed()
    {
        var rule = Rule(name: "  disk full  ");
        RuleValidation.Normalize(rule);
        Assert.Equal("disk full", rule.Name);
    }

    [Fact]
    public void AnOverlongNameIsRefused()
    {
        Assert.Throws<ArgumentException>(
            () => RuleValidation.Normalize(Rule(name: new string('x', 129))));
    }

    [Theory]
    [InlineData(">")]
    [InlineData("<")]
    public void BothComparatorsSurvive(string comparator)
    {
        var rule = Rule(comparator: comparator);
        RuleValidation.Normalize(rule);
        Assert.Equal(comparator, rule.Comparator);
    }

    [Theory]
    [InlineData("")]
    [InlineData(">=")]
    [InlineData("nonsense")]
    public void AnythingElseBecomesGreaterThan(string comparator)
    {
        // The web defaults an empty comparator and rejects a wrong one. Here
        // the field is not free text in the UI, so anything unexpected can
        // only be a hand-edited row; the default is more useful than a refusal.
        var rule = Rule(comparator: comparator);
        RuleValidation.Normalize(rule);
        Assert.Equal(">", rule.Comparator);
    }

    [Theory]
    [InlineData(AlertMetric.Cpu)]
    [InlineData(AlertMetric.Memory)]
    [InlineData(AlertMetric.Disk)]
    public void APercentOutsideItsRangeIsRefused(AlertMetric metric)
    {
        // Not clamped: a rule at 150% would never fire and a rule at -1% would
        // always fire, and both look like they were configured on purpose.
        Assert.Throws<ArgumentException>(
            () => RuleValidation.Normalize(Rule(metric: metric, threshold: 101)));
        Assert.Throws<ArgumentException>(
            () => RuleValidation.Normalize(Rule(metric: metric, threshold: -1)));
    }

    [Fact]
    public void ALoadOrLatencyThresholdIsNotCappedAtAHundred()
    {
        // They are not percentages. A load of 200 on a big box, or a 400 ms
        // link, are both real things to watch for.
        var load = Rule(metric: AlertMetric.Load1, threshold: 200);
        RuleValidation.Normalize(load);
        Assert.Equal(200, load.Threshold);

        Assert.Throws<ArgumentException>(
            () => RuleValidation.Normalize(Rule(metric: AlertMetric.Load1, threshold: -1)));
    }

    [Fact]
    public void AnOfflineRuleIsAboutTimeAndNothingElse()
    {
        // There is no number to compare a host that is not answering against,
        // so the threshold is discarded and the comparator fixed. The duration
        // alone is the rule.
        var rule = Rule(metric: AlertMetric.Offline, comparator: "<", threshold: 42);
        RuleValidation.Normalize(rule);
        Assert.Equal(0, rule.Threshold);
        Assert.Equal(">", rule.Comparator);
        Assert.Equal(300, rule.DurationSeconds);
    }

    [Fact]
    public void AnUnsetDurationBecomesFiveMinutes()
    {
        var rule = Rule(duration: 0);
        RuleValidation.Normalize(rule);
        Assert.Equal(RuleValidation.DefaultDurationSeconds, rule.DurationSeconds);
    }

    [Theory]
    [InlineData(29)]
    [InlineData(24 * 60 * 60 + 1)]
    public void ADurationOutsideTheBoundsIsRefused(int seconds)
    {
        // Under 30 s is shorter than a couple of polls, so it would fire on a
        // spike; over a day is a rule that has stopped being an alert.
        Assert.Throws<ArgumentException>(
            () => RuleValidation.Normalize(Rule(duration: seconds)));
    }
}
