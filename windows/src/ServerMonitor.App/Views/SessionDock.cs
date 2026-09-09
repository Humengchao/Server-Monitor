using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// Terminal and SFTP sessions, in the main window rather than in windows of
/// their own.
/// </summary>
/// <remarks>
/// Both used to be top-level windows. The macOS build does not work that way
/// — its RootView hosts a session inside the main window and switches on the
/// session's kind — and a monitoring app that scatters windows across the
/// desktop was the complaint that started this.
///
/// A dock at the bottom rather than a tab on the machine screen, which is
/// where macOS puts the terminal. The machine screen's content scrolls, and
/// the terminal is an HwndHost: it does not clip to a viewport, it floats over
/// whatever is above and below it. More to the point, a pane on the machine
/// screen dies when the user navigates away, which is worse than the window
/// it replaced — the one thing a separate window was genuinely good at is
/// staying open while you look at something else, and that is the property
/// worth keeping.
///
/// The airspace rule shapes the rest: exactly one pane is
/// <see cref="Visibility.Visible"/> and the others are
/// <see cref="Visibility.Collapsed"/>, never Hidden, because a hidden
/// HwndHost still owns its rectangle of the screen.
/// </remarks>
internal sealed class SessionDock : UserControl
{
    private readonly StackPanel _tabs = Ui.Columns(4);
    private readonly Grid _panes = new();
    private readonly List<Session> _sessions = [];

    private Session? _active;

    /// <summary>Raised when a session opens or closes, so the host can size itself.</summary>
    public event Action? Changed;

    public SessionDock()
    {
        var strip = new Border
        {
            Background = (System.Windows.Media.Brush)FindResource("Brush.Sidebar"),
            Padding = new Thickness(8, 5, 8, 5),
            // Sideways only: the strip is one row of tabs and a vertical
            // scrollbar on it would be a bug, not a feature.
            Child = new ScrollViewer
            {
                Content = _tabs,
                VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
                HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
                Padding = new Thickness(0),
            },
        };

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(strip, 0);
        Grid.SetRow(_panes, 1);
        root.Children.Add(strip);
        root.Children.Add(_panes);
        Content = root;
    }

    public bool HasSessions => _sessions.Count > 0;

    /// <summary>
    /// Opens a session, or brings an existing one forward.
    /// </summary>
    /// <param name="command">
    /// Set only by the container list's "exec": a request that carries a
    /// command is always its own session, because the command is the point of
    /// it and running it in a shell somebody else is using would be rude.
    /// </param>
    public void Open(Server server, SessionKind kind, string? command = null, string? title = null)
    {
        var reuse = IndexToReuse(
            [.. _sessions.Select(s => (s.ServerId, s.Kind))], server.Id, kind, command is { Length: > 0 });
        if (reuse >= 0)
        {
            Activate(_sessions[reuse]);
            return;
        }

        ISessionPane pane = kind == SessionKind.Terminal
            ? new TerminalPane(server, command, title)
            : new SftpPane(server);

        var session = new Session(server.Id, kind, pane, server);
        _sessions.Add(session);
        var element = (UIElement)pane;
        element.Visibility = Visibility.Collapsed;
        _panes.Children.Add(element);

        Activate(session);
        Changed?.Invoke();
    }

    /// <summary>
    /// Which open session a request should land on, or -1 for a new one.
    /// </summary>
    /// <remarks>
    /// Pure, and separate from the tree, because this is the rule people will
    /// argue about rather than the plumbing. Clicking Terminal twice on the
    /// same host should not leave two identical shells behind; asking a
    /// container for a shell should never join one already in use.
    /// </remarks>
    internal static int IndexToReuse(
        IReadOnlyList<(Guid ServerId, SessionKind Kind)> open,
        Guid serverId,
        SessionKind kind,
        bool hasCommand)
    {
        if (hasCommand) return -1;
        for (var i = 0; i < open.Count; i++)
        {
            if (open[i].ServerId == serverId && open[i].Kind == kind) return i;
        }
        return -1;
    }

    /// <summary>Another session on the active one's host, for a second shell.</summary>
    public void Duplicate()
    {
        if (_active is not { } active) return;
        var server = App.Current.Monitor.Server(active.ServerId) ?? active.Server;

        ISessionPane pane = active.Kind == SessionKind.Terminal
            ? new TerminalPane(server)
            : new SftpPane(server);
        var session = new Session(server.Id, active.Kind, pane, server);
        _sessions.Add(session);
        var element = (UIElement)pane;
        element.Visibility = Visibility.Collapsed;
        _panes.Children.Add(element);

        Activate(session);
        Changed?.Invoke();
    }

    public void Close(Session session)
    {
        if (!_sessions.Remove(session)) return;
        session.Pane.CloseSession();
        _panes.Children.Remove((UIElement)session.Pane);

        if (_active == session)
        {
            _active = null;
            // The one to its right, or the last one — whichever the list still
            // has. Closing a tab should not dump the user on an empty dock
            // while other sessions are still running.
            if (_sessions.Count > 0) Activate(_sessions[^1]);
        }

        Rebuild();
        Changed?.Invoke();
    }

    /// <summary>Shuts every session down. Called on the way out.</summary>
    public void CloseAll()
    {
        foreach (var session in _sessions) session.Pane.CloseSession();
        _sessions.Clear();
        _panes.Children.Clear();
        _active = null;
        Rebuild();
        Changed?.Invoke();
    }

    private void Activate(Session session)
    {
        _active = session;
        foreach (var open in _sessions)
        {
            ((UIElement)open.Pane).Visibility =
                open == session ? Visibility.Visible : Visibility.Collapsed;
        }
        Rebuild();
        ((UIElement)session.Pane).Focus();
    }

    private void Rebuild()
    {
        _tabs.Children.Clear();
        foreach (var session in _sessions)
        {
            _tabs.Children.Add(Tab(session));
        }

        if (_sessions.Count == 0) return;

        // Trailing, and quiet: a second shell on the same host is a deliberate
        // thing to want, not the default a click should give you.
        var add = Ui.Quiet("+", Duplicate);
        add.ToolTip = Strings.Get("session.duplicate");
        add.MinWidth = 30;
        _tabs.Children.Add(add);
    }

    private UIElement Tab(Session session)
    {
        var label = Ui.Button(
            session.Pane.Label,
            () => Activate(session),
            session == _active ? "Button.Accent" : "Button.Quiet");
        // A host name is as long as the user made it, and the strip scrolls
        // sideways rather than growing without limit.
        label.MaxWidth = 220;

        var close = Ui.Quiet("✕", () => Close(session));
        close.MinWidth = 26;
        close.Padding = new Thickness(0);
        System.Windows.Automation.AutomationProperties.SetName(
            close, $"{Strings.Get("common.close")} {session.Pane.Label}");

        var tab = Ui.Columns(0, label, close);
        tab.Margin = new Thickness(0, 0, 4, 0);
        return tab;
    }

    /// <summary>One open session.</summary>
    /// <remarks>
    /// Holds the server it was opened for as well as the id: the row can be
    /// deleted from under a live shell, and a tab that has to say whose it is
    /// cannot go and look it up.
    /// </remarks>
    internal sealed record Session(
        Guid ServerId, SessionKind Kind, ISessionPane Pane, Server Server);
}
