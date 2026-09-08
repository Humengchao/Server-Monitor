using System.Text.Json;
using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Traffic in one vnStat bucket — a five-minute slot, an hour, a day, a month
/// or a year, depending on the series it came from.
/// </summary>
public sealed record TrafficBucket(DateTime Date, long Rx, long Tx)
{
    public long Total => Rx + Tx;
}

/// <summary>The series vnStat keeps, in the order the picker shows them.</summary>
public enum TrafficGranularity { FiveMinute, Hour, Day, Month, Year }

public static class TrafficGranularityInfo
{
    /// <summary>
    /// Key inside <c>traffic</c> in vnStat's JSON version 2. Version 1 used
    /// plural keys and had no five-minute series.
    /// </summary>
    public static string JsonKey(this TrafficGranularity granularity) => granularity switch
    {
        TrafficGranularity.FiveMinute => "fiveminute",
        TrafficGranularity.Hour => "hour",
        TrafficGranularity.Day => "day",
        TrafficGranularity.Month => "month",
        _ => "year",
    };

    /// <summary>
    /// How many recent buckets the chart shows. vnStat keeps 288 five-minute
    /// slots; two hours of them is what fits and what anyone looks at.
    /// </summary>
    public static int ShownCount(this TrafficGranularity granularity) => granularity switch
    {
        TrafficGranularity.FiveMinute => 24,
        TrafficGranularity.Hour => 24,
        TrafficGranularity.Day => 30,
        TrafficGranularity.Month => 12,
        _ => 10,
    };
}

public sealed class TrafficInterface
{
    public required string Name { get; init; }
    public string Alias { get; init; } = string.Empty;
    public long TotalRx { get; init; }
    public long TotalTx { get; init; }
    public Dictionary<TrafficGranularity, List<TrafficBucket>> Series { get; init; } = [];

    public string DisplayName => Alias.Length == 0 ? Name : $"{Alias} · {Name}";

    /// <summary>Oldest first, so the chart reads left to right in time.</summary>
    public List<TrafficBucket> Buckets(TrafficGranularity granularity) =>
        Series.TryGetValue(granularity, out var buckets)
            ? buckets.OrderBy(b => b.Date).ToList()
            : [];

    public bool HasData => Series.Values.Any(s => s.Count > 0);
}

public sealed class TrafficReport
{
    public string VnstatVersion { get; set; } = string.Empty;
    public List<TrafficInterface> Interfaces { get; set; } = [];

    /// <summary>
    /// True right after installation: the daemon exists but has recorded
    /// nothing yet, which the card shows as "collecting" rather than as a bare
    /// chart with no bars.
    /// </summary>
    public bool IsCollecting => !Interfaces.Any(i => i.HasData);

    /// <summary>
    /// The interface people mean when they say "the server's traffic": a real
    /// link (not a container bridge or a tunnel) with the most bytes through
    /// it.
    /// </summary>
    public TrafficInterface? PrimaryInterface
    {
        get
        {
            var real = Interfaces.Where(i => !NetInterface.IsVirtualName(i.Name)).ToList();
            var pool = real.Count > 0 ? real : Interfaces;
            return pool.OrderByDescending(i => i.TotalRx + i.TotalTx).FirstOrDefault();
        }
    }
}

public enum VnstatOutcomeKind { NotInstalled, Report }

public sealed record VnstatOutcome(VnstatOutcomeKind Kind, TrafficReport? Report);

/// <summary>
/// Reads <c>vnstat --json</c>.
/// </summary>
/// <remarks>
/// Worth leaning on: the kernel counters a poll reads reset at every reboot,
/// while vnStat's database persists — it is the only way to answer "how much
/// did this box move last month".
/// </remarks>
public static class VnstatParser
{
    public static string Command => Probes.Vnstat;

