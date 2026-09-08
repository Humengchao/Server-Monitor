namespace ServerMonitor.Core.Ssh;

public enum InstallOutcome
{
    Added,
    /// <summary>The key body was already there — possibly under a different comment.</summary>
    AlreadyPresent,
}

/// <summary>
/// Appends a public key to a host's <c>authorized_keys</c>, the way
/// <c>ssh-copy-id</c> does — but over the connection the app already has, so
/// it works for hosts reached through a ProxyJump or a config alias.
/// </summary>
public sealed class PublicKeyInstaller(ISshTransport transport)
{
    public async Task<InstallOutcome> InstallAsync(
        string publicKey, SshTarget target, CancellationToken token = default)
    {
        var key = publicKey.Trim();
        if (!IsPublicKey(key))
        {
            throw new ArgumentException("That does not look like an OpenSSH public key", nameof(publicKey));
        }
        var output = await transport.RunAsync(Command(key), target, 20, token).ConfigureAwait(false);
        var text = output.Trim();
        if (text.Contains("SM_ADDED", StringComparison.Ordinal)) return InstallOutcome.Added;
        if (text.Contains("SM_ALREADY", StringComparison.Ordinal)) return InstallOutcome.AlreadyPresent;
        throw SshException.Failed(
            $"The host did not confirm the change: {(text.Length == 0 ? "(no output)" : text[..Math.Min(200, text.Length)])}");
    }

    /// <summary>
    /// One command, idempotent, and careful about permissions.
    /// </summary>
    /// <remarks>
    /// sshd silently ignores an <c>authorized_keys</c> that is group- or
    /// world-writable, which is a maddening way for this to "work" and change
    /// nothing — hence the explicit chmods.
    ///
    /// The presence check is on the key <em>body</em> rather than the whole
    /// line, so re-exporting the same key under a different comment does not
    /// append a duplicate.
    /// </remarks>
    internal static string Command(string key)
    {
        var quoted = key.ShellQuote();
        return $"K={quoted}; "
            + "B=$(printf '%s' \"$K\" | awk '{print $2}'); "
            + "mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && "
            + "chmod 600 ~/.ssh/authorized_keys && "
            + "if [ -n \"$B\" ] && grep -qF \"$B\" ~/.ssh/authorized_keys; then echo SM_ALREADY; "
            + "else printf '%s\\n' \"$K\" >> ~/.ssh/authorized_keys && echo SM_ADDED; fi";
    }

    /// <summary>
    /// Whether text is an OpenSSH public key.
    /// </summary>
    /// <remarks>
    /// Rejects a private key outright: pasting one here would copy it to a
    /// remote host, which is the opposite of what this feature is for.
    /// </remarks>
    public static bool IsPublicKey(string text)
    {
        var trimmed = text.Trim();
        if (trimmed.Contains("PRIVATE KEY", StringComparison.Ordinal)) return false;
        var fields = trimmed.Fields();
        if (fields.Length < 2) return false;
        string[] algorithms =
        [
            "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256",
            "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com",
            "sk-ecdsa-sha2-nistp256@openssh.com", "ssh-dss",
        ];
        if (!algorithms.Contains(fields[0])) return false;
        // The body is base64; a line that is one word plus junk is not a key.
        return fields[1].Length > 32
            && fields[1].All(c => char.IsLetterOrDigit(c) || c is '+' or '/' or '=');
    }
}
