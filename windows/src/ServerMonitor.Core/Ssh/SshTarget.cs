namespace ServerMonitor.Core.Ssh;

/// <summary>
/// How a connection authenticates.
/// </summary>
/// <remarks>
/// The password case carries no payload on purpose: the secret then cannot end
/// up in a log line, a crash dump, or anything that prints an
/// <see cref="SshTarget"/>. Only the credential store's <em>key</em> travels
/// here, and a Guid is not a secret.
/// </remarks>
public readonly record struct SshCredential(
    AuthMethod Method, string KeyPath, Guid SecretOwner = default)
{
    public static readonly SshCredential ConfigAlias = new(AuthMethod.ConfigAlias, "");
    public static readonly SshCredential Agent = new(AuthMethod.Agent, "");

    /// <summary>A password belonging to the server itself.</summary>
    public static readonly SshCredential Password = new(AuthMethod.Password, "");

    /// <summary>
    /// A password belonging to an identity, and so to every host using it.
    /// </summary>
    /// <remarks>
    /// The whole point of an identity is that one login covers a fleet, so its
    /// password is stored once under the identity's own id rather than copied
    /// to each server. Changing it updates every machine; deleting the
    /// identity takes exactly one secret with it.
    /// </remarks>
    public static SshCredential SharedPassword(Guid identityId) =>
        new(AuthMethod.Password, "", identityId);

    public static SshCredential Key(string path) => new(AuthMethod.IdentityFile, path);

    /// <summary>
    /// Which credential-store entry holds this password.
    /// </summary>
    /// <remarks>
    /// An unset owner means the server's own entry, which is what every
    /// server-level password is and what the store was keyed by before
    /// identities could carry one.
    /// </remarks>
    public Guid SecretKeyFor(Guid serverId) =>
        SecretOwner == default ? serverId : SecretOwner;
}

public enum AuthMethod { ConfigAlias, IdentityFile, Agent, Password }

/// <summary>Everything needed to reach one host.</summary>
public sealed record SshTarget(
    Guid ServerId,
    string Host,
    int Port,
    string Username,
    SshCredential Credential)
{
    /// <summary>
    /// What to hand ssh.exe as its destination argument.
    /// </summary>
    /// <remarks>
    /// For an alias this is the alias alone, so OpenSSH applies the whole
    /// matching Host block — HostName, User, Port, IdentityFile, ProxyJump and
    /// anything else the user configured. The SSH.NET transport resolves the
    /// same block itself (<see cref="SshConfig"/>), since it has no OpenSSH to
    /// defer to.
    /// </remarks>
    public string SshDestination =>
        Credential.Method == AuthMethod.ConfigAlias ? Host : $"{Username}@{Host}";
}

/// <summary>
/// Something went wrong reaching or running on a host.
/// </summary>
/// <remarks>
/// One exception type with a <see cref="Kind"/> rather than a hierarchy: every
/// caller either shows <see cref="Exception.Message"/> or asks one question
/// about the kind (is this a probe answering "not Linux"? is this a dead
/// multiplex socket worth one retry?).
/// </remarks>
public sealed class SshException : Exception
{
    public SshFailure Kind { get; }
    /// <summary>
    /// The remote command's exit status, when a shell answered at all.
    /// </summary>
    /// <remarks>
    /// Load-bearing for OS detection: ssh reserves 255 for its own errors, so
    /// a <em>different</em> non-zero status means a shell answered and simply
    /// had no <c>uname</c> — that is a Windows host. A 255, a timeout or a
    /// failed launch means no shell was ever reached, and guessing "windows"
    /// there sends an unreachable host through a second 10 s connect timeout
    /// and reports the failure in Windows terms.
    /// </remarks>
    public int ExitStatus { get; }
    public string Stderr { get; }

    public SshException(SshFailure kind, string message, int exitStatus = 0, string stderr = "")
        : base(message)
    {
        Kind = kind;
        ExitStatus = exitStatus;
        Stderr = stderr;
    }

    public static SshException LaunchFailed(string message) =>
        new(SshFailure.LaunchFailed, $"Could not start ssh: {message}");

    public static SshException TimedOut(int seconds) =>
        new(SshFailure.TimedOut, $"ssh timed out after {seconds}s");

    public static SshException CommandFailed(int status, string stderr)
    {
        var essence = Essence(stderr);
        var message = essence.Length > 0
            ? essence
            : status == 255
                ? "ssh exited 255 with no message"
                : $"remote command exited {status}";
        return new SshException(SshFailure.CommandFailed, message, status, stderr);
    }

    /// <summary>
    /// A failure whose whole story is its text — an empty batch, unparsable
    /// Windows output. Not called <c>Message</c>: that would hide
    /// <see cref="Exception.Message"/>.
    /// </summary>
    public static SshException Failed(string message) =>
        new(SshFailure.CommandFailed, message);

    public static SshException AuthFailed(string message) =>
        new(SshFailure.AuthFailed, message);

    public static SshException HostKeyChanged(string host) =>
        new(SshFailure.HostKeyChanged,
            $"Host key changed for {host} — remove the old entry from %USERPROFILE%\\.ssh\\known_hosts if this is expected");

    public static SshException MissingCredential() =>
        new(SshFailure.MissingCredential, "This server has no usable SSH credential.");

    /// <summary>
    /// The line of stderr worth showing.
    /// </summary>
    /// <remarks>
    /// sshd prints the host's login banner to stderr before anything else, so
    /// on a host with one every failure began "Authorized users only…" and the
    /// actual reason was buried. The reason is the last line. A changed host
    /// key is the exception: its warning is a paragraph and the last line
    /// alone ("Host key verification failed.") loses the part that matters.
    /// </remarks>
    internal static string Essence(string stderr)
    {
        if (stderr.Contains("REMOTE HOST IDENTIFICATION HAS CHANGED", StringComparison.Ordinal))
        {
            return "Host key changed — remove the old entry from %USERPROFILE%\\.ssh\\known_hosts if this is expected";
        }
        var lines = stderr.Lines().Select(l => l.Trim()).Where(l => l.Length > 0).ToList();
        return lines.Count > 0 ? lines[^1] : string.Empty;
    }

    /// <summary>
    /// ssh exiting 255 without a word is how a multiplex client reports that
    /// its master vanished. Every real connection error carries a message.
    /// </summary>
    internal bool IsDeadMultiplexSocket =>
        Kind == SshFailure.CommandFailed && ExitStatus == 255 && Stderr.Trim().Length == 0;
}

public enum SshFailure
{
    LaunchFailed,
    TimedOut,
    CommandFailed,
    AuthFailed,
    HostKeyChanged,
    MissingCredential,
}
