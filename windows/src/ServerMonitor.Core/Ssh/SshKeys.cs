using System.Diagnostics;
using System.Security.AccessControl;
using System.Security.Principal;

namespace ServerMonitor.Core.Ssh;

/// <summary>A private key found in the ssh directory.</summary>
public sealed record SshKeyFile(
    string Path,
    string Name,
    string Type,
    int Bits,
    string Fingerprint,
    string Comment,
    bool HasPublicKey,
    bool IsEncrypted,
    bool AclIsTight)
{
    /// <summary>
    /// Whether the algorithm is one modern sshd refuses.
    /// </summary>
    /// <remarks>
    /// DSA only. RSA still works because OpenSSH negotiates rsa-sha2-256/512
    /// — the SHA-1 <c>ssh-rsa</c> signature is what servers disabled, not the
    /// key type — so flagging RSA here would be wrong and alarming.
    /// </remarks>
    public bool IsLegacyAlgorithm => Type.Equals("DSA", StringComparison.OrdinalIgnoreCase);
}

/// <summary>
/// Inventories the private keys in the ssh directory by asking ssh-keygen
/// about them.
/// </summary>
/// <remarks>
/// Reads only metadata: fingerprints, types and comments, never key material.
/// </remarks>
public static class SshKeyScanner
{
    /// <summary>Filenames in the ssh directory that are never private keys.</summary>
    private static readonly HashSet<string> Ignored = new(StringComparer.OrdinalIgnoreCase)
    {
        "config", "known_hosts", "known_hosts.old", "authorized_keys", "environment", "rc",
        "agent-environment",
    };

    public static async Task<List<SshKeyFile>> ScanAsync(string? directory = null)
    {
        directory ??= SshConfig.SshDirectory;
        var results = new List<SshKeyFile>();

        string[] names;
        try
        {
            names = Directory.GetFiles(directory);
        }
        catch (Exception)
        {
            // No .ssh directory yet is the normal case on a fresh machine.
            return results;
        }

        foreach (var path in names.OrderBy(p => p, StringComparer.OrdinalIgnoreCase))
        {
            var name = System.IO.Path.GetFileName(path);
            if (Ignored.Contains(name)) continue;
            if (name.EndsWith(".pub", StringComparison.OrdinalIgnoreCase)) continue;
            if (name.StartsWith('.')) continue;

            var described = await DescribeAsync(path, name).ConfigureAwait(false);
            if (described is not null) results.Add(described);
        }
        return results;
    }

    /// <summary>
    /// <c>ssh-keygen -l -f</c> prints "&lt;bits&gt; &lt;fingerprint&gt;
    /// &lt;comment&gt; (&lt;TYPE&gt;)".
    /// </summary>
    internal static async Task<SshKeyFile?> DescribeAsync(string path, string name)
    {
        var keygen = SshLocator.FindKeygen();
        if (keygen is null) return null;

        string output;
        try
        {
            output = await OpenSshExeTransport.ExecuteAsync(
                keygen, ["-l", "-f", path], 5, null, CancellationToken.None).ConfigureAwait(false);
        }
        catch (SshException)
        {
            // Not a key — a stray file in the directory — or an unreadable
            // one. Either way it is not shown rather than shown broken.
            return null;
        }

        if (Parse(output) is not { } parsed) return null;
        return new SshKeyFile(
            path,
            name,
            parsed.Type,
            parsed.Bits,
            parsed.Fingerprint,
            parsed.Comment,
            File.Exists(path + ".pub"),
            IsEncrypted(path),
            // ACLs are a Windows concept; everywhere else OpenSSH checks a
            // permission bitmask, and Core stays platform-neutral.
            !OperatingSystem.IsWindows() || SshKeyManager.AclIsTight(path));
    }

    internal static (int Bits, string Fingerprint, string Comment, string Type)? Parse(string output)
    {
        var line = output.Trim();
        if (line.Length == 0) return null;
        var fields = line.Fields();
        if (fields.Length < 3 || fields[0].ToIntOrNull() is not { } bits) return null;

        var fingerprint = fields[1];
        // The type is the parenthesised last field; whatever sits between it
        // and the fingerprint is the comment.
        var type = string.Empty;
        var comment = fields.Skip(2).ToList();
        if (comment.Count > 0
            && comment[^1].StartsWith('(')
            && comment[^1].EndsWith(')'))
        {
            type = comment[^1][1..^1];
            comment.RemoveAt(comment.Count - 1);
        }
        return (bits, fingerprint, string.Join(" ", comment), type);
    }

