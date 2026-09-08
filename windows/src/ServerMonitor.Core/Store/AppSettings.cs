using System.ComponentModel;
using System.Runtime.CompilerServices;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace ServerMonitor.Core.Store;

/// <summary>Which transport a host uses (D3).</summary>
public enum TransportKind
{
    /// <summary>One long-lived SSH.NET connection per host. The default.</summary>
    Library,
    /// <summary>A <c>ssh.exe</c> subprocess per command. No reuse.</summary>
    OpenSshExe,
}

public enum AppLanguage { System, Zh, En }

public enum AppTheme { System, Light, Dark }

/// <summary>
/// User preferences, in a JSON file next to the database (D6).
/// </summary>
/// <remarks>
/// A file rather than the registry: it can be read, diffed and copied, and a
/// portable install (P8) needs the whole state to live in one directory. Saves
/// are debounced and written atomically — a settings pane with a slider would
/// otherwise write on every tick of the drag, and a crash mid-write would
/// leave unparsable JSON that reads as "all defaults" on next launch.
/// </remarks>
public sealed class AppSettings : INotifyPropertyChanged
{
    public event PropertyChangedEventHandler? PropertyChanged;

    private readonly string? _path;
    private Timer? _saveTimer;
    private readonly Lock _saveLock = new();

    /// <summary>The intervals the picker offers, in seconds.</summary>
    public static readonly double[] AllowedIntervals = [3, 5, 10, 15, 30, 60];
    public static readonly int[] AllowedRetention = [1, 3, 7, 14, 30];
    public static readonly int[] ThresholdChoices = [0, 70, 80, 85, 90, 95];

    /// <summary>
    /// Monospaced faces present on a default Windows install, so the list
    /// never offers something that silently falls back to the system default.
    /// </summary>
    /// <remarks>
    /// Cascadia Mono ships with Windows 10 1909 and later and is the terminal
    /// default; Consolas covers everything older. Both render CJK through
    /// fallback rather than natively, which is why the two Chinese-capable
    /// monospaced faces are offered too.
    /// </remarks>
    public static readonly string[] TerminalFonts =
        ["Cascadia Mono", "Cascadia Code", "Consolas", "Lucida Console", "Courier New", "SimSun-ExtB", "NSimSun"];

    public static readonly double[] TerminalFontSizes = [11, 12, 13, 14, 16, 18];

    // MARK: - Collection

    private double _pollInterval = 5;
    /// <summary>Seconds between collection rounds.</summary>
    public double PollInterval
    {
        get => _pollInterval;
        set => Set(ref _pollInterval, AllowedIntervals.Contains(value) ? value : 5);
    }

    private int _retentionDays = 7;
    /// <summary>History window, in days.</summary>
    public int RetentionDays
    {
        get => _retentionDays;
        set => Set(ref _retentionDays, AllowedRetention.Contains(value) ? value : 7);
    }

    public TimeSpan Retention => TimeSpan.FromDays(RetentionDays);

    private TransportKind _transport = TransportKind.Library;
    /// <summary>The default transport for hosts that do not override it.</summary>
    public TransportKind Transport
    {
        get => _transport;
        set => Set(ref _transport, value);
    }

    /// <summary>
    /// Server ids pinned to the <c>ssh.exe</c> route.
    /// </summary>
    /// <remarks>
    /// R13's escape hatch: a host needing <c>Match</c> blocks, a certificate,
    /// PKCS#11 or a real agent goes here and the library route is bypassed for
    /// it alone.
    /// </remarks>
    public List<Guid> ForceOpenSshExe { get; set; } = [];

    // MARK: - Alerts

    private bool _notificationsEnabled;
    public bool NotificationsEnabled
    {
        get => _notificationsEnabled;
        set => Set(ref _notificationsEnabled, value);
    }

    private bool _notifyOnOffline = true;
    /// <summary>Notify when a host stops answering, and again when it recovers.</summary>
    public bool NotifyOnOffline
    {
        get => _notifyOnOffline;
        set => Set(ref _notifyOnOffline, value);
    }

    private int _cpuThreshold;
    /// <summary>Percentage above which a sustained breach raises an alert. 0 disables.</summary>
    public int CpuThreshold
    {
        get => _cpuThreshold;
        set => Set(ref _cpuThreshold, value);
    }

