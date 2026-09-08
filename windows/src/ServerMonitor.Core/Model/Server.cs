using ServerMonitor.Core.Ssh;

namespace ServerMonitor.Core.Model;

/// <summary>
/// A monitored host.
/// </summary>
/// <remarks>
/// A value carrier, not a live database object: the UI holds copies and writes
/// go through <see cref="Store.Database"/>. Mutable rather than a record with
/// <c>with</c> expressions because the editor binds two-way to these
/// properties.
/// </remarks>
public sealed class Server
{
    /// <summary>Also the credential-manager account name for this server's secret.</summary>
    public Guid Id { get; set; } = Guid.NewGuid();
    public string Name { get; set; } = string.Empty;
    public string Host { get; set; } = string.Empty;
    public int Port { get; set; } = 22;
    public string Username { get; set; } = string.Empty;
    public AuthKind AuthKind { get; set; } = AuthKind.SshConfigAlias;
    /// <summary>ssh config Host alias, used when <see cref="AuthKind"/> is SshConfigAlias.</summary>
    public string SshAlias { get; set; } = string.Empty;
    /// <summary>Private key path, used when <see cref="AuthKind"/> is IdentityFile.</summary>
    public string IdentityFile { get; set; } = string.Empty;
    /// <summary>Points at a shared <see cref="Identity"/>; when set, its username and auth win.</summary>
    public Guid? IdentityId { get; set; }
    /// <summary>Optional <see cref="MachineGroup"/> membership.</summary>
    public Guid? GroupId { get; set; }
    public OSKind OsKind { get; set; } = OSKind.Auto;

    // Per-server alert limits. null means "use the global setting", which is
    // a different thing from 0, meaning "no alert for this metric".
    public int? CpuThreshold { get; set; }
    public int? MemoryThreshold { get; set; }
    public int? DiskThreshold { get; set; }

    public string Notes { get; set; } = string.Empty;
    /// <summary>
    /// ISO 3166-1 alpha-2, rendered as a flag on the dashboard card. Empty
    /// when the user has not set one.
    /// </summary>
    public string CountryCode { get; set; } = string.Empty;
    /// <summary>Comma-separated storage for <see cref="Tags"/>; use that instead.</summary>
    public string TagList { get; set; } = string.Empty;
    public DateTime CreatedAt { get; set; } = DateTime.UtcNow;
    /// <summary>Ordering in the sidebar and on the dashboard.</summary>
    public int SortIndex { get; set; }

    // Host facts, refreshed by the collector rather than typed by the user.
    public int Cores { get; set; }
    public long MemoryTotal { get; set; }
    public long DiskTotal { get; set; }
    public string DockerVersion { get; set; } = string.Empty;

    public bool HasDocker => DockerVersion.Length > 0;

    /// <summary>The credential in the form the ssh layer wants.</summary>
    public SshCredential Credential => AuthKind switch
    {
        AuthKind.IdentityFile => SshCredential.Key(IdentityFile),
        AuthKind.Agent => SshCredential.Agent,
        AuthKind.Password => SshCredential.Password,
        _ => SshCredential.ConfigAlias,
    };

    /// <summary>
    /// Free-text labels. Order and case are preserved as typed, but duplicates
    /// and blanks are dropped so the chips never repeat.
    /// </summary>
    public IReadOnlyList<string> Tags
    {
        get => ParseTags(TagList);
        set => TagList = string.Join(",", ParseTags(string.Join(",", value)));
    }

    public static List<string> ParseTags(string? raw)
    {
        var result = new List<string>();
        if (string.IsNullOrEmpty(raw)) return result;
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var piece in raw.Split([',', '\n', '\r'], StringSplitOptions.RemoveEmptyEntries))
        {
            var tag = piece.Trim();
            if (tag.Length == 0) continue;
            if (seen.Add(tag)) result.Add(tag);
        }
        return result;
    }

    /// <summary>Flag emoji for <see cref="CountryCode"/>, or "" when unset or malformed.</summary>
    /// <remarks>
    /// Kept for parity with the macOS model, and not for drawing on Windows:
    /// no Windows font has glyphs for regional-indicator pairs, so this
    /// renders as two boxed capitals rather than a flag. The Windows views use
    /// <c>Ui.CountryBadge</c> with <see cref="CountryCode"/> instead.
    /// </remarks>
    public string Flag => Format.Flag(CountryCode);

    /// <summary>
    /// "user@host", with the port appended only when it is not the default.
    /// </summary>
    public string DisplayTarget
    {
        get
        {
            if (AuthKind == AuthKind.SshConfigAlias && SshAlias.Length > 0)
            {
                return Host.Length == 0 ? SshAlias : $"{SshAlias} · {Username}@{Host}";
            }
            return Port == 22 ? $"{Username}@{Host}" : $"{Username}@{Host}:{Port}";
        }
    }

    public Server Clone() => (Server)MemberwiseClone();
}
