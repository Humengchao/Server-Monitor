using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Terminal;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// An interactive shell on one host.
/// </summary>
/// <remarks>
/// Its own window rather than a page, because a terminal is something you
/// leave open beside what you are doing — the same reason the macOS build
/// opens one per host. The toolbar is above the terminal and not over it: the
/// control is an HwndHost and WPF cannot draw on top of it (R3's airspace
/// limit), so a snippet picker floating over the session is not available and
/// a toolbar button is.
/// </remarks>
public sealed class TerminalWindow : Window
{
    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    private readonly Server _server;
    private readonly string? _command;
    private readonly TerminalHost _host = new();
    private readonly TextBlock _status = Ui.Caption("");
    private readonly Button _reconnect;

    private SessionRecord? _session;
    private bool _connected;

    /// <summary>
    /// Opens a shell, or runs one command interactively.
    /// </summary>
    /// <param name="command">
    /// Typed and entered as soon as the shell is ready — how "docker exec" and
    /// a container's shell get here without a second kind of window.
    /// </param>
    public TerminalWindow(Server server, string? command = null, string? title = null)
    {
        _server = server;
        _command = command;

        Title = title ?? $"{Strings.Get("nav.terminal")} — {server.Name}";
        Width = 980;
        Height = 620;
        MinWidth = 480;
        MinHeight = 260;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _reconnect = Ui.Quiet(Strings.Get("terminal.reconnect"), () => _ = ConnectAsync());

        var toolbar = Ui.Columns(8,
            Ui.Title(server.Name),
            _status,
            Ui.Quiet(Strings.Get("terminal.snippets"), ShowSnippets),
            Ui.Quiet(Strings.Get("common.copy"), _host.Copy),
            Ui.Quiet(Strings.IsChinese ? "粘贴" : "Paste", _host.Paste),
            _reconnect);
        foreach (var child in toolbar.Children.OfType<FrameworkElement>())
        {
            child.VerticalAlignment = VerticalAlignment.Center;
        }
        toolbar.Margin = new Thickness(12, 8, 12, 8);

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(toolbar, 0);
        Grid.SetRow(_host, 1);
        root.Children.Add(toolbar);
        root.Children.Add(_host);
        Content = root;

        _host.Ended += OnEnded;

        // Ctrl+Shift+C/V, because Ctrl+C belongs to the far side — it is how
        // you stop a runaway process, and binding it to copy is the single
        // most annoying thing a terminal can do.
        InputBindings.Add(new KeyBinding(
            new Ui.Command(_host.Copy), Key.C, ModifierKeys.Control | ModifierKeys.Shift));
        InputBindings.Add(new KeyBinding(
            new Ui.Command(_host.Paste), Key.V, ModifierKeys.Control | ModifierKeys.Shift));

        Loaded += (_, _) =>
        {
            ApplyTheme();
            _ = ConnectAsync();
        };
        Closed += (_, _) =>
        {
            _host.Disconnect();
            EndSession();
        };
    }

    private void ApplyTheme() => _host.ApplyTheme(
        Theme.Palette.IsDark,
        App.Current.Settings.TerminalFontName,
        (int)App.Current.Settings.TerminalFontSize);

    private async Task ConnectAsync()
    {
        SetStatus(Strings.Get("terminal.connect"), connected: false);
        _reconnect.IsEnabled = false;
        try
        {
            var connection = await OpenAsync().ConfigureAwait(true);
            _host.Connect(connection);
            SetStatus(Strings.Get("terminal.connected"), connected: true);
            StartSession();

            if (_command is { Length: > 0 } command)
            {
                // Entered rather than left at the prompt: this path is not a
                // snippet the user is about to review, it is the thing they
                // asked to run when they opened the window.
                _host.Type(command + "\r");
            }
        }
        catch (Exception error)
        {
            SetStatus(error.Message, connected: false);
        }
        finally
        {
            _reconnect.IsEnabled = true;
        }
    }

    /// <summary>
    /// Opens the shell channel.
    /// </summary>
    /// <remarks>
    /// On the pooled connection the poll is already using, so opening a
    /// terminal costs a channel rather than a handshake and a second entry in
    /// the host's auth log. The ssh.exe route has no equivalent — it would
    /// need its own subprocess with a pty — so a host forced onto that
    /// transport says so rather than silently opening a second connection.
    /// </remarks>
    private async Task<ShellStreamConnection> OpenAsync()
    {
        if (App.Current.LibraryTransport is not { } transport)
        {
            throw new InvalidOperationException(
                Strings.IsChinese
                    ? "内置 SSH 传输不可用。"
                    : "The built-in SSH transport is unavailable.");
        }

        var target = Monitor.Target(_server);
        var client = await transport.LeaseClientAsync(target).ConfigureAwait(true);
        // 256 colours by name, because that is what the far side keys its
        // capability database off; the control renders the full palette.
        var stream = client.CreateShellStream(
            "xterm-256color",
            (uint)Math.Max(20, _host.Columns),
            (uint)Math.Max(5, _host.Rows),
            0,
            0,
            8192);
        return new ShellStreamConnection(
            stream,
            action => Dispatcher.Invoke(action),
            App.Log);
    }

    private void SetStatus(string text, bool connected)
    {
        _connected = connected;
        _status.Text = text;
        _status.Foreground = Theme.Ink.Brush(
            connected ? Theme.Palette.Online : Theme.Palette.Tertiary);
    }

    private void OnEnded()
    {
        if (!_connected) return;
        SetStatus(Strings.Get("terminal.disconnected"), connected: false);
        EndSession();
    }

    // MARK: - Session history

    private void StartSession()
    {
        if (_session is not null) return;
        _session = new SessionRecord
        {
            ServerId = _server.Id,
            ServerName = _server.Name,
            Kind = SessionKind.Terminal,
        };
        Monitor.Database.Save(_session);
    }

    private void EndSession()
    {
        if (_session is not { } session) return;
        session.EndedAt = DateTime.UtcNow;
        Monitor.Database.Save(session);
        _session = null;
    }

    // MARK: - Snippets

    /// <summary>
    /// The snippet picker, grouped by category.
    /// </summary>
    /// <remarks>
    /// A context menu opened from the toolbar button. Chosen because it is the
    /// one kind of popup that is allowed to appear over an HwndHost — it is a
    /// window of its own, not WPF content in the same one.
    /// </remarks>
    private void ShowSnippets()
    {
        var menu = new ContextMenu { PlacementTarget = this };
        if (Monitor.Snippets.Count == 0)
        {
            menu.Items.Add(new MenuItem
            {
                Header = Strings.Get("terminal.noSnippets"),
                IsEnabled = false,
            });
        }
        else
        {
            foreach (var group in Monitor.Snippets.GroupBy(s => s.Category))
            {
                ItemsControl parent = menu;
                if (group.Key.Length > 0)
                {
                    var submenu = new MenuItem { Header = group.Key };
                    menu.Items.Add(submenu);
                    parent = submenu;
                }
                foreach (var snippet in group)
                {
                    var item = new MenuItem { Header = snippet.Name };
                    item.Click += (_, _) => Insert(snippet);
                    parent.Items.Add(item);
                }
            }
        }
        menu.IsOpen = true;
    }

    private void Insert(Snippet snippet)
    {
        // No trailing return: the command lands at the prompt for the user to
        // read and send. Counting the use here rather than on send is
        // deliberate — inserting is the act the counter is about.
        _host.Type(snippet.Command);
        snippet.UseCount++;
        Monitor.Save(snippet);
        _host.Focus();
    }
}
