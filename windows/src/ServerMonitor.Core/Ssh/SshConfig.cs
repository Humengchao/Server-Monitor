namespace ServerMonitor.Core.Ssh;

/// <summary>
/// A Host block resolved into what the library route actually needs.
/// </summary>
public sealed record ResolvedHost
{
    public required string HostName { get; init; }
    public required int Port { get; init; }
    public required string User { get; init; }
    public required AuthMethod Method { get; init; }
    /// <summary>The single key, when the server names one explicitly.</summary>
    public string IdentityFile { get; init; } = string.Empty;
    /// <summary>Every IdentityFile the Host block named, in order.</summary>
    public IReadOnlyList<string> IdentityFiles { get; init; } = [];
    /// <summary>Jump hosts, outermost first.</summary>
    public IReadOnlyList<string> ProxyJump { get; init; } = [];

    /// <summary>
    /// Everything a pooled connection was built for.
    /// </summary>
    /// <remarks>
    /// Compared on every lease, so editing a host's address, port, user or
    /// credential cannot leave a poll quietly running on the old session.
    /// </remarks>
    public string Fingerprint =>
        $"{HostName}:{Port}|{User}|{Method}|{IdentityFile}|{string.Join(",", IdentityFiles)}|{string.Join(",", ProxyJump)}";
}

/// <summary>
/// A host entry discovered in the user's ssh config, for the importer.
/// </summary>
public sealed record SshConfigHost(
    string Alias,
    string HostName,
    string User,
    int Port,
    string? IdentityFile,
    string ProxyJump = "")
{
    /// <summary>True when the key file exists and can be read.</summary>
    public bool HasReadableKey
    {
        get
        {
            if (string.IsNullOrEmpty(IdentityFile)) return false;
            try
            {
                return File.Exists(IdentityFile);
            }
            catch (Exception)
            {
                // An unreadable path is "no key", not a crash in the importer.
                return false;
            }
        }
    }
}

/// <summary>
/// Reads <c>%USERPROFILE%\.ssh\config</c>.
/// </summary>
/// <remarks>
/// Two jobs. For the importer it lists hosts so existing ones can be adopted
/// without retyping them. For <see cref="SshNetTransport"/> it does the work
/// OpenSSH would have done, since the library has no config support of its
/// own: resolve an alias to a HostName, User, Port, IdentityFile list and
/// ProxyJump chain.
///
/// A deliberately small subset of the format, which is R13: <c>Host</c> blocks
/// with <c>HostName</c>, <c>User</c>, <c>Port</c>, <c>IdentityFile</c> and
/// <c>ProxyJump</c>. <c>Match</c> blocks, certificates, PKCS#11 and agent
/// forwarding are not supported here — a host that needs them belongs on the
/// <see cref="OpenSshExeTransport"/> route, which defers to real OpenSSH.
/// </remarks>
public sealed class SshConfig
{
    /// <summary>Alias (lowercased) to its settings.</summary>
    private readonly Dictionary<string, Dictionary<string, List<string>>> _blocks;

    private SshConfig(Dictionary<string, Dictionary<string, List<string>>> blocks) => _blocks = blocks;

    public static string DefaultConfigPath =>
        Path.Combine(SshDirectory, "config");

