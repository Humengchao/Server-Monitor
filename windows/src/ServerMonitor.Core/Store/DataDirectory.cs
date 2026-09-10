namespace ServerMonitor.Core.Store;

/// <summary>
/// Where the database, the settings file and the logs live.
/// </summary>
/// <remarks>
/// Two homes, chosen once per process:
///
/// <para><b>Portable.</b> A marker file named <see cref="MarkerName"/> sitting
/// next to the exe moves everything to a <c>Data</c> directory beside it, so
/// the portable zip can live on a USB stick and carry its history with it.
/// The marker is shipped in the portable zip and never in the installer —
/// an installed copy under Program Files could not write beside itself
/// anyway, which is also why a marker in a read-only place falls back to the
/// profile rather than failing to start.</para>
///
/// <para><b>Default.</b> <c>%LOCALAPPDATA%\ServerMonitor</c> — local rather
/// than roaming, because a machine's own monitoring history synced between
/// machines over a roaming profile is a good way to corrupt a SQLite file.</para>
///
/// Everything that touches a file goes through here, so the choice is made in
/// exactly one place.
/// </remarks>
public static class DataDirectory
{
    /// <summary>The opt-in: this file next to the exe turns portable mode on.</summary>
    public const string MarkerName = "ServerMonitor.portable";

    /// <summary>The directory everything lives in when portable.</summary>
    public const string PortableDirectoryName = "Data";

    private static string? _resolved;

    /// <summary>The resolved directory, probed once and then remembered.</summary>
    public static string Current =>
        _resolved ??= Resolve(
            Environment.ProcessPath,
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData));

    /// <summary>
    /// The decision, separated from the process so a test can ask it with
    /// temporary directories.
    /// </summary>
    internal static string Resolve(string? exePath, string localAppData)
    {
        var fallback = Path.Combine(localAppData, "ServerMonitor");
        if (exePath is null) return fallback;

        var beside = Path.GetDirectoryName(exePath)!;
        if (!File.Exists(Path.Combine(beside, MarkerName))) return fallback;

        var portable = Path.Combine(beside, PortableDirectoryName);
        try
        {
            // Probe before committing: a marker in a read-only location (an
            // install under Program Files that someone added the file to)
            // must not turn startup into an UnauthorizedAccessException.
            Directory.CreateDirectory(portable);
            var probe = Path.Combine(portable, ".write-test");
            File.WriteAllText(probe, string.Empty);
            File.Delete(probe);
            return portable;
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            return fallback;
        }
    }
}
