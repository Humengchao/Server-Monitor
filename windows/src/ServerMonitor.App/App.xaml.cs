using System.Windows;
using Microsoft.Win32;
using ServerMonitor.App.Platform;
using ServerMonitor.App.Theme;
using ServerMonitor.App.Views;
using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;

namespace ServerMonitor.App;

public partial class App : Application
{
    /// <summary>
    /// This app, typed. `new` on purpose: it deliberately shadows
    /// <see cref="Application.Current"/>, which returns the base type and is
    /// never what a caller here wants.
    /// </summary>
    public static new App Current => (App)Application.Current;

    public MonitorService Monitor { get; private set; } = null!;
    public AppSettings Settings { get; private set; } = null!;
    public Core.Store.Database Store { get; private set; } = null!;
    public ICredentialStore Credentials { get; private set; } = null!;

    /// <summary>
    /// Why the store could not be opened, if it could not. Shown once by the
    /// window rather than swallowed: a scratch database means the user's
    /// servers are invisible and nothing they do will be saved, which they
    /// need told rather than left to discover.
    /// </summary>
    public string? StoreFailure { get; private set; }

    /// <summary>
    /// The pooled SSH.NET transport, for the two things that need the session
    /// and not a command: the terminal's shell stream and SFTP.
    /// </summary>
    /// <remarks>
    /// Typed rather than <c>ISshTransport</c> because those two are exactly
    /// what the interface does not cover — and there is no ssh.exe equivalent,
    /// so a host pinned to that transport gets told rather than silently
    /// handed a second connection.
    /// </remarks>
    public SshNetTransport? LibraryTransport => _library as SshNetTransport;

    private SingleInstance? _instance;
    private SystemWatchers? _watchers;
    private TrayIcon? _tray;
    private MainWindow? _window;
    private ISshTransport? _library;
    private ISshTransport? _openSsh;

    /// <summary>
    /// Started minimised, from the Run key or the installer's optional task.
    /// </summary>
    private bool _startMinimised;

