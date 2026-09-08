using System.Text.Json;
using ServerMonitor.Core.Collect;

namespace ServerMonitor.Core.Ssh;

/// <summary>Engine-wide counts for one host, as shown on the Docker overview cards.</summary>
/// <remarks>
/// <c>Paused</c> is carried separately because a paused container is neither
/// running nor stopped; without it they are simply missing from the card and
/// the parts stop summing to the total. It is optional so a host still
/// answering the older four-field format keeps parsing.
/// </remarks>
public sealed record DockerSummary(
    string EngineVersion,
    int Images,
    int Running,
    int Stopped,
    int Paused = 0)
{
    public int Total => Running + Stopped + Paused;
}

/// <summary>One container as reported by <c>docker ps</c>.</summary>
public sealed record DockerContainer(
    string Id, string Name, string Image, string State, string Status)
{
    public bool IsRunning => State == "running";

    /// <summary>
    /// <c>docker ps --no-trunc</c> gives a 64-character id, but
    /// <c>docker stats</c> prints the 12-character form, so the two only join
    /// on this.
    /// </summary>
    public string ShortId => Id.Length <= 12 ? Id : Id[..12];
}

/// <summary>One image as reported by <c>docker images</c>.</summary>
/// <remarks>
/// <c>Size</c> is the engine's own string ("1.24GB") rather than a byte count:
/// the format verb has no byte-count equivalent, and re-deriving one would
/// cost another call.
/// </remarks>
public sealed record DockerImage(
    string Id, string Repository, string Tag, string Size, string Created)
{
    /// <summary><c>&lt;none&gt;:&lt;none&gt;</c> is a dangling layer left by a rebuild.</summary>
    public bool IsDangling => Repository == "<none>";

    /// <summary>
    /// What the row shows: the tag, or the short id for a dangling layer.
    /// </summary>
    /// <remarks>
    /// The <c>sha256:</c> prefix is stripped before truncating. Without that,
    /// an id that arrived in the long form — which is what <c>--no-trunc</c>
    /// gives, and what a future call might use — truncates to
    /// <c>sha256:00112</c>, which identifies nothing.
    /// </remarks>
    public string DisplayName
    {
        get
        {
            if (!IsDangling) return $"{Repository}:{Tag}";
            var bare = Id.StartsWith("sha256:", StringComparison.Ordinal) ? Id[7..] : Id;
            return bare.Length <= 12 ? bare : bare[..12];
        }
    }
}

/// <summary>One volume as reported by <c>docker volume ls</c>.</summary>
public sealed record DockerVolume(string Name, string Driver, string Mountpoint);

/// <summary>One network as reported by <c>docker network ls</c>.</summary>
public sealed record DockerNetwork(string Id, string Name, string Driver, string Scope)
{
    /// <summary>The three networks the engine creates itself and that cannot be removed.</summary>
    public bool IsBuiltIn => Name is "bridge" or "host" or "none";
}

/// <summary>One Compose project as <c>docker compose ls</c> reports it.</summary>
public sealed record DockerComposeProject(string Name, string Status, string ConfigFiles)
{
    /// <summary>Counts pulled out of <see cref="Status"/>, so the row can show them separately.</summary>
    public List<(string State, int Count)> Counts
    {
        get
        {
            var result = new List<(string, int)>();
            foreach (var part in Status.Split(','))
            {
                var text = part.Trim();
                var open = text.IndexOf('(');
                if (open < 0 || !text.EndsWith(')')) continue;
                var state = text[..open];
                if (state.Length == 0) continue;
                if (text[(open + 1)..^1].ToIntOrNull() is not { } count) continue;
                result.Add((state, count));
            }
            return result;
        }
    }

    public int RunningCount => Counts.FirstOrDefault(c => c.State == "running").Count;
    public bool IsRunning => RunningCount > 0;

