namespace ServerMonitor.Core.Model;

/// <summary>
/// A reusable login: the username plus how it authenticates.
/// </summary>
/// <remarks>
/// Exists so a fleet sharing one key does not restate it per machine — a
/// server can point at an identity instead of carrying its own settings.
/// </remarks>
public sealed class Identity
{
    public Guid Id { get; set; } = Guid.NewGuid();
    public string Name { get; set; } = string.Empty;
    public string Username { get; set; } = string.Empty;
    public AuthKind AuthKind { get; set; } = AuthKind.IdentityFile;
    /// <summary>Used when <see cref="AuthKind"/> is IdentityFile.</summary>
    public string IdentityFile { get; set; } = string.Empty;
    public DateTime CreatedAt { get; set; } = DateTime.UtcNow;

    public string Summary => AuthKind switch
    {
        AuthKind.IdentityFile => IdentityFile.Length == 0
            ? Username
            : $"{Username} · {System.IO.Path.GetFileName(IdentityFile)}",
        AuthKind.Agent => $"{Username} · agent",
        // No hint of the secret, not even its length — the summary is shown
        // in the list and in every server row pointing at this identity.
        AuthKind.Password => $"{Username} · {(L10n.Strings.IsChinese ? "密码" : "password")}",
        _ => Username,
    };

    public Identity Clone() => (Identity)MemberwiseClone();
}

/// <summary>A named grouping of machines, e.g. "生产" or a customer's fleet.</summary>
public sealed class MachineGroup
{
    public Guid Id { get; set; } = Guid.NewGuid();
    public string Name { get; set; } = string.Empty;
    /// <summary>
    /// A name from <see cref="Palette"/> rather than a hex string, so the
    /// colour keeps its meaning across light and dark themes — the App maps
    /// the name to a brush per theme.
    /// </summary>
    public string ColorName { get; set; } = "blue";
    public int SortIndex { get; set; }
    public DateTime CreatedAt { get; set; } = DateTime.UtcNow;

    public static readonly string[] Palette =
        ["blue", "green", "orange", "purple", "pink", "teal", "red", "gray"];

    public MachineGroup Clone() => (MachineGroup)MemberwiseClone();
}

/// <summary>A saved command, runnable against any host or typed into a terminal.</summary>
public sealed class Snippet
{
    public Guid Id { get; set; } = Guid.NewGuid();
    public string Name { get; set; } = string.Empty;
    public string Command { get; set; } = string.Empty;
    public string Notes { get; set; } = string.Empty;
    /// <summary>Free-form grouping label, e.g. "nginx" or "诊断".</summary>
    public string Category { get; set; } = string.Empty;
    public DateTime CreatedAt { get; set; } = DateTime.UtcNow;
    /// <summary>Bumped on each run, so the list can surface what actually gets used.</summary>
    public int UseCount { get; set; }
    public DateTime? LastUsedAt { get; set; }

    /// <summary>First line, for the collapsed row in the list.</summary>
    public string Summary => Command.Lines().FirstOrDefault() ?? Command;

    public Snippet Clone() => (Snippet)MemberwiseClone();
}

public enum SessionKind { Terminal, Sftp }

/// <summary>One terminal or SFTP session, kept so the sidebar can show history.</summary>
public sealed class SessionRecord
{
    public Guid Id { get; set; } = Guid.NewGuid();
    public Guid? ServerId { get; set; }
    /// <summary>Denormalised so history survives the server being deleted.</summary>
    public string ServerName { get; set; } = string.Empty;
    public SessionKind Kind { get; set; }
    public DateTime StartedAt { get; set; } = DateTime.UtcNow;
    public DateTime? EndedAt { get; set; }

    public TimeSpan? Duration => EndedAt is { } ended ? ended - StartedAt : null;
    public bool IsOpen => EndedAt is null;

    public static string Store(SessionKind kind) => kind == SessionKind.Sftp ? "sftp" : "terminal";
    public static SessionKind ToKind(string? raw) =>
        raw == "sftp" ? SessionKind.Sftp : SessionKind.Terminal;
}
