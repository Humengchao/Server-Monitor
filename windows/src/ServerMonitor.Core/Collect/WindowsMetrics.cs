using System.IO.Compression;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Collection for Windows hosts running OpenSSH.
/// </summary>
/// <remarks>
/// The script itself is <c>shared/probes/windows-metrics.ps1</c> (D9); this
/// class is the delivery and the parse.
/// </remarks>
public static class WindowsMetrics
{
    /// <summary>The full remote command.</summary>
    /// <remarks>
    /// Built once. The deflate and the base64 are deterministic and the script
    /// never changes at runtime, so recomputing this per poll — for every host
    /// — would be pure waste.
    /// </remarks>
    public static string Command { get; } =
        $"powershell -NoProfile -NonInteractive -EncodedCommand {Encode(Stub())}";

    /// <summary>
    /// The self-extracting wrapper actually sent to the host.
    /// </summary>
    /// <remarks>
    /// .NET's <see cref="DeflateStream"/> writes raw DEFLATE (RFC 1951), which
    /// is exactly what PowerShell's own <c>DeflateStream</c> reads — no gzip or
    /// zlib header is involved on either side. The macOS build uses
    /// <c>COMPRESSION_ZLIB</c>, which despite the name is the same raw
    /// DEFLATE, so both produce a payload the same stub can unwrap.
    /// </remarks>
    internal static string Stub()
    {
        var payload = Convert.ToBase64String(Deflate(ForDelivery(Probes.WindowsScript)));
        return $"""
            $s=[IO.Compression.DeflateStream]::new([IO.MemoryStream]::new([Convert]::FromBase64String('{payload}')),[IO.Compression.CompressionMode]::Decompress)
            iex ([IO.StreamReader]::new($s)).ReadToEnd()
            """;
    }

    /// <summary>
    /// The script with everything only a reader needs taken out.
    /// </summary>
    /// <remarks>
    /// Comments are 28% of the file, and the file has to survive a round trip
    /// through cmd.exe's ~8191-character command line: the explanations of why
    /// CIM rather than Get-Counter, or why two PerfRawData reads, are for
    /// whoever edits <c>shared/probes/windows-metrics.ps1</c> and are of no
    /// use to the host running it. Adding one sentence to that file is what
    /// pushed the command to 8122 characters and tripped the test that guards
    /// the limit.
    ///
    /// Only whole-line comments and blank lines go. A trailing comment after
    /// code is left alone, because telling a comment from a <c>#</c> inside a
    /// string needs a parser, and a wrong answer here is a script that no
    /// longer runs. <c>DeliveryDropsOnlyWholeLineComments</c> also pins that
    /// the file contains no here-string, which is the one construct where a
    /// line starting with <c>#</c> would be content rather than a comment.
    /// </remarks>
    internal static string ForDelivery(string script) =>
        string.Join(
            '\n',
            script.Split('\n')
                .Select(line => line.TrimEnd('\r'))
                .Where(line =>
                {
                    var trimmed = line.TrimStart();
                    return trimmed.Length > 0 && !trimmed.StartsWith('#');
                }));

    /// <summary>Raw DEFLATE of the script's UTF-8 bytes.</summary>
    internal static byte[] Deflate(string text)
    {
        var source = System.Text.Encoding.UTF8.GetBytes(text);
        using var output = new MemoryStream();
        // SmallestSize rather than Optimal: this runs once at startup and the
        // whole point is fitting inside cmd.exe's ~8191-character command line,
        // so a few percent matters more than the milliseconds do.
        using (var deflate = new DeflateStream(output, CompressionLevel.SmallestSize, leaveOpen: true))
        {
            deflate.Write(source, 0, source.Length);
        }
        return output.ToArray();
    }

    /// <summary>
    /// UTF-16LE base64, which is what <c>-EncodedCommand</c> takes.
    /// </summary>
    /// <remarks>
    /// Getting this wrong yields a shell error rather than a wrong number, and
    /// only on a real Windows host — hence the unit test that pins the exact
    /// bytes for a two-character input.
    /// </remarks>
    internal static string Encode(string script) =>
        Convert.ToBase64String(System.Text.Encoding.Unicode.GetBytes(script));