    /// <summary>
    /// The directory the compose file lives in — more useful in a list than
    /// the full path to a file that is always called docker-compose.yml.
    /// </summary>
    public string Directory
    {
        get
        {
            var first = ConfigFiles.Split(',').FirstOrDefault() ?? ConfigFiles;
            // The path is the *host's*, so it is POSIX even though we are on
            // Windows; Path.GetDirectoryName would look for a backslash.
            var slash = first.TrimEnd('/').LastIndexOf('/');
            return slash <= 0 ? string.Empty : first[..slash];
        }
    }
}

/// <summary>Live resource use for one container, from <c>docker stats --no-stream</c>.</summary>
/// <remarks>
/// <c>MemoryUsage</c> and the four traffic figures stay as the engine's own
/// strings ("128MiB / 2GiB", "18.5GB / 53.3GB"): docker prints SI units here
/// and re-formatting them with a 1024-based formatter would show numbers that
/// disagree with <c>docker stats</c> itself. The traffic figures are
/// cumulative since the container started.
/// </remarks>
public sealed record DockerContainerStats(
    double CpuPercent,
    double MemoryPercent,
    string MemoryUsage,
    string NetRx = "",
    string NetTx = "",
    string BlockRead = "",
    string BlockWrite = "");

public enum ContainerAction { Start, Stop, Restart }

/// <summary>
/// Docker management over the shared SSH connection.
/// </summary>
/// <remarks>
/// Every call falls back to <c>sudo docker</c> when the plain command fails,
/// which covers hosts where the login user is not in the <c>docker</c> group.
/// </remarks>
public sealed class DockerClient(ISshTransport transport)
{
    /// <summary>
    /// Field separator unlikely to appear in an image name or status string.
    /// </summary>
    /// <remarks>
    /// ASCII Unit Separator, written as a code rather than pasted in as the
    /// character itself: a raw control byte in source is invisible in every
    /// diff and editor, and one lost to a copy-paste would silently make every
    /// Docker table parse as a single column.
    /// </remarks>
    private const char FieldSeparator = (char)0x1F;

    /// <summary>
    /// Engine version and container counts in a single <c>docker info</c> call,
    /// rather than one round trip per number.
    /// </summary>
    public async Task<DockerSummary> SummaryAsync(SshTarget target, CancellationToken token = default)
    {
        var output = await RunAsync(target, $"info --format '{Probes.DockerInfoFormat}'", token: token);
        return ParseSummary(output);
    }

    public static DockerSummary ParseSummary(string output)
    {
        // Hosts print login banners on every command, so the data is the last
        // line that actually looks like data.
        var line = output.Lines().LastOrDefault(l => l.Contains('|')) ?? string.Empty;
        var fields = line.Split('|');
        if (fields.Length < 4) return new DockerSummary(string.Empty, 0, 0, 0);

        int Number(int index) => index < fields.Length ? fields[index].Trim().ToInt() : 0;

        return new DockerSummary(
            fields[0].Trim(),
            Number(1),
            Number(2),
            Number(3),
            // Optional: a host still answering the older four-field format —
            // one polled before this build — must keep parsing.
            Number(4));
    }

    public async Task<List<DockerContainer>> ListContainersAsync(
        SshTarget target, CancellationToken token = default)
    {
        var format = Join("{{.ID}}", "{{.Names}}", "{{.Image}}", "{{.State}}", "{{.Status}}");
        var output = await RunAsync(target, $"ps -a --no-trunc --format '{format}'", token: token);
        return Rows(output, 5)
            .Select(f => new DockerContainer(f[0], f[1], f[2], f[3], f[4]))
            .ToList();
    }

    public async Task<List<DockerImage>> ListImagesAsync(
        SshTarget target, CancellationToken token = default)
    {
        var format = Join("{{.ID}}", "{{.Repository}}", "{{.Tag}}", "{{.Size}}", "{{.CreatedSince}}");
        var output = await RunAsync(target, $"images --format '{format}'", token: token);
        return Rows(output, 5)
            .Select(f => new DockerImage(f[0], f[1], f[2], f[3], f[4]))
            .ToList();
    }

