using Renci.SshNet;
using Renci.SshNet.Sftp;

namespace ServerMonitor.Core.Ssh;

/// <summary>One remote file or directory.</summary>
/// <param name="Name">The last path component.</param>
/// <param name="Path">The full remote path.</param>
/// <param name="Size">Bytes; meaningless for a directory.</param>
public sealed record SftpEntry(
    string Name,
    string Path,
    bool IsDirectory,
    bool IsSymlink,
    long Size,
    DateTime Modified,
    string Permissions)
{
    public bool IsHidden => Name.StartsWith('.');
}

/// <summary>How far a transfer has got.</summary>
public sealed record TransferProgress(string Name, long Transferred, long Total)
{
    public double Fraction => Total > 0 ? Math.Clamp((double)Transferred / Total, 0, 1) : 0;
}

/// <summary>
/// Remote path arithmetic, and the Windows-side naming it has to survive.
/// </summary>
/// <remarks>
/// Static and pure so it can be tested without a host — which matters more
/// here than usual, because the interesting cases are the ones a developer
/// does not have lying around: a file called <c>aux</c>, a directory whose
/// name ends in a space, a name with a colon in it. All three are ordinary on
/// Linux and all three are unrepresentable on Windows.
/// </remarks>
public static class SftpPath
{
    /// <summary>Joins remote path segments with forward slashes.</summary>
    public static string Combine(string directory, string name)
    {
        if (name.StartsWith('/')) return name;
        if (directory.Length == 0) return "/" + name.TrimStart('/');
        return directory.TrimEnd('/') + "/" + name;
    }

    /// <summary>The containing directory, or "/" at the root.</summary>
    public static string Parent(string path)
    {
        var trimmed = path.TrimEnd('/');
        var cut = trimmed.LastIndexOf('/');
        if (cut <= 0) return "/";
        return trimmed[..cut];
    }

    public static string Name(string path)
    {
        var trimmed = path.TrimEnd('/');
        var cut = trimmed.LastIndexOf('/');
        return cut < 0 ? trimmed : trimmed[(cut + 1)..];
    }

    /// <summary>Collapses "." and ".." and repeated slashes.</summary>
    /// <remarks>
    /// So typing <c>/var/log/../lib</c> into the path box goes where the user
    /// meant, and so a ".." at the root cannot walk above it.
    /// </remarks>
    public static string Normalise(string path)
    {
        var absolute = path.StartsWith('/');
        var parts = new List<string>();
        foreach (var part in path.Split('/', StringSplitOptions.RemoveEmptyEntries))
        {
            switch (part)
            {
                case ".":
                    break;
                case "..":
                    if (parts.Count > 0) parts.RemoveAt(parts.Count - 1);
                    break;
                default:
                    parts.Add(part);
                    break;
            }
        }
        var joined = string.Join('/', parts);
        return absolute ? "/" + joined : joined;
    }

    /// <summary>Whether a name can be created remotely.</summary>
    public static bool IsValidName(string name) =>
        name.Length > 0
        && name != "."
        && name != ".."
        && !name.Contains('/')
        && !name.Contains('\0');

    private static readonly char[] ForbiddenOnWindows = ['<', '>', ':', '"', '/', '\\', '|', '?', '*'];

    /// <summary>
    /// A filename Windows will accept, for saving a remote file locally.
    /// </summary>
    /// <remarks>
    /// Windows rejects nine characters, every code point below 32, a trailing
    /// dot or space, and the DOS device names — <c>CON</c>, <c>PRN</c>,
    /// <c>AUX</c>, <c>NUL</c>, <c>COM1</c>–<c>COM9</c>, <c>LPT1</c>–<c>LPT9</c>
    /// — with or without an extension. None of that constrains a Linux host,
    /// so a download has to rename rather than fail: the substitution is
    /// visible in the save dialog, and the user can change it there.
    /// </remarks>
    public static string LocalNameFor(string remoteName)
    {
        var name = Name(remoteName);
        if (name.Length == 0) return "download";

        var characters = name.Select(c =>
            ForbiddenOnWindows.Contains(c) || char.IsControl(c) ? '_' : c).ToArray();
        name = new string(characters).TrimEnd(' ', '.');
        if (name.Length == 0) return "download";

        var stem = name;
        var dot = name.IndexOf('.');
        if (dot > 0) stem = name[..dot];
        if (ReservedNames.Contains(stem)) name = "_" + name;

        // NTFS allows 255 UTF-16 units per component; a longer remote name is
        // legal on ext4 as 255 *bytes*, so this is not unreachable.
        return name.Length > 255 ? name[..255] : name;
    }

