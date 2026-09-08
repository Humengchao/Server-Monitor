using ServerMonitor.Core.Collect;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class VnstatTests
{
    private static TrafficReport Report(string json)
    {
        var outcome = VnstatParser.Parse(json);
        Assert.NotNull(outcome);
        Assert.Equal(VnstatOutcomeKind.Report, outcome.Kind);
        Assert.NotNull(outcome.Report);
        return outcome.Report;
    }

    [Fact]
    public void ParsesEverySeriesInBytes()
    {
        var report = Report(Fixture.Read("vnstat/v2.json"));
        Assert.Equal("2.10", report.VnstatVersion);
        var eth0 = report.Interfaces.First(i => i.Name == "eth0");
        Assert.Equal(918_000_000_000, eth0.TotalRx);
        Assert.Equal(2, eth0.Buckets(TrafficGranularity.FiveMinute).Count);
        Assert.Equal([52_000_000, 21_000_000], eth0.Buckets(TrafficGranularity.Hour).Select(b => b.Rx));
        Assert.Equal([400_000_000, 250_000_000], eth0.Buckets(TrafficGranularity.Day).Select(b => b.Tx));
        Assert.Equal(2, eth0.Buckets(TrafficGranularity.Month).Count);
        Assert.Equal(400_000_000_000, eth0.Buckets(TrafficGranularity.Year)[0].Total);
    }

    [Fact]
    public void SubDayBucketsCarryTheHostsClock()
    {
        // The axis must show the hour vnStat recorded, so the date is built in
        // UTC from the components as written and read back the same way —
        // "14:00" on the server is "14:00" on the chart, whatever this
        // machine's zone is.
        var eth0 = Report(Fixture.Read("vnstat/v2.json")).Interfaces[0];
        var slot = eth0.Buckets(TrafficGranularity.FiveMinute)[0];
        Assert.Equal(DateTimeKind.Utc, slot.Date.Kind);
        Assert.Equal(14, slot.Date.Hour);
        Assert.Equal(15, slot.Date.Minute);

        var hour = eth0.Buckets(TrafficGranularity.Hour)[^1];
        Assert.Equal(14, hour.Date.Hour);
        Assert.Equal(0, hour.Date.Minute);

        var month = eth0.Buckets(TrafficGranularity.Month)[0];
        Assert.Equal(7, month.Date.Month);
        Assert.Equal(1, month.Date.Day);      // a month bucket sits on the 1st
    }

    [Fact]
    public void BucketsComeOutOldestFirst()
    {
        var eth0 = Report(Fixture.Read("vnstat/v2.json")).Interfaces[0];
        var days = eth0.Buckets(TrafficGranularity.Day);
        Assert.True(days[0].Date < days[1].Date);
    }

    [Fact]
    public void VersionOneKilobytesAreScaledAndItsPluralKeysAreRead()
    {
        // vnStat 1.x reported KiB and pluralised the keys; its hourly rows
        // carry the hour as `id` with no `time` object.
        var report = Report(Fixture.Read("vnstat/v1.json"));
        Assert.Equal("1.18", report.VnstatVersion);
        var eth0 = report.Interfaces[0];
        Assert.Equal(896_484_375L * 1024, eth0.TotalRx);
        var hours = eth0.Buckets(TrafficGranularity.Hour);
        Assert.Equal(2, hours.Count);
        Assert.Equal(50_781L * 1024, hours[0].Rx);
        Assert.Equal(13, hours[0].Date.Hour);
        Assert.Equal(14, hours[1].Date.Hour);
    }

    [Fact]
    public void ThePrimaryInterfaceSkipsBridgesAndTunnels()
    {
        // docker0 has more bytes through it in the fixture, deliberately: the
        // interface people mean is the real link.
        var report = Report(Fixture.Read("vnstat/v2.json"));
        Assert.Equal("eth0", report.PrimaryInterface!.Name);
    }

    [Fact]
    public void AFreshInstallReadsAsCollectingRatherThanAnEmptyChart()
    {
        var report = Report(Fixture.Read("vnstat/collecting.json"));
        Assert.True(report.IsCollecting);
        Assert.False(report.Interfaces[0].HasData);
    }

    [Fact]
    public void AHostWithoutVnstatIsAnOutcomeNotAnError()
    {
        // Most hosts have no vnStat, and the card explains how to install it
        // instead of reporting a failure.
        var outcome = VnstatParser.Parse("SM_NO_VNSTAT\n");
        Assert.NotNull(outcome);
        Assert.Equal(VnstatOutcomeKind.NotInstalled, outcome.Kind);
    }

    [Theory]
    [InlineData("")]
    [InlineData("bash: vnstat: command not found")]
    [InlineData("{not json")]
    public void UnreadableOutputIsNull(string output) => Assert.Null(VnstatParser.Parse(output));

    [Fact]
    public void JsonAfterABannerStillParses()
    {
        // Hosts print login banners on every command.
        var json = "Authorized users only.\n" + Fixture.Read("vnstat/v2.json");
        Assert.Equal("2.10", Report(json).VnstatVersion);
    }
}

