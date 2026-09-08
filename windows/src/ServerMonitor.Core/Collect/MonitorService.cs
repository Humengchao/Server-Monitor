using System.Collections.ObjectModel;
using System.ComponentModel;
using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Owns the polling loop and all UI-facing state.
/// </summary>
/// <remarks>
/// Everything published here is touched only on the UI thread; the SSH and
/// SQLite work happens on the thread pool, so the UI never blocks on a slow or
/// unreachable host. The Swift original is <c>@MainActor</c>; the equivalent
/// here is <see cref="_dispatch"/>, a delegate that marshals onto whichever
/// context owns this service — the WPF dispatcher in the app, an immediate
/// call in the CLI, a single-threaded pump in the tests.
/// </remarks>
public sealed class MonitorService : INotifyPropertyChanged, IAsyncDisposable
{
    public event PropertyChangedEventHandler? PropertyChanged;

    /// <summary>
    /// Marshals a callback onto the thread that owns this service.
    /// </summary>
    /// <remarks>
    /// Injected rather than captured from a <c>SynchronizationContext</c> so
    /// the tests can run the whole tick machinery deterministically on one
    /// thread — the tick barrier and the backoff are exactly the parts worth
    /// testing, and both are about ordering.
    /// </remarks>
    private readonly Action<Action> _dispatch;

    public Database Database { get; }
    public AppSettings Settings { get; }
    public DockerClient Docker { get; }
    public GeoLookup Geo { get; } = new();
    public ICredentialStore Credentials { get; }

    /// <summary>Set by the app so poll results can raise notifications.</summary>
    public AlertService? Alerts { get; set; }

    // MARK: - Published state

    /// <summary>
    /// The server list, in <c>sortIndex</c> order.
    /// </summary>
    /// <remarks>
    /// An observable collection so the machine list and the dashboard bind to
    /// the same instance and both follow a reorder without either re-querying.
    /// </remarks>
    public ObservableCollection<Server> Servers { get; } = [];

    /// <summary>Newest snapshot per server, for the dashboard tiles.</summary>
    public IReadOnlyDictionary<Guid, MetricSnapshot> Latest => _latest;
    private readonly Dictionary<Guid, MetricSnapshot> _latest = [];

    public IReadOnlyDictionary<Guid, ServerStatus> Status => _status;
    private readonly Dictionary<Guid, ServerStatus> _status = [];

    /// <summary>Engine summary per Docker host, for the overview cards.</summary>
    public IReadOnlyDictionary<Guid, DockerSummary> DockerSummaries => _dockerSummaries;
    private readonly Dictionary<Guid, DockerSummary> _dockerSummaries = [];

    /// <summary>Shared logins, refreshed alongside the server list.</summary>
    public List<Identity> Identities { get; private set; } = [];
    public List<MachineGroup> Groups { get; private set; } = [];
    public List<Snippet> Snippets { get; private set; } = [];

    /// <summary>
    /// Raised once per publish, after a batch of results has been applied.
    /// </summary>
    /// <remarks>
    /// The views listen to this rather than to a per-property notification:
    /// one tick's results are one redraw (R9). See <see cref="Commit"/>.
    /// </remarks>
    public event Action? Published;

    // MARK: - Machinery

    private readonly MetricsCollector _collector;
    private readonly Func<SshTarget, ISshTransport> _transportFor;
    private readonly Action<string>? _log;
    private CancellationTokenSource? _loop;
    private Task? _pollTask;
    private Task? _maintenanceTask;

    public MonitorService(
        Database database,
        AppSettings settings,
        ICredentialStore credentials,
        Func<SshTarget, ISshTransport> transportFor,
        Action<Action>? dispatch = null,
        Action<string>? log = null,
        // Injected so the service-level tests do not fire real ICMP at
        // addresses that do not exist — three timeouts per poll is both slow
        // and a measurement of nothing.
        ILatencyProbe? latency = null)
    {
        Database = database;
        Settings = settings;
        Credentials = credentials;
        _transportFor = transportFor;
        // Default: run inline. Correct for the CLI and the tests; the app
        // passes the dispatcher.
        _dispatch = dispatch ?? (action => action());
        _log = log;
        _collector = new MetricsCollector(transportFor, latency);
        Docker = new DockerClient(new RoutingTransport(transportFor));
        Reload();
    }

