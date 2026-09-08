using System.Diagnostics;
using System.Text;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Core.Ssh;

/// <summary>
/// Finds the system OpenSSH client.
/// </summary>
public static class SshLocator
{
    /// <summary>
    /// <c>ssh.exe</c>, or null when no usable one is installed.
    /// </summary>
    /// <remarks>
    /// The order matters. The GitHub build in <c>%ProgramFiles%\OpenSSH</c> is
    /// newer than the in-box one and is what a user who installed it expects
    /// to be used; <c>System32\OpenSSH</c> is the in-box copy (F3), 9.5p1 or
    /// later since the 2024-10 cumulative update.
    ///
    /// PATH is searched last and filtered, because Git for Windows puts a
    /// Cygwin-built <c>ssh.exe</c> on it. That build has its own idea of what
    /// a path is — <c>/c/Users/...</c> — and its own home directory, so it
    /// reads a different <c>known_hosts</c> and a different config than
    /// everything else on the machine.
    /// </remarks>
    public static string? Find()
    {
        foreach (var candidate in Candidates())
        {
            if (File.Exists(candidate)) return candidate;
        }
        return null;
    }

    internal static IEnumerable<string> Candidates()
    {
        var programFiles = Environment.GetEnvironmentVariable("ProgramFiles");
        if (!string.IsNullOrEmpty(programFiles))
        {
            yield return Path.Combine(programFiles, "OpenSSH", "ssh.exe");
        }
        var system = Environment.GetFolderPath(Environment.SpecialFolder.System);
        if (!string.IsNullOrEmpty(system))
        {
            yield return Path.Combine(system, "OpenSSH", "ssh.exe");
        }
        foreach (var directory in (Environment.GetEnvironmentVariable("PATH") ?? "")
                 .Split(Path.PathSeparator, StringSplitOptions.RemoveEmptyEntries))
        {
            if (IsCygwinBuild(directory)) continue;
            yield return Path.Combine(directory.Trim(), "ssh.exe");
        }
    }

    /// <summary>
    /// Whether a PATH entry is Git for Windows' Cygwin-flavoured toolchain.
    /// </summary>
    internal static bool IsCygwinBuild(string directory)
    {
        var normalised = directory.Replace('/', '\\').ToLowerInvariant();
        return normalised.Contains("\\git\\usr\\bin")
            || normalised.Contains("\\git\\bin")
            || normalised.Contains("\\cygwin");
    }

    /// <summary><c>ssh-keygen.exe</c> beside whichever ssh.exe was found.</summary>
    public static string? FindKeygen()
    {
        var ssh = Find();
        if (ssh is null) return null;
        var keygen = Path.Combine(Path.GetDirectoryName(ssh)!, "ssh-keygen.exe");
        return File.Exists(keygen) ? keygen : null;
    }
}

/// <summary>
/// The fallback transport: one <c>ssh.exe</c> per command, no reuse.
/// </summary>
/// <remarks>
/// For hosts <see cref="SshNetTransport"/> cannot serve — an algorithm it
/// will not negotiate, a key format it does not read, or ssh config features
/// the library route does not implement: <c>Match</c> blocks, certificates,
/// PKCS#11, agent forwarding (R13). Real OpenSSH inherits all of it.
///
/// The cost is R2, and it is why this is not the default: there is no
/// connection reuse to be had. Windows' OpenSSH does not implement
/// ControlMaster (F1) — the options are accepted, no master is built, and a
/// user config carrying <c>ControlMaster</c> under <c>Host *</c> makes
/// ssh.exe fail outright with <c>getsockname failed: Not a socket</c>. So
/// <c>ControlMaster=no</c> and <c>ControlPath=none</c> are passed explicitly
/// on every call, to override whatever the user's config says rather than to
/// turn off something we wanted.
/// </remarks>
public sealed class OpenSshExeTransport : ISshTransport
{
    public string Name => "ssh.exe";

    private readonly ICredentialStore _credentials;
    private readonly string _executable;
    private readonly Action<string>? _log;

