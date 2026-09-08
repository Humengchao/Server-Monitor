using System.Reflection;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// The shared probe scripts, read from the embedded copies of
/// <c>shared/probes/</c> (D9).
/// </summary>
/// <remarks>
/// Embedded rather than loose files so a published single-file exe carries
/// them with nothing to lose. Read once into static fields: the metrics
/// command is built on every poll for every host, and re-reading a manifest
/// stream each time would be the only I/O on that path.
/// </remarks>
public static class Probes
{
    private static readonly Assembly Owner = typeof(Probes).Assembly;

    /// <summary>The raw Linux batch, one section per line, before substitution.</summary>
    private static readonly string[] LinuxSections = ReadSectionLines("linux-metrics.txt");

    /// <summary>The keyed one-liners from <c>probes.txt</c>.</summary>
    private static readonly Dictionary<string, string> Keyed = ReadKeyed("probes.txt");

    /// <summary>The Windows PowerShell collection, verbatim.</summary>
    public static string WindowsScript { get; } = Read("windows-metrics.ps1");

    public static string Processes => Keyed["processes"];
    public static string DockerInfo => Keyed["docker-info"];
    public static string DockerInfoFormat => Keyed["docker-info-format"];
    public static string OsDetect => Keyed["os-detect"];
    public static string CpuModelFallback => Keyed["cpu-model-fallback"];
    public static string Vnstat => Keyed["vnstat"];
    public static string VnstatInstallProbe => Keyed["vnstat-install-probe"];

    /// <summary>
    /// Filesystem types that are kernel interfaces rather than storage.
    /// </summary>
    /// <remarks>
    /// One list, two uses: it builds <c>df</c>'s <c>-x</c> flags, and it is the
    /// check applied to whatever comes back — because the fallback <c>df</c> on
    /// a host whose <c>df</c> lacks <c>-x</c> returns everything, so the local
    /// check has to be complete on its own.
    /// </remarks>
    public static IReadOnlyList<string> PseudoFilesystemTypes { get; } =
        Keyed["pseudo-filesystem-types"].Fields();

    /// <summary>The <c>-x tmpfs -x devtmpfs …</c> flags for the df line.</summary>
    public static string DfExclusions { get; } =
        string.Join(" ", PseudoFilesystemTypes.Select(t => $"-x {t}"));

    /// <summary>
    /// How many sections the Linux batch has — and therefore how many the
    /// splitter must expect. Derived from the file rather than restated, so
    /// adding a command cannot silently shift every later parser.
    /// </summary>
    public static int LinuxSectionCount => LinuxSections.Length;

    /// <summary>
    /// The Linux batch, with the two expensive sections included or replaced
    /// by <c>true</c>.
    /// </summary>
    /// <remarks>
    /// Measured on a 16-core host: the whole script costs it ~142 ms of CPU
    /// per poll, of which <c>docker info</c> is 89 ms (a Go CLI starting up;
    /// the daemon's share is 7 ms) and <c>ps</c> over every process is 30 ms.
    /// Everything else together is ~23 ms. Every five seconds, on every host,
    /// that is the monitor's whole footprint on the machines it watches — so
    /// the two are asked for only when something will show them. A skipped
    /// section still emits its separator, so the section count never moves.
    /// </remarks>
    public static string LinuxMetricsCommand(bool processes = true, bool docker = true)
    {
        var sections = LinuxSections.Select(line => line
            .Replace("%PROCESSES%", processes ? Processes : "true")
            .Replace("%DOCKER%", docker ? DockerInfo : "true")
            .Replace("%DF_EXCLUDE%", DfExclusions));
        return string.Join($"; echo {ProcParsers.SectionSeparator}; ", sections);
    }

    private static string Read(string name)
    {
        // LinkBase="Probes" in the csproj puts the files under that prefix;
        // the manifest name is <RootNamespace>.<LinkBase>.<file>.
        var resource = $"ServerMonitor.Core.Probes.{name}";
        using var stream = Owner.GetManifestResourceStream(resource)
            ?? throw new InvalidOperationException(
                $"Embedded probe '{resource}' is missing. Available: " +
                string.Join(", ", Owner.GetManifestResourceNames()));
        using var reader = new StreamReader(stream);
        return reader.ReadToEnd();
    }

    /// <summary>
    /// The section lines of the Linux batch: everything that is not blank and
    /// not a comment. Comments deliberately do not count as sections, so a
    /// note can be added between two commands without renumbering anything.
    /// </summary>
    private static string[] ReadSectionLines(string name) =>
        Read(name).Lines()
            .Select(l => l.Trim())
            .Where(l => l.Length > 0 && !l.StartsWith('#'))
            .ToArray();

    private static Dictionary<string, string> ReadKeyed(string name)
    {
        var result = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var raw in Read(name).Lines())
        {
            var line = raw.Trim();
            if (line.Length == 0 || line.StartsWith('#')) continue;
            var split = line.IndexOf('=');
            if (split < 0) continue;
            result[line[..split].Trim()] = line[(split + 1)..].Trim();
        }
        return result;
    }
}
