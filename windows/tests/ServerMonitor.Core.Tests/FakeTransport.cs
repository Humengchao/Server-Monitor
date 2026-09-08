using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// A transport that answers from a script rather than a network.
/// </summary>
/// <remarks>
/// The tick barrier and the backoff are about <em>ordering</em> — which poll
/// finished when, and what got published as a result — so the tests need
/// control over when a call returns, not a real host. Each entry can delay,
/// throw, or answer.
/// </remarks>
internal sealed class FakeTransport : ISshTransport
{
    public string Name => "fake";

    /// <summary>Per-host behaviour, keyed by server id.</summary>
    public Dictionary<Guid, Func<string, Task<string>>> Handlers { get; } = [];

    /// <summary>Used for a host with no handler.</summary>
    public Func<string, Task<string>> Default { get; set; } =
        _ => Task.FromException<string>(SshException.Failed("no handler"));

    public List<(Guid ServerId, string Command)> Calls { get; } = [];
    public List<Guid> Disconnected { get; } = [];

    public Task<string> RunAsync(
        string command, SshTarget target, int timeoutSeconds = 30,
        CancellationToken cancellationToken = default)
    {
        lock (Calls) Calls.Add((target.ServerId, command));
        var handler = Handlers.TryGetValue(target.ServerId, out var h) ? h : Default;
        return handler(command);
    }

    public Task DisconnectAsync(SshTarget target)
    {
        lock (Disconnected) Disconnected.Add(target.ServerId);
        return Task.CompletedTask;
    }

    public ValueTask DisposeAsync() => ValueTask.CompletedTask;

    /// <summary>
    /// A handler that answers a Linux collection with plausible numbers.
    /// </summary>
    /// <remarks>
    /// Built from the shared fixtures, so a change to the section order breaks
    /// these tests too rather than only the parser ones.
    /// </remarks>
    public static Func<string, Task<string>> LinuxHost(
        Func<Task>? beforeAnswering = null, double cpu = 12)
    {
        return async command =>
        {
            if (beforeAnswering is not null) await beforeAnswering();
            if (command == Probes.OsDetect) return "Linux\n";
            if (command.Contains("lscpu", StringComparison.Ordinal)) return "Neoverse-N1";
            return LinuxBatch(cpu);
        };
    }

    /// <summary>One collection round's worth of output, in section order.</summary>
    public static string LinuxBatch(double cpu = 12)
    {
        // A /proc/stat pair whose delta gives the requested busy percentage:
        // 1000 jiffies elapse, `cpu` of them busy.
        var busy = (long)Math.Round(cpu * 10);
        var idle = 1000 - busy;
        var first = "cpu  0 0 0 0 0 0 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0 0 0";
        var second = $"cpu  {busy} 0 0 {idle} 0 0 0 0 0 0\ncpu0 {busy} 0 0 {idle} 0 0 0 0 0 0";

        var sections = new[]
        {
            "1735000000000000000",                       // startClock
            first,
            second,
            Fixture.Read("linux/proc-meminfo.txt"),
            "0.52 0.31 0.14 1/523 12345",
            Fixture.Read("linux/proc-net-dev.txt"),
            Fixture.Read("linux/proc-diskstats.txt"),
            "123456.78 987654.32",
            "8",
            Fixture.Read("linux/df-multi-mount.txt"),
            Fixture.Read("linux/ps.txt"),
            Fixture.Read("linux/host-info.txt"),
            "",                                          // no GPU
            "24.0.7|14|5|3|0",
            "1735000000100000000",                       // endClock
        };
        return string.Join($"\n{ProcParsers.SectionSeparator}\n", sections);
    }
}

/// <summary>
/// Runs everything the service dispatches on the calling thread, in order.
/// </summary>
/// <remarks>
/// Stands in for the WPF dispatcher. The service's contract is that published
/// state is only touched on one thread; the tests hold that thread and pump
/// the queue themselves, so a tick's ordering is deterministic rather than
/// racing a real dispatcher.
/// </remarks>
internal sealed class TestPump
{
    private readonly Queue<Action> _queue = new();
    private readonly Lock _gate = new();

    public void Post(Action action)
    {
        lock (_gate) _queue.Enqueue(action);
    }

    /// <summary>Runs everything queued, including what running it queues.</summary>
    public void Drain()
    {
        while (true)
        {
            Action next;
            lock (_gate)
            {
                if (_queue.Count == 0) return;
                next = _queue.Dequeue();
            }
            next();
        }
    }

    /// <summary>
    /// Pumps until <paramref name="condition"/> holds or the timeout elapses.
    /// </summary>
    /// <remarks>
    /// The polls themselves run on the thread pool, so their completions
    /// arrive asynchronously; this keeps draining while waiting for them
    /// rather than blocking the queue they post to.
    /// </remarks>
    public async Task<bool> PumpUntilAsync(Func<bool> condition, int timeoutMs = 5000)
    {
        var deadline = DateTime.UtcNow.AddMilliseconds(timeoutMs);
        while (DateTime.UtcNow < deadline)
        {
            Drain();
            if (condition())
            {
                // Drain once more before returning. A poll's last act is to
                // dispatch its commit, so a condition that becomes true
                // (the task finished) can be observed with that commit still
                // sitting in the queue — the assertion would then read state
                // the service was about to publish.
                await Task.Delay(10);
                Drain();
                return true;
            }
            await Task.Delay(10);
        }
        Drain();
        return condition();
    }
}

/// <summary>Shared setup for the service-level tests.</summary>
internal sealed class Harness
{
    public Database Database { get; }
    public AppSettings Settings { get; } = new();
    public FakeTransport Transport { get; } = new();
    public TestPump Pump { get; } = new();
    public MonitorService Service { get; }
    public List<(Guid ServerId, string Title, string Body)> Alerts { get; } = [];

    public Harness(bool withAlerts = false)
    {
        Database = Database.InMemory();
        Settings.PollInterval = 5;
        Service = new MonitorService(
            Database,
            Settings,
            new InMemoryCredentialStore(),
            _ => Transport,
            Pump.Post,
            latency: new NoLatencyProbe());
        if (withAlerts)
        {
            Service.Alerts = new Alerts.AlertService(
                Settings,
                (id, title, body) => Alerts.Add((id, title, body)));
        }
    }

    public Server AddServer(string name)
    {
        var server = new Server
        {
            Name = name,
            Host = $"10.0.0.{Database.AllServers().Count + 1}",
            Username = "root",
            AuthKind = AuthKind.Agent,
            OsKind = OSKind.Linux,
            SortIndex = Database.AllServers().Count,
        };
        Service.AddServer(server);
        Pump.Drain();
        return Service.Server(server.Id)!;
    }

    public int PublishCount { get; private set; }

    public void CountPublishes() => Service.Published += () => PublishCount++;
}

/// <summary>
/// Pins the UI language for the duration of a test, then restores it.
/// </summary>
/// <remarks>
/// <see cref="ServerMonitor.Core.L10n.Strings"/> resolves "system" from the
/// machine's UI culture, so a test asserting on English text fails on a
/// Chinese Windows — which is exactly the machine this is developed on.
/// </remarks>
internal sealed class LanguageScope : IDisposable
{
    private readonly AppLanguage _previous;

    public LanguageScope(AppLanguage language)
    {
        _previous = ServerMonitor.Core.L10n.Strings.Language;
        ServerMonitor.Core.L10n.Strings.Language = language;
    }

    public void Dispose() => ServerMonitor.Core.L10n.Strings.Language = _previous;
}