    /// <summary>
    /// One command per host at a time.
    /// </summary>
    /// <remarks>
    /// Without this, a host slow to answer piles up a process per tick behind
    /// the one already running. <see cref="MonitorService"/>'s in-flight set
    /// covers polls; this covers everything else (Docker refreshes, a card's
    /// own fetch) landing on the same host at once.
    /// </remarks>
    private readonly Dictionary<Guid, SemaphoreSlim> _gates = [];
    private readonly object _gateLock = new();

    public OpenSshExeTransport(
        ICredentialStore credentials, string? executable = null, Action<string>? log = null)
    {
        _credentials = credentials;
        _executable = executable
            ?? SshLocator.Find()
            ?? throw new InvalidOperationException(
                "No OpenSSH client found. Install it with: "
                + "Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0");
        _log = log;
    }

    /// <summary>
    /// The argument list for a destination, without the remote command.
    /// </summary>
    internal static List<string> BaseArguments(SshTarget target)
    {
        var arguments = new List<string>
        {
            // Never prompt: a GUI app has nowhere to show a password prompt,
            // and a hung ssh would stall the poll loop.
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=10",
            "-o", "ServerAliveInterval=15",
            "-o", "ServerAliveCountMax=2",
            // Trust on first use, refuse a change. See KnownHosts for why the
            // default `ask` policy cannot be used from a background poll.
            "-o", "StrictHostKeyChecking=accept-new",
            // F1: not a preference. A user config with ControlMaster under
            // `Host *` makes ssh.exe fail before it opens a socket, so these
            // override it rather than disable something wanted.
            "-o", "ControlMaster=no",
            "-o", "ControlPath=none",
        };

        switch (target.Credential.Method)
        {
            case AuthMethod.ConfigAlias:
                // Everything else comes from the config for this alias.
                break;

            case AuthMethod.IdentityFile:
                arguments.AddRange(["-i", target.Credential.KeyPath, "-o", "IdentitiesOnly=yes"]);
                break;

            case AuthMethod.Agent:
                arguments.AddRange(["-o", "PreferredAuthentications=publickey"]);
                break;

            case AuthMethod.Password:
                // BatchMode suppresses every prompt including the askpass
                // helper, so password auth has to switch it off and lean on
                // the helper plus a hard timeout instead.
                RemoveOption("BatchMode=yes", arguments);
                arguments.AddRange([
                    "-o", "BatchMode=no",
                    "-o", "PreferredAuthentications=password,keyboard-interactive",
                    "-o", "NumberOfPasswordPrompts=1",
                ]);
                break;
        }

        // For an alias the port belongs to the Host block; overriding it here
        // would silently ignore what the user configured.
        if (target.Port != 22 && target.Credential.Method != AuthMethod.ConfigAlias)
        {
            arguments.AddRange(["-p", target.Port.ToString(System.Globalization.CultureInfo.InvariantCulture)]);
        }
        return arguments;
    }

    /// <summary>
    /// Removes an <c>-o name=value</c> pair — both elements.
    /// </summary>
    /// <remarks>
    /// Dropping only the value leaves a dangling <c>-o</c>, and ssh exits with
    /// "no argument after keyword" before it ever opens a socket.
    /// </remarks>
    internal static void RemoveOption(string value, List<string> arguments)
    {
        var index = arguments.IndexOf(value);
        if (index <= 0 || arguments[index - 1] != "-o") return;
        arguments.RemoveRange(index - 1, 2);
    }