    /// <summary>
    /// Parses the <c>key=value</c> output. Returns null when nothing parsed,
    /// which is how a Linux host (or a failed command) is told apart from a
    /// real result.
    /// </summary>
    public static MetricSnapshot? Parse(string output)
    {
        var values = new Dictionary<string, long>(StringComparer.Ordinal);
        var dockerVersion = string.Empty;
        DockerSummary? dockerSummary = null;
        var cores = new List<CoreLoad>();
        var filesystems = new List<FilesystemUsage>();
        var interfaces = new List<NetInterface>();
        var processes = new List<HostProcess>();
        var identity = new HostIdentity();

        foreach (var rawLine in output.Lines())
        {
            var line = rawLine.Trim();
            if (!line.TryKeyValue(out var key, out var value)) continue;
            // Repeated keys carry the list-shaped detail, pipe-separated
            // because a Windows path or adapter name may contain anything else.
            var parts = value.Split('|');
            switch (key)
            {
                case "docker":
                    dockerVersion = ProcParsers.DockerVersion(value);
                    if (dockerVersion.Length > 0)
                    {
                        var parsed = DockerClient.ParseSummary(value);
                        if (parsed.EngineVersion.Length > 0) dockerSummary = parsed;
                    }
                    break;

                case "ident":
                    if (parts.Length > 0) identity.Hostname = parts[0];
                    if (parts.Length > 1) identity.Kernel = parts[1];
                    if (parts.Length > 2) identity.Architecture = parts[2];
                    if (parts.Length > 3) identity.OsName = parts[3];
                    // Windows has no user/nice/iowait/steal split to report, so
                    // the card shows the model and leaves the breakdown out.
                    if (parts.Length > 4) identity.CpuModel = parts[4];
                    break;

                case "ips":
                    identity.Addresses = value.Fields()
                        .Where(a => !a.StartsWith("169.254.", StringComparison.Ordinal) && a != "127.0.0.1")
                        .ToList();
                    break;

                case "core":
                    // Name is the core index on Windows, but can be "0,1" on
                    // multi-group machines; only a plain index is usable here.
                    if (parts.Length >= 2
                        && parts[0].ToIntOrNull() is { } index
                        && parts[1].ToDoubleOrNull() is { } percent)
                    {
                        cores.Add(new CoreLoad(index, Math.Clamp(percent, 0, 100)));
                    }
                    break;

                case "fs":
                    if (parts.Length >= 3)
                    {
                        var total = parts[1].ToLong();
                        var used = parts[2].ToLong();
                        if (total > 0) filesystems.Add(new FilesystemUsage(parts[0], parts[0], used, total));
                    }
                    break;

                case "if":
                    if (parts.Length >= 3)
                    {
                        interfaces.Add(new NetInterface(parts[0], parts[1].ToLong(), parts[2].ToLong()));
                    }
                    break;

                case "proc":
                    if (parts.Length >= 5 && parts[0].ToIntOrNull() is { } pid)
                    {
                        processes.Add(new HostProcess(
                            pid,
                            string.Empty,
                            // Get-Process reports CPU as total seconds used,
                            // not a percentage; it is shown as such rather
                            // than faked into one.
                            parts[2].ToDouble(),
                            parts[3].ToDouble(),
                            parts[4].ToLong(),
                            parts[1]));
                    }
                    break;

                default:
                    // Only whole numbers land in the scalar bag; a stray line
                    // of prose must not become a metric.
                    if (long.TryParse(value, out var number)) values[key] = number;
                    break;
            }
        }

        if (values.Count == 0) return null;

        long Value(string key) => values.GetValueOrDefault(key);

        var snapshot = new MetricSnapshot
        {
            CpuPercent = Value("cpu"),
            // TotalVisibleMemorySize and FreePhysicalMemory are in kB, unlike
            // the byte-valued disk fields next to them.
            MemoryTotal = Value("memtotal") * 1024,
            MemoryUsed = Math.Max(0, (Value("memtotal") - Value("memfree")) * 1024),
            DiskTotal = Value("disktotal"),
            DiskUsed = Math.Max(0, Value("disktotal") - Value("diskfree")),
            UptimeSeconds = Value("uptime"),
            Cores = (int)Value("cores"),
            // Windows has no load average; the processor queue length is the
            // closest equivalent and is what the web backend showed too.
            Load1 = Value("queue"),
            NetRxTotal = Value("netrx"),
            NetTxTotal = Value("nettx"),
            DockerVersion = dockerVersion,
            DockerSummary = dockerSummary,
            CoreLoads = cores.OrderBy(c => c.Index).ToList(),
            Filesystems = filesystems.OrderByDescending(f => f.Total).ToList(),
            Interfaces = interfaces.OrderByDescending(i => i.RxTotal + i.TxTotal).ToList(),
            Processes = processes,
            // The Windows script always sends its top 25; there is no cheap
            // variant to skip, so this snapshot always carries an answer.
            SampledProcesses = true,
            Identity = identity,
        };
        snapshot.Memory = new MemoryBreakdown
        {
            Total = snapshot.MemoryTotal,
            Free = Value("memfree") * 1024,
            // Windows does not expose a buffers/cache split the way /proc does,
            // so the card shows used against free and leaves those bands out.
        };
        return snapshot;
    }

    /// <summary>Cumulative counters for the rate calculation.</summary>
    public static ((long Rx, long Tx) Net, (long Read, long Written) Disk) Counters(string output)
    {
        var values = new Dictionary<string, long>(StringComparer.Ordinal);
        foreach (var rawLine in output.Lines())
        {
            if (!rawLine.Trim().TryKeyValue(out var key, out var value)) continue;
            if (long.TryParse(value, out var number)) values[key] = number;
        }
        return (
            (values.GetValueOrDefault("netrx"), values.GetValueOrDefault("nettx")),
            (values.GetValueOrDefault("diskread"), values.GetValueOrDefault("diskwrite")));
    }
}
