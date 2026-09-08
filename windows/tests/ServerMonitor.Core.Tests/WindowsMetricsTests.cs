using System.IO.Compression;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class WindowsMetricsTests
{
    private static string Sample => Fixture.Read("windows/metrics.txt");

    [Fact]
    public void ParsesKeyValueOutput()
    {
        var snapshot = WindowsMetrics.Parse(Sample);
        Assert.NotNull(snapshot);
        Assert.Equal(37, snapshot.CpuPercent);
        Assert.Equal(8, snapshot.Cores);
        Assert.Equal(123_456, snapshot.UptimeSeconds);
        Assert.Equal("24.0.7", snapshot.DockerVersion);
    }

    [Fact]
    public void MemoryIsConvertedFromKilobytes()
    {
        // TotalVisibleMemorySize and FreePhysicalMemory are kB, unlike the
        // byte-valued disk fields next to them.
        var snapshot = WindowsMetrics.Parse(Sample)!;
        Assert.Equal(17_179_869_184, snapshot.MemoryTotal);   // 16_777_216 kB = 16 GiB
        Assert.Equal(8_589_934_592, snapshot.MemoryUsed);
    }

    [Fact]
    public void DiskIsAlreadyInBytes()
    {
        var snapshot = WindowsMetrics.Parse(Sample)!;
        Assert.Equal(536_870_912_000, snapshot.DiskTotal);
        Assert.Equal(400_000_000_000, snapshot.DiskUsed);
    }

    [Fact]
    public void ProcessorQueueStandsInForLoad()
    {
        // Windows has no load average; the queue length is the closest thing
        // and is what the web backend showed too.
        Assert.Equal(2, WindowsMetrics.Parse(Sample)!.Load1);
    }

    [Fact]
    public void NegativeDerivedValuesAreClamped()
    {
        // A host reporting more free than total must not yield negative usage.
        var snapshot = WindowsMetrics.Parse("memtotal=100\nmemfree=500\ndisktotal=10\ndiskfree=99\ncores=1");
        Assert.NotNull(snapshot);
        Assert.Equal(0, snapshot.MemoryUsed);
        Assert.Equal(0, snapshot.DiskUsed);
    }

    [Theory]
    [InlineData("cpu  100 0 100 1000 0 0 0 0")]
    [InlineData("")]
    public void LinuxOutputParsesToNothing(string output)
    {
        // This is what makes auto-detection work: /proc output has no
        // key=value lines, so Windows parsing declines it.
        Assert.Null(WindowsMetrics.Parse(output));
    }

    [Fact]
    public void CountersAreExtractedForRates()
    {
        var counters = WindowsMetrics.Counters(Sample);
        Assert.Equal(1_000_000, counters.Net.Rx);
        Assert.Equal(200_000, counters.Net.Tx);
        Assert.Equal(4096, counters.Disk.Read);
        Assert.Equal(8192, counters.Disk.Written);
    }

    [Fact]
    public void TheDetailListsAreParsed()
    {
        var snapshot = WindowsMetrics.Parse(Sample)!;
        Assert.Equal([0, 1, 2, 3], snapshot.CoreLoads.Select(c => c.Index));
        Assert.Equal(41.2, snapshot.CoreLoads[0].Percent, 2);
        // Biggest first.
        Assert.Equal(["D:", "C:"], snapshot.Filesystems.Select(f => f.Mount));
        Assert.Equal(4812, snapshot.Processes[0].Pid);
        Assert.Equal("sqlservr", snapshot.Processes[0].Command);
    }

    [Fact]
    public void TheIdentityLineIsPositionalWithinItsPipes()
    {
        var identity = WindowsMetrics.Parse(Sample)!.Identity;
        Assert.Equal("WIN-SRV-01", identity.Hostname);
        Assert.Equal("10.0.20348", identity.Kernel);
        Assert.Equal("x64-based PC", identity.Architecture);
        Assert.Equal("Microsoft Windows Server 2022 Datacenter", identity.OsName);
        Assert.Equal("Intel(R) Xeon(R) Gold 6248R CPU @ 3.00GHz", identity.CpuModel);
        Assert.Equal(["10.0.0.20", "192.168.56.1"], identity.Addresses);
    }

    [Fact]
    public void WindowsReportsNoCpuBreakdownSoTheRowIsLeftOut()
    {
        // There is no user/nice/iowait/steal split to report, and five zeroes
        // is worse than nothing.
        Assert.False(WindowsMetrics.Parse(Sample)!.CpuBreakdown.IsReported);
    }

    [Fact]
    public void EncodesAsUtf16LeBase64()
    {
        // -EncodedCommand demands UTF-16LE; getting this wrong yields a shell
        // error rather than a wrong number, and only on a real Windows host.
        var encoded = WindowsMetrics.Encode("AB");
        Assert.Equal(Convert.ToBase64String([0x41, 0x00, 0x42, 0x00]), encoded);
    }

    [Fact]
    public void CommandIsNonInteractiveAndProfileFree()
    {
        var command = WindowsMetrics.Command;
        Assert.Contains("-NoProfile", command, StringComparison.Ordinal);
        Assert.Contains("-NonInteractive", command, StringComparison.Ordinal);
        Assert.Contains("-EncodedCommand", command, StringComparison.Ordinal);
    }

    [Fact]
    public void TheCommandFitsInsideCmdExesLimit()
    {
        // The reason the script is deflated at all: cmd.exe caps its command
        // line near 8191 characters, and the uncompressed script crossed that
        // as soon as the machine-screen detail was added — the host answered
        // "命令行太长" instead of running anything.
        Assert.True(
            WindowsMetrics.Command.Length < 8000,
            $"command is {WindowsMetrics.Command.Length} characters");
    }

    [Fact]
    public void TheDeflatedPayloadIsWhatPowerShellWillRead()
    {
        // Raw DEFLATE (RFC 1951) on both sides — no gzip or zlib header — or
        // the stub's DeflateStream throws on the host and the failure is a
        // PowerShell stack trace in the metrics output.
        const string Script = "Write-Output 'hello'";
        var deflated = WindowsMetrics.Deflate(Script);
        using var input = new MemoryStream(deflated);
        using var inflate = new DeflateStream(input, CompressionMode.Decompress);
        using var reader = new StreamReader(inflate);
        Assert.Equal(Script, reader.ReadToEnd());
    }

    [Fact]
    public void TheEmbeddedScriptCarriesTheProgressFix()
    {
        // F11: the Go copy was missing this, and on Server 2016 PowerShell
        // serialised its progress stream into stdout as CLIXML — "preparing
        // modules for first use" records arrived mixed in with the metrics.
        Assert.Contains("$ProgressPreference='SilentlyContinue'", Probes.WindowsScript, StringComparison.Ordinal);
        // And the two-sample CPU read, rather than a single LoadPercentage.
        Assert.Contains("Win32_PerfRawData_PerfOS_Processor", Probes.WindowsScript, StringComparison.Ordinal);
        Assert.Contains("Start-Sleep -Milliseconds 500", Probes.WindowsScript, StringComparison.Ordinal);
        // And the two lines the Go copy lacked entirely.
        Assert.Contains("(\"ident=\"", Probes.WindowsScript, StringComparison.Ordinal);
        Assert.Contains("(\"ips=\"", Probes.WindowsScript, StringComparison.Ordinal);
    }
}

