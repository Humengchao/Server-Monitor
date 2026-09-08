using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Parsers for the <c>/proc</c> and <c>df</c> output gathered by one collection
/// round.
/// </summary>
/// <remarks>
/// Ported from the Go and Swift collectors so all three read the same fields
/// the same way. Every method is pure and total: malformed or truncated input
/// yields zeros rather than throwing, because a partially readable host should
/// still report the metrics it did return.
/// </remarks>
public static class ProcParsers
{
    /// <summary>Marker printed between batched commands.</summary>
    public const string SectionSeparator = "---SM-SECTION---";

    /// <summary>
    /// The sections of the Linux batch, in order.
    /// </summary>
    /// <remarks>
    /// The clock sections bracket the run so the host can report how long it
    /// spent, which is what makes the latency figure meaningful — see
    /// <see cref="NetworkLatency"/>. The order must match
    /// <c>shared/probes/linux-metrics.txt</c> line for line; a test asserts the
    /// counts agree, because a mismatch is not an error but plausible-looking
    /// nonsense (the CPU model arriving as the IP list).
    /// </remarks>
    public enum Section
    {
        StartClock = 0,
        StatFirst,
        StatSecond,
        MemInfo,
        LoadAvg,
        NetDev,
        DiskStats,
        Uptime,
        Nproc,
        DiskUsage,
        Processes,
        HostInfo,
        Gpu,
        Docker,
        EndClock,
    }

    public static int SectionCount => Enum.GetValues<Section>().Length;

    /// <summary>
    /// Splits batched output on separator lines. A truncated run leaves the
    /// trailing sections empty rather than misaligning the ones that arrived.
    /// </summary>
    public static string[] SplitSections(string output, int want)
    {
        var sections = new List<string>();
        var current = new System.Text.StringBuilder();
        foreach (var line in output.KeepEmptyLines())
        {
            if (line.Trim() == SectionSeparator)
            {
                sections.Add(current.ToString());
                current.Clear();
                continue;
            }
            current.Append(line).Append('\n');
        }
        sections.Add(current.ToString());
        while (sections.Count < want) sections.Add(string.Empty);
        return [.. sections];
    }

    // MARK: - CPU

    /// <summary>
    /// CPU usage from two <c>/proc/stat</c> snapshots taken half a second
    /// apart.
    /// </summary>
    public static double CpuPercent(string first, string second)
    {
        var (idle1, total1) = ProcStatCpu(first);
        var (idle2, total2) = ProcStatCpu(second);
        var deltaIdle = idle2 - idle1;
        var deltaTotal = total2 - total1;
        if (deltaTotal <= 0) return 0;
        return (1.0 - (double)deltaIdle / deltaTotal) * 100;
    }

    /// <summary>
    /// (idle, total) jiffies from the aggregate <c>cpu </c> line. idle counts
    /// idle+iowait, matching the Go collector.
    /// </summary>
    internal static (long Idle, long Total) ProcStatCpu(string output)
    {
        foreach (var line in output.Lines())
        {
            if (!line.StartsWith("cpu ", StringComparison.Ordinal)) continue;
            var fields = line.Fields();
            if (fields.Length < 8) return (0, 0);
            var values = new long[8];
            for (var i = 0; i < 8 && i + 1 < fields.Length; i++) values[i] = fields[i + 1].ToLong();
            return (values[3] + values[4], values.Sum());
        }
        return (0, 0);
    }

    /// <summary>
    /// Per-core utilisation from the same two <c>/proc/stat</c> reads.
    /// </summary>
    /// <remarks>
    /// The aggregate <c>cpu</c> line is skipped; only the numbered
    /// <c>cpuN</c> lines are cores. A core missing from the second read is
    /// dropped rather than reported as 0%, which would look like an idle core
    /// instead of a gap.
    /// </remarks>
    public static List<CoreLoad> CoreLoads(string first, string second)
    {
        var before = PerCoreTimes(first);
        var after = PerCoreTimes(second);
        var result = new List<CoreLoad>();
        foreach (var index in after.Keys.OrderBy(k => k))
        {
            if (!before.TryGetValue(index, out var start)) continue;
            var end = after[index];
            var deltaTotal = end.Total - start.Total;
            if (deltaTotal <= 0)
            {
                result.Add(new CoreLoad(index, 0));
                continue;
            }
            var deltaIdle = end.Idle - start.Idle;
            var percent = (1.0 - (double)deltaIdle / deltaTotal) * 100;
            result.Add(new CoreLoad(index, Math.Clamp(percent, 0, 100)));
        }
        return result;
    }