    /// <summary>
    /// Null when the output is neither the marker nor parseable JSON.
    /// </summary>
    /// <remarks>
    /// <c>SM_NO_VNSTAT</c> is a distinct outcome rather than an error, because
    /// most hosts have no vnStat and the card explains how to install it
    /// instead of reporting a failure.
    /// </remarks>
    public static VnstatOutcome? Parse(string output)
    {
        if (output.Contains("SM_NO_VNSTAT", StringComparison.Ordinal))
        {
            return new VnstatOutcome(VnstatOutcomeKind.NotInstalled, null);
        }

        var start = output.IndexOf('{');
        var end = output.LastIndexOf('}');
        if (start < 0 || end <= start) return null;

        JsonDocument document;
        try
        {
            document = JsonDocument.Parse(output[start..(end + 1)]);
        }
        catch (JsonException)
        {
            return null;
        }
        using (document)
        {
            var root = document.RootElement;
            var report = new TrafficReport
            {
                VnstatVersion = String(root, "vnstatversion"),
            };
            // JSON version 1 (vnStat 1.x) reported KiB; version 2 reports bytes.
            var version = String(root, "jsonversion");
            long scale = version == "1" ? 1024 : 1;

            if (root.TryGetProperty("interfaces", out var interfaces)
                && interfaces.ValueKind == JsonValueKind.Array)
            {
                foreach (var entry in interfaces.EnumerateArray())
                {
                    // 1.x named the field "id", 2.x "name".
                    var name = String(entry, "name");
                    if (name.Length == 0) name = String(entry, "id");
                    if (name.Length == 0) continue;

                    var traffic = Object(entry, "traffic");
                    var total = Object(traffic, "total");
                    var series = new Dictionary<TrafficGranularity, List<TrafficBucket>>();
                    foreach (var granularity in Enum.GetValues<TrafficGranularity>())
                    {
                        var key = granularity.JsonKey();
                        // Version 1 pluralised the keys ("hours", "days").
                        var rows = Array(traffic, key);
                        if (rows is null) rows = Array(traffic, key + "s");
                        var buckets = new List<TrafficBucket>();
                        if (rows is not null)
                        {
                            foreach (var row in rows.Value.EnumerateArray())
                            {
                                if (DateOf(row, granularity) is not { } date) continue;
                                buckets.Add(new TrafficBucket(
                                    date, Number(row, "rx") * scale, Number(row, "tx") * scale));
                            }
                        }
                        series[granularity] = buckets;
                    }

                    report.Interfaces.Add(new TrafficInterface
                    {
                        Name = name,
                        Alias = String(entry, "alias"),
                        TotalRx = Number(total, "rx") * scale,
                        TotalTx = Number(total, "tx") * scale,
                        Series = series,
                    });
                }
            }
            return new VnstatOutcome(VnstatOutcomeKind.Report, report);
        }
    }

    /// <summary>
    /// vnStat writes dates as the host's local calendar components with no
    /// zone. They are rebuilt as UTC and the chart labels them in UTC too, so
    /// the axis shows exactly the clock the host recorded — "14:00" on the
    /// server is "14:00" on the chart, whatever this machine's zone is.
    /// </summary>
    internal static DateTime? DateOf(JsonElement row, TrafficGranularity granularity)
    {
        var date = Object(row, "date");
        var time = Object(row, "time");
        var year = (int)Number(date, "year");
        if (year <= 1970) return null;
        var month = date.ValueKind == JsonValueKind.Object && date.TryGetProperty("month", out _)
            ? (int)Number(date, "month") : 1;
        var day = date.ValueKind == JsonValueKind.Object && date.TryGetProperty("day", out _)
            ? (int)Number(date, "day") : 1;
        var hour = 0;
        var minute = 0;
        if (granularity is TrafficGranularity.FiveMinute or TrafficGranularity.Hour)
        {
            // Version 1 hourly rows carry the hour as `id` and have no `time`.
            hour = time.ValueKind == JsonValueKind.Object && time.TryGetProperty("hour", out _)
                ? (int)Number(time, "hour")
                : (int)Number(row, "id");
            minute = (int)Number(time, "minute");
        }
        try
        {
            return new DateTime(
                year,
                Math.Clamp(month, 1, 12),
                Math.Clamp(day, 1, 28) == day ? day : Math.Clamp(day, 1, 31),
                Math.Clamp(hour, 0, 23),
                Math.Clamp(minute, 0, 59),
                0,
                DateTimeKind.Utc);
        }
        catch (ArgumentOutOfRangeException)
        {
            // A day out of range for the month (a corrupt database, a leap-day
            // edge) drops the bucket rather than the whole report.
            return null;
        }
    }

    private static string String(JsonElement element, string name) =>
        element.ValueKind == JsonValueKind.Object
            && element.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.String
            ? value.GetString() ?? string.Empty
            : string.Empty;

    private static JsonElement Object(JsonElement element, string name) =>
        element.ValueKind == JsonValueKind.Object && element.TryGetProperty(name, out var value)
            ? value
            : default;

    private static JsonElement? Array(JsonElement element, string name) =>
        element.ValueKind == JsonValueKind.Object
            && element.TryGetProperty(name, out var value)
            && value.ValueKind == JsonValueKind.Array
            ? value
            : null;

    /// <summary>
    /// A number that may arrive as a JSON number or as a quoted string —
    /// vnStat 1.x quotes some of them.
    /// </summary>
    internal static long Number(JsonElement element, string name)
    {
        if (element.ValueKind != JsonValueKind.Object) return 0;
        if (!element.TryGetProperty(name, out var value)) return 0;
        return value.ValueKind switch
        {
            JsonValueKind.Number => value.TryGetInt64(out var number) ? number : (long)value.GetDouble(),
            JsonValueKind.String => value.GetString().ToLong(),
            _ => 0,
        };
    }
}

/// <summary>
/// Installs vnStat on a Linux host over the connection the app already has.
/// </summary>
/// <remarks>
/// Two round trips on purpose. The first only asks which package manager the
/// host has and whether we are root; the exact command that would run is then
/// shown to the user, and nothing is installed until they confirm it. Changing
/// software on somebody's server is a thing they should see happen.
/// </remarks>
public static class VnstatInstaller
{
    public enum PackageManager { Apt, Dnf, Yum, Apk, Pacman, Zypper }