    private int _memoryThreshold;
    public int MemoryThreshold
    {
        get => _memoryThreshold;
        set => Set(ref _memoryThreshold, value);
    }

    private int _diskThreshold = 90;
    public int DiskThreshold
    {
        get => _diskThreshold;
        set => Set(ref _diskThreshold, value);
    }

    // MARK: - Terminal

    private string _terminalFontName = "Cascadia Mono";
    public string TerminalFontName
    {
        get => _terminalFontName;
        set => Set(ref _terminalFontName, TerminalFonts.Contains(value) ? value : "Cascadia Mono");
    }

    private double _terminalFontSize = 13;
    public double TerminalFontSize
    {
        get => _terminalFontSize;
        set => Set(ref _terminalFontSize, TerminalFontSizes.Contains(value) ? value : 13);
    }

    // MARK: - Shell

    private bool _launchAtLogin;
    public bool LaunchAtLogin
    {
        get => _launchAtLogin;
        set => Set(ref _launchAtLogin, value);
    }

    private bool _closeToTray = true;
    /// <summary>
    /// Closing the window leaves the app collecting in the tray, rather than
    /// quitting — the counterpart of the macOS build's menu-bar residency.
    /// </summary>
    public bool CloseToTray
    {
        get => _closeToTray;
        set => Set(ref _closeToTray, value);
    }

    private AppLanguage _language = AppLanguage.System;
    public AppLanguage Language
    {
        get => _language;
        set => Set(ref _language, value);
    }

    private AppTheme _theme = AppTheme.System;
    public AppTheme Theme
    {
        get => _theme;
        set => Set(ref _theme, value);
    }

    private bool _useMicaBackdrop;
    /// <summary>
    /// Whether the window uses the Mica backdrop.
    /// </summary>
    /// <remarks>
    /// Off by default, which is not what D1 hoped for, and the reason is a
    /// failure mode with no API to detect it: Mica needs the window's own
    /// background to be transparent, and where DWM declines to composite the
    /// backdrop — a virtual or indirect display adapter, some remote
    /// streaming setups — nothing is drawn behind it and the entire client
    /// area renders black. <c>DwmSetWindowAttribute</c> returns success in
    /// that case, so the app cannot tell.
    ///
    /// An unreadable monitor is a worse outcome than a flat one, so the
    /// texture is opt-in. The dark title bar, which has no such failure mode,
    /// is applied either way.
    /// </remarks>
    public bool UseMicaBackdrop
    {
        get => _useMicaBackdrop;
        set => Set(ref _useMicaBackdrop, value);
    }

    // Window state, so a relaunch comes back where it was.
    public double WindowWidth { get; set; } = 1280;
    public double WindowHeight { get; set; } = 820;
    public double WindowLeft { get; set; } = double.NaN;
    public double WindowTop { get; set; } = double.NaN;
    public bool WindowMaximized { get; set; }

    // MARK: - Persistence

    /// <summary>
    /// Where a failed read or write goes.
    /// </summary>
    /// <remarks>
    /// Set by the app to its log. Not a thrown exception, because a settings
    /// write happens on a timer thread in the middle of a session and there is
    /// nobody to catch it; and not silence, because silence is what let a
    /// serialisation bug drop every settings change on a fresh profile for as
    /// long as this class existed.
    /// </remarks>
    public Action<string>? OnError { get; set; }

    /// <summary>An unbacked instance, for tests and previews.</summary>
    public AppSettings() { }

    private AppSettings(string path) => _path = path;

    public static string DefaultPath => Path.Combine(Database.DefaultDirectory, "settings.json");

    /// <param name="onError">
    /// Where a failed read goes. Taken here rather than assigned afterwards
    /// because the first read happens inside this call, and a hook set on the
    /// returned object could never hear about it.
    /// </param>
    public static AppSettings Load(string? path = null, Action<string>? onError = null)
    {
        path ??= DefaultPath;
        var settings = new AppSettings(path) { OnError = onError };
        try
        {
            // StoredOptions, the same as the write side. Without them the
            // enums are the difference between a file that round-trips and one
            // that silently reverts: the writer turns Theme into "Dark" via
            // JsonStringEnumConverter, and a reader without that converter
            // cannot turn "Dark" back into AppTheme.Dark — it throws, the
            // catch below took it, and the defaults came back. Every stored
            // file has three enums in it, so nothing ever loaded.
            var stored = JsonSerializer.Deserialize<Stored>(
                File.ReadAllText(path), StoredOptions);
            if (stored is not null) settings.Apply(stored);
        }
        catch (FileNotFoundException)
        {
            // The first run. Not worth a word.
        }
        catch (DirectoryNotFoundException)
        {
            // Likewise: nothing has been written yet.
        }
        catch (Exception error)
        {
            // An unparsable file is a crash mid-write, and the next save
            // repairs it — but a file that exists and will not load is worth
            // saying out loud, because the user sees their settings revert.
            settings.OnError?.Invoke($"could not read {path}: {error.Message}");
        }
        return settings;
    }

