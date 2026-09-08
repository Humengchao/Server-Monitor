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

    // Window state, so a relaunch comes back where it was.
    public double WindowWidth { get; set; } = 1280;
    public double WindowHeight { get; set; } = 820;
    public double WindowLeft { get; set; } = double.NaN;
    public double WindowTop { get; set; } = double.NaN;
    public bool WindowMaximized { get; set; }

    // MARK: - Persistence

    /// <summary>An unbacked instance, for tests and previews.</summary>
    public AppSettings() { }

    private AppSettings(string path) => _path = path;

    public static string DefaultPath => Path.Combine(Database.DefaultDirectory, "settings.json");

    public static AppSettings Load(string? path = null)
    {
        path ??= DefaultPath;
        var settings = new AppSettings(path);
        try
        {
            var stored = JsonSerializer.Deserialize<Stored>(File.ReadAllText(path));
            if (stored is not null) settings.Apply(stored);
        }
        catch (Exception)
        {
            // A missing file is the first run; an unparsable one is a crash
            // mid-write. Both mean "use the defaults" — and the next save
            // repairs the file.
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
        if (_path is null) return;
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
        catch (Exception)
        {
            // Settings that will not persist are a nuisance, not a reason to
            // take the app down mid-session.
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
        public double WindowWidth { get; set; } = 1280;
        public double WindowHeight { get; set; } = 820;
        public double WindowLeft { get; set; } = double.NaN;
        public double WindowTop { get; set; } = double.NaN;
        public bool WindowMaximized { get; set; }
    }

    private void Apply(Stored stored)
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
        WindowWidth = stored.WindowWidth;
        WindowHeight = stored.WindowHeight;
        WindowLeft = stored.WindowLeft;
        WindowTop = stored.WindowTop;
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
        WindowWidth = WindowWidth,
        WindowHeight = WindowHeight,
        WindowLeft = WindowLeft,
        WindowTop = WindowTop,
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
        WindowLeft = left;
        WindowTop = top;
        WindowWidth = width;
        WindowHeight = height;
        WindowMaximized = maximized;
        ScheduleSave();
    }
}