    private static readonly HashSet<string> ReservedNames = new(StringComparer.OrdinalIgnoreCase)
    {
        "CON", "PRN", "AUX", "NUL",
        "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
        "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
    };

    /// <summary>The mode bits as <c>ls -l</c> would print them.</summary>
    public static string Permissions(SftpFile file)
    {
        var flags = new char[10];
        flags[0] = file.IsDirectory ? 'd' : file.IsSymbolicLink ? 'l' : '-';
        flags[1] = file.OwnerCanRead ? 'r' : '-';
        flags[2] = file.OwnerCanWrite ? 'w' : '-';
        flags[3] = file.OwnerCanExecute ? 'x' : '-';
        flags[4] = file.GroupCanRead ? 'r' : '-';
        flags[5] = file.GroupCanWrite ? 'w' : '-';
        flags[6] = file.GroupCanExecute ? 'x' : '-';
        flags[7] = file.OthersCanRead ? 'r' : '-';
        flags[8] = file.OthersCanWrite ? 'w' : '-';
        flags[9] = file.OthersCanExecute ? 'x' : '-';
        return new string(flags);
    }
}

/// <summary>
/// Browsing and moving files over SFTP.
/// </summary>
/// <remarks>
/// The macOS build shells out — <c>ls -l</c> parsed by hand, then <c>scp</c>
/// per file. This uses SSH.NET's <c>SftpClient</c> instead, which is the one
/// place the Windows client is straightforwardly better off: no locale-
/// dependent <c>ls</c> output to parse, no quoting of paths through a shell,
/// and a progress callback rather than a subprocess to watch.
///
/// The client itself is leased rather than owned, so the connection is pooled
/// with everything else the app holds for that host and closed by the same
/// idle sweep.
/// </remarks>
public sealed class SftpBrowser(Func<SshTarget, CancellationToken, Task<SftpClient>> lease)
{
    /// <summary>Where a host puts you when you log in.</summary>
    public async Task<string> HomeAsync(SshTarget target, CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        // WorkingDirectory is what the server reported at connect; on a host
        // where it is somehow empty, "/" is a directory that always exists.
        var home = client.WorkingDirectory;
        return string.IsNullOrEmpty(home) ? "/" : home;
    }

    /// <summary>
    /// One directory, directories first then files, each group by name.
    /// </summary>
    /// <remarks>
    /// Ordinal-ignore-case rather than the current culture: the same listing
    /// must not reorder itself when the app's language changes, and a Turkish
    /// locale's dotless i would do exactly that to a directory of hostnames.
    /// </remarks>
    public async Task<List<SftpEntry>> ListAsync(
        SshTarget target,
        string path,
        bool showHidden = false,
        CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        var listing = await client.ListDirectoryAsync(path, cancellationToken)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);

        var entries = new List<SftpEntry>();
        foreach (var file in listing.OfType<SftpFile>())
        {
            if (file.Name is "." or "..") continue;
            var entry = new SftpEntry(
                file.Name,
                SftpPath.Combine(path, file.Name),
                file.IsDirectory,
                file.IsSymbolicLink,
                file.IsDirectory ? 0 : file.Length,
                file.LastWriteTime,
                SftpPath.Permissions(file));
            if (!showHidden && entry.IsHidden) continue;
            entries.Add(entry);
        }