    /// <summary>
    /// Detects a passphrase without prompting for one.
    /// </summary>
    /// <remarks>
    /// An OpenSSH key stores its cipher name in the <em>decoded</em> body, so
    /// the base64 has to be decoded first — searching the armoured text for
    /// "none" matches nothing and reports every key as encrypted.
    /// </remarks>
    internal static bool IsEncrypted(string path)
    {
        string text;
        try
        {
            text = File.ReadAllText(path);
        }
        catch (Exception)
        {
            return false;
        }

        // Classic PEM keys announce it in the headers.
        if (text.Contains("Proc-Type:", StringComparison.Ordinal)
            && text.Contains("ENCRYPTED", StringComparison.Ordinal))
        {
            return true;
        }
        if (!text.Contains("OPENSSH PRIVATE KEY", StringComparison.Ordinal)) return false;

        var body = string.Concat(text.Lines().Where(l => !l.StartsWith("-----", StringComparison.Ordinal)));
        byte[] bytes;
        try
        {
            bytes = Convert.FromBase64String(body);
        }
        catch (FormatException)
        {
            return false;
        }

        // Layout: "openssh-key-v1\0" then a uint32-length-prefixed cipher name.
        var magic = System.Text.Encoding.ASCII.GetBytes("openssh-key-v1\0");
        if (bytes.Length <= magic.Length + 4) return false;
        if (!bytes.AsSpan(0, magic.Length).SequenceEqual(magic)) return false;

        var offset = magic.Length;
        var length = (bytes[offset] << 24) | (bytes[offset + 1] << 16)
            | (bytes[offset + 2] << 8) | bytes[offset + 3];
        offset += 4;
        if (length <= 0 || offset + length > bytes.Length) return false;
        var cipher = System.Text.Encoding.ASCII.GetString(bytes, offset, length);
        return cipher != "none";
    }

    /// <summary>Public key text, for copying into a host's authorized_keys.</summary>
    public static string? PublicKey(SshKeyFile key)
    {
        try
        {
            return File.ReadAllText(key.Path + ".pub").Trim();
        }
        catch (Exception)
        {
            return null;
        }
    }
}

/// <summary>
/// Creates and imports private keys, always into the ssh directory.
/// </summary>
/// <remarks>
/// Writing to the directory OpenSSH already owns — rather than a private store
/// inside the app — means a key made here works immediately in
/// <c>ssh.exe</c>, in <c>~/.ssh/config</c>, and for every other tool on the
/// machine.
/// </remarks>
public static class SshKeyManager
{
    public enum KeyType { Ed25519, Rsa4096 }

    private static string[] Arguments(KeyType type) => type switch
    {
        KeyType.Rsa4096 => ["-t", "rsa", "-b", "4096"],
        _ => ["-t", "ed25519"],
    };

    /// <summary>
    /// A filename safe to place in the ssh directory and to pass to a process.
    /// </summary>
    /// <remarks>
    /// The name reaches the filesystem and ssh-keygen's argument list, so
    /// traversal and metacharacters must never get through. A leading dot
    /// would hide the key; ".pub" would collide with the public half the
    /// generator writes.
    /// </remarks>
    public static bool IsValidName(string name)
    {
        var trimmed = name.Trim();
        if (trimmed.Length == 0 || trimmed.Length > 64) return false;
        if (trimmed.StartsWith('.')) return false;
        if (trimmed.EndsWith(".pub", StringComparison.OrdinalIgnoreCase)) return false;
        return trimmed.All(c => char.IsAsciiLetterOrDigit(c) || c is '.' or '_' or '-');
    }

    private static string Destination(string name)
    {
        if (!IsValidName(name))
        {
            throw new ArgumentException(
                "The key name may only contain letters, digits, dot, dash and underscore.",
                nameof(name));
        }
        var path = System.IO.Path.Combine(SshConfig.SshDirectory, name.Trim());
        if (File.Exists(path))
        {
            throw new IOException($"{path} already exists.");
        }
        return path;
    }

    /// <summary>
    /// Generates a key pair with ssh-keygen.
    /// </summary>
    /// <remarks>
    /// An empty passphrase is passed explicitly with <c>-N ""</c> so
    /// ssh-keygen never drops into its interactive prompt, which a GUI app
    /// cannot answer.
    /// </remarks>
    public static async Task<string> GenerateAsync(
        string name, KeyType type, string comment, string passphrase)
    {
        var keygen = SshLocator.FindKeygen()
            ?? throw new InvalidOperationException(
                "No ssh-keygen.exe found. Install it with: "
                + "Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0");

        var path = Destination(name);
        EnsureDirectory();

        var arguments = new List<string>(Arguments(type));
        arguments.AddRange(["-f", path, "-N", passphrase, "-q"]);
        if (comment.Trim().Length > 0) arguments.AddRange(["-C", comment.Trim()]);

        await OpenSshExeTransport.ExecuteAsync(
            keygen, arguments, 60, null, CancellationToken.None).ConfigureAwait(false);

        if (!File.Exists(path))
        {
            throw new IOException("ssh-keygen reported success but wrote no key.");
        }
        RestrictPermissions(path);
        return path;
    }