    private static Dictionary<int, (long Idle, long Total)> PerCoreTimes(string output)
    {
        var result = new Dictionary<int, (long, long)>();
        foreach (var line in output.Lines())
        {
            if (!line.StartsWith("cpu", StringComparison.Ordinal)) continue;
            var fields = line.Fields();
            if (fields.Length < 8) continue;
            var label = fields[0].Length > 3 ? fields[0][3..] : string.Empty;   // "cpu12" -> "12"
            if (label.Length == 0 || fields[0][3..].ToIntOrNull() is not { } index) continue;
            var values = new long[8];
            for (var i = 0; i < 8 && i + 1 < fields.Length; i++) values[i] = fields[i + 1].ToLong();
            result[index] = (values[3] + values[4], values.Sum());
        }
        return result;
    }

    /// <summary>
    /// How the sampling window was spent, from the same two <c>/proc/stat</c>
    /// reads.
    /// </summary>
    /// <remarks>
    /// Percentages are of the whole window, so they sum to the busy figure
    /// <see cref="CpuPercent"/> reports (plus idle), rather than each being a
    /// share of the busy time.
    /// </remarks>
    public static CpuBreakdown CpuBreakdownOf(string first, string second)
    {
        var before = AggregateFields(first);
        var after = AggregateFields(second);
        if (before.Length < 8 || after.Length < 8) return new CpuBreakdown();
        var deltas = before.Zip(after, (b, a) => Math.Max(0, a - b)).ToArray();
        var total = deltas.Sum();
        if (total <= 0) return new CpuBreakdown();

        double Share(long value) => (double)value / total * 100;
        return new CpuBreakdown
        {
            User = Share(deltas[0]),
            Nice = Share(deltas[1]),
            // irq + softirq belong with system; `top` shows them folded in and
            // splitting them out would leave a row nobody reads.
            System = Share(deltas[2] + deltas[5] + deltas[6]),
            Iowait = Share(deltas[4]),
            Steal = Share(deltas[7]),
        };
    }

    /// <summary>The eight counters on the aggregate <c>cpu </c> line.</summary>
    private static long[] AggregateFields(string output)
    {
        foreach (var line in output.Lines())
        {
            if (!line.StartsWith("cpu ", StringComparison.Ordinal)) continue;
            var fields = line.Fields();
            if (fields.Length < 9) return [];
            return Enumerable.Range(1, 8).Select(i => fields[i].ToLong()).ToArray();
        }
        return [];
    }

    // MARK: - Memory

    /// <summary>
    /// Used and total memory in bytes. <c>/proc/meminfo</c> reports kB.
    /// </summary>
    public static (long Used, long Total) MemInfo(string output)
    {
        long total = 0, free = 0, buffers = 0, cached = 0;
        foreach (var line in output.Lines())
        {
            var fields = line.Fields();
            if (fields.Length < 2) continue;
            var value = fields[1].ToLong();
            switch (fields[0])
            {
                case "MemTotal:": total = value; break;
                case "MemFree:": free = value; break;
                case "Buffers:": buffers = value; break;
                case "Cached:": cached = value; break;
            }
        }
        var used = Math.Max(0, total - free - buffers - cached);
        return (used * 1024, total * 1024);
    }

    /// <summary>
    /// The same file, broken out for the memory card. <c>/proc/meminfo</c> is
    /// in kB.
    /// </summary>
    public static MemoryBreakdown MemoryBreakdownOf(string output)
    {
        var fields = new Dictionary<string, long>(StringComparer.Ordinal);
        foreach (var line in output.Lines())
        {
            var parts = line.Fields();
            if (parts.Length < 2) continue;
            // Drop the trailing ":".
            fields[parts[0].TrimEnd(':')] = parts[1].ToLong() * 1024;
        }
        var memory = new MemoryBreakdown
        {
            Total = fields.GetValueOrDefault("MemTotal"),
            Free = fields.GetValueOrDefault("MemFree"),
            Buffers = fields.GetValueOrDefault("Buffers"),
            Cached = fields.GetValueOrDefault("Cached"),
            SwapTotal = fields.GetValueOrDefault("SwapTotal"),
        };
        memory.SwapUsed = Math.Max(0, memory.SwapTotal - fields.GetValueOrDefault("SwapFree"));
        return memory;
    }

    // MARK: - Load

    public static (double One, double Five, double Fifteen) LoadAverage(string output)
    {
        var fields = output.Fields();
        if (fields.Length < 3) return (0, 0, 0);
        return (fields[0].ToDouble(), fields[1].ToDouble(), fields[2].ToDouble());
    }