    /// <summary>
    /// Writes now, skipping the debounce. For shutdown, where nothing will get
    /// around to the timer.
    /// </summary>
    public void Flush()
    {
        if (_path is null) return;
        lock (_saveLock)
        {
            _saveTimer?.Dispose();
            _saveTimer = null;
        }
        WriteNow();
    }

    private void ScheduleSave()
    {
        if (_path is null || _applying) return;
        lock (_saveLock)
        {
            _saveTimer?.Dispose();
            _saveTimer = new Timer(_ => WriteNow(), null, 400, System.Threading.Timeout.Infinite);
        }
    }

    private void WriteNow()
    {
        if (_path is null) return;
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(_path)!);
            var json = JsonSerializer.Serialize(ToStored(), StoredOptions);
            // Write-then-move, so a crash mid-write cannot truncate the real
            // file: the worst case is a leftover .tmp.
            var temporary = _path + ".tmp";
            File.WriteAllText(temporary, json);
            File.Move(temporary, _path, overwrite: true);
        }
        catch (Exception error)
        {
            // Settings that will not persist are a nuisance, not a reason to
            // take the app down mid-session — but they must not be a silent
            // one. An empty catch here hid a serialisation failure that
            // dropped every settings change on a fresh profile, and it hid it
            // for as long as the code existed, because the only symptom was
            // defaults coming back after a restart.
            OnError?.Invoke($"could not write {_path}: {error.Message}");
        }
    }

    private static readonly JsonSerializerOptions StoredOptions = new()
    {
        WriteIndented = true,
        Converters = { new JsonStringEnumConverter() },
    };

    /// <summary>
    /// The on-disk shape.
    /// </summary>
    /// <remarks>
    /// Separate from the live object so the properties can stay
    /// change-notifying and validating, and so adding a computed property does
    /// not change the file format.
    /// </remarks>
    private sealed class Stored
    {
        public double PollInterval { get; set; } = 5;
        public int RetentionDays { get; set; } = 7;
        public TransportKind Transport { get; set; } = TransportKind.Library;
        public List<Guid> ForceOpenSshExe { get; set; } = [];
        public bool NotificationsEnabled { get; set; }
        public bool NotifyOnOffline { get; set; } = true;
        public int CpuThreshold { get; set; }
        public int MemoryThreshold { get; set; }
        public int DiskThreshold { get; set; } = 90;
        public string TerminalFontName { get; set; } = "Cascadia Mono";
        public double TerminalFontSize { get; set; } = 13;
        public bool LaunchAtLogin { get; set; }
        public bool CloseToTray { get; set; } = true;
        public AppLanguage Language { get; set; } = AppLanguage.System;
        public AppTheme Theme { get; set; } = AppTheme.System;
        public bool UseMicaBackdrop { get; set; }
        public double WindowWidth { get; set; } = 1280;
        public double WindowHeight { get; set; } = 820;

        /// <summary>
        /// Where the window was, or null if it has never been positioned.
        /// </summary>
        /// <remarks>
        /// Nullable rather than NaN, which is what the live property uses:
        /// JSON has no NaN, and <c>JsonSerializer</c> throws
        /// <c>ArgumentException</c> rather than writing one. That threw inside
        /// WriteNow's catch, so on a fresh profile — where these are NaN until
        /// the window is first closed — every settings change was dropped
        /// without a word, settings.json was never created, and every launch
        /// came up with the defaults.
        /// </remarks>
        public double? WindowLeft { get; set; }
        public double? WindowTop { get; set; }
        public bool WindowMaximized { get; set; }
    }

    /// <summary>
    /// Set while <see cref="Apply"/> runs, so loading does not schedule a save.
    /// </summary>
    /// <remarks>
    /// Apply assigns through the properties on purpose, to clamp a
    /// hand-edited file — and every one of those assignments went through
    /// Set, which calls ScheduleSave. So reading settings marked them dirty
    /// and rewrote the file 400 ms later, on every launch, for no change.
    /// Harmless in the app, since the content was identical; visible in the
    /// tests, where a file deleted at the end of a test reappeared while the
    /// next one ran.
    /// </remarks>
    private bool _applying;

    private void Apply(Stored stored)
    {
        _applying = true;
        try
        {
            ApplyCore(stored);
        }
        finally
        {
            _applying = false;
        }
    }

    private void ApplyCore(Stored stored)
    {
        // Through the properties, so a hand-edited file with a poll interval of
        // 0 is clamped to something sane rather than spinning the loop.
        PollInterval = stored.PollInterval;
        RetentionDays = stored.RetentionDays;
        Transport = stored.Transport;
        ForceOpenSshExe = stored.ForceOpenSshExe;
        NotificationsEnabled = stored.NotificationsEnabled;
        NotifyOnOffline = stored.NotifyOnOffline;
        CpuThreshold = stored.CpuThreshold;
        MemoryThreshold = stored.MemoryThreshold;
        DiskThreshold = stored.DiskThreshold;
        TerminalFontName = stored.TerminalFontName;
        TerminalFontSize = stored.TerminalFontSize;
        LaunchAtLogin = stored.LaunchAtLogin;
        CloseToTray = stored.CloseToTray;
        Language = stored.Language;
        Theme = stored.Theme;
        UseMicaBackdrop = stored.UseMicaBackdrop;
        WindowWidth = stored.WindowWidth;
        WindowHeight = stored.WindowHeight;
        WindowLeft = stored.WindowLeft ?? double.NaN;
        WindowTop = stored.WindowTop ?? double.NaN;
        WindowMaximized = stored.WindowMaximized;
    }

    private Stored ToStored() => new()
    {
        PollInterval = PollInterval,
        RetentionDays = RetentionDays,
        Transport = Transport,
        ForceOpenSshExe = ForceOpenSshExe,
        NotificationsEnabled = NotificationsEnabled,
        NotifyOnOffline = NotifyOnOffline,
        CpuThreshold = CpuThreshold,
        MemoryThreshold = MemoryThreshold,
        DiskThreshold = DiskThreshold,
        TerminalFontName = TerminalFontName,
        TerminalFontSize = TerminalFontSize,
        LaunchAtLogin = LaunchAtLogin,
        CloseToTray = CloseToTray,
        Language = Language,
        Theme = Theme,
        UseMicaBackdrop = UseMicaBackdrop,
        // Every double crossing into JSON is checked for finiteness, not just
        // the two that default to NaN: RestoreBounds is Rect.Empty for a
        // window that was never shown, and Rect.Empty's Width is negative
        // infinity, which JSON cannot express either.
        WindowWidth = double.IsFinite(WindowWidth) && WindowWidth > 0 ? WindowWidth : 1280,
        WindowHeight = double.IsFinite(WindowHeight) && WindowHeight > 0 ? WindowHeight : 820,
        WindowLeft = double.IsFinite(WindowLeft) ? WindowLeft : null,
        WindowTop = double.IsFinite(WindowTop) ? WindowTop : null,
        WindowMaximized = WindowMaximized,
    };

    private void Set<T>(ref T field, T value, [CallerMemberName] string? name = null)
    {
        if (EqualityComparer<T>.Default.Equals(field, value)) return;
        field = value;
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));
        ScheduleSave();
    }

    /// <summary>Persists a window geometry change without a property notification.</summary>
    public void SaveWindowState(
        double left, double top, double width, double height, bool maximized)
    {
        // Rejected rather than stored: RestoreBounds is Rect.Empty for a
        // window that has not been shown, and Rect.Empty is
        // (infinity, infinity, -infinity, -infinity). Storing that would put
        // the window off-screen at the next launch, and ToStored would have to
        // throw it away anyway.
        if (double.IsFinite(left) && double.IsFinite(top))
        {
            WindowLeft = left;
            WindowTop = top;
        }
        if (double.IsFinite(width) && width > 0) WindowWidth = width;
        if (double.IsFinite(height) && height > 0) WindowHeight = height;
        WindowMaximized = maximized;
        ScheduleSave();
    }
}