    /// <summary>Saves pasted or loaded key text into the ssh directory.</summary>
    public static string ImportKey(string text, string name)
    {
        var trimmed = text.Trim();
        if (!LooksLikePrivateKey(trimmed))
        {
            throw new ArgumentException("That text is not an OpenSSH private key.", nameof(text));
        }
        var path = Destination(name);
        EnsureDirectory();
        File.WriteAllText(path, trimmed + "\n");
        // Tightened immediately rather than afterwards, so the key is never
        // briefly readable by other users.
        RestrictPermissions(path);
        return path;
    }

    /// <summary>
    /// Copies an existing key file in, keeping its public half if present.
    /// </summary>
    public static string ImportFile(string source, string? name = null)
    {
        var text = File.ReadAllText(source);
        var path = ImportKey(text, name ?? System.IO.Path.GetFileName(source));

        var publicSource = source + ".pub";
        if (File.Exists(publicSource))
        {
            try
            {
                File.Copy(publicSource, path + ".pub", overwrite: true);
            }
            catch (Exception)
            {
                // The private half is what matters; the public one can be
                // regenerated with `ssh-keygen -y`.
            }
        }
        return path;
    }

    internal static bool LooksLikePrivateKey(string text) =>
        text.Contains("-----BEGIN", StringComparison.Ordinal)
        && text.Contains("PRIVATE KEY-----", StringComparison.Ordinal);

    private static void EnsureDirectory()
    {
        var directory = SshConfig.SshDirectory;
        if (Directory.Exists(directory)) return;
        Directory.CreateDirectory(directory);
        RestrictPermissions(directory);
    }

    /// <summary>
    /// Removes inherited ACLs so only this user can read the key (R11).
    /// </summary>
    /// <remarks>
    /// The Windows equivalent of the macOS build's <c>chmod 600</c>, and not
    /// cosmetic: Windows OpenSSH checks the ACL, not a permission bitmask, and
    /// refuses a key other users can read — with an error message that does
    /// not say so.
    ///
    /// Done by shelling out to <c>icacls</c> rather than through
    /// <c>System.Security.AccessControl</c> so Core stays free of Windows-only
    /// APIs, which is the whole point of it being a portable assembly.
    /// </remarks>
    public static bool RestrictPermissions(string path)
    {
        var user = Environment.UserName;
        if (user.Length == 0) return false;
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
            if (process is null) return false;
            process.WaitForExit(5000);
            return process.HasExited && process.ExitCode == 0;
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>
    /// Whether a key's ACL is narrow enough that ssh will use it.
    /// </summary>
    /// <remarks>
    /// OpenSSH's rule on Windows is "no access for anyone but the owner,
    /// SYSTEM and Administrators", so this reads the ACL and compares
    /// <em>SIDs</em>. Two reasons not to parse <c>icacls</c>'s text, both of
    /// which this code got wrong before:
    ///
    /// Its first line is <c>&lt;path&gt; DOMAIN\\principal:(perms)</c>, and a
    /// Windows path contains a colon. Splitting on the first one made the
    /// principal <c>"C"</c>, which matches nobody — so every key on every
    /// ordinary machine was reported as loose, with a banner offering to
    /// "tighten" permissions that were already right, and a button that would
    /// have stripped inheritance from the user's real keys.
    ///
    /// And the names are localised: <c>Administrators</c> is
    /// <c>Administratoren</c> on a German Windows, so a name allowlist is
    /// wrong there even once the parsing is fixed. A well-known SID is the
    /// same everywhere.
    /// </remarks>
    [System.Runtime.Versioning.SupportedOSPlatform("windows")]
    public static bool AclIsTight(string path)
    {
        try
        {
            var rules = new FileInfo(path)
                .GetAccessControl(AccessControlSections.Access)
                .GetAccessRules(includeExplicit: true, includeInherited: true, typeof(SecurityIdentifier));

            var me = WindowsIdentity.GetCurrent().User;
            foreach (FileSystemAccessRule rule in rules)
            {
                // A deny entry never widens access, and ssh does not object to
                // one.
                if (rule.AccessControlType != AccessControlType.Allow) continue;
                if (rule.IdentityReference is not SecurityIdentifier sid) continue;
                if (me is not null && sid.Equals(me)) continue;
                if (sid.IsWellKnown(WellKnownSidType.LocalSystemSid)) continue;
                if (sid.IsWellKnown(WellKnownSidType.BuiltinAdministratorsSid)) continue;
                // Anyone else — Users, Authenticated Users, a group the file
                // inherited the entry from — is what makes ssh refuse it.
                return false;
            }
            return true;
        }
        catch (Exception)
        {
            // An unreadable ACL is not evidence of a loose one. Reporting
            // "tight" keeps a warning off every key just because the file
            // could not be examined.
            return true;
        }
    }

    /// <summary>Deletes a key and its public half.</summary>
    public static void Delete(SshKeyFile key)
    {
        File.Delete(key.Path);
        var publicPath = key.Path + ".pub";
        if (File.Exists(publicPath))
        {
            try
            {
                File.Delete(publicPath);
            }
            catch (Exception)
            {
                // The private half is gone, which is what the user asked for.
            }
        }
    }
}