    // MARK: - Network

    /// <summary>Cumulative rx/tx bytes across every interface except loopback.</summary>
    public static (long Rx, long Tx) NetDev(string output)
    {
        long rxTotal = 0, txTotal = 0;
        foreach (var line in output.Lines())
        {
            if (!line.Contains(':')) continue;
            var fields = line.Fields();
            if (fields.Length < 10) continue;
            var name = fields[0].EndsWith(':') ? fields[0][..^1] : fields[0];
            if (name == "lo") continue;
            rxTotal += fields[1].ToLong();
            txTotal += fields[9].ToLong();
        }
        return (rxTotal, txTotal);
    }

    /// <summary>
    /// Each interface separately, for the network card. Loopback is skipped
    /// for the same reason it is skipped in the totals: it is not real traffic.
    /// </summary>
    public static List<NetInterface> NetInterfaces(string output)
    {
        var interfaces = new List<NetInterface>();
        foreach (var line in output.Lines())
        {
            var colon = line.IndexOf(':');
            if (colon < 0) continue;
            // The name and its colon may be joined to the first count on a
            // busy interface ("eth0:1234"), so split on the colon first rather
            // than on whitespace.
            var name = line[..colon].Trim();
            if (name.Length == 0 || name == "lo" || name.Contains('|')) continue;
            var counts = line[(colon + 1)..].Fields();
            if (counts.Length < 9) continue;
            interfaces.Add(new NetInterface(name, counts[0].ToLong(), counts[8].ToLong()));
        }
        return interfaces.OrderByDescending(i => i.RxTotal + i.TxTotal).ToList();
    }

    // MARK: - Disk I/O

    /// <summary>
    /// Cumulative bytes read/written across whole disks, skipping partitions.
    /// <c>/proc/diskstats</c> counts 512-byte sectors.
    /// </summary>
    public static (long Read, long Written) DiskStats(string output)
    {
        long readSectors = 0, writeSectors = 0;
        foreach (var line in output.Lines())
        {
            var fields = line.Fields();
            if (fields.Length < 14) continue;
            var name = fields[2];
            if (!name.StartsWith("sd", StringComparison.Ordinal)
                && !name.StartsWith("vd", StringComparison.Ordinal)
                && !name.StartsWith("nvme", StringComparison.Ordinal)) continue;
            if (name.Contains("nvme", StringComparison.Ordinal))
            {
                // nvme partitions look like nvme0n1p1.
                if (name.Length > 4 && name[4..].Contains('p')) continue;
            }
            else if (char.IsDigit(name[^1]))
            {
                // sd/vd ending in a digit is a partition.
                continue;
            }
            readSectors += fields[5].ToLong();
            writeSectors += fields[9].ToLong();
        }
        return (readSectors * 512, writeSectors * 512);
    }

    // MARK: - Latency

    /// <summary>
    /// Nanosecond epoch from <c>date +%s%N</c>, or null where the shell lacks
    /// <c>%N</c> (it then prints a literal "N").
    /// </summary>
    public static long? Clock(string output)
    {
        var text = output.Trim();
        if (text.Length == 0 || !text.All(char.IsAsciiDigit)) return null;
        return long.TryParse(text, out var value) ? value : null;
    }

    /// <summary>
    /// Network round trip, in milliseconds, for one collection round.
    /// </summary>
    /// <remarks>
    /// <paramref name="elapsedSeconds"/> is measured locally around the whole
    /// invocation, and the host's own two clock readings say how much of that
    /// it spent working — including the deliberate half-second sleep between
    /// CPU samples. Subtracting leaves the time on the wire.
    ///
    /// This is the only latency probe that survives a VPN or proxy tunnel:
    /// ICMP and even a TCP handshake are answered locally by the tunnel,
    /// whereas this requires bytes to reach the host and come back.
    /// </remarks>
    public static double NetworkLatency(double elapsedSeconds, string startClock, string endClock)
    {
        if (Clock(startClock) is not { } start || Clock(endClock) is not { } end || end < start)
        {
            return 0;
        }
        var remoteSeconds = (double)(end - start) / 1_000_000_000;
        return Math.Max(0, (elapsedSeconds - remoteSeconds) * 1000);
    }

    // MARK: - GPU