    /// <summary>
    /// Hands each call to whichever transport that host is configured for.
    /// </summary>
    /// <remarks>
    /// <see cref="DockerClient"/> and the SFTP and terminal code take a single
    /// <see cref="ISshTransport"/>, but the choice is per host (D3). This
    /// adapter keeps that decision in one place instead of threading a
    /// selector through every caller.
    /// </remarks>
    private sealed class RoutingTransport(Func<SshTarget, ISshTransport> route) : ISshTransport
    {
        public string Name => "routing";

        public Task<string> RunAsync(
            string command, SshTarget target, int timeoutSeconds = 30,
            CancellationToken cancellationToken = default) =>
            route(target).RunAsync(command, target, timeoutSeconds, cancellationToken);

        public Task DisconnectAsync(SshTarget target) => route(target).DisconnectAsync(target);

        // The routed transports are owned by whoever built them, not by this
        // adapter, so disposing it must not close their connections.
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }

    // MARK: - Server list

    public void Reload()
    {
        try
        {
            Identities = Database.AllIdentities();
            Groups = Database.AllGroups();
            Snippets = Database.AllSnippets();
            var servers = Database.AllServers();

            Servers.Clear();
            foreach (var server in servers) Servers.Add(server);

            // Seed tiles from history so a relaunch shows the last known
            // values instead of empty cards until the first poll lands.
            foreach (var server in servers)
            {
                if (_latest.ContainsKey(server.Id)) continue;
                if (Database.LatestSample(server.Id) is { } sample)
                {
                    _latest[server.Id] = sample.ToSnapshot();
                }
            }
            Notify(nameof(Identities));
            Notify(nameof(Groups));
            Notify(nameof(Snippets));
            Published?.Invoke();
        }
        catch (Exception error)
        {
            _log?.Invoke($"reload failed: {error.Message}");
            Servers.Clear();
        }
    }

    public Server? Server(Guid id) => Servers.FirstOrDefault(s => s.Id == id);

    internal bool HasServer(Guid id) => Servers.Any(s => s.Id == id);

    public void AddServer(Server server)
    {
        Database.Save(server);
        Reload();
    }

    public void UpdateServer(Server server)
    {
        var previous = TargetOrNull(server);
        Database.Save(server);
        // Address or credential may have changed; drop the connection so the
        // next poll reconnects with the new settings.
        if (previous is not null) _ = _collector.ForgetAsync(previous);
        // Editing a failing host is usually the fix for it. Sitting out a
        // five-minute backoff afterwards would look like the edit did nothing.
        NoteSuccess(server.Id);
        Reload();
    }

    /// <summary>
    /// Records a looked-up country without touching any other column.
    /// </summary>
    /// <remarks>
    /// The card that asks holds a copy of the row that may be a poll behind,
    /// and saving that copy back undid the poll's facts and probed OS. Nor is
    /// a lookup a sign the host answered, so the backoff is left alone.
    /// </remarks>
    public void UpdateCountryCode(Guid serverId, string countryCode)
    {
        Database.UpdateCountryCode(serverId, countryCode);
        Reload();
    }

    /// <summary>
    /// Reorders the sidebar and the dashboard together: both draw
    /// <see cref="Servers"/> in <c>sortIndex</c> order, and that order is
    /// persisted so it survives a relaunch.
    /// </summary>
    public void MoveServer(int fromIndex, int toIndex)
    {
        if (fromIndex < 0 || fromIndex >= Servers.Count) return;
        var reordered = Servers.ToList();
        var moved = reordered[fromIndex];
        reordered.RemoveAt(fromIndex);
        reordered.Insert(Math.Clamp(toIndex, 0, reordered.Count), moved);
        Database.ReorderServers(reordered.Select(s => s.Id).ToList());
        Reload();
    }

    public void DeleteServer(Server server)
    {
        var target = TargetOrNull(server);
        Database.DeleteServer(server.Id);
        // The credential store is the App's, so Database cannot clear it —
        // this is the one place that knows about both.
        Credentials.DeletePassword(server.Id);
        if (target is not null) _ = _collector.ForgetAsync(target);
        Alerts?.Forget(server.Id);
        _latest.Remove(server.Id);
        _status.Remove(server.Id);
        _detailedServers.Remove(server.Id);
        _failureStreak.Remove(server.Id);
        _retryAfter.Remove(server.Id);
        _pending.RemoveAll(p => p.ServerId == server.Id);
        Reload();
    }

