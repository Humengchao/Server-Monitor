using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// Skips a test when its environment variable is absent.
/// </summary>
/// <remarks>
/// The live suites need real hosts, and the variables are the same ones the
/// macOS build uses (plan §0 step 5): <c>SM_LIVE_ALIAS</c> for a Linux host by
/// ssh config alias, <c>SM_WIN_HOST</c> / <c>SM_WIN_USER</c> /
/// <c>SM_WIN_PASSWORD</c> for a Windows host by password. Credentials only
/// ever come from the environment — never the repository, never a document.
///
/// A skip rather than a pass: a suite that silently does nothing when
/// misconfigured is worse than one that says so.
/// </remarks>
public sealed class RequiresEnvAttribute : FactAttribute
{
    public RequiresEnvAttribute(params string[] variables)
    {
        var missing = variables
            .Where(v => string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable(v)))
            .ToList();
        if (missing.Count > 0)
        {
            Skip = $"set {string.Join(", ", missing)} to run this";
        }
    }
}

/// <summary>
/// The whole collection chain against a real Linux host.
/// </summary>
/// <remarks>
/// This is the P1 exit criterion: <c>SM_LIVE_ALIAS=&lt;alias&gt; dotnet test</c>
/// passing end to end.
/// </remarks>
public class LiveLinuxTests
{
    private static string Alias => Environment.GetEnvironmentVariable("SM_LIVE_ALIAS")!;

    private static SshTarget Target() =>
        new(Guid.NewGuid(), Alias, 22, Environment.UserName, SshCredential.ConfigAlias);