public class OsProbeTests
{
    [Fact]
    public void LinuxIsRecognisedFromUname()
    {
        Assert.Equal(Model.OSKind.Linux, MetricsCollector.OsKindFromUname("Linux\n"));
        Assert.Equal(Model.OSKind.Linux, MetricsCollector.OsKindFromUname(" linux "));
    }

    [Theory]
    [InlineData("")]
    [InlineData("'uname' is not recognized as an internal or external command")]
    public void AnythingElseIsTreatedAsWindows(string output) =>
        Assert.Equal(Model.OSKind.Windows, MetricsCollector.OsKindFromUname(output));

    [Fact]
    public void ARemoteShellFailingIsTheWindowsSignal()
    {
        // A Windows host answers by failing: it has no `uname`. Any exit status
        // other than ssh's own 255 means a shell answered.
        var failure = SshException.CommandFailed(1, "not recognized");
        Assert.Equal(Model.OSKind.Windows, MetricsCollector.OsKindFromProbeFailure(failure));
    }

    [Fact]
    public void SshsOwnFailuresSayNothingAboutTheHost()
    {
        // 255, a timeout or a launch failure all mean no shell was ever
        // reached. Guessing Windows there sent an unreachable host through a
        // second 10 s connect timeout and reported the failure in Windows
        // terms.
        Assert.Null(MetricsCollector.OsKindFromProbeFailure(SshException.CommandFailed(255, "")));
        Assert.Null(MetricsCollector.OsKindFromProbeFailure(SshException.TimedOut(30)));
        Assert.Null(MetricsCollector.OsKindFromProbeFailure(SshException.LaunchFailed("no ssh.exe")));
    }

    [Fact]
    public void TheProbeIsOneCheapCommand()
    {
        // The Linux batch contains a `sleep` and several `cat`s; against a
        // Windows shell that does not fail fast it hung until the timeout.
        Assert.Equal("uname -s", Probes.OsDetect);
    }
}

public class DockerSummaryTests
{
    [Fact]
    public void ParsesInfoLine()
    {
        var summary = DockerClient.ParseSummary("29.1.3|14|5|3|1\n");
        Assert.Equal("29.1.3", summary.EngineVersion);
        Assert.Equal(14, summary.Images);
        Assert.Equal(5, summary.Running);
        Assert.Equal(3, summary.Stopped);
        Assert.Equal(1, summary.Paused);
        Assert.Equal(9, summary.Total);
    }

