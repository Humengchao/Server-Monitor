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
    /// The rule list the engine judges on, cached because it is re-read in
    /// full on every poll of every host. Invalidated by the rules page after
    /// a save or delete.
    /// </summary>
    private List<Core.Model.AlertRule>? _alertRules;

    /// <summary>Drops the cache so the next poll sees the edited rules.</summary>
    public void InvalidateAlertRules() => _alertRules = null;

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

        // A settings read or write that fails says so in the log. It used to
        // say nothing, and what it was not saying was that none of them
        // worked. Passed to Load rather than assigned after it, so the first
        // read's own failure is covered too.
        Settings = AppSettings.Load(onError: message => Log($"settings: {message}"));
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

        Monitor.Alerts = new RuleEngine(
            rules: () =>
            {
                // Rules only change on the UI thread (the rules page edits
                // them), so a plain cache is safe. Without it the engine would
                // hit SQLite on every poll of every host.
                return _alertRules ??= Store.AllAlertRules();
            },
            webhookUrl: rule => Credentials.GetWebhook(rule.Id),
            recordOpen: Store.OpenAlertEvent,
            recordResolve: Store.ResolveAlertEvent,
            deliver: DeliverAlert,
            log: Log);

        if (Store.SchemaAdvanced && Store.AllAlertRules().Count == 0)
        {
            foreach (var seeded in RuleSeed.From(Settings, Store.AllServers()))
                Store.Save(seeded);
        }

        // A deleted host takes its scoped rules with it (ON DELETE CASCADE),
        // and an added one can be picked as a scope straight away. Either way
        // the cached list is out of date the moment the collection changes.
        Monitor.Servers.CollectionChanged += (_, _) => InvalidateAlertRules();

        Monitor.Alerts.Restore(Store.UnresolvedAlertEvents());

        // Every window, not just this one's: a class handler fires for each
        // Window the app ever loads, so the terminal, the file browser, the
        // editors and the dialogs get a matching title bar without each one
        // having to remember. Registered before the first window is built.
        EventManager.RegisterClassHandler(
            typeof(Window),
            FrameworkElement.LoadedEvent,
            new RoutedEventHandler((sender, _) =>
            {
                if (sender is Window window) Backdrop.ApplyTitleBar(window, Theme.Palette.IsDark);
            }));

        ApplyTheme();
        SystemEvents.UserPreferenceChanged += OnUserPreferenceChanged;

        _watchers = new SystemWatchers(action => Dispatcher.Invoke(action));
        _watchers.RetryEverything += reason =>
        {
            // The cached DNS answers go with the network they were resolved
            // on: a laptop that moved may reach the same name somewhere else,
            // and latency to an address it can no longer reach is worse than
            // no reading.
            Core.Collect.PingProbe.ForgetResolutions();
            Monitor.RetryEverythingNow(reason);
        };

        _window = new MainWindow();
        // A closed Window cannot be shown again, so forget it when it goes and
        // let ShowWindow build a fresh one. Without this, reopening from the
        // tray after any real close threw out of ShowWindow.
        _window.Closed += (_, _) => _window = null;
        _tray = new TrayIcon(Monitor, ShowWindow, QuitApp, OpenServer);
        // Redrawn on publish rather than on a timer, so the icon's number is
        // never a tick behind what the window shows.
        Monitor.Published += () => _tray.Refresh();

        _instance.ListenForActivation(() => Dispatcher.Invoke(ShowWindow));

        // Clicking a toast opens the host it is about (plan §D). The callback
        // arrives on a thread-pool thread, so it marshals here.
        Toasts.OnClicked(serverId => Dispatcher.Invoke(() =>
        {
            ShowWindow();
            OpenServer(serverId);
        }));

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

    /// <summary>
    /// Hands an alert to the notification area.
    /// </summary>
    /// <remarks>
    /// Logged as well as shown. An alert that does not appear is otherwise
    /// impossible to diagnose: there is no way to tell "the threshold never
    /// tripped" from "it tripped and Windows swallowed the toast", and the
    /// two need completely different fixes. Found while chasing exactly that
    /// against real hosts.
    /// </remarks>
    private void DeliverAlert(Guid serverId, string title, string body)
    {
        Log($"alert: {title} — {body}");
        // The one thing the settings page still says about alerts. It gates
        // the toast and nothing else: the rule was still judged, the event was
        // still recorded, and a webhook the user configured per-rule still
        // fires — "do not interrupt me" is not "stop watching".
        if (!Settings.NotificationsEnabled)
        {
            Log("alert: notifications are off, so no toast");
            return;
        }
        // A real toast, per the plan. The tray balloon is the fallback and not
        // the other way round: on Windows 11 ShowBalloonTip displays nothing
        // whatsoever for an unpackaged app, so for a year of this app's life
        // every alert it raised went nowhere. See Platform/Toasts.
        if (Toasts.Show(title, body, serverId)) return;
        Log("alert: toast unavailable, falling back to the tray balloon");
        Dispatcher.Invoke(() => _tray?.Notify(title, body, serverId));
    }

    // MARK: - Window

    /// <summary>
    /// The shell window, once there is one.
    /// </summary>
    /// <remarks>
    /// Not <c>Application.MainWindow</c>: this app can be running with no
    /// window at all — closing to the tray disposes it — and MainWindow keeps
    /// pointing at whatever was shown first, which after a dialog is not
    /// necessarily this. The session dock hangs off this one.
    /// </remarks>
    internal MainWindow? Shell => _window;

    public void ShowWindow()
    {
        if (_window is null)
        {
            _window = new MainWindow();
            _window.Closed += (_, _) => _window = null;
        }
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

    /// <summary>
    /// Stops collecting, saves, and ends the process.
    /// </summary>
    /// <remarks>
    /// Guarded because there are two ways in: the tray menu and the Settings
    /// page call it directly, and the window's close handler calls it when
    /// "keep running in the notification area" is off. Shutdown then closes
    /// the window, which can re-enter the close handler — so without the flag
    /// the monitor would be stopped and the settings flushed twice.
    ///
    /// Nothing here bypasses the close-to-tray interception, because nothing
    /// needs to: Application.Shutdown closes its windows ignoring Cancel.
    /// Measured, after assuming the opposite and writing a fix for it.
    /// </remarks>
    public void QuitApp()
    {
        if (_quitting) return;
        _quitting = true;
        Monitor.Stop();
        Settings.Flush();
        Shutdown();
    }

    private bool _quitting;

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
        // The windows already open, which the class handler above has
        // already fired for. Switching the theme with a terminal open would
        // otherwise leave that window's title bar on the old one.
        foreach (Window window in Windows)
        {
            if (!ReferenceEquals(window, _window)) Backdrop.ApplyTitleBar(window, dark);
        }
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
        Toasts.Clear();
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
