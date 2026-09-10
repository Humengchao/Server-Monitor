using System.ComponentModel;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Platform;
using ServerMonitor.App.Theme;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>Which page the content area is showing.</summary>
public enum Page
{
    Dashboard,
    Machines,
    Identities,
    SshKeys,
    Snippets,
    Docker,
    Alerts,
    Sessions,
    Settings,
    ServerDetail,
}

public partial class MainWindow : Window
{
    private MonitorService Monitor => App.Current.Monitor;
    private Core.Store.AppSettings Settings => App.Current.Settings;

    /// <summary>The sidebar's fixed entries, in order.</summary>
    private readonly List<(Page Page, string Key, bool IsHeader)> _navigation =
    [
        (Page.Dashboard, "nav.dashboard", false),
        (Page.Machines, "nav.resources", true),
        (Page.Machines, "nav.machines", false),
        (Page.Identities, "nav.identities", false),
        (Page.SshKeys, "nav.sshKeys", false),
        (Page.Snippets, "nav.toolbox", true),
        (Page.Snippets, "nav.snippets", false),
        (Page.Docker, "nav.docker", false),
        (Page.Alerts, "nav.alerts", false),
        (Page.Sessions, "nav.sessions", false),
    ];

    private Page _page = Page.Dashboard;
    private Guid? _openServerId;
    private bool _navigating;

    /// <summary>
    /// Terminal and SFTP sessions, which used to be windows of their own.
    /// </summary>
    /// <remarks>
    /// Built with the window and never rebuilt: a session has to outlive
    /// navigation, which is the whole point of putting it here, and an
    /// HwndHost that gets re-parented stops drawing.
    /// </remarks>
    internal SessionDock Dock { get; } = new();

    /// <summary>
    /// Gives the dock its row back when a session opens, and takes it away
    /// when the last one closes.
    /// </summary>
    /// <remarks>
    /// Both the height and the visibility, because either alone is not enough:
    /// a zero-height row still lets an HwndHost paint outside it, and a
    /// collapsed border in a 300px row leaves a 300px hole.
    /// </remarks>
    private void ShowDockIfUsed()
    {
        var used = Dock.HasSessions;
        DockHost.Visibility = used ? Visibility.Visible : Visibility.Collapsed;
        DockSplitter.Visibility = used ? Visibility.Visible : Visibility.Collapsed;
        DockRow.MinHeight = used ? 120 : 0;
        DockRow.Height = used ? new GridLength(DockHeight()) : new GridLength(0);
    }

    /// <summary>The stored dock height, kept inside what the window can show.</summary>
    private double DockHeight()
    {
        var wanted = Settings.SessionDockHeight;
        if (!double.IsFinite(wanted) || wanted <= 0) wanted = 300;
        // Leave the page something: restoring a tall dock into a short window
        // would otherwise open the app with no dashboard visible at all.
        var ceiling = Math.Max(120, ActualHeight - 260);
        return Math.Min(wanted, ceiling);
    }

    private void OnDockResized(object sender, System.Windows.Controls.Primitives.DragCompletedEventArgs e)
    {
        // On completion rather than during: the terminal tells the far side
        // its new row and column count on every resize, and a drag is a few
        // hundred of them.
        if (!Dock.HasSessions) return;
        Settings.SessionDockHeight = DockHost.ActualHeight;
    }
    public MainWindow()
    {
        InitializeComponent();
        RestoreGeometry();
        BuildNavigation();
        ApplyLanguage();

        if (App.Current.StoreFailure is { } failure)
        {
            StoreWarning.Visibility = Visibility.Visible;
            StoreWarningText.Text =
                $"{Strings.Get("common.error")}: {failure}\n{Strings.Get("settings.storage")}: "
                + Core.Store.Database.DefaultPath;
        }

        // The sidebar's lists sit in a StackPanel inside a ScrollViewer, so
        // they are measured unbounded and never scroll themselves — but they
        // still swallow the wheel, which left the sidebar draggable by its
        // scrollbar and dead to the wheel.
        Nav.PassWheelUp();
        ServerList.PassWheelUp();

        DockHost.Child = Dock;
        Dock.Changed += ShowDockIfUsed;

        Monitor.Published += OnPublished;
        Settings.PropertyChanged += OnSettingsChanged;
        Loaded += (_, _) =>
        {
            ThemeChanged(Palette.IsDark);
            Show(Page.Dashboard);
            RefreshServerList();
        };
        InstallShortcuts();
    }

