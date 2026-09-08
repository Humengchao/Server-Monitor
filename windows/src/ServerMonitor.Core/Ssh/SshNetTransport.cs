using Renci.SshNet;
using ServerMonitor.Core.Store;
using Renci.SshNet.Common;

namespace ServerMonitor.Core.Ssh;

/// <summary>
/// The default transport: one long-lived SSH.NET connection per host.
/// </summary>
/// <remarks>
/// This is the Windows stand-in for OpenSSH's ControlMaster, and the reason
/// the plan made it the default (D3). Windows' own OpenSSH does not implement
/// ControlMaster at all (F1): the options are accepted and no master is built,
/// so a subprocess route pays a full handshake and login on every poll — at a
/// 5 s interval, ~17,000 sshd logins per host per day across auth.log, wtmp
/// and PAM. The macOS build keeps that under 300 with
/// <c>ControlPersist=300</c>, and the Go backend already made this same choice
/// (<c>web/backend/internal/services/ssh_cache.go</c>) and runs on it.
///
/// The library's old disqualifier does not apply here: SSH.NET has supported
/// rsa-sha2-256/512 and OpenSSH-format keys since 2023.0.0, and encrypted
/// OpenSSH keys plus ed25519 since 2024.2.0 (F4). The macOS build's reason for
/// shelling out — Citadel's RSA being SHA-1 only, which modern sshd refuses —
/// has no equivalent in .NET.
///
/// What it does <em>not</em> do is agent authentication: 2026.0.0 exports no
/// such method, which settles the one thing D3 left to the S2 pre-study. See
/// <see cref="AgentMethod"/> for what happens instead and when a host has to
/// move to <see cref="OpenSshExeTransport"/>.
/// </remarks>
public sealed class SshNetTransport : ISshTransport
{
    public string Name => "library";

    private readonly ICredentialStore _credentials;
    private readonly SshConfig _config;
    private readonly KnownHosts _knownHosts;
    private readonly Action<string>? _log;

    /// <summary>
    /// One entry per server row, keyed by id rather than by endpoint.
    /// </summary>
    /// <remarks>
    /// The server id is deliberate, and matches the macOS build's control
    /// socket naming. Two rows may point at the same endpoint with different
    /// identities, passwords, ProxyJump settings or host-key policies; sharing
    /// a connection between them lets the first row's authenticated session
    /// silently satisfy the second, which is both surprising and wrong. A
    /// connection belongs to one configured server row — and the poll, the
    /// Docker calls and the terminal's shell stream for that row all ride on
    /// it as channels.
    ///
    /// SFTP is the exception: SSH.NET's <c>SftpClient</c> is its own client
    /// built from a <c>ConnectionInfo</c>, not a channel factory, so it opens
    /// a second connection to the host. <see cref="SftpSession"/> pools that
    /// one separately rather than pretending otherwise.
    /// </remarks>
    private readonly Dictionary<Guid, Pooled> _pool = [];
    private readonly SemaphoreSlim _poolLock = new(1, 1);
    private readonly CancellationTokenSource _sweeper = new();
    private bool _disposed;

    /// <summary>
    /// Idle time after which a pooled connection is closed by the sweeper.
    /// </summary>
    /// <remarks>
    /// Matches the macOS build's <c>ControlPersist=300</c>, so both clients
    /// leave the same footprint on a host that stops being polled.
    /// </remarks>
    internal static readonly TimeSpan IdleTimeout = TimeSpan.FromSeconds(300);

    private sealed class Pooled
    {
        public required SshClient Client { get; init; }
        /// <summary>
        /// What the connection was built for. A poll after the user edits the
        /// host, port, user or credential must not silently keep using the old
        /// session — <see cref="MonitorService"/> calls
        /// <see cref="DisconnectAsync"/> on an edit, and this is the belt to
        /// that braces.
        /// </summary>
        public required string Fingerprint { get; init; }
        public DateTime LastUsed { get; set; } = DateTime.UtcNow;
        /// <summary>
        /// Serialises this host's channel opens. SSH.NET's <c>SshClient</c> is
        /// not documented as thread-safe, and a poll can overlap a terminal
        /// resize or a Docker refresh on the same connection.
        /// </summary>
        public SemaphoreSlim Gate { get; } = new(1, 1);
    }

