using System.Diagnostics;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Runs one collection round against a host and turns raw counters into rates.
/// </summary>
/// <remarks>
/// Holds no UI or store types: it takes a target and returns a value, leaving
/// persistence to the caller. Its own state is the per-host baselines and
/// caches, guarded by a lock rather than an actor — the Swift original is an
/// <c>actor</c>, and a lock around a handful of dictionary touches is the
/// direct equivalent.
/// </remarks>
public sealed class MetricsCollector(
    Func<SshTarget, ISshTransport> transportFor,
    ILatencyProbe? latency = null)
{
    private readonly ILatencyProbe _ping = latency ?? new PingProbe();
    private readonly Lock _state = new();

    /// <summary>
    /// Latency changes far more slowly than load, so ICMP runs on its own
    /// slower cadence and the reading is reused between polls. At a 5-second
    /// interval, probing every round would be 12 measurements a minute per
    /// host for a number that barely moves.
    /// </summary>
    private readonly Dictionary<Guid, (PingProbe.Reading Reading, DateTime TakenAt)> _pingCache = [];
    private static readonly TimeSpan PingInterval = TimeSpan.FromSeconds(30);

    /// <summary>
    /// Previous cumulative counters per server, needed to derive per-second
    /// rates. Lost on relaunch, which costs exactly one poll of rate data.
    /// </summary>
    private readonly Dictionary<Guid, CounterBaseline> _baselines = [];
    private readonly Dictionary<Guid, InterfaceBaseline> _interfaceBaselines = [];

    /// <summary>
    /// CPU model per server, for hosts whose <c>/proc/cpuinfo</c> does not
    /// name it. Looked up with <c>lscpu</c> once — it is a fixed fact, and
    /// <c>lscpu</c> walks every core's sysfs topology, which is not cheap on
    /// an 80-core ARM box — then carried into every later snapshot.
    /// </summary>
    private readonly Dictionary<Guid, string> _cpuModelCache = [];
    /// <summary>Hosts already asked, so one that cannot answer is not asked every poll.</summary>
    private readonly HashSet<Guid> _cpuModelLookedUp = [];

    /// <summary>
    /// Docker engine facts per server, and when they were last asked for.
    /// </summary>
    /// <remarks>
    /// <c>docker info</c> costs the host ~90 ms of CPU and its answer changes
    /// when an image is pulled or a container starts — not every five seconds
    /// — so it is sampled every <see cref="DockerInterval"/> and carried
    /// forward in between.
    /// </remarks>
    private readonly Dictionary<Guid, (string Version, DockerSummary? Summary)> _dockerCache = [];
    private readonly Dictionary<Guid, DateTime> _dockerSampledAt = [];
    internal static readonly TimeSpan DockerInterval = TimeSpan.FromSeconds(30);

    /// <summary>
    /// Resolved addresses for config aliases, which rarely change. Used by the
    /// ping probe, which needs an address rather than an alias.
    /// </summary>
    private readonly Dictionary<string, string> _resolvedHosts = [];

    /// <summary>
    /// One collection round.
    /// </summary>
    /// <param name="processes">
    /// Whether to fetch the process list. Only the machine screen shows it,
    /// and <c>ps</c> over every process costs the host ~30 ms per poll, so the
    /// dashboard's polls leave it out.
    /// </param>
    public async Task<MetricSnapshot> CollectAsync(
        SshTarget target,
        OSKind osKind = OSKind.Auto,
        bool processes = true,
        CancellationToken cancellationToken = default)
    {
        // Kick the ICMP probe off alongside the SSH round trip when it is due,
        // so latency costs no extra wall-clock time.
        var icmp = PingIfDueAsync(target, cancellationToken);

        MetricSnapshot snapshot;
        var elapsed = 0.0;
        var clocks = (Start: string.Empty, End: string.Empty);

        switch (osKind)
        {
            case OSKind.Windows:
                snapshot = await CollectWindowsAsync(target, cancellationToken).ConfigureAwait(false);
                break;

            case OSKind.Linux:
                (snapshot, elapsed, clocks) =
                    await CollectLinuxAsync(target, processes, cancellationToken).ConfigureAwait(false);
                break;

            default:
                {
                    // Probe with one cheap command rather than speculatively
                    // running a whole collection. The Linux batch contains a
                    // `sleep` and several `cat`s; against a Windows shell that
                    // does not fail fast it hung until the timeout — a
                    // detection should cost a moment, not 30 s.
                    var detected = await DetectOSAsync(target, cancellationToken).ConfigureAwait(false);
                    if (detected == OSKind.Linux)
                    {
                        (snapshot, elapsed, clocks) =
                            await CollectLinuxAsync(target, processes, cancellationToken).ConfigureAwait(false);
                    }
                    else
                    {
                        snapshot = await CollectWindowsAsync(target, cancellationToken).ConfigureAwait(false);
                    }
                    snapshot.DetectedOS = detected;
                    break;
                }
        }

        // Prefer real ICMP over the physical NIC: it is the actual network
        // round trip. The remote-clock figure is the fallback for hosts that
        // filter ICMP, and for the tunnel case (R12) — it always works, but it
        // also carries any proxy hop, so it reads higher.
        var clockLatency = ProcParsers.NetworkLatency(elapsed, clocks.Start, clocks.End);
        // Bounded, not just awaited. The probe already caps itself, but the
        // poll must not wait on it at all beyond the SSH round trip it was
        // meant to hide behind: if it has not answered by now, take the clock
        // figure and let the reading land in the cache for the next poll.
        var reading = await AwaitBounded(icmp, cancellationToken).ConfigureAwait(false);
        snapshot.LatencyMs = reading?.AverageMs ?? clockLatency;
        return snapshot;
    }

    /// <summary>
    /// The probe's answer if it has one within a moment, else null.
    /// </summary>
    /// <remarks>
    /// The task is deliberately not cancelled when this gives up: it is still
    /// writing to the reading cache, so the next poll gets the number for
    /// free. Only the waiting is abandoned.
    /// </remarks>
    private static async Task<PingProbe.Reading?> AwaitBounded(
        Task<PingProbe.Reading?> probe, CancellationToken cancellationToken)
    {
        if (probe.IsCompleted) return await probe.ConfigureAwait(false);
        var grace = Task.Delay(PingProbe.TotalBudget, cancellationToken);
        var finished = await Task.WhenAny(probe, grace).ConfigureAwait(false);
        return ReferenceEquals(finished, probe) ? await probe.ConfigureAwait(false) : null;
    }

    /// <summary>
    /// Asks the host what it is, with a short timeout.
    /// </summary>
    /// <remarks>
    /// A Windows host answers by <em>failing</em> — it has no <c>uname</c> —
    /// so a non-zero exit from the remote shell is the Windows signal rather
    /// than an error. ssh's own failures are a different thing entirely: 255,
    /// a timeout, or a failed launch all mean no shell was ever reached.
    /// Swallowing those and guessing Windows sent an unreachable host through
    /// the whole Windows collection as well, so every poll of a down host paid
    /// two 10 s connect timeouts instead of one and reported the failure in
    /// Windows terms.
    /// </remarks>
    private async Task<OSKind> DetectOSAsync(SshTarget target, CancellationToken cancellationToken)
    {
        try
        {
            var output = await transportFor(target)
                .RunAsync(Probes.OsDetect, target, 15, cancellationToken)
                .ConfigureAwait(false);
            return OsKindFromUname(output);
        }
        catch (SshException failure)
        {
            var kind = OsKindFromProbeFailure(failure);
            if (kind is null) throw;
            return kind.Value;
        }
    }

    /// <summary>
    /// What a failed <c>uname -s</c> says about the host, or null when it says
    /// nothing — in which case the caller must rethrow rather than guess.
    /// </summary>
    public static OSKind? OsKindFromProbeFailure(SshException failure) =>
        failure.Kind == SshFailure.CommandFailed && failure.ExitStatus != 0 && failure.ExitStatus != 255
            ? OSKind.Windows
            : null;

    /// <summary>
    /// A Linux host prints "Linux"; a Windows shell prints an error, or
    /// nothing at all, so anything else is treated as Windows.
    /// </summary>
    public static OSKind OsKindFromUname(string output) =>
        output.Trim().Contains("linux", StringComparison.OrdinalIgnoreCase)
            ? OSKind.Linux
            : OSKind.Windows;

    /// <summary>Whether this poll should ask the engine again.</summary>
    /// <remarks>
    /// Always when nothing is cached — a first poll, or a host that has just
    /// gained Docker.
    /// </remarks>
    internal static bool ShouldSampleDocker(DateTime? lastSampled, DateTime now) =>
        lastSampled is not { } last || now - last >= DockerInterval;

    private async Task<(MetricSnapshot Snapshot, double Elapsed, (string Start, string End) Clocks)>
        CollectLinuxAsync(SshTarget target, bool processes, CancellationToken cancellationToken)
    {
        var started = DateTime.UtcNow;
        bool askDocker;
        lock (_state)
        {
            askDocker = ShouldSampleDocker(
                _dockerSampledAt.TryGetValue(target.ServerId, out var last) ? last : null, started);
        }

        var command = Probes.LinuxMetricsCommand(processes, askDocker);
        var stopwatch = Stopwatch.StartNew();
        var output = await transportFor(target)
            .RunAsync(command, target, cancellationToken: cancellationToken)
            .ConfigureAwait(false);
        var elapsed = stopwatch.Elapsed.TotalSeconds;

        var sections = ProcParsers.SplitSections(output, ProcParsers.SectionCount);
        string Section(ProcParsers.Section which) => sections[(int)which];

        var snapshot = new MetricSnapshot
        {
            CpuPercent = ProcParsers.CpuPercent(
                Section(ProcParsers.Section.StatFirst), Section(ProcParsers.Section.StatSecond)),
            UptimeSeconds = ProcParsers.Uptime(Section(ProcParsers.Section.Uptime)),
            Cores = ProcParsers.Cores(Section(ProcParsers.Section.Nproc)),
        };
        (snapshot.Load1, snapshot.Load5, snapshot.Load15) =
            ProcParsers.LoadAverage(Section(ProcParsers.Section.LoadAvg));
        (snapshot.MemoryUsed, snapshot.MemoryTotal) =
            ProcParsers.MemInfo(Section(ProcParsers.Section.MemInfo));
        (snapshot.DiskUsed, snapshot.DiskTotal) =
            ProcParsers.DiskUsage(Section(ProcParsers.Section.DiskUsage));

        if (askDocker)
        {
            snapshot.DockerVersion = ProcParsers.DockerVersion(Section(ProcParsers.Section.Docker));
            if (snapshot.DockerVersion.Length > 0)
            {
                var summary = DockerClient.ParseSummary(Section(ProcParsers.Section.Docker));
                // ParseSummary needs the pipe-separated form; a host that
                // answered with a bare version leaves the counts at zero,
                // which would read as "no containers" rather than "not
                // reported".
                if (summary.EngineVersion.Length > 0) snapshot.DockerSummary = summary;
            }
            lock (_state)
            {
                _dockerSampledAt[target.ServerId] = started;
                _dockerCache[target.ServerId] = (snapshot.DockerVersion, snapshot.DockerSummary);
            }
        }
        else
        {
            lock (_state)
            {
                if (_dockerCache.TryGetValue(target.ServerId, out var cached))
                {
                    // Between samples the last answer stands; an empty version
                    // here would read as "Docker was removed" and hide the card.
                    snapshot.DockerVersion = cached.Version;
                    snapshot.DockerSummary = cached.Summary;
                }
            }
        }

        // Detail for the machine screen. Most of this comes from output the
        // batch already fetched and used to throw away.
        snapshot.CoreLoads = ProcParsers.CoreLoads(
            Section(ProcParsers.Section.StatFirst), Section(ProcParsers.Section.StatSecond));
        snapshot.CpuBreakdown = ProcParsers.CpuBreakdownOf(
            Section(ProcParsers.Section.StatFirst), Section(ProcParsers.Section.StatSecond));
        snapshot.Memory = ProcParsers.MemoryBreakdownOf(Section(ProcParsers.Section.MemInfo));
        snapshot.Filesystems = ProcParsers.Filesystems(Section(ProcParsers.Section.DiskUsage));
        snapshot.Processes = ProcParsers.Processes(Section(ProcParsers.Section.Processes));
        snapshot.Identity = ProcParsers.HostIdentityOf(Section(ProcParsers.Section.HostInfo));
        snapshot.Identity.CpuModel = await CpuModelAsync(
            snapshot.Identity.CpuModel, target, cancellationToken).ConfigureAwait(false);
        snapshot.Gpu = ProcParsers.GpuStatusOf(Section(ProcParsers.Section.Gpu));
        snapshot.Interfaces = ProcParsers.NetInterfaces(Section(ProcParsers.Section.NetDev));

        var net = ProcParsers.NetDev(Section(ProcParsers.Section.NetDev));
        var disk = ProcParsers.DiskStats(Section(ProcParsers.Section.DiskStats));
        snapshot.NetRxTotal = net.Rx;
        snapshot.NetTxTotal = net.Tx;
        ApplyRates(target.ServerId, snapshot, net, disk);
        ApplyInterfaceRates(target.ServerId, snapshot);

        // A live Linux host always has memory and at least one CPU. Both zero
        // means the batch came back empty or truncated — the transport exited 0
        // with no usable stdout, which it does not treat as an error.
        // Returning this would paint a real host as a zeroed-out machine on the
        // dashboard (0 cores, 0 memory, no filesystems); a thrown error marks
        // it offline and the next poll recovers. Seen intermittently under
        // load, when a channel is torn down mid-read.
        if (snapshot.MemoryTotal <= 0 && snapshot.Cores <= 0)
        {
            throw SshException.Failed("empty metrics from host (truncated collection)");
        }

        return (snapshot, elapsed,
            (Section(ProcParsers.Section.StartClock), Section(ProcParsers.Section.EndClock)));
    }

    private async Task<MetricSnapshot> CollectWindowsAsync(
        SshTarget target, CancellationToken cancellationToken)
    {
        var output = await transportFor(target)
            .RunAsync(WindowsMetrics.Command, target, cancellationToken: cancellationToken)
            .ConfigureAwait(false);
        var snapshot = WindowsMetrics.Parse(output);
        if (snapshot is null)
        {
            // Carry the output: "no metrics" alone says nothing about whether
            // the shell rejected the command, PowerShell is missing, or the
            // host answered with something else entirely.
            var sample = output.Trim();
            if (sample.Length > 300) sample = sample[..300];
            throw SshException.Failed(sample.Length == 0
                ? "Windows host returned no output"
                : $"unparsed Windows output: {sample}");
        }
        var counters = WindowsMetrics.Counters(output);
        ApplyRates(target.ServerId, snapshot, counters.Net, counters.Disk);
        ApplyInterfaceRates(target.ServerId, snapshot);
        return snapshot;
    }

    /// <summary>
    /// The parsed CPU model when the host gave one; otherwise the cached
    /// answer, or one <c>lscpu</c> round trip the first time.
    /// </summary>
    private async Task<string> CpuModelAsync(
        string parsed, SshTarget target, CancellationToken cancellationToken)
    {
        if (parsed.Length > 0) return parsed;
        lock (_state)
        {
            if (_cpuModelCache.TryGetValue(target.ServerId, out var cached)) return cached;
            if (!_cpuModelLookedUp.Add(target.ServerId)) return string.Empty;
        }
        string answer;
        try
        {
            answer = (await transportFor(target)
                .RunAsync(Probes.CpuModelFallback, target, 15, cancellationToken)
                .ConfigureAwait(false)).Trim();
        }
        catch (SshException)
        {
            // A host that cannot answer this is not a failed poll; the card
            // simply shows no model. The id stays in the looked-up set so it
            // is not asked again.
            return string.Empty;
        }
        if (answer.Length > 0)
        {
            lock (_state) _cpuModelCache[target.ServerId] = answer;
        }
        return answer;
    }

    /// <summary>
    /// Per-interface rates, kept separate from <see cref="ApplyRates"/>
    /// because an interface can appear or disappear between polls (a container
    /// bridge coming up, a VPN going down) and must not be compared against a
    /// baseline that belonged to a different NIC.
    /// </summary>
    private void ApplyInterfaceRates(Guid serverId, MetricSnapshot snapshot)
    {
        var now = DateTime.UtcNow;
        InterfaceBaseline? previous;
        lock (_state)
        {
            _interfaceBaselines.TryGetValue(serverId, out previous);
            _interfaceBaselines[serverId] = new InterfaceBaseline
            {
                Counters = snapshot.Interfaces
                    .GroupBy(i => i.Name)
                    .ToDictionary(g => g.Key, g => (g.First().RxTotal, g.First().TxTotal)),
                TakenAt = now,
            };
        }
        if (previous is null) return;
        var seconds = (now - previous.TakenAt).TotalSeconds;
        if (seconds <= 0.5) return;

        foreach (var nic in snapshot.Interfaces)
        {
            if (!previous.Counters.TryGetValue(nic.Name, out var before)) continue;
            var rx = nic.RxTotal - before.Rx;
            var tx = nic.TxTotal - before.Tx;
            // A counter that went backwards means the NIC was reset; report
            // nothing rather than a nonsense spike.
            nic.RxRate = rx >= 0 ? rx / seconds : 0;
            nic.TxRate = tx >= 0 ? tx / seconds : 0;
        }
    }

    /// <summary>
    /// Converts cumulative counters into per-second rates using the previous
    /// poll as the baseline. A counter that went backwards (host reboot, NIC
    /// reset) yields zero instead of a spike.
    /// </summary>
    private void ApplyRates(
        Guid serverId,
        MetricSnapshot snapshot,
        (long Rx, long Tx) net,
        (long Read, long Written) disk)
    {
        var now = DateTime.UtcNow;
        CounterBaseline? previous;
        lock (_state)
        {
            _baselines.TryGetValue(serverId, out previous);
            _baselines[serverId] = new CounterBaseline
            {
                NetRx = net.Rx,
                NetTx = net.Tx,
                DiskRead = disk.Read,
                DiskWrite = disk.Written,
                TakenAt = now,
            };
        }
        if (previous is null) return;
        var seconds = (now - previous.TakenAt).TotalSeconds;
        if (seconds <= 0) return;

        double Rate(long current, long earlier)
        {
            var delta = current - earlier;
            return delta >= 0 ? delta / seconds : 0;
        }

        snapshot.NetRxRate = Rate(net.Rx, previous.NetRx);
        snapshot.NetTxRate = Rate(net.Tx, previous.NetTx);
        snapshot.DiskReadRate = Rate(disk.Read, previous.DiskRead);
        snapshot.DiskWriteRate = Rate(disk.Written, previous.DiskWrite);
    }

    /// <summary>
    /// A fresh ICMP reading when one is due, otherwise the cached one.
    /// </summary>
    private async Task<PingProbe.Reading?> PingIfDueAsync(
        SshTarget target, CancellationToken cancellationToken)
    {
        lock (_state)
        {
            if (_pingCache.TryGetValue(target.ServerId, out var cached)
                && DateTime.UtcNow - cached.TakenAt < PingInterval)
            {
                return cached.Reading;
            }
        }

        var address = await AddressForAsync(target, cancellationToken).ConfigureAwait(false);
        if (address is null) return null;

        PingProbe.Reading? reading;
        try
        {
            reading = await _ping.MeasureAsync(address, cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            throw;
        }
        catch (Exception)
        {
            reading = null;
        }

        lock (_state)
        {
            if (reading is null)
            {
                // Leave any previous reading in place rather than blanking the
                // display on one dropped probe.
                return _pingCache.TryGetValue(target.ServerId, out var stale) ? stale.Reading : null;
            }
            _pingCache[target.ServerId] = (reading, DateTime.UtcNow);
            return reading;
        }
    }

    /// <summary>
    /// The address to ping. For a config alias the stored host may be an alias
    /// rather than an address, so the config is consulted.
    /// </summary>
    private Task<string?> AddressForAsync(SshTarget target, CancellationToken cancellationToken)
    {
        _ = cancellationToken;
        if (target.Credential.Method != AuthMethod.ConfigAlias)
        {
            return Task.FromResult<string?>(target.Host.Length == 0 ? null : target.Host);
        }
        lock (_state)
        {
            if (_resolvedHosts.TryGetValue(target.Host, out var cached))
            {
                return Task.FromResult<string?>(cached.Length == 0 ? null : cached);
            }
        }
        // Resolved from the config we already parse, rather than by shelling
        // out to `ssh -G` the way the macOS build does: there is no process to
        // start here, and the same parser already backs the library transport.
        var resolved = SshConfig.FromDefaultLocation().Resolve(target.Host).HostName;
        lock (_state) _resolvedHosts[target.Host] = resolved;
        return Task.FromResult<string?>(resolved.Length == 0 ? null : resolved);
    }

    /// <summary>
    /// Forgets a host's rate baseline and drops its connection.
    /// </summary>
    public async Task ForgetAsync(SshTarget target)
    {
        lock (_state)
        {
            _baselines.Remove(target.ServerId);
            _interfaceBaselines.Remove(target.ServerId);
            _pingCache.Remove(target.ServerId);
            _dockerCache.Remove(target.ServerId);
            _dockerSampledAt.Remove(target.ServerId);
            _cpuModelCache.Remove(target.ServerId);
            _cpuModelLookedUp.Remove(target.ServerId);
        }
        await transportFor(target).DisconnectAsync(target).ConfigureAwait(false);
    }
}