    // MARK: - Theme and language

    /// <summary>Re-applies the backdrop and repaints what the code draws.</summary>
    public void ThemeChanged(bool dark)
    {
        // The title bar always; the backdrop only when asked for. A
        // transparent background over a backdrop DWM declined to composite is
        // not translucent, it is black — see AppSettings.UseMicaBackdrop.
        var mica = Backdrop.Apply(this, dark, Settings.UseMicaBackdrop);
        Background = mica
            ? System.Windows.Media.Brushes.Transparent
            : Backdrop.SolidBackground(dark);
        // The pages draw into a DrawingContext, where a DynamicResource is not
        // reachable, so they are told rather than notified.
        if (PageHost.Content is FrameworkElement content) content.InvalidateVisual();
        RefreshServerList();
    }

    private void OnSettingsChanged(object? sender, PropertyChangedEventArgs e)
    {
        switch (e.PropertyName)
        {
            case nameof(Core.Store.AppSettings.Theme):
            case nameof(Core.Store.AppSettings.UseMicaBackdrop):
                App.Current.ApplyTheme();
                break;
            case nameof(Core.Store.AppSettings.Language):
                Strings.Language = Settings.Language;
                ApplyLanguage();
                // The whole tree carries text, so the current page is rebuilt
                // rather than walked: a language switch is rare and this is
                // the one place it costs nothing to be thorough.
                Show(_page, _openServerId);
                break;
        }
    }

    private void ApplyLanguage()
    {
        Title = Strings.Get("app.title");
        AppTitle.Text = Strings.Get("app.title");
        ServersHeading.Text = Strings.Get("nav.servers");
        NoServers.Text = Strings.Get("nav.noServers");
        AddServerButton.Content = Strings.Get("server.add");
        SettingsButton.Content = Strings.Get("nav.settings");
        RefreshButton.Content = Strings.Get("common.refresh");
        Search.Tag = Strings.Get("common.search");
        ToolTipService.SetToolTip(Search, Strings.Get("common.search"));
        // Named for assistive technology, here rather than in the XAML so a
        // runtime language switch renames them too. A screen reader otherwise
        // met an unnamed edit field and two unnamed lists on every page.
        AutomationProperties.SetName(Search, Strings.Get("common.search"));
        AutomationProperties.SetName(Nav, Strings.Get("nav.goTo"));
        AutomationProperties.SetName(ServerList, Strings.Get("nav.servers"));
        ToolTipService.SetToolTip(RefreshButton, $"{Strings.Get("common.refresh")} (F5)");
        BuildNavigation();
        PageTitle.Text = TitleFor(_page);
    }

    private void BuildNavigation()
    {
        _navigating = true;
        Nav.Items.Clear();
        foreach (var (page, key, isHeader) in _navigation)
        {
            if (isHeader)
            {
                // A header is a non-selectable label, so the group name cannot
                // become the current page — and a NavHeader rather than a
                // ListBoxItem so it reads as a heading to a screen reader
                // instead of as a disabled page.
                Nav.Items.Add(new NavHeader
                {
                    Content = Strings.Get(key),
                    IsEnabled = false,
                    Focusable = false,
                    FontSize = 11,
                    Foreground = (Brush)FindResource("Brush.Tertiary"),
                    Margin = new Thickness(6, 10, 6, 2),
                });
                continue;
            }
            Nav.Items.Add(new ListBoxItem { Content = Strings.Get(key), Tag = page });
        }
        _navigating = false;
        SelectNavigation(_page);
    }