    /// <summary>
    /// <c>%USERPROFILE%\.ssh</c> — the directory OpenSSH itself owns.
    /// </summary>
    /// <remarks>
    /// Writing keys here rather than into a private store inside the app means
    /// a key made by this app works immediately in <c>ssh.exe</c>, in
    /// <c>~/.ssh/config</c> and for every other tool on the machine.
    /// </remarks>
    public static string SshDirectory =>
        Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.UserProfile),
            ".ssh");

    public static SshConfig FromDefaultLocation() => FromFile(DefaultConfigPath);

    public static SshConfig FromFile(string path)
    {
        try
        {
            return Parse(File.ReadAllText(path));
        }
        catch (Exception)
        {
            // No config is the normal case on a fresh machine.
            return new SshConfig([]);
        }
    }

    public static SshConfig Parse(string text)
    {
        var blocks = new Dictionary<string, Dictionary<string, List<string>>>(StringComparer.OrdinalIgnoreCase);
        List<string> currentAliases = [];

        foreach (var rawLine in text.KeepEmptyLines())
        {
            var line = rawLine.Trim();
            if (line.Length == 0 || line.StartsWith('#')) continue;
            // Keys may be separated by whitespace or '=' — the latter appears
            // in generated configs.
            var parts = line.Split([' ', '\t', '='], StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length < 2) continue;
            var key = parts[0].ToLowerInvariant();
            var value = string.Join(" ", parts.Skip(1));

            if (key == "host")
            {
                // One Host line can list several patterns. Wildcard-only
                // entries describe defaults rather than a machine, so they are
                // not importable — but their settings still have to be kept
                // out of the previous block.
                currentAliases = value.Split(' ', StringSplitOptions.RemoveEmptyEntries)
                    .Where(a => !a.Contains('*') && !a.Contains('?'))
                    .ToList();
                foreach (var alias in currentAliases)
                {
                    blocks.TryAdd(alias, new Dictionary<string, List<string>>(StringComparer.Ordinal));
                }
                continue;
            }

            foreach (var alias in currentAliases)
            {
                var settings = blocks[alias];
                if (!settings.TryGetValue(key, out var values))
                {
                    values = [];
                    settings[key] = values;
                }
                // IdentityFile may legitimately repeat; everything else takes
                // the first, which is what OpenSSH does.
                values.Add(value);
            }
        }
        return new SshConfig(blocks);
    }

    /// <summary>The importable hosts: those with a plain, non-wildcard alias.</summary>
    public List<SshConfigHost> Discover()
    {
        var result = new List<SshConfigHost>();
        foreach (var (alias, settings) in _blocks)
        {
            string First(string key) =>
                settings.TryGetValue(key, out var values) && values.Count > 0 ? values[0] : string.Empty;

            var identity = First("identityfile");
            result.Add(new SshConfigHost(
                alias,
                // Without a HostName the alias itself is the address.
                First("hostname") is { Length: > 0 } host ? host : alias,
                First("user") is { Length: > 0 } user ? user : "root",
                First("port").ToIntOrNull() ?? 22,
                identity.Length > 0 ? ExpandPath(identity) : null,
                First("proxyjump")));
        }
        return result.OrderBy(h => h.Alias, StringComparer.OrdinalIgnoreCase).ToList();
    }

    /// <summary>
    /// What the library route should actually dial for a target.
    /// </summary>
    /// <remarks>
    /// For an alias, the Host block wins for anything it names — that is the
    /// whole point of choosing "ssh config alias" as the auth kind. For every
    /// other kind the stored row wins, and the config is not consulted.
    /// </remarks>
    public ResolvedHost Resolve(SshTarget target)
    {
        if (target.Credential.Method != AuthMethod.ConfigAlias)
        {
            return new ResolvedHost
            {
                HostName = target.Host,
                Port = target.Port,
                User = target.Username,
                Method = target.Credential.Method,
                IdentityFile = target.Credential.KeyPath,
                IdentityFiles = target.Credential.KeyPath.Length > 0 ? [target.Credential.KeyPath] : [],
            };
        }

        var resolved = Resolve(target.Host);
        // A row may still carry a username and port worth using when the block
        // is silent about them.
        return resolved with
        {
            User = resolved.User.Length > 0 ? resolved.User
                : target.Username.Length > 0 ? target.Username
                : "root",
            Port = resolved.Port > 0 ? resolved.Port : target.Port,
        };
    }

    /// <summary>Resolves one alias (also used for each ProxyJump hop).</summary>
    public ResolvedHost Resolve(string alias)
    {
        // A hop may be written "user@host:port" rather than as an alias.
        var (inlineUser, inlineHost, inlinePort) = SplitHopSpec(alias);
        var lookup = inlineHost;

        if (!_blocks.TryGetValue(lookup, out var settings))
        {
            return new ResolvedHost
            {
                HostName = lookup,
                Port = inlinePort ?? 22,
                User = inlineUser ?? string.Empty,
                Method = AuthMethod.ConfigAlias,
            };
        }

        string First(string key) =>
            settings.TryGetValue(key, out var values) && values.Count > 0 ? values[0] : string.Empty;
        List<string> All(string key) =>
            settings.TryGetValue(key, out var values) ? values : [];

        var identityFiles = All("identityfile").Select(ExpandPath).ToList();
        return new ResolvedHost
        {
            HostName = First("hostname") is { Length: > 0 } host ? host : lookup,
            Port = inlinePort ?? (First("port").ToIntOrNull() ?? 22),
            User = inlineUser ?? First("user"),
            Method = AuthMethod.ConfigAlias,
            IdentityFiles = identityFiles,
            ProxyJump = First("proxyjump")
                .Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
                .Where(j => j != "none")
                .ToList(),
        };
    }

    /// <summary>Pulls an optional user and port out of a <c>ProxyJump</c> spec.</summary>
    internal static (string? User, string Host, int? Port) SplitHopSpec(string spec)
    {
        var rest = spec.Trim();
        string? user = null;
        var at = rest.LastIndexOf('@');
        if (at >= 0)
        {
            user = rest[..at];
            rest = rest[(at + 1)..];
        }
        int? port = null;
        var colon = rest.LastIndexOf(':');
        // Only a trailing numeric segment is a port; an IPv6 literal has
        // several colons and none of them mean this.
        if (colon > 0 && rest.IndexOf(':') == colon && rest[(colon + 1)..].ToIntOrNull() is { } parsed)
        {
            port = parsed;
            rest = rest[..colon];
        }
        return (user, rest, port);
    }

    /// <summary>
    /// The key names OpenSSH tries when nothing names one explicitly, in its
    /// own order of preference.
    /// </summary>
    public static IEnumerable<string> DefaultKeyPaths()
    {
        string[] names = ["id_ed25519", "id_ecdsa", "id_rsa", "id_dsa"];
        foreach (var name in names)
        {
            var path = Path.Combine(SshDirectory, name);
            if (File.Exists(path)) yield return path;
        }
    }

    /// <summary>
    /// Expands <c>~</c> and quotes in a config path.
    /// </summary>
    /// <remarks>
    /// A config written on a Mac (which is how these arrive, per the plan's
    /// step 3) uses <c>~/.ssh/…</c> and forward slashes. Both work on Windows
    /// once the tilde is replaced — .NET accepts forward slashes throughout —
    /// so nothing rewrites the separators.
    /// </remarks>
    public static string ExpandPath(string path)
    {
        var unquoted = path.Trim().Trim('"', '\'');
        if (unquoted.StartsWith('~'))
        {
            var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            unquoted = home + unquoted[1..];
        }
        return unquoted;
    }
}