    /// <summary>
    /// Resolves a stored server into something the transport can dial.
    /// </summary>
    /// <remarks>
    /// A server pointing at an identity takes that identity's username and
    /// auth, so changing the shared login updates every machine using it.
    /// </remarks>
    public SshTarget Target(Server server)
    {
        var username = server.Username;
        var credential = server.Credential;
        var host = server.AuthKind == AuthKind.SshConfigAlias ? server.SshAlias : server.Host;

        if (server.IdentityId is { } identityId
            && Identities.FirstOrDefault(i => i.Id == identityId) is { } identity)
        {
            username = identity.Username;
            switch (identity.AuthKind)
            {
                case AuthKind.IdentityFile:
                    credential = SshCredential.Key(identity.IdentityFile);
                    host = server.Host;
                    break;
                case AuthKind.Agent:
                    credential = SshCredential.Agent;
                    host = server.Host;
                    break;
                default:
                    // An identity that is itself an alias or a password adds
                    // nothing the server row does not already say.
                    break;
            }
        }

        return new SshTarget(server.Id, host, server.Port, username, credential);
    }

    private SshTarget? TargetOrNull(Server server)
    {
        try
        {
            return Target(server);
        }
        catch (Exception)
        {
            return null;
        }
    }

    // MARK: - Toolbox

    public void Save(Snippet snippet)
    {
        Database.Save(snippet);
        Snippets = Database.AllSnippets();
        Notify(nameof(Snippets));
    }

    public void DeleteSnippet(Guid id)
    {
        Database.DeleteSnippet(id);
        Snippets = Database.AllSnippets();
        Notify(nameof(Snippets));
    }

    /// <summary>Runs a snippet on a host and returns its combined output.</summary>
    public async Task<string> RunAsync(Snippet snippet, Server server, CancellationToken token = default)
    {
        var target = Target(server);
        Database.MarkSnippetUsed(snippet.Id);
        Snippets = Database.AllSnippets();
        Notify(nameof(Snippets));
        return await _transportFor(target)
            .RunAsync($"{snippet.Command} 2>&1", target, 120, token)
            .ConfigureAwait(false);
    }

    /// <summary>
    /// One command over the server's connection, for cards that fetch
    /// something the metrics poll does not carry.
    /// </summary>
    public Task<string> RunAsync(
        string command, Server server, int timeoutSeconds = 30, CancellationToken token = default)
    {
        var target = Target(server);
        return _transportFor(target).RunAsync(command, target, timeoutSeconds, token);
    }

    public void Save(Identity identity)
    {
        Database.Save(identity);
        Reload();
    }

    public void DeleteIdentity(Guid id)
    {
        Database.DeleteIdentity(id);
        Reload();
    }

    public int ServerCountUsingIdentity(Guid id) => Database.ServerCountUsingIdentity(id);

    // MARK: - Machine groups

    public void Save(MachineGroup group)
    {
        Database.Save(group);
        Reload();
    }

    public void DeleteGroup(Guid id)
    {
        Database.DeleteGroup(id);
        Reload();
    }

    public int NextGroupSortIndex() => Database.NextGroupSortIndex();

    public List<Server> ServersIn(MachineGroup group) =>
        Servers.Where(s => s.GroupId == group.Id).ToList();

    /// <summary>Machines with no group, shown on their own below the groups.</summary>
    public List<Server> UngroupedServers => Servers.Where(s => s.GroupId is null).ToList();

    public void Assign(Server server, MachineGroup? group)
    {
        var updated = server.Clone();
        updated.GroupId = group?.Id;
        Database.Save(updated);
        Reload();
    }

    // MARK: - Docker

    /// <summary>
    /// Refreshes the engine summary for one host.
    /// </summary>
    /// <remarks>
    /// The machine screen needs its own host's figures, and the whole-fleet
    /// sweep is the wrong tool for that: <c>docker info</c> is a separate SSH
    /// round trip per host, so opening one machine would poll every other one
    /// too.
    /// </remarks>
    public async Task RefreshDockerSummaryAsync(Server server, CancellationToken token = default)
    {
        if (!server.HasDocker) return;
        try
        {
            var summary = await Docker.SummaryAsync(Target(server), token).ConfigureAwait(false);
            _dispatch(() =>
            {
                _dockerSummaries[server.Id] = summary;
                Published?.Invoke();
            });
        }
        catch (Exception error) when (error is SshException or OperationCanceledException)
        {
            // A host that will not answer `docker info` is not a failed poll.
        }
    }

    // MARK: - Polling

