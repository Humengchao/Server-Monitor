namespace ServerMonitor.Core.Model;

/// <summary>One logical CPU's utilisation for the current poll.</summary>
public readonly record struct CoreLoad(int Index, double Percent);

/// <summary>
/// One NVIDIA GPU, as <c>nvidia-smi --query-gpu</c> reports it.
/// </summary>
/// <remarks>
/// The nullable fields really are optional: nvidia-smi prints <c>[N/A]</c> for
/// a figure the card does not expose — datacentre parts have no fan, and some
/// report no power limit — and a card that shows 0 °C or 0 W for those is
/// worse than one that shows nothing.
/// </remarks>
public sealed record GpuInfo(
    int Index,
    string Name,
    double UtilizationPercent,
    long MemoryTotal,
    long MemoryUsed,
    double? TemperatureC = null,
    double? FanPercent = null,
    double? PowerDrawW = null,
    double? PowerLimitW = null)
{
    public double MemoryPercent => MemoryTotal > 0 ? (double)MemoryUsed / MemoryTotal * 100 : 0;
}

/// <summary>A process holding GPU memory.</summary>
public sealed record GpuProcess(int Pid, string Name, long MemoryUsed);

/// <summary>
/// Everything the GPU card shows. Empty on the overwhelming majority of hosts,
/// which is why the card is left out entirely rather than shown with zeroes.
/// </summary>
public sealed class GpuStatus
{
    public string DriverVersion { get; set; } = string.Empty;
    public string CudaVersion { get; set; } = string.Empty;
    public List<GpuInfo> Gpus { get; set; } = [];
    public List<GpuProcess> Processes { get; set; } = [];

    public bool IsPresent => Gpus.Count > 0;
}

/// <summary>
/// Where the CPU's time actually went, as percentages of the sampling window.
/// </summary>
/// <remarks>
/// The single "busy" number hides the difference between a box doing work and
/// one stuck on disk or robbed by its hypervisor — <c>iowait</c> and
/// <c>steal</c> are the two that change what you do about it.
/// </remarks>
public sealed class CpuBreakdown
{
    public double User { get; set; }
    /// <summary>Includes irq and softirq, the way <c>top</c> folds them in.</summary>
    public double System { get; set; }
    public double Nice { get; set; }
    public double Iowait { get; set; }
    public double Steal { get; set; }

    /// <summary>
    /// False on hosts that do not report the split (Windows), so the card can
    /// leave the row out instead of drawing five zeroes.
    /// </summary>
    public bool IsReported => User > 0 || System > 0 || Nice > 0 || Iowait > 0 || Steal > 0;
}

/// <summary>
/// <c>/proc/meminfo</c> broken out the way <c>free</c> presents it, so the
/// memory card can show where the RAM actually went rather than one "used"
/// number.
/// </summary>
public sealed class MemoryBreakdown
{
    public long Total { get; set; }
    public long Free { get; set; }
    public long Buffers { get; set; }
    public long Cached { get; set; }
    public long SwapTotal { get; set; }
    public long SwapUsed { get; set; }

    /// <summary>Everything the kernel has not classified as free, buffer or cache.</summary>
    public long Used => Math.Max(0, Total - Free - Buffers - Cached);

    public double UsedPercent => Total > 0 ? (double)Used / Total * 100 : 0;
    public double SwapPercent => SwapTotal > 0 ? (double)SwapUsed / SwapTotal * 100 : 0;
    public bool HasSwap => SwapTotal > 0;
}

/// <summary>
/// A network interface and its traffic. Rates are filled in by the collector
/// from the previous poll's counters; the totals come straight from the host.
/// </summary>
public sealed record NetInterface(string Name, long RxTotal, long TxTotal)
{
    public double RxRate { get; set; }
    public double TxRate { get; set; }

    /// <summary>
    /// Container bridges, veth pairs and tunnels. Real traffic is what the
    /// network card leads with; these are hidden behind a toggle.
    /// </summary>
    public bool IsVirtual => IsVirtualName(Name);

    public static bool IsVirtualName(string name) =>
        VirtualPrefixes.Any(prefix => name.StartsWith(prefix, StringComparison.OrdinalIgnoreCase));

    internal static readonly string[] VirtualPrefixes =
        ["veth", "br-", "docker", "virbr", "cni", "flannel", "tun", "tap", "utun", "kube", "lo"];
}

/// <summary>One mounted filesystem.</summary>
public sealed record FilesystemUsage(string Mount, string Device, long Used, long Total)
{
    public double Percent => Total > 0 ? (double)Used / Total * 100 : 0;
}

/// <summary>
/// A row of <c>ps</c>. Named to avoid colliding with
/// <c>System.Diagnostics.Process</c>.
/// </summary>
public sealed record HostProcess(
    int Pid,
    string User,
    double CpuPercent,
    double MemPercent,
    long ResidentBytes,
    string Command);

/// <summary>
/// Slow-moving facts about the machine itself, refreshed with every poll
/// because they cost nothing extra once the connection is open.
/// </summary>
public sealed class HostIdentity
{
    public string Hostname { get; set; } = string.Empty;
    public string OsName { get; set; } = string.Empty;
    public string Kernel { get; set; } = string.Empty;
    public string Architecture { get; set; } = string.Empty;
    /// <summary>e.g. "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz".</summary>
    public string CpuModel { get; set; } = string.Empty;
    public List<string> Addresses { get; set; } = [];

    public bool IsEmpty =>
        Hostname.Length == 0 && OsName.Length == 0 && Kernel.Length == 0 && Addresses.Count == 0;
}
