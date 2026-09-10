namespace ServerMonitor.Core.Store;

/// <summary>
/// Where SSH passwords and key passphrases live.
/// </summary>
/// <remarks>
/// An interface in Core so nothing here depends on Windows: the real
/// implementation is <c>WindowsCredentialStore</c> in the App, which writes
/// generic credentials the user can see and delete in Control Panel — the
/// counterpart to the macOS build's keychain (D4). Tests use an in-memory one.
///
/// Private keys deliberately never come here: they stay in
/// <c>%USERPROFILE%\.ssh</c> under OpenSSH's own ACLs. A password has nowhere
/// else to live, so it goes to the credential manager rather than into this
/// app's database.
/// </remarks>
public interface ICredentialStore
{
    /// <summary>The SSH password for a server, or null when none is stored.</summary>
    string? GetPassword(Guid serverId);

    void SetPassword(Guid serverId, string password);

    void DeletePassword(Guid serverId);

    /// <summary>
    /// The passphrase for an encrypted private key.
    /// </summary>
    /// <remarks>
    /// Keyed by both server and key path: one key may be shared by a whole
    /// fleet through an identity, and the same server may change which key it
    /// uses. Implementations are free to fall back to a path-only entry so a
    /// phrase typed once covers every server using that key.
    /// </remarks>
    string? GetKeyPassphrase(Guid serverId, string keyPath);

    void SetKeyPassphrase(Guid serverId, string keyPath, string passphrase);

    /// <summary>The webhook a rule posts its transitions to, or null.</summary>
    /// <remarks>
    /// Keyed by the rule id because a rule is the thing that decides whether
    /// there is a webhook at all. Kept here rather than in the rule row for
    /// the same reason a password is: it is a credential-shaped value, and the
    /// user would not expect it to be readable from the database file.
    /// </remarks>
    string? GetWebhook(Guid ruleId);

    void SetWebhook(Guid ruleId, string url);

    void DeleteWebhook(Guid ruleId);
}

/// <summary>
/// A store that keeps nothing, for tests and the CLI.
/// </summary>
/// <remarks>
/// The CLI uses this deliberately: <c>smctl</c> is for checking the collection
/// chain against a real host, and it should reach hosts by key or agent rather
/// than pulling the user's saved passwords out of the credential manager.
/// </remarks>
public sealed class InMemoryCredentialStore : ICredentialStore
{
    private readonly Dictionary<Guid, string> _passwords = [];
    private readonly Dictionary<string, string> _passphrases = [];
    private readonly Dictionary<Guid, string> _webhooks = [];

    public string? GetPassword(Guid serverId) => _passwords.GetValueOrDefault(serverId);

    public void SetPassword(Guid serverId, string password) => _passwords[serverId] = password;

    public void DeletePassword(Guid serverId) => _passwords.Remove(serverId);

    public string? GetKeyPassphrase(Guid serverId, string keyPath) =>
        _passphrases.GetValueOrDefault($"{serverId}|{keyPath}")
        ?? _passphrases.GetValueOrDefault(keyPath);

    public void SetKeyPassphrase(Guid serverId, string keyPath, string passphrase) =>
        _passphrases[$"{serverId}|{keyPath}"] = passphrase;

    public string? GetWebhook(Guid ruleId) =>
        _webhooks.TryGetValue(ruleId, out var url) ? url : null;

    public void SetWebhook(Guid ruleId, string url) => _webhooks[ruleId] = url;

    public void DeleteWebhook(Guid ruleId) => _webhooks.Remove(ruleId);
}