    /// <summary>
    /// The <c>nvidia-smi</c> section. Returns an empty status on the ~all hosts
    /// without a card, which is what makes the GPU card conditional.
    /// </summary>
    public static GpuStatus GpuStatusOf(string output)
    {
        var status = new GpuStatus();
        foreach (var line in output.Lines())
        {
            if (!line.TryKeyValue(out var key, out var value)) continue;
            switch (key)
            {
                case "driver": status.DriverVersion = value; break;
                case "cuda": status.CudaVersion = value; break;
                case "gpu":
                    if (ParseGpu(value) is { } gpu) status.Gpus.Add(gpu);
                    break;
                case "proc":
                    if (ParseGpuProcess(value) is { } process) status.Processes.Add(process);
                    break;
            }
        }
        status.Gpus.Sort((a, b) => a.Index.CompareTo(b.Index));
        return status;
    }

    /// <summary>
    /// index, name, util%, memTotal, memUsed, temp, fan, powerDraw, powerLimit
    /// — with <c>nounits</c>, so every value is a bare number or
    /// <c>[N/A]</c>.
    /// </summary>
    internal static GpuInfo? ParseGpu(string row)
    {
        var fields = row.Split(',').Select(f => f.Trim()).ToArray();
        if (fields.Length < 5 || fields[0].ToIntOrNull() is not { } index) return null;

        double? Optional(int position) =>
            position < fields.Length ? fields[position].ToDoubleOrNull() : null;

        // nvidia-smi reports memory in MiB under `nounits`.
        const long Mib = 1024 * 1024;
        return new GpuInfo(
            index,
            fields[1],
            fields[2].ToDouble(),
            fields[3].ToLong() * Mib,
            fields[4].ToLong() * Mib,
            Optional(5),
            Optional(6),
            Optional(7),
            Optional(8));
    }

    internal static GpuProcess? ParseGpuProcess(string row)
    {
        var fields = row.Split(',').Select(f => f.Trim()).ToArray();
        if (fields.Length < 3 || fields[0].ToIntOrNull() is not { } pid) return null;
        return new GpuProcess(pid, fields[1], fields[2].ToLong() * 1024 * 1024);
    }

    // MARK: - Uptime, cores, disk usage, docker

    public static long Uptime(string output)
    {
        var first = output.Fields().FirstOrDefault();
        return first is null ? 0 : (long)first.ToDouble();
    }

    public static int Cores(string output) => output.Trim().ToIntOrNull() ?? 0;

    /// <summary>
    /// Every mount from <c>df -P -B1</c>, for the storage card.
    /// </summary>
    /// <remarks>
    /// Fields are counted from the end so a long device name that wrapped onto
    /// its own line still parses, and pseudo-filesystems that slipped past the
    /// <c>-x</c> flags are dropped: they report a size but hold nothing.
    /// </remarks>
    public static List<FilesystemUsage> Filesystems(string output)
    {
        var result = new List<FilesystemUsage>();
        foreach (var line in output.Lines().Skip(1))        // drop the header
        {
            var fields = line.Fields();
            // Five fields is a row whose long device name wrapped onto the
            // previous line; six is a whole row. Counting from the end handles
            // both, which is why the device is read only when it is present.
            if (fields.Length < 5) continue;
            var mount = fields[^1];
            var total = fields[^5].ToLong();
            var used = fields[^4].ToLong();
            if (total <= 0) continue;
            var device = fields.Length >= 6 ? fields[0] : string.Empty;
            if (IsPseudoFilesystem(device, mount)) continue;
            result.Add(new FilesystemUsage(mount, device, used, total));
        }
        // Biggest first, but "/" always leads: it is the one people look for.
        return result
            .OrderByDescending(f => f.Mount == "/")
            .ThenByDescending(f => f.Total)
            .ToList();
    }

    /// <summary>
    /// Whether a <c>df</c> row is a kernel interface rather than storage.
    /// </summary>
    /// <remarks>
    /// Type alone is not enough: <c>efivarfs</c> slipped through and put the
    /// 128 KB firmware variable store on the storage card of every UEFI host,
    /// next to its real disks. Any mount under /sys, /proc or /dev is the same
    /// kind of thing whatever its type happens to be called, which also covers
    /// the next one of these rather than waiting to be surprised by it.
    /// </remarks>
    public static bool IsPseudoFilesystem(string device, string mount = "")
    {
        if (Probes.PseudoFilesystemTypes.Contains(device)) return true;
        if (device.StartsWith("/dev/loop", StringComparison.Ordinal)) return true;
        return mount.StartsWith("/sys/", StringComparison.Ordinal)
            || mount.StartsWith("/proc/", StringComparison.Ordinal)
            || mount.StartsWith("/dev/", StringComparison.Ordinal);
    }