    private void SelectNavigation(Page page)
    {
        _navigating = true;
        Nav.SelectedItem = Nav.Items
            .OfType<ListBoxItem>()
            .FirstOrDefault(item => item.Tag is Page tagged && tagged == page);
        _navigating = false;
    }

    // MARK: - Navigation

    private void OnNavigationChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_navigating) return;
        if (Nav.SelectedItem is ListBoxItem { Tag: Page page }) Show(page);
    }

    private void OnServerSelected(object sender, SelectionChangedEventArgs e)
    {
        if (_navigating) return;
        if (ServerList.SelectedItem is SidebarServer entry) Show(Page.ServerDetail, entry.Id);
    }

    public void OpenServer(Guid serverId) => Show(Page.ServerDetail, serverId);

    /// <summary>
    /// Navigates from outside the shell, as the settings page's pointer at
    /// the rules does.
    /// </summary>
    public void GoTo(Page page) => Show(page);

    /// <summary>Swaps the content area.</summary>
    /// <remarks>
    /// A fresh page instance each time rather than a cache. The pages hold no
    /// state worth keeping across a navigation — they read everything from the
    /// service — and a cache would mean each one keeping its
    /// <see cref="MonitorService.Published"/> subscription alive while
    /// invisible, which is exactly the work the hidden-window rule exists to
    /// avoid.
    /// </remarks>
    private void Show(Page page, Guid? serverId = null)
    {
        _page = page;
        _openServerId = serverId;

        // Tell the service the previous detail page is gone, so its host stops
        // paying for the process list.
        if (PageHost.Content is ServerDetailPage previous) previous.Closing();

        PageHost.Content = page switch
        {
            Page.Dashboard => new DashboardPage(OpenServer),
            Page.Machines => new MachinesPage(OpenServer),
            Page.Identities => new IdentitiesPage(),
            Page.SshKeys => new SshKeysPage(),
            Page.Snippets => new SnippetsPage(),
            Page.Docker => new DockerPage(OpenServer),
            Page.Alerts => new AlertsPage(),
            Page.Sessions => new SessionsPage(),
            Page.Settings => new SettingsPage(),
            Page.ServerDetail when serverId is { } id => new ServerDetailPage(id, () => Show(Page.Dashboard)),
            _ => new DashboardPage(OpenServer),
        };

        PageTitle.Text = TitleFor(page);
        // Search only means something where there is a list to filter.
        Search.Visibility = page is Page.Dashboard or Page.Machines or Page.Snippets
            ? Visibility.Visible
            : Visibility.Collapsed;

        // The same idea for Refresh. This button re-polls the hosts, which is
        // what "refresh" means on the screens that show host readings. The
        // keys page and the container page have their own Refresh — rescan
        // ~/.ssh, reload the container lists — so both were on screen at once,
        // twenty pixels apart, same word, different actions. Only one is ever
        // shown now. F5 still re-polls from anywhere, since a shortcut cannot
        // be mistaken for the other button.
        RefreshButton.Visibility = page is Page.SshKeys or Page.Docker
            ? Visibility.Collapsed
            : Visibility.Visible;

        if (page == Page.ServerDetail)
        {
            _navigating = true;
            Nav.SelectedItem = null;
            _navigating = false;
        }
        else
        {
            SelectNavigation(page);
            _navigating = true;
            ServerList.SelectedItem = null;
            _navigating = false;
        }
    }

    private string TitleFor(Page page) => page switch
    {
        Page.Dashboard => Strings.Get("dashboard.title"),
        Page.Machines => Strings.Get("machines.all"),
        Page.Identities => Strings.Get("nav.identities"),
        Page.SshKeys => Strings.Get("nav.sshKeys"),
        Page.Snippets => Strings.Get("nav.snippets"),
        Page.Docker => Strings.Get("nav.docker"),
        Page.Alerts => Strings.Get("nav.alerts"),
        Page.Sessions => Strings.Get("nav.sessions"),
        Page.Settings => Strings.Get("nav.settings"),
        Page.ServerDetail => Monitor.Server(_openServerId ?? Guid.Empty)?.Name
            ?? Strings.Get("nav.machines"),
        _ => Strings.Get("app.title"),
    };

    // MARK: - Sidebar server list

    /// <summary>One row of the sidebar's server list.</summary>
    /// <remarks>
    /// A projection rather than the <see cref="Server"/> itself, because the
    /// row needs a status brush the model has no business knowing about.
    /// </remarks>
    /// <summary>One row of the sidebar's server list.</summary>
    /// <remarks>
    /// <c>ToString</c> is overridden because a ListBoxItem takes its
    /// accessible name from the item, and a record's generated ToString reads
    /// out as "SidebarServer { Id = 3b712492-…, Name = web-01, StatusBrush =
    /// #FFD11F2E }" — which is what a screen reader would say for every host.
    /// </remarks>
    public sealed record SidebarServer(Guid Id, string Name, Brush StatusBrush)
    {
        public override string ToString() => Name;
    }

    private void RefreshServerList()
    {
        _navigating = true;
        var selected = ServerList.SelectedItem as SidebarServer;
        ServerList.Items.Clear();
        foreach (var server in Monitor.Servers)
        {
            var kind = Monitor.Status.TryGetValue(server.Id, out var status)
                ? status.Kind
                : StatusKind.Unknown;
            ServerList.Items.Add(new SidebarServer(
                server.Id, server.Name, Ink.Brush(Palette.ForStatus(kind))));
        }
        if (selected is not null)
        {
            ServerList.SelectedItem = ServerList.Items
                .OfType<SidebarServer>()
                .FirstOrDefault(s => s.Id == selected.Id);
        }
        _navigating = false;

        NoServers.Visibility = Monitor.Servers.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private void OnPublished()
    {
        RefreshServerList();
        if (_page == Page.ServerDetail) PageTitle.Text = TitleFor(_page);
    }

    // MARK: - Toolbar

    private void OnSearchChanged(object sender, TextChangedEventArgs e)
    {
        if (PageHost.Content is ISearchable searchable) searchable.Search(Search.Text);
    }

    private void OnRefresh(object sender, RoutedEventArgs e) => _ = Monitor.PollAllAsync();

    private void OnAddServer(object sender, RoutedEventArgs e)
    {
        var editor = new ServerEditorWindow(null) { Owner = this };
        if (editor.ShowDialog() == true) Show(Page.Machines);
    }

    private void OnOpenSettings(object sender, RoutedEventArgs e) => Show(Page.Settings);

    // MARK: - Shortcuts

    /// <summary>
    /// The keyboard map from plan §5: Ctrl+1–8 for the pages, Ctrl+N for a new
    /// server, F5 to refresh.
    /// </summary>
    private void InstallShortcuts()
    {
        void Bind(Key key, ModifierKeys modifiers, Action action) =>
            InputBindings.Add(new KeyBinding(new Ui.Command(action), key, modifiers));

        Bind(Key.D1, ModifierKeys.Control, () => Show(Page.Dashboard));
        Bind(Key.D2, ModifierKeys.Control, () => Show(Page.Machines));
        Bind(Key.D3, ModifierKeys.Control, () => Show(Page.Identities));
        Bind(Key.D4, ModifierKeys.Control, () => Show(Page.SshKeys));
        Bind(Key.D5, ModifierKeys.Control, () => Show(Page.Snippets));
        Bind(Key.D6, ModifierKeys.Control, () => Show(Page.Docker));
        Bind(Key.D7, ModifierKeys.Control, () => Show(Page.Alerts));
        Bind(Key.D8, ModifierKeys.Control, () => Show(Page.Sessions));
        Bind(Key.OemComma, ModifierKeys.Control, () => Show(Page.Settings));
        Bind(Key.N, ModifierKeys.Control, () => OnAddServer(this, new RoutedEventArgs()));
        Bind(Key.F5, ModifierKeys.None, () => _ = Monitor.PollAllAsync());
        Bind(Key.F, ModifierKeys.Control, () =>
        {
            if (Search.Visibility == Visibility.Visible) Search.Focus();
        });
    }

    // MARK: - Window state

    private void RestoreGeometry()
    {
        Width = Math.Max(MinWidth, Settings.WindowWidth);
        Height = Math.Max(MinHeight, Settings.WindowHeight);
        if (!double.IsNaN(Settings.WindowLeft) && !double.IsNaN(Settings.WindowTop))
        {
            // Only if it would land on a screen that still exists: restoring a
            // window to a monitor that has been unplugged puts it somewhere
            // the user cannot reach it.
            var virtualScreen = new Rect(
                SystemParameters.VirtualScreenLeft,
                SystemParameters.VirtualScreenTop,
                SystemParameters.VirtualScreenWidth,
                SystemParameters.VirtualScreenHeight);
            var wanted = new Rect(Settings.WindowLeft, Settings.WindowTop, Width, Height);
            if (virtualScreen.IntersectsWith(wanted))
            {
                WindowStartupLocation = WindowStartupLocation.Manual;
                Left = Settings.WindowLeft;
                Top = Settings.WindowTop;
            }
        }
        if (Settings.WindowMaximized) WindowState = WindowState.Maximized;
    }

    protected override void OnStateChanged(EventArgs e)
    {
        base.OnStateChanged(e);
        if (WindowState == WindowState.Minimized) App.Current.WindowHidden();
        else App.Current.Monitor.SetUiVisible(true);
    }

    protected override void OnClosing(CancelEventArgs e)
    {
        SaveGeometry();

        switch (CloseBehaviour.For(Settings.CloseToTray))
        {
            case CloseAction.HideToTray:
                // Closing leaves the app collecting in the notification area —
                // the counterpart of the macOS build's menu-bar residency, and
                // the reason a monitor can be "closed" without stopping.
                e.Cancel = true;
                Hide();
                App.Current.WindowHidden();
                return;

            default:
                // The setting is off, so closing the window means closing the
                // app — and it has to be said explicitly, because
                // ShutdownMode is OnExplicitShutdown. Without this the window
                // went away and the process stayed: polling, with no UI, and
                // no way back since a closed Window cannot be shown again.
                base.OnClosing(e);
                if (!e.Cancel) App.Current.QuitApp();
                return;
        }
    }

    private void SaveGeometry()
    {
        // RestoreBounds rather than the live values: while maximised, Left and
        // Top are the maximised frame's, and restoring to those puts the
        // window off-screen on a smaller display next time.
        var bounds = WindowState == WindowState.Normal
            ? new Rect(Left, Top, Width, Height)
            : RestoreBounds;
        Settings.SaveWindowState(
            bounds.Left, bounds.Top, bounds.Width, bounds.Height,
            WindowState == WindowState.Maximized);
    }

    protected override void OnClosed(EventArgs e)
    {
        Monitor.Published -= OnPublished;
        Settings.PropertyChanged -= OnSettingsChanged;
        // The panes are not windows any more, so nothing else ends their
        // sessions: the shell channel and the history row both close here.
        // Deliberately not in OnClosing — the close-to-tray path cancels that
        // close and only hides the window, and a session must survive it.
        Dock.CloseAll();
        base.OnClosed(e);
    }
}

/// <summary>A page the toolbar's search box can filter.</summary>
public interface ISearchable
{
    void Search(string query);
}
