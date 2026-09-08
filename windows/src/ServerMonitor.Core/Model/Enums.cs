namespace ServerMonitor.Core.Model;

/// <summary>
/// How a server authenticates.
/// </summary>
/// <remarks>
/// Stored as the same strings the macOS build writes, so a database can move
/// between the two. No case holds key material: private keys stay in
/// <c>%USERPROFILE%\.ssh</c> under OpenSSH's own ACLs, and a password lives in
/// the Windows credential manager (D4) — never in this app's store.
/// </remarks>
public enum AuthKind
{
    /// <summary>
    /// Use a Host block from <c>~/.ssh/config</c> verbatim — the whole entry
    /// applies, including ProxyJump and any per-host options.
    /// </summary>
    SshConfigAlias,
    /// <summary>An explicit private key file path.</summary>
    IdentityFile,
    /// <summary>Whatever the ssh agent offers.</summary>
    Agent,
    /// <summary>A password, kept in the credential manager.</summary>
    Password,
}

/// <summary>Which collection script a host understands.</summary>
public enum OSKind
{
    /// <summary>Try Linux, fall back to Windows, then remember which worked.</summary>
    Auto,
    Linux,
    Windows,
}

/// <summary>
/// The stored spelling of the two enums above, and the parse back.
/// </summary>
/// <remarks>
/// Hand-written rather than <c>Enum.Parse</c> for one reason that matters: an
/// unrecognised stored value must <em>fall back</em>, not throw. Row decoding
/// is all-or-nothing, so a single row written by an older build (or a future
/// one) would otherwise make the whole fetch fail and the app show no servers
/// at all, with nothing to explain why. The macOS side has the same guard in
/// <c>AuthKind.init(from:)</c>.
/// </remarks>
public static class EnumNames
{
    public static string Store(this AuthKind kind) => kind switch
    {
        AuthKind.SshConfigAlias => "sshConfigAlias",
        AuthKind.IdentityFile => "identityFile",
        AuthKind.Agent => "agent",
        AuthKind.Password => "password",
        _ => "sshConfigAlias",
    };

    public static AuthKind ToAuthKind(string? raw) => raw switch
    {
        "sshConfigAlias" => AuthKind.SshConfigAlias,
        "identityFile" => AuthKind.IdentityFile,
        "agent" => AuthKind.Agent,
        "password" => AuthKind.Password,
        _ => AuthKind.SshConfigAlias,
    };

    public static string Store(this OSKind kind) => kind switch
    {
        OSKind.Linux => "linux",
        OSKind.Windows => "windows",
        _ => "auto",
    };

    public static OSKind ToOSKind(string? raw) => raw switch
    {
        "linux" => OSKind.Linux,
        "windows" => OSKind.Windows,
        _ => OSKind.Auto,
    };
}