    public async Task<List<DockerVolume>> ListVolumesAsync(
        SshTarget target, CancellationToken token = default)
    {
        var format = Join("{{.Name}}", "{{.Driver}}", "{{.Mountpoint}}");
        var output = await RunAsync(target, $"volume ls --format '{format}'", token: token);
        return Rows(output, 3)
            .Where(f => f[0].Length > 0)
            .Select(f => new DockerVolume(f[0], f[1], f[2]))
            .ToList();
    }

    public async Task<List<DockerNetwork>> ListNetworksAsync(
        SshTarget target, CancellationToken token = default)
    {
        var format = Join("{{.ID}}", "{{.Name}}", "{{.Driver}}", "{{.Scope}}");
        var output = await RunAsync(target, $"network ls --format '{format}'", token: token);
        return Rows(output, 4)
            .Where(f => f[1].Length > 0)
            .Select(f => new DockerNetwork(f[0], f[1], f[2], f[3]))
            .ToList();
    }

    /// <summary>
    /// Compose projects on the host, stopped ones included.
    /// </summary>
    /// <remarks>
    /// Returns nothing rather than throwing when the engine has no
    /// <c>compose</c> subcommand — Compose v1 was a separate
    /// <c>docker-compose</c> binary with no <c>ls</c> at all, and a host
    /// running it should show an empty tab, not an error.
    /// </remarks>
    public async Task<List<DockerComposeProject>> ListComposeProjectsAsync(
        SshTarget target, CancellationToken token = default)
    {
        try
        {
            var output = await RunAsync(target, "compose ls -a --format json", token: token);
            return ParseComposeProjects(output);
        }
        catch (SshException)
        {
            return [];
        }
    }

    public static List<DockerComposeProject> ParseComposeProjects(string output)
    {
        var rows = FirstJsonArray(output);
        if (rows is null) return [];
        var result = new List<DockerComposeProject>();
        foreach (var row in rows)
        {
            if (!row.TryGetProperty("Name", out var nameNode)) continue;
            var name = nameNode.GetString();
            if (string.IsNullOrEmpty(name)) continue;
            result.Add(new DockerComposeProject(
                name,
                row.TryGetProperty("Status", out var s) ? s.GetString() ?? "" : "",
                row.TryGetProperty("ConfigFiles", out var c) ? c.GetString() ?? "" : ""));
        }
        return result.OrderBy(p => p.Name, StringComparer.Ordinal).ToList();
    }

    /// <summary>
    /// The first substring that actually parses as a JSON array of objects.
    /// </summary>
    /// <remarks>
    /// The engine may print a deprecation warning above the array, and that
    /// warning contains a bracket of its own ("WARN[0000] …") — so the first
    /// <c>[</c> in the output is not necessarily where the JSON starts. Try
    /// each candidate rather than guessing.
    /// </remarks>
    internal static List<JsonElement>? FirstJsonArray(string output)
    {
        var end = output.LastIndexOf(']');
        if (end < 0) return null;
        var cursor = 0;
        while (true)
        {
            var start = output.IndexOf('[', cursor);
            if (start < 0 || start >= end) return null;
            try
            {
                using var document = JsonDocument.Parse(output[start..(end + 1)]);
                if (document.RootElement.ValueKind == JsonValueKind.Array)
                {
                    return document.RootElement.EnumerateArray()
                        .Where(e => e.ValueKind == JsonValueKind.Object)
                        .Select(e => e.Clone())
                        .ToList();
                }
            }
            catch (JsonException)
            {
                // Not the array — the next bracket might be.
            }
            cursor = start + 1;
        }
    }