    public void Start()
    {
        if (_loop is not null) return;
        _loop = new CancellationTokenSource();
        var token = _loop.Token;

        _pollTask = Task.Run(async () =>
        {
            while (!token.IsCancellationRequested)
            {
                // Fire and move on. Awaiting the whole fleet here would couple
                // every host's cadence to the slowest: one unreachable host
                // (10 s connect timeout) stretched everyone's 5 s interval to
                // 15 s, and a hung one (30 s watchdog) to 35 s.
                _dispatch(() => PollDue());
                var interval = EffectiveInterval(Settings.PollInterval, IsEnergySaverOn?.Invoke() ?? false);
                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(interval), token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }
            }
        }, token);

        _maintenanceTask = Task.Run(async () =>
        {
            while (!token.IsCancellationRequested)
            {
                // Give startup a moment before touching the disk, then keep a
                // slow cadence: pruning a bounded window is cheap.
                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(60), token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }
                try
                {
                    Database.PruneHistory(Settings.Retention);
                }
                catch (Exception error)
                {
                    _log?.Invoke($"prune failed: {error.Message}");
                }
            }
        }, token);
    }

    /// <summary>
    /// Whether Windows is in energy-saver mode. Set by the App.
    /// </summary>
    /// <remarks>
    /// In Core as a delegate because <c>PowerManager</c> is a Windows API and
    /// this assembly must stay portable. The corresponding macOS input is
    /// <c>isLowPowerModeEnabled</c>.
    /// </remarks>
    public Func<bool>? IsEnergySaverOn { get; set; }

    /// <summary>
    /// The gap between ticks.
    /// </summary>
    /// <remarks>
    /// In energy-saver mode the user has asked the whole machine to do less;
    /// nine SSH round trips every five seconds is exactly the kind of
    /// background work that request is about, so the cadence drops to a third.
    /// Read on every tick, so flipping the switch takes effect at the next one.
    /// </remarks>
    internal static double EffectiveInterval(double configured, bool energySaver) =>
        energySaver ? configured * 3 : configured;

    public void Stop()
    {
        _loop?.Cancel();
        _loop?.Dispose();
        _loop = null;
        _pollTask = null;
        _maintenanceTask = null;
        _tickCap?.Dispose();
        _tickCap = null;
        _tickMembers.Clear();
        // A stopped service publishes nothing further, so what an open tick
        // was holding goes with it; and a restart begins with a clean backoff.
        _pending.Clear();
        _failureStreak.Clear();
        _retryAfter.Clear();
    }

    /// <summary>
    /// Servers whose previous poll has not returned yet.
    /// </summary>
    /// <remarks>
    /// A host slow to answer is polled once, not once per tick, so a stall
    /// cannot pile up a queue of connections behind it.
    /// </remarks>
    private readonly HashSet<Guid> _inFlight = [];
    internal IReadOnlySet<Guid> InFlight => _inFlight;

    /// <summary>
    /// Servers whose machine screen is on screen.
    /// </summary>
    /// <remarks>
    /// Only they get the process list, the one part of a poll the dashboard
    /// never shows and the host pays ~30 ms of CPU for.
    /// </remarks>
    private readonly HashSet<Guid> _detailedServers = [];
    internal IReadOnlySet<Guid> DetailedServers => _detailedServers;

    /// <summary>
    /// Called by the machine screen as it appears and disappears.
    /// </summary>
    /// <remarks>
    /// Opening one polls the host at once, so the process card fills in about
    /// a second rather than waiting out the rest of the current interval.
    /// </remarks>
    public Task? SetDetailVisible(Guid serverId, bool visible)
    {
        if (visible)
        {
            _detailedServers.Add(serverId);
            return PollNow(serverId);
        }
        _detailedServers.Remove(serverId);
        return null;
    }

    /// <summary>
    /// One host, now, outside the tick — for a screen that has just opened.
    /// Nothing if it is already being polled; that poll is seconds from done.
    /// </summary>
    internal Task? PollNow(Guid serverId)
    {
        if (Server(serverId) is not { } server || _inFlight.Contains(serverId)) return null;
        _inFlight.Add(serverId);
        return Task.Run(async () =>
        {
            await PollAsync(server).ConfigureAwait(false);
            _dispatch(() => PollFinished(serverId));
        });
    }

    // MARK: - One tick, one publish

    /// <summary>Servers launched by the tick in progress whose polls have not returned.</summary>
    private readonly HashSet<Guid> _tickMembers = [];
    /// <summary>
    /// Fires <see cref="TickCap"/> after the tick opened; null whenever no
    /// tick is open, which is what <see cref="TickOpen"/> reads.
    /// </summary>
    private CancellationTokenSource? _tickCap;

    internal bool TickOpen => _tickCap is not null;

    /// <summary>
    /// Longest a tick's results are held for a straggler before what has
    /// arrived is shown anyway.
    /// </summary>
    /// <remarks>
    /// Measured spread between the first and last host finishing one tick on
    /// the macOS build: p50 1.0 s, p95 1.6 s. A host that has gone away takes
    /// the full 10 s connect timeout, so the cap is what keeps one dead host
    /// from delaying eight live ones.
    /// </remarks>
    internal TimeSpan TickCap { get; set; } = TimeSpan.FromSeconds(2);

    /// <summary>
    /// Holds publishing until every poll this tick launched has answered.
    /// </summary>
    /// <remarks>
    /// A plain debounce window does not work here. Measured on the macOS build
    /// over 221 ticks, a 250 ms window gave 3.4 publishes per tick: hosts
    /// finish about a second apart (latency differs by 200 ms and the script
    /// itself samples twice, 0.5 s apart), so the first result opened a
    /// window, the next few closed it, and the stragglers each opened another.
    /// Every publish is a full dashboard pass, so that was three layouts to
    /// show one tick's numbers. The tick knows exactly which polls it started;
    /// waiting for those turns it into one.
    /// </remarks>
    private void OpenTick(HashSet<Guid> members)
    {
        _tickMembers.UnionWith(members);
        if (TickOpen) return;
        var cap = new CancellationTokenSource();
        _tickCap = cap;
        var delay = TickCap;
        _ = Task.Run(async () =>
        {
            try
            {
                await Task.Delay(delay, cap.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                return;
            }
            _dispatch(() =>
            {
                // Another tick may have closed and reopened while this was
                // sleeping; only close the one this task armed.
                if (ReferenceEquals(_tickCap, cap)) CloseTick("cap");
            });
        });
    }

    internal void PollFinished(Guid serverId)
    {
        _inFlight.Remove(serverId);
        _tickMembers.Remove(serverId);
        if (TickOpen && _tickMembers.Count == 0) CloseTick("complete");
    }

    private void CloseTick(string reason)
    {
        _tickCap?.Cancel();
        _tickCap?.Dispose();
        _tickCap = null;
        // A straggler the cap gave up on must not be carried into the next
        // tick: it stays in flight for its whole timeout, and with it still a
        // member every later tick could only close by the cap — eight live
        // hosts waiting 2 s each round for the one that is gone.
        _tickMembers.Clear();
        _log?.Invoke($"tick closed ({reason}), {_pending.Count} results held");
        // Hidden: leave them queued for SetUiVisible(true), exactly as a
        // result arriving outside a tick would be.
        if (_uiIsVisible) FlushPending();
    }

    // MARK: - Failure backoff

    /// <summary>Consecutive failed polls per server, reset by any success.</summary>
    private readonly Dictionary<Guid, int> _failureStreak = [];
    /// <summary>Earliest time <see cref="PollDue"/> will try a server again.</summary>
    private readonly Dictionary<Guid, DateTime> _retryAfter = [];

    internal IReadOnlyDictionary<Guid, int> FailureStreak => _failureStreak;
    internal IReadOnlyDictionary<Guid, DateTime> RetryAfter => _retryAfter;

    /// <summary>
    /// Longest a failing host waits between attempts. It still gets retried
    /// often enough that a recovery is noticed within a few minutes.
    /// </summary>
    internal static readonly TimeSpan MaxBackoff = TimeSpan.FromSeconds(300);

    /// <summary>
    /// A host that is simply gone costs a full 10 s connect timeout per
    /// attempt. Polling it on the normal 5 s cadence means doing that forever
    /// — on battery, for a host that has been down all day. Back off
    /// geometrically instead, and reset the moment it answers.
    /// </summary>
    internal TimeSpan BackoffDelay(int streak)
    {
        if (streak <= 1) return TimeSpan.Zero;
        var grown = Settings.PollInterval * Math.Pow(2, streak - 1);
        return TimeSpan.FromSeconds(Math.Min(grown, MaxBackoff.TotalSeconds));
    }

    /// <summary>
    /// Whether a server's backoff window is still open.
    /// </summary>
    /// <remarks>
    /// The window is an absolute time, so a clock that moves backwards — an
    /// NTP correction on a machine with a dead RTC, a restored VM snapshot —
    /// would otherwise strand a host in a wait far longer than any backoff can
    /// legitimately produce, with no way out but relaunching. A remaining wait
    /// longer than the cap is not a backoff; it is a moved clock.
    /// </remarks>
    internal static bool IsStillWaiting(DateTime due, DateTime now) =>
        due > now && due - now <= MaxBackoff;

    /// <summary>Records a failed poll and pushes the next attempt out.</summary>
    internal void NoteFailure(Guid serverId, DateTime? now = null)
    {
        var at = now ?? DateTime.UtcNow;
        var streak = _failureStreak.GetValueOrDefault(serverId) + 1;
        _failureStreak[serverId] = streak;
        var delay = BackoffDelay(streak);
        if (delay > TimeSpan.Zero) _retryAfter[serverId] = at + delay;
        else _retryAfter.Remove(serverId);
    }

    /// <summary>
    /// Any answer at all clears the backoff, so a host that comes back is
    /// polled at the normal cadence from its very next tick.
    /// </summary>
    internal void NoteSuccess(Guid serverId)
    {
        _failureStreak.Remove(serverId);
        _retryAfter.Remove(serverId);
    }

    /// <summary>
    /// Forgets every backoff and polls at once.
    /// </summary>
    /// <remarks>
    /// Called by the App on wake from sleep and when the network comes back.
    /// Both are their own signal: the fleet almost certainly did not go down
    /// while the lid was shut, and on a laptop the usual reason every host
    /// fails at once is this machine — a closed lid, a train, a different
    /// Wi-Fi — not nine servers. Without this, the backoff built up during the
    /// outage keeps the fleet dark for up to five minutes after the network is
    /// already back.
    /// </remarks>
    public void RetryEverythingNow(string reason)
    {
        _log?.Invoke($"{reason}; retrying the whole fleet");
        _failureStreak.Clear();
        _retryAfter.Clear();
        PollDue();
    }

    /// <summary>
    /// Starts a poll for every server not already being polled, and returns
    /// those tasks without waiting for them. Each host runs on its own clock.
    /// </summary>
    internal List<Task> PollDue(bool ignoringBackoff = false, DateTime? now = null)
    {
        var at = now ?? DateTime.UtcNow;
        var launched = new HashSet<Guid>();
        var tasks = new List<Task>();

        foreach (var server in Servers.ToList())
        {
            if (_inFlight.Contains(server.Id)) continue;
            if (!ignoringBackoff
                && _retryAfter.TryGetValue(server.Id, out var due)
                && IsStillWaiting(due, at))
            {
                continue;
            }
            _inFlight.Add(server.Id);
            launched.Add(server.Id);
            var captured = server;
            tasks.Add(Task.Run(async () =>
            {
                await PollAsync(captured).ConfigureAwait(false);
                _dispatch(() => PollFinished(captured.Id));
            }));
        }

        // Opened before any of those can report, so the first result is held
        // rather than published on its own.
        if (launched.Count > 0) OpenTick(launched);
        return tasks;
    }

    /// <summary>
    /// One pass over every server, waited for — the manual refresh.
    /// </summary>
    /// <remarks>
    /// A user-driven refresh is an explicit "try now", so it overrides the
    /// backoff rather than silently skipping the hosts that need it most.
    /// Hosts with a poll already in flight are left to it rather than polled
    /// twice.
    /// </remarks>
    public async Task PollAllAsync()
    {
        var tasks = PollDue(ignoringBackoff: true);
        await Task.WhenAll(tasks).ConfigureAwait(false);
        // Deterministic for the caller: what it started is applied when this
        // returns — and regardless of visibility, since tests and the manual
        // refresh both rely on it. Unless everything was already in flight:
        // then this refresh joined a tick still in progress, and flushing here
        // would publish half of it; the tick's own close publishes the rest.
        _dispatch(() =>
        {
            if (!TickOpen) FlushPending();
        });
    }

    public async Task PollAsync(Server server)
    {
        // A task created by PollDue starts on the pool, so a server can be
        // deleted between the tick and this line; without the check it came
        // back as a Polling entry for a row that no longer exists.
        if (!HasServer(server.Id)) return;

        _dispatch(() =>
        {
            if (!_status.TryGetValue(server.Id, out var current)
                || current.Kind == StatusKind.Unknown)
            {
                Commit(server.Id, () => _status[server.Id] = ServerStatus.Polling);
            }
        });

        try
        {
            var target = Target(server);
            var snapshot = await _collector.CollectAsync(
                target,
                server.OsKind,
                processes: _detailedServers.Contains(server.Id)).ConfigureAwait(false);

            // Deleted while this poll was in flight: nothing to record, and
            // the sample insert would fail its foreign key anyway.
            if (!HasServer(server.Id)) return;

            // Decided against the row as it is now, not the copy this poll
            // started with: the user may have set the OS by hand while the
            // poll ran.
            var current = Server(server.Id) ?? server;
            var detected = current.OsKind == OSKind.Auto ? snapshot.DetectedOS : null;
            // Facts change once (the first poll) or never, so the row is only
            // touched when one did; the common case is a single INSERT.
            var factsChanged = current.Cores != snapshot.Cores
                || current.MemoryTotal != snapshot.MemoryTotal
                || current.DiskTotal != snapshot.DiskTotal
                || current.DockerVersion != snapshot.DockerVersion;

            // Durable writes go straight to the store; they publish nothing.
            await Database.RecordPollAsync(server.Id, snapshot, detected, factsChanged)
                .ConfigureAwait(false);

            if (!HasServer(server.Id)) return;

            var completedAt = DateTime.UtcNow;
            _dispatch(() =>
            {
                if (!HasServer(server.Id)) return;
                NoteSuccess(server.Id);
                Commit(server.Id, () =>
                {
                    _latest[server.Id] = snapshot;
                    _status[server.Id] = ServerStatus.Online(completedAt);
                    // Free with the poll, so the machine screen and the Docker
                    // page both stay live without their own round trip.
                    if (snapshot.DockerSummary is { } summary)
                    {
                        _dockerSummaries[server.Id] = summary;
                    }
                    else
                    {
                        // Docker can disappear, lose permissions, or stop
                        // between polls. Do not leave the previous engine card
                        // visible as if it were still current.
                        _dockerSummaries.Remove(server.Id);
                    }
                    // Only when something moved: assigning identical values
                    // still costs a notification on every poll.
                    if (factsChanged || detected is not null)
                    {
                        if (Server(server.Id) is { } row)
                        {
                            if (detected is { } os && row.OsKind == OSKind.Auto) row.OsKind = os;
                            row.Cores = snapshot.Cores;
                            row.MemoryTotal = snapshot.MemoryTotal;
                            row.DiskTotal = snapshot.DiskTotal;
                            row.DockerVersion = snapshot.DockerVersion;
                        }
                    }
                });
                // The user may have edited the name or per-host thresholds
                // while this SSH round was in flight. Notifications must use
                // the row as it is now, not the value captured when polling
                // started.
                Alerts?.Evaluate(
                    Server(server.Id) ?? server, ServerStatus.Online(completedAt), snapshot);
            });
        }
        catch (OperationCanceledException)
        {
            // A cancelled poll says nothing about the host.
        }
        catch (Exception error)
        {
            _dispatch(() =>
            {
                if (!HasServer(server.Id)) return;
                NoteFailure(server.Id);
                _log?.Invoke($"{server.Name}: {error.Message}");
                var failure = ServerStatus.Offline(error.Message);
                Commit(server.Id, () => _status[server.Id] = failure);
                Alerts?.Evaluate(Server(server.Id) ?? server, failure, null);
            });
        }
    }

    // MARK: - Publishing

    /// <summary>
    /// Results waiting to be applied together — at most one per server, the
    /// newest.
    /// </summary>
    /// <remarks>
    /// Replaced on arrival rather than de-duplicated at flush so this stays
    /// bounded by the fleet. On the macOS build, with the window hidden (the
    /// app's normal tray mode) an append-only queue grew by one closure
    /// holding a full snapshot per host per poll — tens of megabytes an hour —
    /// until the window was next shown.
    /// </remarks>
    private readonly List<(Guid ServerId, Action Apply)> _pending = [];
    internal int PendingCount => _pending.Count;

    /// <summary>
    /// False while the window is hidden or minimised.
    /// </summary>
    /// <remarks>
    /// Windows has no occlusion notification, so this is driven by minimise
    /// and hide-to-tray rather than by another window covering ours — the
    /// cases that actually matter, since those are where the app spends hours.
    /// Collection, the database and alerts are unaffected;
    /// <c>SetUiVisible(true)</c> applies whatever accumulated so the first
    /// frame shown is current.
    /// </remarks>
    private bool _uiIsVisible = true;
    internal bool UiIsVisible => _uiIsVisible;

    public void SetUiVisible(bool visible)
    {
        if (visible == _uiIsVisible) return;
        _uiIsVisible = visible;
        if (visible) FlushPending();
    }

    /// <summary>
    /// Publishes a result now, or holds it for the tick.
    /// </summary>
    /// <remarks>
    /// Every dashboard card is redrawn on <see cref="Published"/>, so each
    /// publish is a full pass over the dashboard. Results are held while a
    /// tick is in progress and applied together when it closes. Two kinds go
    /// straight through: a result for a host with nothing on screen yet — a
    /// spinner turning into numbers is worth its own pass, once — and one
    /// arriving with no tick open, a straggler whose tick the cap already
    /// closed. Nothing is applied while the window is hidden.
    /// </remarks>
    private void Commit(Guid serverId, Action apply)
    {
        if (!_uiIsVisible)
        {
            Hold(serverId, apply);
            return;
        }
        if (!TickOpen || !HasContent(serverId))
        {
            apply();
            Published?.Invoke();
            return;
        }
        Hold(serverId, apply);
    }

    private void Hold(Guid serverId, Action apply)
    {
        var index = _pending.FindIndex(p => p.ServerId == serverId);
        if (index >= 0) _pending[index] = (serverId, apply);
        else _pending.Add((serverId, apply));
    }

    /// <summary>
    /// Whether the card for this server shows anything but a spinner: a
    /// snapshot, or a verdict.
    /// </summary>
    /// <remarks>
    /// A host that keeps failing has a verdict on screen, so its next failure
    /// waits for the tick like any other update.
    /// </remarks>
    private bool HasContent(Guid serverId) =>
        _latest.ContainsKey(serverId)
        || (_status.TryGetValue(serverId, out var status) && status.HasVerdict);

    /// <summary>
    /// Applies everything held, as one change. Results for a server deleted in
    /// the meantime are dropped rather than resurrecting it.
    /// </summary>
    internal void FlushPending()
    {
        if (_pending.Count == 0) return;
        var batch = _pending.ToList();
        _pending.Clear();
        foreach (var item in batch)
        {
            if (HasServer(item.ServerId)) item.Apply();
        }
        _log?.Invoke($"published {batch.Count} results");
        Published?.Invoke();
    }

    /// <summary>Servers that failed their last poll, for the tray summary.</summary>
    public List<Server> OfflineServers => Servers
        .Where(s => _status.TryGetValue(s.Id, out var status) && status.Kind == StatusKind.Offline)
        .ToList();

    /// <summary>Highest CPU currently reported, or null when nothing has reported yet.</summary>
    public (Server Server, double Percent)? PeakCpu => Servers
        .Where(s => _latest.ContainsKey(s.Id))
        .Select(s => (Server: s, Percent: _latest[s.Id].CpuPercent))
        .OrderByDescending(p => p.Percent)
        .Select(p => ((Server, double)?)p)
        .FirstOrDefault();

    /// <summary>One-shot connectivity check for the editor's "test connection" button.</summary>
    public Task<MetricSnapshot> TestConnectionAsync(
        SshTarget target, OSKind osKind = OSKind.Auto, CancellationToken token = default) =>
        _collector.CollectAsync(target, osKind, processes: false, token);

    public List<MetricSample> History(Guid serverId, DateTime since)
    {
        try
        {
            return Database.Samples(serverId, since);
        }
        catch (Exception)
        {
            return [];
        }
    }

    /// <summary>
    /// History for the charts: bucketed by SQLite to
    /// <paramref name="maxPoints"/>, read off the UI thread.
    /// </summary>
    /// <remarks>
    /// The 24 h range used to decode a day of polls on the main thread every
    /// 15 s on the macOS build — a ~120 ms hitch each time. Bucketing in SQL
    /// took it to ~18 ms, and none of it on the thread that draws.
    /// </remarks>
    public Task<List<MetricSample>> ChartHistoryAsync(
        Guid serverId, DateTime since, int maxPoints = 240) =>
        Task.Run(() =>
        {
            try
            {
                return Database.ReducedSamples(serverId, since, maxPoints: maxPoints);
            }
            catch (Exception)
            {
                return [];
            }
        });

    private void Notify(string name) =>
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));

    public async ValueTask DisposeAsync()
    {
        Stop();
        Settings.Flush();
        await ValueTask.CompletedTask;
    }
}
