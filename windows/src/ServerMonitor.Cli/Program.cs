using System.Text.Json;
using System.Text.Json.Serialization;
using ServerMonitor.Core;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Cli;

/// <summary>
/// <c>smctl</c> — the whole collection chain with no window.
/// </summary>
/// <remarks>
/// Exists so a real host can be polled from a terminal and the JSON diffed
/// against the Go and Swift collectors, which is how the three are kept from
/// drifting apart. It is also the fastest way to see <em>why</em> a host will
/// not connect: the error comes out on stderr with nothing between it and you.
///
/// Deliberately not shipped in the installer, and deliberately reads no saved
/// credentials — see <see cref="InMemoryCredentialStore"/>.
/// </remarks>
internal static class Program
{
    private static readonly JsonSerializerOptions Json = new()
    {
        WriteIndented = true,
        Converters = { new JsonStringEnumConverter() },
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private static async Task<int> Main(string[] args)
    {
        // R5, from this side: the console code page on a Chinese Windows is
        // GBK, so anything non-ASCII this tool prints — a host's own output, a
        // Chinese path in a `df` listing, the dashes in this help text —
        // arrives as mojibake unless the streams are told otherwise.
        try
        {
            Console.OutputEncoding = System.Text.Encoding.UTF8;
        }
        catch (IOException)
        {
            // Redirected to something that will not take a code page change.
            // The bytes are still UTF-8; only a console would have needed this.
        }

        if (args.Length == 0 || args[0] is "-h" or "--help" or "help")
        {
            Usage();
            return args.Length == 0 ? 2 : 0;
        }

        var command = args[0];
        var rest = args.Skip(1).ToArray();
        var options = Options.Parse(rest);
        if (options.Host is null)
        {
            Console.Error.WriteLine("A host or ssh config alias is required.");
            Usage();
            return 2;
        }

        await using var transport = BuildTransport(options);
        var target = BuildTarget(options);

        try
        {
            switch (command)
            {
                case "poll":
                    return await PollAsync(transport, target, options, detail: false);
                case "detail":
                    return await PollAsync(transport, target, options, detail: true);
                case "docker":
                    return await DockerAsync(transport, target);
                case "probe":
                    return await ProbeAsync(transport, target);
                default:
                    Console.Error.WriteLine($"Unknown command: {command}");
                    Usage();
                    return 2;
            }
        }
        catch (SshException error)
        {
            // The message is the whole point of this tool on a failing host,
            // so it goes to stderr plainly rather than as a stack trace.
            Console.Error.WriteLine($"{error.Kind}: {error.Message}");
            return 1;
        }
    }

    private static void Usage() => Console.Error.WriteLine(
        """
        smctl — Server Monitor collection, without the window

        Usage:
          smctl poll   <host|alias> [options]   one snapshot, as the dashboard sees it
          smctl detail <host|alias> [options]   the same plus per-core, mounts, NICs, processes
          smctl docker <host|alias> [options]   engine summary, containers, images, compose
          smctl probe  <host|alias> [options]   which OS the host reports, and how long it took

        Options:
          -u, --user <name>       login user (default: the current user, or the config's)
          -p, --port <n>          port (default 22)
          -i, --identity <path>   private key file
          --alias                 treat the host as a %USERPROFILE%\.ssh\config Host alias
          --agent                 offer the default key names in %USERPROFILE%\.ssh
          --os <auto|linux|windows>  skip detection
          --exe                   use the ssh.exe transport instead of the library
          --timeout <seconds>     per-command timeout (default 30)
          -v, --verbose           transport logging on stderr

        Credentials are never read from the Windows credential manager here, so
        password-only hosts will not authenticate; use a key, or the app.

        --agent does not talk to the OpenSSH agent: SSH.NET has no agent
        authentication, so it offers the default key files instead. A host whose
        key only lives in the agent needs --exe.
        """);

    private sealed class Options
    {
        public string? Host { get; private set; }
        public string User { get; private set; } = Environment.UserName;
        public int Port { get; private set; } = 22;
        public string? Identity { get; private set; }
        public bool Alias { get; private set; }
        public bool Agent { get; private set; }
        public OSKind Os { get; private set; } = OSKind.Auto;
        public bool UseExe { get; private set; }
        public int Timeout { get; private set; } = 30;
        public bool Verbose { get; private set; }

        public static Options Parse(string[] args)
        {
            var options = new Options();
            for (var i = 0; i < args.Length; i++)
            {
                switch (args[i])
                {
                    case "-u" or "--user" when i + 1 < args.Length: options.User = args[++i]; break;
                    case "-p" or "--port" when i + 1 < args.Length: options.Port = args[++i].ToInt(); break;
                    case "-i" or "--identity" when i + 1 < args.Length: options.Identity = args[++i]; break;
                    case "--alias": options.Alias = true; break;
                    case "--agent": options.Agent = true; break;
                    case "--exe": options.UseExe = true; break;
                    case "-v" or "--verbose": options.Verbose = true; break;
                    case "--timeout" when i + 1 < args.Length: options.Timeout = args[++i].ToInt(); break;
                    case "--os" when i + 1 < args.Length:
                        options.Os = EnumNames.ToOSKind(args[++i].ToLowerInvariant());
                        break;
                    default:
                        // The first bare word is the host; a second is a typo
                        // worth flagging rather than silently ignoring.
                        if (args[i].StartsWith('-')) { Console.Error.WriteLine($"Ignoring {args[i]}"); }
                        else if (options.Host is null) { options.Host = args[i]; }
                        else { Console.Error.WriteLine($"Ignoring extra argument {args[i]}"); }
                        break;
                }
            }
            return options;
        }
    }

    private static ISshTransport BuildTransport(Options options)
    {
        var credentials = new InMemoryCredentialStore();
        Action<string>? log = options.Verbose ? message => Console.Error.WriteLine($"· {message}") : null;
        return options.UseExe
            ? new OpenSshExeTransport(credentials, log: log)
            : new SshNetTransport(credentials, log: log);
    }

    private static SshTarget BuildTarget(Options options)
    {
        var credential = options switch
        {
            { Alias: true } => SshCredential.ConfigAlias,
            { Identity: { } path } => SshCredential.Key(SshConfig.ExpandPath(path)),
            { Agent: true } => SshCredential.Agent,
            // No flag: behave like ssh with no -i, which is the least
            // surprising thing for `smctl poll somehost`.
            _ => SshCredential.Agent,
        };
        // A stable id, derived from the destination, so two runs against the
        // same host reuse nothing accidental but also collide with nothing.
        var id = DeterministicId($"{options.User}@{options.Host}:{options.Port}");
        return new SshTarget(id, options.Host!, options.Port, options.User, credential);
    }

    /// <summary>A repeatable id from a string, so the CLI needs no store.</summary>
    private static Guid DeterministicId(string seed)
    {
        var hash = System.Security.Cryptography.SHA256.HashData(
            System.Text.Encoding.UTF8.GetBytes(seed));
        return new Guid(hash.AsSpan(0, 16));
    }

    private static async Task<int> PollAsync(
        ISshTransport transport, SshTarget target, Options options, bool detail)
    {
        var collector = new MetricsCollector(_ => transport);
        var started = System.Diagnostics.Stopwatch.StartNew();
        var snapshot = await collector.CollectAsync(target, options.Os, processes: detail);
        started.Stop();

        Console.Error.WriteLine(
            $"· collected in {started.ElapsedMilliseconds} ms via {transport.Name}");
        // Serialised per branch rather than from a ternary: Detail and Summary
        // share no base but object, so one call would bind to the
        // JsonTypeInfo overload and lose the options.
        Console.WriteLine(detail
            ? JsonSerializer.Serialize(Detail.From(snapshot), Json)
            : JsonSerializer.Serialize(Summary.From(snapshot), Json));
        return 0;
    }

    private static async Task<int> DockerAsync(ISshTransport transport, SshTarget target)
    {
        var docker = new DockerClient(transport);
        var summary = await docker.SummaryAsync(target);
        if (summary.EngineVersion.Length == 0)
        {
            Console.Error.WriteLine("No usable Docker on that host.");
            return 1;
        }
        Console.WriteLine(JsonSerializer.Serialize(new
        {
            summary,
            containers = await docker.ListContainersAsync(target),
            images = await docker.ListImagesAsync(target),
            volumes = await docker.ListVolumesAsync(target),
            networks = await docker.ListNetworksAsync(target),
            compose = await docker.ListComposeProjectsAsync(target),
            stats = await docker.StatsAsync(target),
        }, Json));
        return 0;
    }

    /// <summary>
    /// What the host says it is, and what each step cost — the first thing to
    /// run when a host behaves oddly.
    /// </summary>
    private static async Task<int> ProbeAsync(ISshTransport transport, SshTarget target)
    {
        var stopwatch = System.Diagnostics.Stopwatch.StartNew();
        var uname = await transport.RunAsync(Probes.OsDetect, target, 15);
        var first = stopwatch.ElapsedMilliseconds;
        // A second call on the same target shows whether the transport is
        // actually reusing a connection: on the library route this should be a
        // fraction of the first, and on the ssh.exe route it will not be (F1).
        stopwatch.Restart();
        await transport.RunAsync("echo ok", target, 15);
        var second = stopwatch.ElapsedMilliseconds;

        Console.WriteLine(JsonSerializer.Serialize(new
        {
            transport = transport.Name,
            uname = uname.Trim(),
            detected = MetricsCollector.OsKindFromUname(uname).ToString(),
            firstCallMs = first,
            secondCallMs = second,
            reusesConnection = second * 2 < first,
        }, Json));
        return 0;
    }

    // The JSON shapes. Explicit rather than serialising MetricSnapshot
    // directly, so the output is a documented contract the Go and Swift
    // collectors can be diffed against rather than whatever the type happens
    // to look like today.

    private sealed record Summary(
        double CpuPercent, double Load1, double Load5, double Load15,
        long MemoryUsed, long MemoryTotal, double MemoryPercent,
        long DiskUsed, long DiskTotal, double DiskPercent,
        double NetRxRate, double NetTxRate, double DiskReadRate, double DiskWriteRate,
        long NetRxTotal, long NetTxTotal,
        long UptimeSeconds, string Uptime, double LatencyMs,
        int Cores, string? DetectedOS, string DockerVersion, DockerSummary? Docker,
        HostIdentity Identity)
    {
        public static Summary From(MetricSnapshot s) => new(
            s.CpuPercent, s.Load1, s.Load5, s.Load15,
            s.MemoryUsed, s.MemoryTotal, s.MemoryPercent,
            s.DiskUsed, s.DiskTotal, s.DiskPercent,
            s.NetRxRate, s.NetTxRate, s.DiskReadRate, s.DiskWriteRate,
            s.NetRxTotal, s.NetTxTotal,
            s.UptimeSeconds, Format.Uptime(s.UptimeSeconds, chinese: false), s.LatencyMs,
            s.Cores, s.DetectedOS?.ToString(), s.DockerVersion, s.DockerSummary,
            s.Identity);
    }

    private sealed record Detail(
        Summary Snapshot,
        List<CoreLoad> CoreLoads,
        CpuBreakdown CpuBreakdown,
        MemoryBreakdown Memory,
        List<FilesystemUsage> Filesystems,
        List<NetInterface> Interfaces,
        List<HostProcess> Processes,
        GpuStatus? Gpu)
    {
        public static Detail From(MetricSnapshot s) => new(
            Summary.From(s),
            s.CoreLoads,
            s.CpuBreakdown,
            s.Memory,
            s.Filesystems,
            s.Interfaces,
            s.Processes,
            // Omitted entirely on a host with no card, matching the UI, where
            // the whole GPU card is left out rather than shown with zeroes.
            s.Gpu.IsPresent ? s.Gpu : null);
    }
}