    public async Task<string> RunAsync(
        string command,
        SshTarget target,
        int timeoutSeconds = 30,
        CancellationToken cancellationToken = default)
    {
        var gate = GateFor(target.ServerId);
        await gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            var arguments = BaseArguments(target);
            arguments.Add(target.SshDestination);
            arguments.Add(command);

            using var askpass = Askpass.Create(target, _credentials);
            return await ExecuteAsync(
                _executable, arguments, timeoutSeconds, askpass?.Environment, cancellationToken)
                .ConfigureAwait(false);
        }
        finally
        {
            gate.Release();
        }
    }

    private SemaphoreSlim GateFor(Guid serverId)
    {
        lock (_gateLock)
        {
            if (_gates.TryGetValue(serverId, out var gate)) return gate;
            gate = new SemaphoreSlim(1, 1);
            _gates[serverId] = gate;
            return gate;
        }
    }

    /// <summary>
    /// Nothing to disconnect: this transport holds no connection. Present so
    /// the two are interchangeable behind <see cref="ISshTransport"/>.
    /// </summary>
    public Task DisconnectAsync(SshTarget target)
    {
        lock (_gateLock)
        {
            if (_gates.Remove(target.ServerId, out var gate)) gate.Dispose();
        }
        return Task.CompletedTask;
    }

    /// <summary>
    /// Runs a subprocess, returning stdout, with a hard timeout.
    /// </summary>
    /// <remarks>
    /// Both pipes are read concurrently: a full stderr buffer would otherwise
    /// deadlock a process still writing stdout — which sshd's login banner
    /// makes a real case, not a theoretical one.
    ///
    /// Output is decoded as UTF-8 explicitly (R5). The console code page on a
    /// Chinese Windows is GBK, and trusting the default encoding turns every
    /// non-ASCII byte a host sends into mojibake — including the paths in a
    /// <c>df</c> listing and the process names in <c>ps</c>.
    /// </remarks>
    internal static async Task<string> ExecuteAsync(
        string executable,
        IReadOnlyList<string> arguments,
        int timeoutSeconds,
        IReadOnlyDictionary<string, string>? environment,
        CancellationToken cancellationToken)
    {
        var info = new ProcessStartInfo(executable)
        {
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            RedirectStandardInput = true,
            UseShellExecute = false,
            CreateNoWindow = true,
            StandardOutputEncoding = Encoding.UTF8,
            StandardErrorEncoding = Encoding.UTF8,
        };
        foreach (var argument in arguments) info.ArgumentList.Add(argument);
        if (environment is not null)
        {
            foreach (var (key, value) in environment) info.Environment[key] = value;
        }

        using var process = new Process { StartInfo = info };
        try
        {
            if (!process.Start()) throw SshException.LaunchFailed("process did not start");
        }
        catch (Exception error) when (error is not SshException)
        {
            throw SshException.LaunchFailed(error.Message);
        }

        // Nothing may inherit this app's stdin; ssh must not try to read from
        // it, and closing the pipe is how it is told there is nothing there.
        process.StandardInput.Close();

        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(TimeSpan.FromSeconds(timeoutSeconds));

        var stdoutTask = process.StandardOutput.ReadToEndAsync(CancellationToken.None);
        var stderrTask = process.StandardError.ReadToEndAsync(CancellationToken.None);

        var timedOut = false;
        try
        {
            await process.WaitForExitAsync(timeout.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            timedOut = !cancellationToken.IsCancellationRequested;
            try { process.Kill(entireProcessTree: true); } catch (Exception) { /* already gone */ }
            // Let the reads finish against the now-closed pipes rather than
            // abandoning them, which would leak both tasks.
            await Task.WhenAll(stdoutTask, stderrTask).ConfigureAwait(false);
            cancellationToken.ThrowIfCancellationRequested();
        }

        var stdout = await stdoutTask.ConfigureAwait(false);
        var stderr = await stderrTask.ConfigureAwait(false);

        // Before accepting partial stdout: a timed-out metrics command can
        // have printed a plausible-looking prefix, and returning it would
        // silently publish a half-read snapshot as if it were current.
        if (timedOut) throw SshException.TimedOut(timeoutSeconds);

        if (process.ExitCode != 0 && stdout.Trim().Length == 0)
        {
            throw SshException.CommandFailed(process.ExitCode, stderr);
        }
        return stdout;
    }

    public ValueTask DisposeAsync()
    {
        lock (_gateLock)
        {
            foreach (var gate in _gates.Values) gate.Dispose();
            _gates.Clear();
        }
        return ValueTask.CompletedTask;
    }

    /// <summary>
    /// A throwaway <c>SSH_ASKPASS</c> helper that feeds ssh a stored password.
    /// </summary>
    /// <remarks>
    /// ssh will not read a password from a pipe, and putting one on the
    /// command line would expose it in the process list. The helper is a
    /// one-line batch file that prints the contents of a file only this user
    /// can read; both live in a directory deleted as soon as the command
    /// returns.
    ///
    /// <c>SSH_ASKPASS_REQUIRE=force</c> is not optional: without it ssh
    /// prefers a terminal prompt when one exists, and on Windows 11's 8.6p1
    /// the helper is only consulted with it set (F2). Windows ships no
    /// askpass program of its own, which is why we provide one.
    /// </remarks>
    internal sealed class Askpass : IDisposable
    {
        public required Dictionary<string, string> Environment { get; init; }
        public required string Directory { get; init; }

        public static Askpass? Create(SshTarget target, ICredentialStore credentials)
        {
            if (target.Credential.Method != AuthMethod.Password) return null;
            var password = credentials.GetPassword(target.ServerId);
            if (string.IsNullOrEmpty(password)) return null;

            var directory = Path.Combine(Path.GetTempPath(), $"sm-askpass-{Guid.NewGuid():N}");
            System.IO.Directory.CreateDirectory(directory);
            Harden(directory);

            var secretPath = Path.Combine(directory, "secret");
            // No BOM and no trailing newline: ssh takes the helper's whole
            // first line as the password, and a BOM would become part of it.
            File.WriteAllText(secretPath, password, new UTF8Encoding(false));
            Harden(secretPath);

            var helperPath = Path.Combine(directory, "askpass.cmd");
            // The secret's *path* travels in the environment; the secret
            // itself never appears in an argument list. `set /p` reads one
            // line without echoing anything else.
            File.WriteAllText(
                helperPath,
                "@echo off\r\n"
                + "set /p SM_PW=<\"%SM_SECRET%\"\r\n"
                + "echo %SM_PW%\r\n",
                new UTF8Encoding(false));
            Harden(helperPath);

            return new Askpass
            {
                Directory = directory,
                Environment = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase)
                {
                    ["SSH_ASKPASS"] = helperPath,
                    ["SSH_ASKPASS_REQUIRE"] = "force",
                    ["SM_SECRET"] = secretPath,
                    // ssh checks DISPLAY before consulting an askpass helper
                    // on some builds; a value costs nothing and removes a
                    // difference between them.
                    ["DISPLAY"] = "localhost:0",
                },
            };
        }

        /// <summary>
        /// Strips inherited ACLs so only this user can read the secret.
        /// </summary>
        /// <remarks>
        /// The Windows counterpart of the macOS build's 0600, and the same
        /// mechanism R11 needs for private keys: Windows OpenSSH checks ACLs,
        /// not a permission bitmask. Done by shelling out to
        /// <c>icacls</c> rather than through <c>System.Security.AccessControl</c>
        /// so Core stays free of Windows-only APIs — the whole point of it
        /// being a portable assembly.
        /// </remarks>
        internal static void Harden(string path)
        {
            var user = System.Environment.GetEnvironmentVariable("USERNAME");
            if (string.IsNullOrEmpty(user)) return;
            try
            {
                using var process = Process.Start(new ProcessStartInfo("icacls")
                {
                    ArgumentList = { path, "/inheritance:r", "/grant:r", $"{user}:(F)" },
                    UseShellExecute = false,
                    CreateNoWindow = true,
                    RedirectStandardOutput = true,
                    RedirectStandardError = true,
                });
                process?.WaitForExit(5000);
            }
            catch (Exception)
            {
                // Best effort. The file is in the user's own temp directory,
                // which is already not world-readable on a default install;
                // this narrows it further where it can.
            }
        }

        public void Dispose()
        {
            try
            {
                System.IO.Directory.Delete(Directory, recursive: true);
            }
            catch (Exception)
            {
                // A temp directory that will not delete is not worth failing a
                // poll over; Windows cleans %TEMP% eventually.
            }
        }
    }
}
