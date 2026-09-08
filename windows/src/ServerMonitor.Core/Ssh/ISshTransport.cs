namespace ServerMonitor.Core.Ssh;

/// <summary>
/// Running commands on a remote host.
/// </summary>
/// <remarks>
/// Two implementations, one interface (D3):
/// <list type="bullet">
/// <item><see cref="SshNetTransport"/> — the default. One long-lived SSH.NET
/// connection per host, which is what stands in for OpenSSH's ControlMaster.
/// Windows' own OpenSSH does not implement ControlMaster at all (F1), so the
/// subprocess route pays a full handshake and login on every poll: at a 5 s
/// interval that is ~17,000 sshd logins per host per day in auth.log, wtmp and
/// PAM. The macOS build keeps that under 300 with
/// <c>ControlPersist=300</c>.</item>
/// <item><see cref="OpenSshExeTransport"/> — the fallback, for hosts SSH.NET
/// cannot negotiate with or that need ssh config features the library route
/// does not implement (Match blocks, certificates, PKCS#11 — R13).</item>
/// </list>
/// Selected globally in settings and overridable per host.
/// </remarks>
public interface ISshTransport : IAsyncDisposable
{
    /// <summary>
    /// Runs <paramref name="command"/> on the target and returns stdout.
    /// </summary>
    /// <remarks>
    /// Partial output is not an error: a host where one <c>cat</c> failed
    /// should still report the rest, so a non-zero exit with usable stdout
    /// comes back normally. A non-zero exit with <em>nothing</em> on stdout
    /// throws <see cref="SshException"/>.
    /// </remarks>
    Task<string> RunAsync(
        string command,
        SshTarget target,
        int timeoutSeconds = 30,
        CancellationToken cancellationToken = default);

    /// <summary>
    /// Drops whatever this transport is holding for a host — after its
    /// settings changed, or it was deleted.
    /// </summary>
    Task DisconnectAsync(SshTarget target);

    /// <summary>A short name for logs and the settings UI.</summary>
    string Name { get; }
}