public class VnstatInstallerTests
{
    [Fact]
    public void TheProbeIsReadOnly()
    {
        // It only asks which package manager the host has and whether we are
        // root; nothing is installed until the user confirms the exact line.
        var probe = VnstatInstaller.ProbeCommand;
        Assert.Contains("command -v", probe, StringComparison.Ordinal);
        Assert.Contains("id -u", probe, StringComparison.Ordinal);
        Assert.DoesNotContain("install", probe, StringComparison.Ordinal);
    }

    [Fact]
    public void APlanIsReadFromTheProbeOutput()
    {
        var plan = VnstatInstaller.PlanFromProbe("apt-get\nSM_PROBE_END\n0\n");
        Assert.NotNull(plan);
        Assert.Equal(VnstatInstaller.PackageManager.Apt, plan.Manager);
        Assert.True(plan.AsRoot);
    }

    [Fact]
    public void ANonRootUserGetsSudoInTheDisplayedCommand()
    {
        var plan = VnstatInstaller.PlanFromProbe("dnf\nSM_PROBE_END\n1000\n");
        Assert.NotNull(plan);
        Assert.False(plan.AsRoot);
        Assert.StartsWith("sudo ", plan.DisplayCommand, StringComparison.Ordinal);
    }

    [Fact]
    public void EveryHalfOfACompoundLineGetsItsOwnSudo()
    {
        // `sudo a && b` runs b as the user, which then fails — the apt line is
        // two commands.
        var plan = new VnstatInstaller.Plan(VnstatInstaller.PackageManager.Apt, AsRoot: false);
        Assert.Equal(2, plan.DisplayCommand.Split("sudo ").Length - 1);
    }

    [Fact]
    public void AHostWithNoSupportedManagerHasNoPlan()
    {
        Assert.Null(VnstatInstaller.PlanFromProbe("SM_PROBE_END\n0\n"));
        Assert.Null(VnstatInstaller.PlanFromProbe("nothing useful"));
    }

    [Fact]
    public void TheRemoteScriptReportsThroughMarkersNotExitCodes()
    {
        // Everything folds into stdout and the exit code is always 0, so the
        // result is read from the markers rather than from a thrown error with
        // the output lost.
        var script = VnstatInstaller.RemoteScript(
            new VnstatInstaller.Plan(VnstatInstaller.PackageManager.Apk, AsRoot: true));
        Assert.Contains("SM_INSTALLED", script, StringComparison.Ordinal);
        Assert.Contains("SM_FAILED", script, StringComparison.Ordinal);
        // And it enables the service, or vnStat records nothing.
        Assert.Contains("rc-update add vnstatd", script, StringComparison.Ordinal);
        Assert.Contains("systemctl enable --now vnstat", script, StringComparison.Ordinal);
    }

    [Fact]
    public void TheRemoteScriptNeverWaitsOnAPasswordPrompt()
    {
        // `sudo -n` fails at once rather than blocking on a prompt nothing will
        // answer.
        var script = VnstatInstaller.RemoteScript(
            new VnstatInstaller.Plan(VnstatInstaller.PackageManager.Apt, AsRoot: false));
        Assert.Contains("sudo -n env", script, StringComparison.Ordinal);
        Assert.DoesNotContain("sudo apt-get", script, StringComparison.Ordinal);
    }

    [Fact]
    public void YumGetsSudoOnBothOfItsCommands()
    {
        // The yum line is "yum install epel-release; yum install vnstat",
        // separated by a semicolon rather than &&.
        var script = VnstatInstaller.RemoteScript(
            new VnstatInstaller.Plan(VnstatInstaller.PackageManager.Yum, AsRoot: false));
        Assert.Equal(2, script.Split("sudo -n env yum").Length - 1);
    }

    [Fact]
    public void TheOutcomeIsReadFromTheMarkers()
    {
        Assert.Equal(
            VnstatInstaller.OutcomeKind.Installed,
            VnstatInstaller.OutcomeFrom("some noise\nSM_INSTALLED\n").Kind);
        Assert.Equal(
            VnstatInstaller.OutcomeKind.NeedsSudoPassword,
            VnstatInstaller.OutcomeFrom("sudo: a password is required").Kind);
        var failed = VnstatInstaller.OutcomeFrom("E: Unable to locate package vnstat\nSM_FAILED rc=100");
        Assert.Equal(VnstatInstaller.OutcomeKind.Failed, failed.Kind);
        Assert.Contains("Unable to locate", failed.Detail, StringComparison.Ordinal);
    }

    [Fact]
    public void AnEmptyOutcomeSaysSoRatherThanShowingNothing()
    {
        Assert.Equal("no output", VnstatInstaller.OutcomeFrom("").Detail);
    }
}