    protected override void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);

        _instance = SingleInstance.Acquire();
        if (!_instance.IsFirstInstance)
        {
            // Bring the running copy forward rather than exiting silently: the
            // user double-clicked and would otherwise see nothing happen,
            // because the running copy is a tray icon they did not notice.
            SingleInstance.SignalExistingInstance();
            Shutdown();
            return;
        }

        _startMinimised = e.Args.Any(a =>
            a.Equals("--minimised", StringComparison.OrdinalIgnoreCase)
            || a.Equals("--minimized", StringComparison.OrdinalIgnoreCase));

        // Anything unhandled reaches the user as a dialog rather than a silent
        // disappearance. A monitor that vanishes is worse than one that
        // complains: the user believes it is still watching.
        DispatcherUnhandledException += (_, args) =>
        {
            MessageBox.Show(
                args.Exception.ToString(),
                Strings.Get("common.error"),
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            args.Handled = true;
        };

        Settings = AppSettings.Load();
        Strings.Language = Settings.Language;

        try
        {
            Store = Core.Store.Database.Open();
        }
        catch (Exception error)
        {
            StoreFailure = error.Message;
            Store = Core.Store.Database.Scratch()
                ?? throw new InvalidOperationException(
                    "SQLite is unusable on this machine: " + error.Message, error);
        }

        // Sessions left open by a crash would otherwise sit in history as
        // "in progress" forever.
        try
        {
            Store.CloseDanglingSessions();
        }
        catch (Exception)
        {
            // Not worth failing a launch over.
        }

        Credentials = new WindowsCredentialStore();

        _library = new SshNetTransport(Credentials, log: Log);
        // The fallback is built lazily: constructing it throws when no
        // OpenSSH client is installed, and that must not stop a launch on a
        // machine where nothing needs it.
        Monitor = new MonitorService(
            Store,
            Settings,
            Credentials,
            TransportFor,
            action => Dispatcher.Invoke(action),
            Log,
            latency: new PingProbe())
        {
            IsEnergySaverOn = SystemWatchers.IsEnergySaverOn,
        };

        Monitor.Alerts = new AlertService(Settings, DeliverAlert);

        ApplyTheme();
        SystemEvents.UserPreferenceChanged += OnUserPreferenceChanged;

        _watchers = new SystemWatchers(action => Dispatcher.Invoke(action));
        _watchers.RetryEverything += reason => Monitor.RetryEverythingNow(reason);

        _window = new MainWindow();
        _tray = new TrayIcon(Monitor, ShowWindow, QuitApp, OpenServer);
        // Redrawn on publish rather than on a timer, so the icon's number is
        // never a tick behind what the window shows.
        Monitor.Published += () => _tray.Refresh();

        _instance.ListenForActivation(() => Dispatcher.Invoke(ShowWindow));

        if (_startMinimised)
        {
            // Never shown at all, rather than shown and hidden: showing it
            // would steal focus at sign-in from whatever the user is doing,
            // and the point of starting minimised is not to.
            Monitor.SetUiVisible(false);
        }
        else
        {
            ShowWindow();
        }

        Monitor.Start();
    }

    /// <summary>
    /// Which transport a host uses (D3).
    /// </summary>
    /// <remarks>
    /// The global default, overridable per host through
    /// <see cref="AppSettings.ForceOpenSshExe"/> — R13's escape hatch for a
    /// host needing <c>Match</c> blocks, a certificate, PKCS#11 or a real
    /// agent.
    /// </remarks>
    private ISshTransport TransportFor(SshTarget target)
    {
        var wantsExe = Settings.Transport == TransportKind.OpenSshExe
            || Settings.ForceOpenSshExe.Contains(target.ServerId);
        if (!wantsExe) return _library!;

        try
        {
            return _openSsh ??= new OpenSshExeTransport(Credentials, log: Log);
        }
        catch (InvalidOperationException error)
        {
            // No ssh.exe on this machine. Falling back to the library is
            // better than failing every poll — and the message says why, so
            // the host's failure is not a mystery.
            Log($"{target.Host}: {error.Message}; using the library transport instead");
            return _library!;
        }
    }

    private void DeliverAlert(Guid serverId, string title, string body) =>
        Dispatcher.Invoke(() => _tray?.Notify(title, body, serverId));

    // MARK: - Window

    public void ShowWindow()
    {
        _window ??= new MainWindow();
        if (!_window.IsVisible) _window.Show();
        if (_window.WindowState == WindowState.Minimized)
        {
            _window.WindowState = WindowState.Normal;
        }
        _window.Activate();
        _window.Topmost = true;
        _window.Topmost = false;
        Monitor.SetUiVisible(true);
    }

    /// <summary>
    /// Called by the window when it hides or minimises.
    /// </summary>
    /// <remarks>
    /// Windows has no occlusion notification, so this is driven by minimise
    /// and close-to-tray rather than by another window covering ours — which
    /// are the cases that matter, since those are where the app spends hours.
    /// Collection, the database and alerts carry on regardless.
    /// </remarks>
    public void WindowHidden() => Monitor.SetUiVisible(false);

    private void OpenServer(Guid serverId)
    {
        ShowWindow();
        _window?.OpenServer(serverId);
    }

    public void QuitApp()
    {
        Monitor.Stop();
        Settings.Flush();
        Shutdown();
    }

    // MARK: - Theme

    private void OnUserPreferenceChanged(object sender, UserPreferenceChangedEventArgs e)
    {
        // General covers the light/dark switch, and also the accent colour.
        if (e.Category is UserPreferenceCategory.General or UserPreferenceCategory.VisualStyle)
        {
            Dispatcher.Invoke(ApplyTheme);
        }
    }

    /// <summary>
    /// Repaints every themed brush in place.
    /// </summary>
    /// <remarks>
    /// The brushes are looked up as <c>DynamicResource</c> throughout, so
    /// rewriting the values under the same keys re-themes the whole tree
    /// without rebuilding it — which is what keeps a theme switch from losing
    /// the current page, the scroll position and every open editor.
    /// </remarks>
    public void ApplyTheme()
    {
        var dark = Settings.Theme switch
        {
            AppTheme.Light => false,
            AppTheme.Dark => true,
            _ => IsSystemDark(),
        };

        Theme.ThemeBrushes.Apply(Resources, dark);

        _window?.ThemeChanged(dark);
        _tray?.SetDark(dark);
    }

    /// <summary>
    /// Whether Windows is in dark mode.
    /// </summary>
    /// <remarks>
    /// <c>AppsUseLightTheme</c> rather than <c>SystemUsesLightTheme</c>: the
    /// two are separate settings, and the first is the one about application
    /// windows. Absent on Windows versions before 1809, where light is the
    /// only answer — which matches D8's floor.
    /// </remarks>
    private static bool IsSystemDark()
    {
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(
                @"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize");
            return key?.GetValue("AppsUseLightTheme") is int value && value == 0;
        }
        catch (Exception)
        {
            return false;
        }
    }

    // MARK: - Logging

    private static readonly Lock LogGate = new();
    private static string? _logPath;

    /// <summary>
    /// A rolling text log beside the database (D6).
    /// </summary>
    /// <remarks>
    /// Best-effort and never throws: a monitor that falls over because it
    /// could not write a log line has failed at its actual job. One file per
    /// day, and days beyond a week are removed on first write.
    /// </remarks>
    public static void Log(string message)
    {
        try
        {
            lock (LogGate)
            {
                if (_logPath is null)
                {
                    var directory = System.IO.Path.Combine(
                        Core.Store.Database.DefaultDirectory, "logs");
                    System.IO.Directory.CreateDirectory(directory);
                    _logPath = System.IO.Path.Combine(
                        directory,
                        $"{DateTime.Now:yyyy-MM-dd}.log");
                    Prune(directory);
                }
                System.IO.File.AppendAllText(
                    _logPath,
                    $"{DateTime.Now:HH:mm:ss.fff}  {message}{Environment.NewLine}");
            }
        }
        catch (Exception)
        {
            // Deliberately silent.
        }
    }

    private static void Prune(string directory)
    {
        try
        {
            var cutoff = DateTime.Now.AddDays(-7);
            foreach (var file in System.IO.Directory.GetFiles(directory, "*.log"))
            {
                if (System.IO.File.GetLastWriteTime(file) < cutoff) System.IO.File.Delete(file);
            }
        }
        catch (Exception)
        {
            // Same.
        }
    }

    protected override void OnExit(ExitEventArgs e)
    {
        SystemEvents.UserPreferenceChanged -= OnUserPreferenceChanged;
        Monitor?.Stop();
        Settings?.Flush();
        _tray?.Dispose();
        _watchers?.Dispose();
        _instance?.Dispose();
        // Fire and forget: the process is going away, and waiting on a socket
        // teardown would only delay it.
        _ = _library?.DisposeAsync();
        _ = _openSsh?.DisposeAsync();
        base.OnExit(e);
    }
}