    /// <summary>
    /// Live stats for every running container, keyed by container id.
    /// </summary>
    /// <remarks>
    /// <c>--no-stream</c> because the streaming form never exits, and this
    /// runs over a call that reads to EOF.
    /// </remarks>
    public async Task<Dictionary<string, DockerContainerStats>> StatsAsync(
        SshTarget target, CancellationToken token = default)
    {
        var format = Join(
            "{{.ID}}", "{{.CPUPerc}}", "{{.MemPerc}}", "{{.MemUsage}}",
            "{{.NetIO}}", "{{.BlockIO}}");
        var output = await RunAsync(target, $"stats --no-stream --format '{format}'", token: token);
        return ParseStats(output);
    }

    public static Dictionary<string, DockerContainerStats> ParseStats(string output)
    {
        var result = new Dictionary<string, DockerContainerStats>(StringComparer.Ordinal);
        foreach (var line in output.Lines())
        {
            var fields = line.Split(FieldSeparator);
            if (fields.Length < 4 || fields[0].Length == 0) continue;
            var net = Pair(fields.Length > 4 ? fields[4] : string.Empty);
            var block = Pair(fields.Length > 5 ? fields[5] : string.Empty);
            result[fields[0]] = new DockerContainerStats(
                Percent(fields[1]),
                Percent(fields[2]),
                fields[3].Trim(),
                net.Item1, net.Item2,
                block.Item1, block.Item2);
        }
        return result;
    }

    /// <summary>
    /// Splits the engine's "18.5GB / 53.3GB" into its two halves.
    /// </summary>
    /// <remarks>
    /// Kept as the engine's own strings rather than reparsed into bytes:
    /// docker prints SI units here and re-formatting them with a 1024-based
    /// formatter would show numbers that disagree with <c>docker stats</c>
    /// itself.
    /// </remarks>
    internal static (string, string) Pair(string text)
    {
        var halves = text.Split('/');
        return halves.Length >= 2 ? (halves[0].Trim(), halves[1].Trim()) : ("", "");
    }

    /// <summary>
    /// "12.34%" -> 12.34. The engine prints "--" for a container it could not
    /// sample, which becomes 0 rather than a parse failure.
    /// </summary>
    internal static double Percent(string text) => text.Trim().TrimEnd('%').ToDouble();

    public async Task PerformAsync(
        ContainerAction action, string containerId, SshTarget target, CancellationToken token = default)
    {
        var verb = action switch
        {
            ContainerAction.Start => "start",
            ContainerAction.Stop => "stop",
            _ => "restart",
        };
        await RunAsync(target, $"{verb} {containerId.ShellQuote()}", token: token);
    }

    public Task<string> LogsAsync(
        string containerId, SshTarget target, int tail = 200, CancellationToken token = default) =>
        // stderr is where most container logs land, so fold it into stdout.
        RunAsync(target, $"logs --tail {tail} {containerId.ShellQuote()} 2>&1", token: token);

    /// <summary>
    /// The command that opens a shell inside a container, for the terminal.
    /// </summary>
    /// <remarks>
    /// Tries bash then falls back to sh: an alpine image has no bash, and
    /// "OCI runtime exec failed" is a confusing way to learn that.
    /// </remarks>
    public static string ExecShellCommand(string containerId)
    {
        var quoted = containerId.ShellQuote();
        return $"docker exec -it {quoted} sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh' "
            + $"|| sudo -n docker exec -it {quoted} sh";
    }

    private static string Join(params string[] verbs) => string.Join(FieldSeparator, verbs);

    private static IEnumerable<string[]> Rows(string output, int minimum) =>
        output.Lines()
            .Select(line => line.Split(FieldSeparator))
            .Where(fields => fields.Length >= minimum);

    private Task<string> RunAsync(SshTarget target, string arguments, CancellationToken token)
    {
        // -n so sudo fails immediately rather than blocking on a password
        // prompt that nothing is there to answer.
        var command = $"docker {arguments} 2>/dev/null || sudo -n docker {arguments}";
        return transport.RunAsync(command, target, cancellationToken: token);
    }
}