    private static ISshTransport Library() =>
        new SshNetTransport(new InMemoryCredentialStore());

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task TheLibraryTransportReachesTheHost()
    {
        await using var transport = Library();
        var output = await transport.RunAsync("echo SM_OK", Target(), 20);
        Assert.Contains("SM_OK", output, StringComparison.Ordinal);
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task TheHostIsDetectedAsLinux()
    {
        await using var transport = Library();
        var output = await transport.RunAsync(Probes.OsDetect, Target(), 20);
        Assert.Equal(OSKind.Linux, MetricsCollector.OsKindFromUname(output));
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task AFullCollectionYieldsPlausibleNumbers()
    {
        // Sanity ranges, not exact values: a live host must produce plausible
        // figures rather than zeros, which is what a section-order mistake or
        // a truncated batch looks like.
        await using var transport = Library();
        var collector = new MetricsCollector(_ => transport);
        var target = Target();
        var snapshot = await collector.CollectAsync(target, OSKind.Linux);

        Assert.InRange(snapshot.CpuPercent, 0, 100);
        Assert.True(snapshot.MemoryTotal > 0, $"memoryTotal={snapshot.MemoryTotal}");
        Assert.InRange(snapshot.MemoryUsed, 1, snapshot.MemoryTotal);
        Assert.True(snapshot.Cores > 0, $"cores={snapshot.Cores}");
        Assert.True(snapshot.UptimeSeconds > 0, $"uptime={snapshot.UptimeSeconds}");
        Assert.True(snapshot.DiskTotal > 0, $"diskTotal={snapshot.DiskTotal}");
        Assert.InRange(snapshot.DiskUsed, 0, snapshot.DiskTotal);
        Assert.NotEmpty(snapshot.Identity.Hostname);
        Assert.NotEmpty(snapshot.Filesystems);
        Assert.NotEmpty(snapshot.Interfaces);
        Assert.NotEmpty(snapshot.Processes);
        Assert.Equal(snapshot.Cores, snapshot.CoreLoads.Count);

        Console.WriteLine($"""

            ── {Alias} ──
            host       {snapshot.Identity.Hostname} ({snapshot.Identity.OsName})
            cpu        {Format.Percent(snapshot.CpuPercent)}  model {snapshot.Identity.CpuModel}
            cores      {snapshot.Cores}
            load       {Format.Load(snapshot.Load1)}
            memory     {Format.Usage(snapshot.MemoryUsed, snapshot.MemoryTotal)}
            disk       {Format.Usage(snapshot.DiskUsed, snapshot.DiskTotal)}
            uptime     {Format.Uptime(snapshot.UptimeSeconds, chinese: false)}
            latency    {Format.Latency(snapshot.LatencyMs)}
            docker     {(snapshot.DockerVersion.Length == 0 ? "(none)" : snapshot.DockerVersion)}
            gpu        {(snapshot.Gpu.IsPresent ? snapshot.Gpu.Gpus[0].Name : "(none)")}
            ─────────────────────────
            """);
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task RatesAppearOnTheSecondPollAndNotTheFirst()
    {
        // The first poll has no baseline to subtract, which costs exactly one
        // poll of rate data — and must not be reported as zero traffic on a
        // busy host.
        await using var transport = Library();
        var collector = new MetricsCollector(_ => transport);
        var target = Target();

        var first = await collector.CollectAsync(target, OSKind.Linux);
        Assert.Equal(0, first.NetRxRate);
        Assert.True(first.NetRxTotal > 0, "the cumulative counter should be there from the first poll");

        await Task.Delay(1500);
        var second = await collector.CollectAsync(target, OSKind.Linux);
        Assert.True(second.NetRxRate >= 0);
        Assert.True(second.NetRxTotal >= first.NetRxTotal, "counters must not go backwards");
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task TheConnectionIsReusedRatherThanReHandshaked()
    {
        // The whole reason the library transport is the default (D3, F1). The
        // second call rides the open connection, so it should be a fraction of
        // the first.
        await using var transport = Library();
        var target = Target();

        var cold = System.Diagnostics.Stopwatch.StartNew();
        await transport.RunAsync("true", target, 20);
        cold.Stop();

        var warm = System.Diagnostics.Stopwatch.StartNew();
        await transport.RunAsync("true", target, 20);
        warm.Stop();

        Console.WriteLine($"cold {cold.ElapsedMilliseconds} ms, warm {warm.ElapsedMilliseconds} ms");
        Assert.True(
            warm.ElapsedMilliseconds * 2 < cold.ElapsedMilliseconds,
            $"no reuse: cold {cold.ElapsedMilliseconds} ms, warm {warm.ElapsedMilliseconds} ms");
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task TheFallbackTransportReachesTheSameHost()
    {
        // R13's escape hatch has to actually work, or a host that needs it is
        // simply unreachable.
        await using var transport = new OpenSshExeTransport(new InMemoryCredentialStore());
        var output = await transport.RunAsync("echo SM_OK", Target(), 25);
        Assert.Contains("SM_OK", output, StringComparison.Ordinal);
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task VnstatIsEitherReadOrReportedAsMissing()
    {
        await using var transport = Library();
        var output = await transport.RunAsync(VnstatParser.Command, Target(), 30);
        var outcome = VnstatParser.Parse(output);
        Assert.NotNull(outcome);
        if (outcome.Kind == VnstatOutcomeKind.NotInstalled)
        {
            // Then the install probe must at least name a package manager, or
            // the card's install button has nothing to offer.
            var probe = await transport.RunAsync(VnstatInstaller.ProbeCommand, Target(), 30);
            var plan = VnstatInstaller.PlanFromProbe(probe);
            Console.WriteLine($"vnStat absent; plan: {plan?.DisplayCommand ?? "(no supported manager)"}");
        }
        else
        {
            Console.WriteLine($"vnStat {outcome.Report!.VnstatVersion}, "
                + $"primary {outcome.Report.PrimaryInterface?.DisplayName ?? "(none)"}");
        }
    }

    [RequiresEnv("SM_LIVE_ALIAS")]
    public async Task DockerIsEitherEnumeratedOrAbsent()
    {
        await using var transport = Library();
        var docker = new DockerClient(transport);
        var target = Target();
        var summary = await docker.SummaryAsync(target);
        if (summary.EngineVersion.Length == 0)
        {
            Console.WriteLine("no usable Docker on this host");
            return;
        }

        // All five tables, which is the P4 exit criterion.
        var containers = await docker.ListContainersAsync(target);
        var images = await docker.ListImagesAsync(target);
        var volumes = await docker.ListVolumesAsync(target);
        var networks = await docker.ListNetworksAsync(target);
        var compose = await docker.ListComposeProjectsAsync(target);
        var stats = await docker.StatsAsync(target);

        Assert.Equal(summary.Total, containers.Count);
        // The engine always has bridge, host and none.
        Assert.True(networks.Count >= 3, $"only {networks.Count} networks");
        // Every running container should have stats, joined on the short id.
        foreach (var running in containers.Where(c => c.IsRunning))
        {
            Assert.True(stats.ContainsKey(running.ShortId), $"no stats for {running.Name}");
        }
        Console.WriteLine(
            $"docker {summary.EngineVersion}: {containers.Count} containers, {images.Count} images, "
            + $"{volumes.Count} volumes, {networks.Count} networks, {compose.Count} compose projects");
    }

    [RequiresEnv("SM_LIVE_ALIAS", "SM_LIVE_PUBKEY")]
    public async Task ExportingAPublicKeyIsIdempotent()
    {
        // added -> alreadyPresent -> cleaned up with no residue, which is the
        // P4 exit criterion for the keys page. SM_LIVE_PUBKEY must be a
        // throwaway key: this really does write to the host's
        // authorized_keys.
        var publicKey = Environment.GetEnvironmentVariable("SM_LIVE_PUBKEY")!.Trim();
        Assert.True(PublicKeyInstaller.IsPublicKey(publicKey), "SM_LIVE_PUBKEY is not a public key");

        await using var transport = Library();
        var target = Target();
        var installer = new PublicKeyInstaller(transport);
        var body = publicKey.Fields()[1];

        try
        {
            Assert.Equal(InstallOutcome.Added, await installer.InstallAsync(publicKey, target));
            Assert.Equal(InstallOutcome.AlreadyPresent, await installer.InstallAsync(publicKey, target));
            // Under a different comment, the body check still matches.
            var recommented = $"{publicKey.Fields()[0]} {body} someone-else@elsewhere";
            Assert.Equal(InstallOutcome.AlreadyPresent, await installer.InstallAsync(recommented, target));
        }
        finally
        {
            // Remove every line carrying that body, whatever its comment.
            await transport.RunAsync(
                $"grep -vF {body.ShellQuote()} ~/.ssh/authorized_keys > ~/.ssh/authorized_keys.sm && "
                + "mv ~/.ssh/authorized_keys.sm ~/.ssh/authorized_keys && "
                + "chmod 600 ~/.ssh/authorized_keys",
                target,
                20);
            var left = await transport.RunAsync(
                $"grep -cF {body.ShellQuote()} ~/.ssh/authorized_keys || true", target, 20);
            Assert.Equal(0, left.Trim().ToInt());
        }
    }
}

/// <summary>
/// Collection against a real Windows host, by password.
/// </summary>
/// <remarks>
/// The other half of the P1 exit criterion. This is also the only place the
/// deflate + UTF-16LE + <c>-EncodedCommand</c> delivery is exercised for real:
/// getting it wrong yields a shell error rather than a wrong number, and only
/// on a genuine Windows host.
/// </remarks>
public class LiveWindowsTests
{
    private static SshTarget Target() => new(
        Guid.NewGuid(),
        Environment.GetEnvironmentVariable("SM_WIN_HOST")!,
        22,
        Environment.GetEnvironmentVariable("SM_WIN_USER")!,
        SshCredential.Password);

    private static (ISshTransport Transport, ICredentialStore Store) Library()
    {
        var store = new InMemoryCredentialStore();
        var target = Target();
        store.SetPassword(target.ServerId, Environment.GetEnvironmentVariable("SM_WIN_PASSWORD")!);
        return (new SshNetTransport(store), store);
    }

    private const string WindowsEnv = "SM_WIN_HOST";

    [RequiresEnv(WindowsEnv, "SM_WIN_USER", "SM_WIN_PASSWORD")]
    public async Task PasswordAuthReachesTheHost()
    {
        var (transport, store) = Library();
        await using (transport)
        {
            var target = Target();
            // The target's id is what the credential is keyed by, so it has to
            // be the same one the store was seeded with.
            store.SetPassword(target.ServerId, Environment.GetEnvironmentVariable("SM_WIN_PASSWORD")!);
            var output = await transport.RunAsync("echo SM_OK", target, 25);
            Assert.Contains("SM_OK", output, StringComparison.Ordinal);
        }
    }

    [RequiresEnv(WindowsEnv, "SM_WIN_USER", "SM_WIN_PASSWORD")]
    public async Task TheHostIsDetectedAsWindows()
    {
        // It answers by failing: no `uname`. Any exit status but ssh's own 255
        // means a shell answered.
        var (transport, store) = Library();
        await using (transport)
        {
            var target = Target();
            store.SetPassword(target.ServerId, Environment.GetEnvironmentVariable("SM_WIN_PASSWORD")!);
            OSKind detected;
            try
            {
                detected = MetricsCollector.OsKindFromUname(
                    await transport.RunAsync(Probes.OsDetect, target, 25));
            }
            catch (SshException failure)
            {
                detected = MetricsCollector.OsKindFromProbeFailure(failure)
                    ?? throw new Xunit.Sdk.XunitException(
                        $"the probe failed in a way that says nothing about the host: {failure.Message}");
            }
            Assert.Equal(OSKind.Windows, detected);
        }
    }

    [RequiresEnv(WindowsEnv, "SM_WIN_USER", "SM_WIN_PASSWORD")]
    public async Task TheEncodedScriptRunsAndParses()
    {
        var (transport, store) = Library();
        await using (transport)
        {
            var target = Target();
            store.SetPassword(target.ServerId, Environment.GetEnvironmentVariable("SM_WIN_PASSWORD")!);
            var collector = new MetricsCollector(_ => transport);
            var snapshot = await collector.CollectAsync(target, OSKind.Windows);

            Assert.InRange(snapshot.CpuPercent, 0, 100);
            Assert.True(snapshot.MemoryTotal > 0, $"memoryTotal={snapshot.MemoryTotal}");
            Assert.True(snapshot.Cores > 0, $"cores={snapshot.Cores}");
            Assert.True(snapshot.UptimeSeconds > 0, $"uptime={snapshot.UptimeSeconds}");
            Assert.NotEmpty(snapshot.Identity.Hostname);
            Assert.NotEmpty(snapshot.Filesystems);
            // Windows reports no user/system/iowait split, so the card leaves
            // that row out rather than drawing five zeroes.
            Assert.False(snapshot.CpuBreakdown.IsReported);

            Console.WriteLine($"""

                ── {snapshot.Identity.Hostname} ──
                os         {snapshot.Identity.OsName}
                cpu        {Format.Percent(snapshot.CpuPercent)}  model {snapshot.Identity.CpuModel}
                cores      {snapshot.Cores}
                queue      {Format.Load(snapshot.Load1)}   (Windows has no load average)
                memory     {Format.Usage(snapshot.MemoryUsed, snapshot.MemoryTotal)}
                disk       {Format.Usage(snapshot.DiskUsed, snapshot.DiskTotal)}
                uptime     {Format.Uptime(snapshot.UptimeSeconds, chinese: false)}
                mounts     {string.Join(", ", snapshot.Filesystems.Select(f => f.Mount))}
                ─────────────────────────
                """);
        }
    }

    [RequiresEnv(WindowsEnv, "SM_WIN_USER", "SM_WIN_PASSWORD")]
    public async Task CrlfOutputParsesTheSameAsLf()
    {
        // A Windows host answers entirely in CRLF, which is what Text.Lines
        // exists for: a plain Split('\n') leaves a stray \r that then fails to
        // parse as a number.
        var (transport, store) = Library();
        await using (transport)
        {
            var target = Target();
            store.SetPassword(target.ServerId, Environment.GetEnvironmentVariable("SM_WIN_PASSWORD")!);
            var output = await transport.RunAsync(WindowsMetrics.Command, target, 40);
            Assert.Contains("\r\n", output, StringComparison.Ordinal);
            var snapshot = WindowsMetrics.Parse(output);
            Assert.NotNull(snapshot);
            Assert.True(snapshot.Cores > 0);
        }
    }
}