    /// <summary>
    /// Rows of <c>ps -eo pid=,user=,pcpu=,pmem=,rss=,args=</c>.
    /// </summary>
    /// <remarks>
    /// The command is everything after the five fixed columns, kept whole so a
    /// path with spaces survives; <c>rss</c> is in kB.
    /// </remarks>
    public static List<HostProcess> Processes(string output)
    {
        var result = new List<HostProcess>();
        foreach (var line in output.Lines())
        {
            var fields = SplitAtMost(line, 6);
            if (fields.Length < 6) continue;
            if (fields[0].ToIntOrNull() is not { } pid) continue;
            result.Add(new HostProcess(
                pid,
                fields[1],
                fields[2].ToDouble(),
                fields[3].ToDouble(),
                fields[4].ToLong() * 1024,
                fields[5].Trim()));
        }
        return result;
    }

    /// <summary>
    /// Splits on whitespace into at most <paramref name="count"/> pieces, the
    /// last of which keeps its internal spaces.
    /// </summary>
    /// <remarks>
    /// <c>string.Split</c> with a count keeps leading whitespace in the
    /// remainder and, worse, counts empty runs toward the limit — so a
    /// <c>ps</c> row with its columns right-padded came back with the command
    /// truncated. Written out instead.
    /// </remarks>
    private static string[] SplitAtMost(string line, int count)
    {
        var parts = new List<string>(count);
        var index = 0;
        while (parts.Count < count - 1)
        {
            while (index < line.Length && char.IsWhiteSpace(line[index])) index++;
            if (index >= line.Length) return [.. parts];
            var start = index;
            while (index < line.Length && !char.IsWhiteSpace(line[index])) index++;
            parts.Add(line[start..index]);
        }
        while (index < line.Length && char.IsWhiteSpace(line[index])) index++;
        if (index < line.Length) parts.Add(line[index..]);
        return [.. parts];
    }

    /// <summary>
    /// The <c>key=value</c> lines emitted by the host-info section.
    /// </summary>
    /// <remarks>
    /// Keyed rather than positional so a fact the host could not answer simply
    /// goes missing instead of shifting every later field: empty lines are
    /// dropped when the output is split, so with positional parsing a host
    /// with no <c>hostname -I</c> reported its CPU model as its IP address.
    /// </remarks>
    public static HostIdentity HostIdentityOf(string output)
    {
        var fields = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var line in output.Lines())
        {
            if (line.TryKeyValue(out var key, out var value)) fields[key] = value;
        }
        return new HostIdentity
        {
            Hostname = fields.GetValueOrDefault("host", string.Empty),
            Kernel = fields.GetValueOrDefault("kern", string.Empty),
            Architecture = fields.GetValueOrDefault("arch", string.Empty),
            OsName = fields.GetValueOrDefault("os", string.Empty),
            CpuModel = fields.GetValueOrDefault("cpu", string.Empty),
            Addresses = fields.GetValueOrDefault("ips", string.Empty)
                .Fields()
                // Docker bridges' link-local addresses and loopback are noise
                // on this card.
                .Where(a => !a.StartsWith("169.254.", StringComparison.Ordinal) && a != "127.0.0.1")
                .ToList(),
        };
    }

    /// <summary>
    /// Used and total bytes for the root filesystem — what "disk" means on the
    /// dashboard and in the alert thresholds.
    /// </summary>
    /// <remarks>
    /// <c>df</c> is asked for every mount, so picking the last line would
    /// report whichever filesystem happened to sort last: on a host with a
    /// <c>/data</c> volume the headline figure silently became that volume's.
    /// </remarks>
    public static (long Used, long Total) DiskUsage(string output)
    {
        var mounts = Filesystems(output);
        var root = mounts.FirstOrDefault(m => m.Mount == "/") ?? mounts.FirstOrDefault();
        return root is null ? (0, 0) : (root.Used, root.Total);
    }

    /// <summary>
    /// A real server version is a short single token like "24.0.7". Anything
    /// else (error text, sudo noise) means docker is not usable on the host.
    /// </summary>
    public static string DockerVersion(string output)
    {
        // The section carries "version|images|running|stopped|paused" now;
        // older hosts and the Windows script may still send the bare version.
        // The split must keep an empty first field: a daemon that answered with
        // an empty version ("|14|5|3") would otherwise make the image count
        // the version.
        var pipe = output.IndexOf('|');
        var firstField = pipe < 0 ? output : output[..pipe];
        var version = firstField.Trim();
        if (version.Length == 0 || version.Length > 31 || version.Any(char.IsWhiteSpace))
        {
            return string.Empty;
        }
        return version;
    }
}