        entries.Sort((left, right) =>
            left.IsDirectory != right.IsDirectory
                ? (left.IsDirectory ? -1 : 1)
                : string.Compare(left.Name, right.Name, StringComparison.OrdinalIgnoreCase));
        return entries;
    }

    public async Task<bool> IsDirectoryAsync(
        SshTarget target, string path, CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        try
        {
            return client.GetAttributes(path).IsDirectory;
        }
        catch (Exception)
        {
            return false;
        }
    }

    /// <summary>
    /// Copies a remote file to a local path.
    /// </summary>
    /// <remarks>
    /// Written to a <c>.part</c> file and moved into place at the end, so an
    /// interrupted download cannot be mistaken for a complete one — the
    /// failure mode that matters when the thing being copied is a backup.
    /// </remarks>
    public async Task DownloadAsync(
        SshTarget target,
        string remotePath,
        string localPath,
        IProgress<TransferProgress>? progress = null,
        CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        var total = client.GetAttributes(remotePath).Size;
        var name = SftpPath.Name(remotePath);
        var temporary = localPath + ".part";

        try
        {
            await using (var file = File.Create(temporary))
            {
                await Task.Factory.FromAsync(
                    client.BeginDownloadFile(
                        remotePath,
                        file,
                        null,
                        null,
                        downloaded => progress?.Report(
                            new TransferProgress(name, (long)downloaded, total))),
                    client.EndDownloadFile).ConfigureAwait(false);
            }
            File.Move(temporary, localPath, overwrite: true);
        }
        catch
        {
            try { File.Delete(temporary); } catch (Exception) { /* nothing to clean up */ }
            throw;
        }
    }

    public async Task UploadAsync(
        SshTarget target,
        string localPath,
        string remotePath,
        IProgress<TransferProgress>? progress = null,
        CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        var info = new FileInfo(localPath);
        var name = info.Name;

        await using var file = File.OpenRead(localPath);
        await Task.Factory.FromAsync(
            client.BeginUploadFile(
                file,
                remotePath,
                true,
                null,
                null,
                uploaded => progress?.Report(
                    new TransferProgress(name, (long)uploaded, info.Length))),
            client.EndUploadFile).ConfigureAwait(false);
    }

    public async Task CreateDirectoryAsync(
        SshTarget target, string path, CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        client.CreateDirectory(path);
    }

    public async Task RenameAsync(
        SshTarget target, string from, string to, CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        client.RenameFile(from, to);
    }

    /// <summary>
    /// Deletes a file, or a directory and everything under it.
    /// </summary>
    /// <remarks>
    /// The recursion is ours rather than the server's: SFTP has no recursive
    /// remove, and the alternative — <c>rm -rf</c> through an exec channel —
    /// means quoting a user-supplied path into a shell command, which is one
    /// stray quote away from deleting the wrong thing.
    /// </remarks>
    public async Task DeleteAsync(
        SshTarget target, string path, CancellationToken cancellationToken = default)
    {
        var client = await lease(target, cancellationToken).ConfigureAwait(false);
        await DeleteAsync(client, path, cancellationToken).ConfigureAwait(false);
    }

    private static async Task DeleteAsync(
        SftpClient client, string path, CancellationToken cancellationToken)
    {
        var attributes = client.GetAttributes(path);
        // A symlink to a directory is deleted as a link: following it would
        // delete the target's contents, which is not what "delete this link"
        // means anywhere else.
        if (!attributes.IsDirectory || attributes.IsSymbolicLink)
        {
            client.DeleteFile(path);
            return;
        }

        var children = await client.ListDirectoryAsync(path, cancellationToken)
            .ToListAsync(cancellationToken)
            .ConfigureAwait(false);
        foreach (var child in children)
        {
            if (child.Name is "." or "..") continue;
            await DeleteAsync(client, SftpPath.Combine(path, child.Name), cancellationToken)
                .ConfigureAwait(false);
        }
        client.DeleteDirectory(path);
    }
}
