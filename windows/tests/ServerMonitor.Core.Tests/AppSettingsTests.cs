using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// Whether a setting the user changes is still set next launch.
/// </summary>
/// <remarks>
/// It was not, ever, for either of two independent reasons — and there were no
/// tests here to notice.
///
/// The write threw. WindowLeft and WindowTop default to NaN, meaning "never
/// positioned", and JSON has no NaN: JsonSerializer raises ArgumentException
/// rather than writing one. That landed in an empty catch, so on a fresh
/// profile — where the geometry stays NaN until the window is first closed —
/// every settings change was dropped and settings.json was never created.
///
/// The read threw too. Serialization passed StoredOptions, which carries
/// JsonStringEnumConverter; deserialization did not. So "Theme": "Dark" could
/// not become AppTheme.Dark, that also landed in an empty catch, and the
/// defaults came back. Every stored file has three enums in it, so even once
/// writing worked nothing would have loaded.
///
/// Both were invisible because the only symptom was settings reverting after a
/// restart. Both catches now report through OnError.
/// </remarks>
public class AppSettingsTests
{
    [Fact]
    public void AChangedSettingSurvivesAReload()
    {
        var path = Temp();
        try
        {
            var settings = AppSettings.Load(path);
            settings.Theme = AppTheme.Dark;
            settings.PollInterval = 10;
            settings.Flush();

            Assert.True(File.Exists(path), "nothing was written");

            var reloaded = AppSettings.Load(path);
            Assert.Equal(AppTheme.Dark, reloaded.Theme);
            Assert.Equal(10, reloaded.PollInterval);
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AFreshProfileCanSaveBeforeTheWindowHasEverBeenPositioned()
    {
        // The exact NaN case. A first-run user who changes a setting and then
        // quits from the tray icon never triggers a geometry save, so this is
        // the only state their settings are ever written from.
        var path = Temp();
        try
        {
            var settings = AppSettings.Load(path);
            Assert.True(double.IsNaN(settings.WindowLeft), "the geometry did not start unset");

            var errors = new List<string>();
            settings.OnError = errors.Add;
            settings.CloseToTray = false;
            settings.Flush();

            Assert.Empty(errors);
            Assert.False(AppSettings.Load(path).CloseToTray);
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void EveryStoredPropertyRoundTrips()
    {
        // Walked as a set rather than one assertion per field: what broke was
        // a converter that applied to the whole file, so a single enum would
        // have caught it and a single number would not have.
        var path = Temp();
        try
        {
            var id = Guid.NewGuid();
            var settings = AppSettings.Load(path);
            settings.PollInterval = 30;
            settings.RetentionDays = 14;
            settings.Transport = TransportKind.OpenSshExe;
            settings.ForceOpenSshExe = [id];
            settings.NotificationsEnabled = true;
            settings.NotifyOnOffline = false;
            settings.CpuThreshold = 85;
            settings.MemoryThreshold = 90;
            settings.DiskThreshold = 95;
            settings.TerminalFontName = "Consolas";
            settings.TerminalFontSize = 16;
            settings.LaunchAtLogin = true;
            settings.CloseToTray = false;
            settings.Language = AppLanguage.En;
            settings.Theme = AppTheme.Light;
            settings.UseMicaBackdrop = true;
            settings.SaveWindowState(120, 60, 1000, 700, maximized: true);
            settings.Flush();

            var back = AppSettings.Load(path);
            Assert.Equal(30, back.PollInterval);
            Assert.Equal(14, back.RetentionDays);
            Assert.Equal(TransportKind.OpenSshExe, back.Transport);
            Assert.Equal([id], back.ForceOpenSshExe);
            Assert.True(back.NotificationsEnabled);
            Assert.False(back.NotifyOnOffline);
            Assert.Equal(85, back.CpuThreshold);
            Assert.Equal(90, back.MemoryThreshold);
            Assert.Equal(95, back.DiskThreshold);
            Assert.Equal("Consolas", back.TerminalFontName);
            Assert.Equal(16, back.TerminalFontSize);
            Assert.True(back.LaunchAtLogin);
            Assert.False(back.CloseToTray);
            Assert.Equal(AppLanguage.En, back.Language);
            Assert.Equal(AppTheme.Light, back.Theme);
            Assert.True(back.UseMicaBackdrop);
            Assert.Equal(120, back.WindowLeft);
            Assert.Equal(60, back.WindowTop);
            Assert.Equal(1000, back.WindowWidth);
            Assert.Equal(700, back.WindowHeight);
            Assert.True(back.WindowMaximized);
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void TheGeometryOfAWindowThatWasNeverShownIsRefused()
    {
        // RestoreBounds is Rect.Empty for a window that has not been shown,
        // and Rect.Empty is (infinity, infinity, -infinity, -infinity).
        // Storing it would put the window off-screen next launch, and JSON
        // cannot express it either.
        var path = Temp();
        try
        {
            var settings = AppSettings.Load(path);
            settings.SaveWindowState(200, 100, 900, 600, maximized: false);
            settings.SaveWindowState(
                double.PositiveInfinity, double.PositiveInfinity,
                double.NegativeInfinity, double.NegativeInfinity, maximized: false);
            settings.Flush();

            var back = AppSettings.Load(path);
            Assert.Equal(200, back.WindowLeft);
            Assert.Equal(900, back.WindowWidth);
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AMissingFileIsNotAnError()
    {
        // The first run. Reporting it would put a line in the log on every
        // clean install.
        var errors = new List<string>();
        var settings = AppSettings.Load(Temp(), errors.Add);
        Assert.Empty(errors);
        Assert.Equal(AppTheme.System, settings.Theme);
    }

    [Fact]
    public void AFileThatWillNotParseIsReportedAndFallsBackToTheDefaults()
    {
        // A crash mid-write. The defaults are the right behaviour; silence
        // about it is not, because the user sees their settings revert.
        var path = Temp();
        try
        {
            File.WriteAllText(path, "{ this is not json");
            var errors = new List<string>();
            var settings = AppSettings.Load(path, errors.Add);

            Assert.Equal(AppTheme.System, settings.Theme);
            var reported = Assert.Single(errors);
            Assert.Contains(path, reported, StringComparison.Ordinal);
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AWriteThatCannotHappenIsReported()
    {
        // A path under a file rather than a directory: CreateDirectory throws,
        // which is the closest reachable stand-in for a locked or read-only
        // profile.
        var blocker = Temp();
        try
        {
            File.WriteAllText(blocker, "not a directory");
            var settings = AppSettings.Load(Path.Combine(blocker, "settings.json"));
            var errors = new List<string>();
            settings.OnError = errors.Add;
            settings.Theme = AppTheme.Dark;
            settings.Flush();

            Assert.NotEmpty(errors);
        }
        finally
        {
            Delete(blocker);
        }
    }

    [Fact]
    public void AnUnbackedInstanceNeitherWritesNorComplains()
    {
        // What the tests and the design previews use. It has no path, so a
        // save is a no-op — and must not be reported as a failure.
        var errors = new List<string>();
        var settings = new AppSettings { OnError = errors.Add };
        settings.Theme = AppTheme.Dark;
        settings.Flush();
        Assert.Empty(errors);
        Assert.Equal(AppTheme.Dark, settings.Theme);
    }

    private static string Temp() =>
        Path.Combine(Path.GetTempPath(), $"sm-settings-{Guid.NewGuid():N}.json");

    private static void Delete(string path)
    {
        foreach (var candidate in new[] { path, path + ".tmp" })
        {
            try
            {
                File.Delete(candidate);
            }
            catch (Exception)
            {
                // A test that cannot clean up should still report its result.
            }
        }
    }
}
