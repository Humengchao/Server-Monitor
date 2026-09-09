using ServerMonitor.Core.Ssh;

namespace ServerMonitor.Core.Model;

/// <summary>
/// A collection result in transit, before it is persisted.
/// </summary>
/// <remarks>
/// A plain carrier so the collector can run off the UI thread and hand results
/// across without touching the store.
/// </remarks>
public sealed class MetricSnapshot
{
    public double CpuPercent { get; set; }
    public double Load1 { get; set; }
    public double Load5 { get; set; }
    public double Load15 { get; set; }
    public long MemoryUsed { get; set; }
    public long MemoryTotal { get; set; }
    public long DiskUsed { get; set; }
    public long DiskTotal { get; set; }
    public double NetRxRate { get; set; }
    public double NetTxRate { get; set; }
    public double DiskReadRate { get; set; }
    public double DiskWriteRate { get; set; }
    public long NetRxTotal { get; set; }
    public long NetTxTotal { get; set; }
    public long UptimeSeconds { get; set; }
    /// <summary>Network round trip to the host, in milliseconds.</summary>
    public double LatencyMs { get; set; }

    // Detail for the machine screen. Current-state only, and deliberately not
    // persisted with the history samples: the charts need the scalars above,
    // and a per-core/per-process array on every row would grow the store for
    // data nobody reads back.
    public List<CoreLoad> CoreLoads { get; set; } = [];
    public CpuBreakdown CpuBreakdown { get; set; } = new();
    public GpuStatus Gpu { get; set; } = new();
    public MemoryBreakdown Memory { get; set; } = new();
    public List<NetInterface> Interfaces { get; set; } = [];
    public List<FilesystemUsage> Filesystems { get; set; } = [];
    public List<HostProcess> Processes { get; set; } = [];

    /// <summary>
    /// Whether this poll asked the host for its process list.
    /// </summary>
    /// <remarks>
    /// An empty <see cref="Processes"/> means two different things and the
    /// screen has to tell them apart. A live host always has processes, so an
    /// empty list from a poll that asked is a failure worth naming; an empty
    /// list from a poll that did not ask is simply a card that has not been
    /// filled in yet. Only the machine screen's host is asked — `ps` over
    /// every process costs it ~30 ms a poll — so most snapshots are the
    /// second kind, and stating "no process data" about them was telling the
    /// user their host reports nothing when the app had not enquired.
    /// </remarks>
    public bool SampledProcesses { get; set; }
    public HostIdentity Identity { get; set; } = new();

    // Host facts that ride along with the same round trip.
    public int Cores { get; set; }
    /// <summary>
    /// Set when Auto collection worked out which script the host speaks, so
    /// the caller can record it and stop probing.
    /// </summary>
    public OSKind? DetectedOS { get; set; }
    public string DockerVersion { get; set; } = string.Empty;
    /// <summary>
    /// Engine counts, when the host answered with them. Null on a host without
    /// Docker, or one whose <c>docker info</c> only gave a version.
    /// </summary>
    public DockerSummary? DockerSummary { get; set; }

    public double MemoryPercent => MemoryTotal > 0 ? (double)MemoryUsed / MemoryTotal * 100 : 0;
    public double DiskPercent => DiskTotal > 0 ? (double)DiskUsed / DiskTotal * 100 : 0;
}

/// <summary>
/// One collection result for one server, as stored.
/// </summary>
/// <remarks>
/// The table grows by a row per server per poll, so it is indexed on
/// (serverID, timestamp) and pruned on a schedule — see
/// <see cref="Store.Database"/>.
/// </remarks>
public sealed class MetricSample
{
    public long? Id { get; set; }
    public Guid ServerId { get; set; }
    public DateTime Timestamp { get; set; }

    public double CpuPercent { get; set; }
    public double Load1 { get; set; }
    public double Load5 { get; set; }
    public double Load15 { get; set; }
    public long MemoryUsed { get; set; }
    public long MemoryTotal { get; set; }
    public long DiskUsed { get; set; }
    public long DiskTotal { get; set; }
    public double NetRxRate { get; set; }
    public double NetTxRate { get; set; }
    public double DiskReadRate { get; set; }
    public double DiskWriteRate { get; set; }
    public long NetRxTotal { get; set; }
    public long NetTxTotal { get; set; }
    public long UptimeSeconds { get; set; }
    public double LatencyMs { get; set; }

    /// <summary>For the charts, which take a selector rather than a closure.</summary>
    public double MemoryPercent => MemoryTotal > 0 ? (double)MemoryUsed / MemoryTotal * 100 : 0;
    public double DiskPercent => DiskTotal > 0 ? (double)DiskUsed / DiskTotal * 100 : 0;

    public MetricSample() { }

    public MetricSample(Guid serverId, MetricSnapshot snapshot, DateTime? timestamp = null)
    {
        ServerId = serverId;
        Timestamp = timestamp ?? DateTime.UtcNow;
        CpuPercent = snapshot.CpuPercent;
        Load1 = snapshot.Load1;
        Load5 = snapshot.Load5;
        Load15 = snapshot.Load15;
        MemoryUsed = snapshot.MemoryUsed;
        MemoryTotal = snapshot.MemoryTotal;
        DiskUsed = snapshot.DiskUsed;
        DiskTotal = snapshot.DiskTotal;
        NetRxRate = snapshot.NetRxRate;
        NetTxRate = snapshot.NetTxRate;
        DiskReadRate = snapshot.DiskReadRate;
        DiskWriteRate = snapshot.DiskWriteRate;
        NetRxTotal = snapshot.NetRxTotal;
        NetTxTotal = snapshot.NetTxTotal;
        UptimeSeconds = snapshot.UptimeSeconds;
        LatencyMs = snapshot.LatencyMs;
    }

    /// <summary>The stored values as a snapshot, for tile and chart rendering.</summary>
    public MetricSnapshot ToSnapshot() => new()
    {
        CpuPercent = CpuPercent,
        Load1 = Load1,
        Load5 = Load5,
        Load15 = Load15,
        MemoryUsed = MemoryUsed,
        MemoryTotal = MemoryTotal,
        DiskUsed = DiskUsed,
        DiskTotal = DiskTotal,
        NetRxRate = NetRxRate,
        NetTxRate = NetTxRate,
        DiskReadRate = DiskReadRate,
        DiskWriteRate = DiskWriteRate,
        NetRxTotal = NetRxTotal,
        NetTxTotal = NetTxTotal,
        UptimeSeconds = UptimeSeconds,
        LatencyMs = LatencyMs,
    };
}

/// <summary>Cumulative counters carried between polls to turn deltas into rates.</summary>
internal sealed class CounterBaseline
{
    public long NetRx { get; init; }
    public long NetTx { get; init; }
    public long DiskRead { get; init; }
    public long DiskWrite { get; init; }
    public DateTime TakenAt { get; init; }
}

/// <summary>Per-interface counters from the previous poll, keyed by interface name.</summary>
internal sealed class InterfaceBaseline
{
    public Dictionary<string, (long Rx, long Tx)> Counters { get; init; } = [];
    public DateTime TakenAt { get; init; }
}