    public SshNetTransport(
        ICredentialStore credentials,
        SshConfig? config = null,
        KnownHosts? knownHosts = null,
        Action<string>? log = null)
    {
        _credentials = credentials;
        _config = config ?? SshConfig.FromDefaultLocation();
        _knownHosts = knownHosts ?? KnownHosts.AtDefaultLocation();
        _log = log;
        _ = SweepAsync(_sweeper.Token);
    }

    public async Task<string> RunAsync(
        string command,
        SshTarget target,
        int timeoutSeconds = 30,
        CancellationToken cancellationToken = default)
    {
        var pooled = await LeaseAsync(target, cancellationToken).ConfigureAwait(false);
        await pooled.Gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            return await ExecAsync(pooled, command, timeoutSeconds, cancellationToken)
                .ConfigureAwait(false);
        }
        catch (Exception error) when (error is SshConnectionException or ObjectDisposedException)
        {
            // The session died between polls — a host rebooted, a NAT dropped
            // the flow, sshd was restarted. Its entry is stale; drop it and
            // try once on a fresh connection. Beyond that the host really is
            // unreachable and the caller's backoff should see it.
            _log?.Invoke($"{target.Host}: reconnecting after {error.GetType().Name}");
            await EvictAsync(target.ServerId).ConfigureAwait(false);
            var fresh = await LeaseAsync(target, cancellationToken).ConfigureAwait(false);
            await fresh.Gate.WaitAsync(cancellationToken).ConfigureAwait(false);
            try
            {
                return await ExecAsync(fresh, command, timeoutSeconds, cancellationToken)
                    .ConfigureAwait(false);
            }
            finally
            {
                fresh.Gate.Release();
            }
        }
        finally
        {
            pooled.Gate.Release();
        }
    }

    private static async Task<string> ExecAsync(
        Pooled pooled, string command, int timeoutSeconds, CancellationToken cancellationToken)
    {
        pooled.LastUsed = DateTime.UtcNow;
        using var ssh = pooled.Client.CreateCommand(command);
        ssh.CommandTimeout = TimeSpan.FromSeconds(timeoutSeconds);

        string stdout;
        try
        {
            // ExecuteAsync returns a bare Task; the output lands on Result.
            await ssh.ExecuteAsync(cancellationToken).ConfigureAwait(false);
            stdout = ssh.Result ?? string.Empty;
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // A cancelled poll says nothing about the host; let the caller see
            // the cancellation rather than an invented failure.
            throw;
        }
        catch (SshOperationTimeoutException)
        {
            throw SshException.TimedOut(timeoutSeconds);
        }

        if (ssh.ExitStatus is { } status && status != 0)
        {
            // Partial output still parses — a host where one `cat` failed
            // should report the rest — so only a completely empty result is a
            // real failure.
            if (stdout.Trim().Length == 0)
            {
                throw SshException.CommandFailed(status, ssh.Error ?? string.Empty);
            }
        }
        return stdout;
    }

    /// <summary>
    /// A pooled connection for this target, opening one if needed.
    /// </summary>
    private async Task<Pooled> LeaseAsync(SshTarget target, CancellationToken cancellationToken)
    {
        var resolved = _config.Resolve(target);
        var fingerprint = resolved.Fingerprint;

        await _poolLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            if (_pool.TryGetValue(target.ServerId, out var existing))
            {
                if (existing.Fingerprint == fingerprint && existing.Client.IsConnected)
                {
                    existing.LastUsed = DateTime.UtcNow;
                    return existing;
                }
                Close(existing);
                _pool.Remove(target.ServerId);
            }

            var client = await ConnectAsync(resolved, target, cancellationToken).ConfigureAwait(false);
            var pooled = new Pooled { Client = client, Fingerprint = fingerprint };
            _pool[target.ServerId] = pooled;
            return pooled;
        }
        finally
        {
            _poolLock.Release();
        }
    }

    private Task<SshClient> ConnectAsync(
        ResolvedHost resolved, SshTarget target, CancellationToken cancellationToken) =>
        ConnectAsync(
            resolved,
            target,
            info =>
            {
                var client = new SshClient(info);
                // Keepalive rather than waiting to discover a dead session on
                // the next poll: this is the ServerAliveInterval the macOS
                // build passes ssh, and it also keeps a NAT from dropping an
                // idle flow between polls of a slow-cadence fleet.
                client.KeepAliveInterval = TimeSpan.FromSeconds(15);
                return client;
            },
            cancellationToken);

    /// <summary>
    /// Opens one connection, including any ProxyJump hops.
    /// </summary>
    /// <remarks>
    /// A jump host becomes a real nested connection plus a forwarded port,
    /// rather than SSH.NET's own proxy support, which speaks HTTP/SOCKS and
    /// not <c>ProxyJump</c>. Chains resolve outermost-first so
    /// <c>ProxyJump a,b</c> tunnels through a, then b.
    ///
    /// Generic in the client type because SFTP is a second connection to the
    /// same host (see <see cref="_pool"/>) and has to arrive through the same
    /// bastions, with the same credentials and the same host-key policy.
    /// </remarks>
    private async Task<TClient> ConnectAsync<TClient>(
        ResolvedHost resolved,
        SshTarget target,
        Func<ConnectionInfo, TClient> build,
        CancellationToken cancellationToken)
        where TClient : BaseClient
    {
        var hops = new List<SshClient>();
        var forwards = new List<ForwardedPortLocal>();
        TClient? client = null;
        try
        {
            var host = resolved.HostName;
            var port = resolved.Port;

            foreach (var jump in resolved.ProxyJump)
            {
                var jumpResolved = _config.Resolve(jump);
                var jumpClient = new SshClient(BuildInfo(jumpResolved, target.ServerId));
                Arm(jumpClient, jumpResolved.HostName);
                await jumpClient.ConnectAsync(cancellationToken).ConfigureAwait(false);
                hops.Add(jumpClient);

                // Port 0 lets the OS pick, so two hosts behind the same
                // bastion cannot collide on a hard-coded local port.
                var forward = new ForwardedPortLocal("127.0.0.1", 0u, host, (uint)port);
                jumpClient.AddForwardedPort(forward);
                forward.Start();
                forwards.Add(forward);

                host = "127.0.0.1";
                port = (int)forward.BoundPort;
            }

            var info = BuildInfo(resolved with { HostName = host, Port = port }, target.ServerId);
            client = build(info);
            Arm(client, resolved.HostName);
            await client.ConnectAsync(cancellationToken).ConfigureAwait(false);

            // The hops belong to this connection now: closing it must close
            // them, or a bastion session leaks for every poll of a host behind
            // it. Recorded here and released in Close, since the clients raise
            // no "disconnected" event to hang that off.
            _owned[client] = (hops, forwards);
            return client;
        }
        catch
        {
            // The client too, not just the hops. A failed ConnectAsync leaves
            // the socket it opened inside the client, and an unreachable host
            // is retried forever — a 30-minute soak against five dead hosts
            // climbed by about three handles a minute until this disposed it.
            try { client?.Dispose(); } catch (Exception) { /* already broken */ }
            Release(hops, forwards);
            throw;
        }
    }

    /// <summary>Jump connections and forwards owned by an outer client.</summary>
    private readonly Dictionary<BaseClient, (List<SshClient> Hops, List<ForwardedPortLocal> Forwards)> _owned = [];

    private static void Release(List<SshClient> hops, List<ForwardedPortLocal> forwards)
    {
        foreach (var forward in forwards)
        {
            try { forward.Stop(); forward.Dispose(); } catch (Exception) { /* already gone */ }
        }
        foreach (var hop in hops)
        {
            try { hop.Disconnect(); hop.Dispose(); } catch (Exception) { /* already gone */ }
        }
    }

    private ConnectionInfo BuildInfo(ResolvedHost resolved, Guid serverId)
    {
        var methods = new List<AuthenticationMethod>();
        var user = resolved.User;

        switch (resolved.Method)
        {
            case AuthMethod.Password:
                {
                    var password = _credentials.GetPassword(serverId)
                        ?? throw SshException.MissingCredential();
                    methods.Add(new PasswordAuthenticationMethod(user, password));
                    // Some sshd configurations answer password auth as
                    // keyboard-interactive instead; offering both means the
                    // user does not have to know which.
                    var interactive = new KeyboardInteractiveAuthenticationMethod(user);
                    interactive.AuthenticationPrompt += (_, e) =>
                    {
                        foreach (var prompt in e.Prompts) prompt.Response = password;
                    };
                    methods.Add(interactive);
                    break;
                }

            case AuthMethod.IdentityFile:
                methods.Add(KeyMethod(user, [resolved.IdentityFile], serverId));
                break;

            case AuthMethod.Agent:
                methods.Add(AgentMethod(user, serverId));
                break;

            default:
                // A config alias: whatever the Host block named, else the
                // agent, else the default key names OpenSSH would try.
                if (resolved.IdentityFiles.Count > 0)
                {
                    methods.Add(KeyMethod(user, resolved.IdentityFiles, serverId));
                }
                // Then the default names OpenSSH would try, which is also
                // what the agent is most likely holding.
                var defaults = SshConfig.DefaultKeyPaths().ToList();
                if (defaults.Count > 0) methods.Add(KeyMethod(user, defaults, serverId));
                break;
        }

        var info = new ConnectionInfo(resolved.HostName, resolved.Port, user, [.. methods])
        {
            // Matches the macOS build's ConnectTimeout=10. A host that is
            // simply gone must cost a bounded amount per attempt, since the
            // backoff is what limits how often that is paid.
            Timeout = TimeSpan.FromSeconds(10),
        };
        return info;
    }

    /// <summary>
    /// Public-key auth over whichever of the named files exist and load.
    /// </summary>
    /// <remarks>
    /// A key with a passphrase is opened with the phrase from the credential
    /// store, under the same account as a password would be. An unreadable or
    /// wrong-passphrase key is skipped rather than fatal: OpenSSH tries every
    /// IdentityFile in turn, and a stale entry in a Host block should not stop
    /// a working key later in the list from being offered.
    /// </remarks>
    private PrivateKeyAuthenticationMethod KeyMethod(
        string user, IReadOnlyList<string> paths, Guid serverId)
    {
        var files = new List<IPrivateKeySource>();
        foreach (var path in paths)
        {
            var expanded = SshConfig.ExpandPath(path);
            if (!File.Exists(expanded)) continue;
            try
            {
                var passphrase = _credentials.GetKeyPassphrase(serverId, expanded);
                files.Add(string.IsNullOrEmpty(passphrase)
                    ? new PrivateKeyFile(expanded)
                    : new PrivateKeyFile(expanded, passphrase));
            }
            catch (Exception error) when (error is Renci.SshNet.Common.SshException or SshPassPhraseNullOrEmptyException
                                              or IOException or UnauthorizedAccessException)
            {
                _log?.Invoke($"skipping key {expanded}: {error.Message}");
            }
        }
        return new PrivateKeyAuthenticationMethod(user, [.. files]);
    }

    /// <summary>
    /// What "agent" means on the library route: the key files the agent is
    /// most likely holding.
    /// </summary>
    /// <remarks>
    /// This is the S2 question D3 left open, and the answer is no: SSH.NET
    /// 2026.0.0 exports no agent authentication method at all, and it offers
    /// no hook for signing outside the library either — so driving the
    /// Windows agent's named pipe (<c>openssh-ssh-agent</c>; there is no
    /// <c>SSH_AUTH_SOCK</c> on Windows) would mean reimplementing
    /// authentication, not extending it.
    ///
    /// D3's stated fallback therefore applies: read the default key files
    /// directly, with any passphrase from the credential store. A host whose
    /// key genuinely only exists inside the agent — a smartcard, a forwarded
    /// agent — needs <see cref="OpenSshExeTransport"/>, which inherits the
    /// agent for free. That is exactly R13's "switch that host to the
    /// fallback transport", and the settings UI says so.
    /// </remarks>
    private PrivateKeyAuthenticationMethod AgentMethod(string user, Guid serverId) =>
        KeyMethod(user, SshConfig.DefaultKeyPaths().ToList(), serverId);

    /// <summary>
    /// Host-key checking with <c>accept-new</c> semantics: record an unknown
    /// key, refuse a changed one.
    /// </summary>
    /// <remarks>
    /// The default <c>ask</c> policy has nowhere to ask from in a background
    /// poll. <c>accept-new</c> is what the macOS build passes ssh, and for the
    /// same reason: it is the protection that actually matters after first
    /// contact, and it avoids the trap where an askpass helper answers the
    /// fingerprint question with the password and the connection hangs.
    /// </remarks>
    private void Arm(BaseClient client, string displayHost)
    {
        client.HostKeyReceived += (_, e) =>
        {
            var verdict = _knownHosts.Check(displayHost, e.HostKeyName, e.HostKey);
            switch (verdict)
            {
                case HostKeyVerdict.Known:
                    e.CanTrust = true;
                    break;
                case HostKeyVerdict.Unknown:
                    _knownHosts.Add(displayHost, e.HostKeyName, e.HostKey);
                    e.CanTrust = true;
                    break;
                default:
                    // Refuse rather than throw from the handler: SSH.NET turns
                    // CanTrust=false into a connection failure with its own
                    // message, and an exception thrown here would cross a
                    // socket callback.
                    e.CanTrust = false;
                    _log?.Invoke($"{displayHost}: host key CHANGED — refusing");
                    break;
            }
        };
    }

    public async Task DisconnectAsync(SshTarget target)
    {
        await EvictAsync(target.ServerId);
        await EvictSftpAsync(target.ServerId);
    }

    private async Task EvictAsync(Guid serverId)
    {
        await _poolLock.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_pool.Remove(serverId, out var pooled)) Close(pooled);
        }
        finally
        {
            _poolLock.Release();
        }
    }

    private void Close(Pooled pooled)
    {
        try
        {
            if (_owned.Remove(pooled.Client, out var owned)) Release(owned.Hops, owned.Forwards);
            pooled.Client.Disconnect();
            pooled.Client.Dispose();
        }
        catch (Exception)
        {
            // Closing a connection that is already gone is the normal case
            // here — it is why we are closing it.
        }
        pooled.Gate.Dispose();
    }

    /// <summary>
    /// Closes connections nothing has used for <see cref="IdleTimeout"/>.
    /// </summary>
    /// <remarks>
    /// Without this, deleting a host or turning off polling leaves its session
    /// open on the far side indefinitely — visible in <c>who</c> and holding a
    /// PAM session. The Go backend sweeps its cache the same way.
    /// </remarks>
    private async Task SweepAsync(CancellationToken token)
    {
        while (!token.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(60), token).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                return;
            }

            await _poolLock.WaitAsync(CancellationToken.None).ConfigureAwait(false);
            try
            {
                var now = DateTime.UtcNow;
                foreach (var (id, pooled) in _pool.ToList())
                {
                    if (now - pooled.LastUsed < IdleTimeout && pooled.Client.IsConnected) continue;
                    _pool.Remove(id);
                    Close(pooled);
                }
            }
            finally
            {
                _poolLock.Release();
            }

            await _sftpLock.WaitAsync(CancellationToken.None).ConfigureAwait(false);
            try
            {
                var now = DateTime.UtcNow;
                foreach (var (id, pooled) in _sftp.ToList())
                {
                    if (now - pooled.LastUsed < IdleTimeout && pooled.Client.IsConnected) continue;
                    _sftp.Remove(id);
                    CloseSftp(pooled);
                }
            }
            finally
            {
                _sftpLock.Release();
            }
        }
    }

    /// <summary>
    /// The live connection for a host, for the things that need the session
    /// itself rather than one command: the terminal's shell stream.
    /// </summary>
    public async Task<SshClient> LeaseClientAsync(
        SshTarget target, CancellationToken cancellationToken = default) =>
        (await LeaseAsync(target, cancellationToken).ConfigureAwait(false)).Client;

    /// <summary>
    /// A connected <see cref="SftpClient"/> for a host.
    /// </summary>
    /// <remarks>
    /// Its own pool, because SSH.NET's SftpClient is a client and not a
    /// channel factory: it cannot be built from the <see cref="SshClient"/>
    /// the poll is using, so file browsing is a second connection to the host.
    /// Pooled all the same — opening one per directory listing would be a
    /// handshake per click — and swept on the same idle timer, so closing the
    /// browser eventually closes the connection rather than holding it for the
    /// life of the app.
    /// </remarks>
    public async Task<SftpClient> LeaseSftpAsync(
        SshTarget target, CancellationToken cancellationToken = default)
    {
        var resolved = _config.Resolve(target);
        var fingerprint = resolved.Fingerprint;

        await _sftpLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            if (_sftp.TryGetValue(target.ServerId, out var existing))
            {
                if (existing.Fingerprint == fingerprint && existing.Client.IsConnected)
                {
                    existing.LastUsed = DateTime.UtcNow;
                    return existing.Client;
                }
                CloseSftp(existing);
                _sftp.Remove(target.ServerId);
            }

            var client = await ConnectAsync(
                resolved,
                target,
                info => new SftpClient(info) { KeepAliveInterval = TimeSpan.FromSeconds(15) },
                cancellationToken).ConfigureAwait(false);
            _sftp[target.ServerId] = new PooledSftp { Client = client, Fingerprint = fingerprint };
            return client;
        }
        finally
        {
            _sftpLock.Release();
        }
    }

    private sealed class PooledSftp
    {
        public required SftpClient Client { get; init; }
        public required string Fingerprint { get; init; }
        public DateTime LastUsed { get; set; } = DateTime.UtcNow;
    }

    private readonly Dictionary<Guid, PooledSftp> _sftp = [];
    private readonly SemaphoreSlim _sftpLock = new(1, 1);

    private void CloseSftp(PooledSftp pooled)
    {
        try
        {
            if (_owned.Remove(pooled.Client, out var owned)) Release(owned.Hops, owned.Forwards);
            pooled.Client.Disconnect();
            pooled.Client.Dispose();
        }
        catch (Exception)
        {
            // Same as Close: this runs because the connection is finished.
        }
    }

    private async Task EvictSftpAsync(Guid serverId)
    {
        await _sftpLock.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_sftp.Remove(serverId, out var pooled)) CloseSftp(pooled);
        }
        finally
        {
            _sftpLock.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (_disposed) return;
        _disposed = true;
        await _sweeper.CancelAsync().ConfigureAwait(false);
        await _poolLock.WaitAsync().ConfigureAwait(false);
        try
        {
            foreach (var pooled in _pool.Values) Close(pooled);
            _pool.Clear();
            foreach (var pooled in _sftp.Values) CloseSftp(pooled);
            _sftp.Clear();
        }
        finally
        {
            _poolLock.Release();
        }
        _sweeper.Dispose();
        _poolLock.Dispose();
    }
}
