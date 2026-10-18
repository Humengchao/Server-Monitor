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

    /// <summary>Reserves distinct Windows names without overwriting existing files.</summary>
    public static List<string> LocalDownloadPaths(string directory, IEnumerable<string> remoteNames)
    {
        var used = new HashSet<string>(Directory.EnumerateFileSystemEntries(directory)
            .Select(Path.GetFileName)!, StringComparer.OrdinalIgnoreCase);
        var result = new List<string>();
        foreach (var remote in remoteNames)
        {
            var original = LocalNameFor(remote);
            var name = original;
            for (var suffix = 2; !used.Add(name); suffix++)
            {
                var extension = Path.GetExtension(original);
                if (extension.Length > 100) extension = extension[..100];
                var stem = Path.GetFileNameWithoutExtension(original);
                var tail = $" ({suffix}){extension}";
                name = stem[..Math.Min(stem.Length, 255 - tail.Length)] + tail;
            }
            result.Add(Path.Combine(directory, name));
        }
        return result;
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
public sealed class SftpBrowser(Func<SshTarget, CancellationToken, Task<SftpLease>> lease)
{
    /// <summary>Where a host puts you when you log in.</summary>
    public async Task<string> HomeAsync(SshTarget target, CancellationToken cancellationToken = default)
    {
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        var client = operation.Client;
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
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        var client = operation.Client;
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
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        var client = operation.Client;
        try
        {
            return (await client.GetAttributesAsync(path, cancellationToken).ConfigureAwait(false)).IsDirectory;
        }
        catch (Exception error) when (error is not OperationCanceledException)
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
        CancellationToken cancellationToken = default,
        bool overwrite = true)
    {
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        var client = operation.Client;
        var total = (await client.GetAttributesAsync(remotePath, cancellationToken).ConfigureAwait(false)).Size;
        var name = SftpPath.Name(remotePath);
        var temporary = Path.Combine(Path.GetDirectoryName(Path.GetFullPath(localPath))!, $".sm-download-{Guid.NewGuid():N}.part");
        var created = false;

        try
        {
            await using (var file = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None,
                64 * 1024, FileOptions.Asynchronous))
            {
                created = true;
                await client.DownloadFileAsync(remotePath, file,
                    new InlineProgress<DownloadFileProgressReport>(report => progress?.Report(
                        new TransferProgress(name, (long)report.TotalBytesDownloaded, total))),
                    cancellationToken).ConfigureAwait(false);
            }
            cancellationToken.ThrowIfCancellationRequested();
            File.Move(temporary, localPath, overwrite);
        }
        catch
        {
            if (created) try { File.Delete(temporary); } catch (IOException) { /* preserve original failure */ }
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
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        var client = operation.Client;
        var info = new FileInfo(localPath);
        var name = info.Name;

        var temporary = SftpPath.Combine(SftpPath.Parent(remotePath), $".sm-upload-{Guid.NewGuid():N}.part");
        var created = false;
        try
        {
            SftpFileAttributes? original = null;
            try { original = await client.GetAttributesAsync(remotePath, cancellationToken).ConfigureAwait(false); }
            catch (Renci.SshNet.Common.SftpPathNotFoundException) { /* A new destination starts private. */ }

            await using (var file = new FileStream(localPath, FileMode.Open, FileAccess.Read, FileShare.Read,
                64 * 1024, FileOptions.Asynchronous))
            {
                await using (var empty = await client.OpenAsync(temporary, FileMode.CreateNew, FileAccess.Write,
                    cancellationToken).ConfigureAwait(false))
                {
                    created = true;
                    // SSH.NET 2026 exposes SetAttributes only synchronously. Do
                    // not write content until the empty temporary is owner-only.
                    var privateAttributes = await client.GetAttributesAsync(temporary, cancellationToken).ConfigureAwait(false);
                    privateAttributes.SetPermissions(600);
                    await Task.Run(() => client.SetAttributes(temporary, privateAttributes), cancellationToken).ConfigureAwait(false);
                }
                // The native upload truncates this existing private inode; it
                // retains cancellable writes/progress without SftpFileStream's
                // uncancellable DisposeAsync retry of a failed buffered flush.
                await client.UploadFileAsync(file, temporary, canOverride: true,
                    new InlineProgress<UploadFileProgressReport>(report => progress?.Report(
                        new TransferProgress(name, (long)report.TotalBytesUploaded, info.Length))),
                    cancellationToken).ConfigureAwait(false);
            }
            if (original is not null)
            {
                // Start with the completed temporary's attributes: copying the
                // old Size/timestamps would truncate or misdate the new file.
                var attributes = await client.GetAttributesAsync(temporary, cancellationToken).ConfigureAwait(false);
                attributes.UserId = original.UserId;
                attributes.GroupId = original.GroupId;
                var mode = (short)((original.IsUIDBitSet ? 4000 : 0) + (original.IsGroupIDBitSet ? 2000 : 0)
                    + (original.IsStickyBitSet ? 1000 : 0) + (original.OwnerCanRead ? 400 : 0)
                    + (original.OwnerCanWrite ? 200 : 0) + (original.OwnerCanExecute ? 100 : 0)
                    + (original.GroupCanRead ? 40 : 0) + (original.GroupCanWrite ? 20 : 0)
                    + (original.GroupCanExecute ? 10 : 0) + (original.OthersCanRead ? 4 : 0)
                    + (original.OthersCanWrite ? 2 : 0) + (original.OthersCanExecute ? 1 : 0));
                attributes.SetPermissions(mode);
                // Failure to preserve metadata must leave the original intact.
                await Task.Run(() => client.SetAttributes(temporary, attributes), cancellationToken).ConfigureAwait(false);
            }
            cancellationToken.ThrowIfCancellationRequested();
            // SSH.NET exposes the POSIX atomic-replace extension synchronously.
            // Keep it off the dispatcher, and finish the commit once sent.
            try
            {
                await Task.Run(() => client.RenameFile(temporary, remotePath, isPosix: true), cancellationToken)
                    .ConfigureAwait(false);
            }
            catch (Exception error) when (error is Renci.SshNet.Common.SshException or NotSupportedException)
            {
                // A server lacking atomic replacement can still accept a new
                // file. Never implement overwrite as delete-then-rename.
                if (await client.ExistsAsync(remotePath, cancellationToken).ConfigureAwait(false))
                    throw new IOException("The server could not atomically replace the destination. The original file was kept.", error);
                await client.RenameFileAsync(temporary, remotePath, cancellationToken).ConfigureAwait(false);
            }
        }
        catch
        {
            if (created)
            {
                using var cleanup = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                try { await client.DeleteFileAsync(temporary, cleanup.Token).ConfigureAwait(false); }
                catch (Exception) { /* Connection loss can leave the uniquely named partial, never the destination. */ }
            }
            throw;
        }
    }

    public async Task CreateDirectoryAsync(
        SshTarget target, string path, CancellationToken cancellationToken = default)
    {
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        await operation.Client.CreateDirectoryAsync(path, cancellationToken).ConfigureAwait(false);
    }

    public async Task RenameAsync(
        SshTarget target, string from, string to, CancellationToken cancellationToken = default)
    {
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        await operation.Client.RenameFileAsync(from, to, cancellationToken).ConfigureAwait(false);
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
        using var operation = await lease(target, cancellationToken).ConfigureAwait(false);
        await DeleteAsync(operation.Client, path, cancellationToken).ConfigureAwait(false);
    }

    private static async Task DeleteAsync(
        ISftpClient client, string path, CancellationToken cancellationToken)
    {
        var attributes = await client.GetAttributesAsync(path, cancellationToken).ConfigureAwait(false);
        // A symlink to a directory is deleted as a link: following it would
        // delete the target's contents, which is not what "delete this link"
        // means anywhere else.
        if (!attributes.IsDirectory || attributes.IsSymbolicLink)
        {
            await client.DeleteFileAsync(path, cancellationToken).ConfigureAwait(false);
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
        await client.DeleteDirectoryAsync(path, cancellationToken).ConfigureAwait(false);
    }
}

internal sealed class InlineProgress<T>(Action<T> report) : IProgress<T>
{
    public void Report(T value) => report(value);
}