    [Fact]
    public void IgnoresBannerLinesBeforeTheData()
    {
        // Hosts print login banners on every command; the data is the last
        // line that actually looks like data.
        var summary = DockerClient.ParseSummary("""
            SSH warning: Authorized users only.
            29.1.3|14|5|3
            """);
        Assert.Equal("29.1.3", summary.EngineVersion);
    }

    [Fact]
    public void TheOlderFourFieldFormatStillParses()
    {
        // A host polled before the paused count was added.
        var summary = DockerClient.ParseSummary("29.1.3|14|5|3");
        Assert.Equal(0, summary.Paused);
        Assert.Equal(8, summary.Total);
    }

    [Fact]
    public void MissingDockerYieldsZeroes()
    {
        var summary = DockerClient.ParseSummary("bash: docker: command not found");
        Assert.Equal("", summary.EngineVersion);
        Assert.Equal(0, summary.Total);
    }

    [Fact]
    public void PartialFieldsDoNotThrow() =>
        Assert.Equal("", DockerClient.ParseSummary("29.1.3|14").EngineVersion);

    [Fact]
    public void StatsJoinOnTheShortId()
    {
        var stats = DockerClient.ParseStats(Fixture.Read("docker/stats.txt"));
        Assert.Equal(2, stats.Count);
        var running = stats["ab12cd34ef56"];
        Assert.Equal(12.34, running.CpuPercent, 2);
        Assert.Equal(4.21, running.MemoryPercent, 2);
        Assert.Equal("340MiB / 7.77GiB", running.MemoryUsage);
        Assert.Equal("18.5GB", running.NetRx);
        Assert.Equal("53.3GB", running.NetTx);
    }

    [Fact]
    public void AContainerTheEngineCouldNotSampleReadsZeroNotAParseFailure()
    {
        // The engine prints "--" for those.
        var stats = DockerClient.ParseStats(Fixture.Read("docker/stats.txt"));
        Assert.Equal(0, stats["00ff11ee22dd"].CpuPercent);
    }

    [Fact]
    public void ComposeProjectsSkipTheEnginesWarningLine()
    {
        // The deprecation warning contains a bracket of its own
        // ("WARN[0000] …"), so the first '[' in the output is not where the
        // JSON starts.
        var projects = DockerClient.ParseComposeProjects(Fixture.Read("docker/compose-ls.json"));
        Assert.Equal(2, projects.Count);
        Assert.Equal(["legacy", "server-monitor"], projects.Select(p => p.Name));
    }

    [Fact]
    public void ComposeStatusCountsAreBrokenOut()
    {
        var projects = DockerClient.ParseComposeProjects(Fixture.Read("docker/compose-ls.json"));
        var legacy = projects.First(p => p.Name == "legacy");
        Assert.Equal([("exited", 2), ("running", 1)], legacy.Counts);
        Assert.Equal(1, legacy.RunningCount);
        Assert.True(legacy.IsRunning);
        Assert.Equal("/srv/legacy", legacy.Directory);
    }

    [Fact]
    public void AHostWithoutComposeGetsAnEmptyTabNotAnError()
    {
        // Compose v1 was a separate docker-compose binary with no `ls` at all.
        Assert.Empty(DockerClient.ParseComposeProjects("docker: 'compose' is not a docker command."));
    }

    [Fact]
    public void ContainerIdsAreTruncatedForTheStatsJoin()
    {
        // `docker ps --no-trunc` gives 64 characters; `docker stats` prints 12.
        var container = new DockerContainer(new string('a', 64), "n", "i", "running", "Up");
        Assert.Equal(new string('a', 12), container.ShortId);
    }

    [Fact]
    public void ADanglingImageShowsItsIdRatherThanNoneNone()
    {
        var image = new DockerImage("0011223344ff", "<none>", "<none>", "412MB", "5 days ago");
        Assert.True(image.IsDangling);
        Assert.Equal("0011223344ff", image.DisplayName);
        Assert.Equal("nginx:1.27-alpine",
            new DockerImage("9a1b", "nginx", "1.27-alpine", "78.6MB", "3 weeks ago").DisplayName);
    }

    [Fact]
    public void ALongFormImageIdLosesItsSha256PrefixNotItsDigits()
    {
        // `--no-trunc` gives "sha256:<64 hex>"; truncating that as-is yields
        // "sha256:00112", which identifies nothing.
        var image = new DockerImage(
            "sha256:0011223344ff5566778899aabbccddeeff", "<none>", "<none>", "412MB", "now");
        Assert.Equal("0011223344ff", image.DisplayName);
    }

    [Fact]
    public void TheExecShellFallsBackFromBashToSh()
    {
        // An alpine image has no bash, and "OCI runtime exec failed" is a
        // confusing way to learn that.
        var command = DockerClient.ExecShellCommand("abc123");
        Assert.Contains("exec bash", command, StringComparison.Ordinal);
        Assert.Contains("exec sh", command, StringComparison.Ordinal);
        Assert.Contains("'abc123'", command, StringComparison.Ordinal);
    }
}