    internal static string Binary(this PackageManager manager) => manager switch
    {
        PackageManager.Apt => "apt-get",
        PackageManager.Dnf => "dnf",
        PackageManager.Yum => "yum",
        PackageManager.Apk => "apk",
        PackageManager.Pacman => "pacman",
        _ => "zypper",
    };

    internal static PackageManager? FromBinary(string name) => name switch
    {
        "apt-get" => PackageManager.Apt,
        "dnf" => PackageManager.Dnf,
        "yum" => PackageManager.Yum,
        "apk" => PackageManager.Apk,
        "pacman" => PackageManager.Pacman,
        "zypper" => PackageManager.Zypper,
        _ => null,
    };

    /// <summary>
    /// The install line as root. <c>-y</c>/<c>--noconfirm</c> everywhere:
    /// there is nobody at a terminal to answer a prompt.
    /// </summary>
    internal static string InstallLine(this PackageManager manager) => manager switch
    {
        PackageManager.Apt =>
            "DEBIAN_FRONTEND=noninteractive apt-get update -qq && "
            + "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq vnstat",
        PackageManager.Dnf => "dnf install -y vnstat",
        // vnStat lives in EPEL on RHEL/CentOS; enabling it is harmless where
        // it already is.
        PackageManager.Yum => "yum install -y epel-release; yum install -y vnstat",
        PackageManager.Apk => "apk add vnstat",
        PackageManager.Pacman => "pacman -Sy --noconfirm vnstat",
        _ => "zypper -n install vnstat",
    };

    /// <summary>What the confirmation shows and what then runs.</summary>
    public sealed record Plan(PackageManager Manager, bool AsRoot)
    {
        /// <summary>Human-readable: the line a person would type.</summary>
        public string DisplayCommand => AsRoot
            ? Manager.InstallLine()
            : "sudo " + Manager.InstallLine().Replace(" && ", " && sudo ");
    }

    /// <summary>Finds the package manager and whether we are root. Read-only.</summary>
    public static string ProbeCommand => Probes.VnstatInstallProbe;

    /// <summary>Null when no supported package manager answered.</summary>
    public static Plan? PlanFromProbe(string output)
    {
        var lines = output.Lines().Select(l => l.Trim()).ToList();
        var end = lines.IndexOf("SM_PROBE_END");
        if (end < 0) return null;
        var manager = lines.Take(end).Select(FromBinary).FirstOrDefault(m => m is not null);
        if (manager is null) return null;
        var uid = end + 1 < lines.Count ? lines[end + 1] : string.Empty;
        return new Plan(manager.Value, uid == "0");
    }

    /// <summary>
    /// The script that runs after confirmation.
    /// </summary>
    /// <remarks>
    /// Everything is folded into stdout and the exit code is always 0, so the
    /// result is read from the markers rather than from a thrown error with
    /// the output lost.
    /// </remarks>
    public static string RemoteScript(Plan plan)
    {
        // `sudo -n`: fail at once rather than wait on a password prompt nothing
        // will answer. `env` because the apt line sets a variable.
        var sudo = plan.AsRoot ? string.Empty : "sudo -n env ";
        var install = string.Join(
            " && ",
            plan.Manager.InstallLine().Split(" && ").Select(part => sudo + part))
            .Replace("; yum", $"; {sudo}yum");
        var systemd = $"command -v systemctl >/dev/null 2>&1 && {sudo}systemctl enable --now vnstat >/dev/null 2>&1";
        var openrc = $"command -v rc-update >/dev/null 2>&1 && {sudo}rc-update add vnstatd default >/dev/null 2>&1 && {sudo}rc-service vnstatd start >/dev/null 2>&1";
        return $"{{ {install}; }} 2>&1; rc=$?; "
            + $"{{ {systemd} || {openrc} || true; }} 2>&1; "
            + "if command -v vnstat >/dev/null 2>&1; then echo SM_INSTALLED; else echo SM_FAILED rc=$rc; fi";
    }

    public enum OutcomeKind
    {
        Installed,
        /// <summary><c>sudo -n</c> refused: the user needs a password we cannot type.</summary>
        NeedsSudoPassword,
        Failed,
    }

    public sealed record Outcome(OutcomeKind Kind, string Detail = "");

    public static Outcome OutcomeFrom(string output)
    {
        if (output.Contains("SM_INSTALLED", StringComparison.Ordinal))
        {
            return new Outcome(OutcomeKind.Installed);
        }
        if (output.Contains("sudo: a password is required", StringComparison.Ordinal)
            || output.Contains("sudo: a terminal is required", StringComparison.Ordinal)
            || output.Contains("sudo: command not found", StringComparison.Ordinal))
        {
            return new Outcome(OutcomeKind.NeedsSudoPassword);
        }
        // The last few lines are where a package manager says what went wrong.
        var tail = string.Join("\n", output.Lines().TakeLast(4));
        return new Outcome(OutcomeKind.Failed, tail.Length == 0 ? "no output" : tail);
    }
}
