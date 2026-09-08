using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// The machine inventory: group cards over a table of every host.
/// </summary>
public sealed class MachinesPage : UserControl, ISearchable
{
    private readonly Action<Guid> _openServer;
    private readonly StackPanel _root = Ui.Rows(0);
    private string _query = string.Empty;
    /// <summary>Null means "every tag"; otherwise only hosts wearing it.</summary>
    private string? _tagFilter;

    private static MonitorService Monitor => App.Current.Monitor;

    public MachinesPage(Action<Guid> openServer)
    {
        _openServer = openServer;
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);

        Monitor.Published += Rebuild;
        Unloaded += (_, _) => Monitor.Published -= Rebuild;
        Rebuild();
    }

    public void Search(string query)
    {
        _query = query.Trim();
        Rebuild();
    }

    private List<Server> Visible()
    {
        IEnumerable<Server> servers = Monitor.Servers;
        if (_tagFilter is { } tag)
        {
            servers = servers.Where(s =>
                s.Tags.Any(t => t.Equals(tag, StringComparison.OrdinalIgnoreCase)));
        }
        if (_query.Length > 0)
        {
            servers = servers.Where(s =>
                s.Name.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || s.Host.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || s.Username.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || s.Tags.Any(t => t.Contains(_query, StringComparison.OrdinalIgnoreCase)));
        }
        return servers.ToList();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        if (Monitor.Servers.Count == 0)
        {
            _root.Children.Add(Ui.Empty(
                "dashboard.emptyTitle",
                "dashboard.empty",
                FirstRun.Actions(Window.GetWindow(this), Rebuild)));
            return;
        }

        _root.Children.Add(Toolbar());
        if (Monitor.Groups.Count > 0) _root.Children.Add(GroupCards());
        _root.Children.Add(Table());
    }

    private UIElement Toolbar()
    {
        var row = Ui.Columns(8);

        // Tag filter. Derived by scanning the servers rather than stored,
        // which is the same reason tagList is a comma-separated column and not
        // a join table.
        var tags = Monitor.Servers
            .SelectMany(s => s.Tags)
            .Distinct(StringComparer.OrdinalIgnoreCase)
            .OrderBy(t => t, StringComparer.CurrentCultureIgnoreCase)
            .ToList();

        if (tags.Count > 0)
        {
            var options = new List<string?> { null };
            options.AddRange(tags.Cast<string?>());
            row.Children.Add(Ui.Picker(
                options,
                _tagFilter,
                tag => tag ?? Strings.Get("server.allTags"),
                tag =>
                {
                    _tagFilter = tag;
                    Rebuild();
                }));
        }

        row.Children.Add(Ui.Button(Strings.Get("import.title"), ImportSshConfig));
        row.Children.Add(Ui.Button(Strings.Get("group.new"), NewGroup));
        row.Children.Add(Ui.Accent(Strings.Get("server.add"), AddServer));
        row.Margin = new Thickness(0, 0, 0, 14);
        row.HorizontalAlignment = HorizontalAlignment.Left;
        return row;
    }

    /// <summary>One card per group, plus the ungrouped machines.</summary>
    private UIElement GroupCards()
    {
        var wrap = new WrapPanel { Margin = new Thickness(0, 0, 0, 14) };
        foreach (var group in Monitor.Groups)
        {
            var members = Monitor.ServersIn(group);
            var tint = Palette.ForGroup(group.ColorName);
            var heading = Ui.Columns(8, Ui.Dot(tint, 9), Ui.Title(group.Name));

            var actions = Ui.Columns(6,
                Ui.Quiet(Strings.Get("common.edit"), () => EditGroup(group)),
                Ui.Quiet(Strings.Get("common.delete"), () => DeleteGroup(group)));

            var names = members.Count == 0
                ? Ui.Tertiary(Strings.Get("common.none"))
                : Ui.Wrapped(
                    string.Join(", ", members.Select(m => m.Name)), "Text.Caption");

            var card = Ui.Card(Ui.Rows(6,
                heading,
                Ui.Caption(Strings.Get("group.machines",
                    members.Count.ToString(System.Globalization.CultureInfo.InvariantCulture))),
                names,
                actions));
            card.Width = 260;
            card.Margin = new Thickness(0, 0, 12, 12);
            wrap.Children.Add(card);
        }

        var ungrouped = Monitor.UngroupedServers;
        if (ungrouped.Count > 0)
        {
            var card = Ui.Card(Ui.Rows(6,
                Ui.Title(Strings.Get("group.none")),
                Ui.Caption(Strings.Get("group.machines",
                    ungrouped.Count.ToString(System.Globalization.CultureInfo.InvariantCulture))),
                Ui.Wrapped(string.Join(", ", ungrouped.Select(m => m.Name)), "Text.Caption")));
            card.Width = 260;
            card.Margin = new Thickness(0, 0, 12, 12);
            wrap.Children.Add(card);
        }
        return wrap;
    }

    /// <summary>One row per host.</summary>
    /// <remarks>
    /// A projection, so the table's columns bind to strings and brushes rather
    /// than to the model plus a pile of converters.
    /// </remarks>
    private sealed record Row(
        Guid Id,
        string Flag,
        string Name,
        string Target,
        string Os,
        string Group,
        string Cores,
        string Memory,
        string Disk,
        string Status,
        System.Windows.Media.Brush StatusBrush,
        string Tags,
        Server Server);

    private UIElement Table()
    {
        var list = new ListView
        {
            Style = (Style)FindResource("Table"),
            // The page itself scrolls, so the table must not: nesting two
            // scrollers means the wheel does something different depending on
            // where the pointer is.
            MaxHeight = double.PositiveInfinity,
        };
        ScrollViewer.SetVerticalScrollBarVisibility(list, ScrollBarVisibility.Disabled);
        // Sideways, though. Nine columns need about 1100px and the window may
        // be 900 wide with 228 of that spent on the sidebar — at which point
        // memory, disk and tags were simply clipped, with no scrollbar and no
        // way to reach them. Horizontal nesting has none of the wheel
        // ambiguity that vertical nesting does, because the wheel is vertical.
        ScrollViewer.SetHorizontalScrollBarVisibility(list, ScrollBarVisibility.Auto);

        var view = new GridView { AllowsColumnReorder = false };
        void Column(string headerKey, string path, double width, bool tooltip = false) =>
            view.Columns.Add(Ui.TextColumn(Strings.Get(headerKey), path, width, tooltip));

        // The status dot needs a brush, so it is the one templated column.
        view.Columns.Add(new GridViewColumn
        {
            Header = string.Empty,
            Width = 26,
            CellTemplate = DotTemplate(),
        });
        Column("server.name", nameof(Row.Name), 170, tooltip: true);
        Column("server.host", nameof(Row.Target), 190, tooltip: true);
        Column("server.osKind", nameof(Row.Os), 80);
        Column("group.assign", nameof(Row.Group), 100, tooltip: true);
        Column("metric.cores", nameof(Row.Cores), 60);
        Column("metric.memory", nameof(Row.Memory), 90);
        Column("metric.disk", nameof(Row.Disk), 90);
        Column("server.tags", nameof(Row.Tags), 140, tooltip: true);
        list.View = view;

        foreach (var server in Visible())
        {
            var status = Monitor.Status.TryGetValue(server.Id, out var value)
                ? value
                : ServerStatus.Unknown;
            var group = Monitor.Groups.FirstOrDefault(g => g.Id == server.GroupId);
            list.Items.Add(new Row(
                server.Id,
                // The country code, not Format.Flag's emoji: see
                // Ui.CountryBadge — Windows has no flag glyphs.
                server.CountryCode,
                server.Name,
                server.DisplayTarget,
                server.OsKind switch
                {
                    OSKind.Linux => Strings.Get("server.osLinux"),
                    OSKind.Windows => Strings.Get("server.osWindows"),
                    _ => Strings.Get("server.osAuto"),
                },
                group?.Name ?? Strings.Get("group.none"),
                server.Cores > 0 ? server.Cores.ToString(System.Globalization.CultureInfo.InvariantCulture) : "—",
                server.MemoryTotal > 0 ? Format.Bytes(server.MemoryTotal) : "—",
                server.DiskTotal > 0 ? Format.Bytes(server.DiskTotal) : "—",
                status.Kind == StatusKind.Offline
                    ? Strings.Get("common.offline")
                    : status.IsOnline ? Strings.Get("common.online") : Strings.Get("common.connecting"),
                Ink.Brush(Palette.ForStatus(status.Kind)),
                string.Join(", ", server.Tags),
                server));
        }

        // Double-click opens the host; the context menu is where editing and
        // deleting live, so a stray click cannot destroy a row.
        list.MouseDoubleClick += (_, _) =>
        {
            if (list.SelectedItem is Row row) _openServer(row.Id);
        };
        list.ContextMenu = RowMenu(list);
        return Ui.Card(list, 8);
    }

    private static DataTemplate DotTemplate()
    {
        var factory = new FrameworkElementFactory(typeof(System.Windows.Shapes.Ellipse));
        factory.SetValue(FrameworkElement.WidthProperty, 8.0);
        factory.SetValue(FrameworkElement.HeightProperty, 8.0);
        factory.SetValue(FrameworkElement.VerticalAlignmentProperty, VerticalAlignment.Center);
        factory.SetBinding(
            System.Windows.Shapes.Shape.FillProperty,
            new System.Windows.Data.Binding(nameof(Row.StatusBrush)));
        return new DataTemplate { VisualTree = factory };
    }

    /// <summary>SFTP has no L10n key of its own — it is the same word everywhere.</summary>
    private MenuItem FilesItem(ListView list)
    {
        var item = new MenuItem { Header = "SFTP" };
        item.Click += (_, _) =>
        {
            if (list.SelectedItem is Row row) Windows.Files(Window.GetWindow(this), row.Server);
        };
        return item;
    }

    private ContextMenu RowMenu(ListView list)
    {
        var menu = new ContextMenu();

        void Item(string key, Action<Row> action)
        {
            var item = new MenuItem { Header = Strings.Get(key) };
            item.Click += (_, _) =>
            {
                if (list.SelectedItem is Row row) action(row);
            };
            menu.Items.Add(item);
        }

        Item("nav.overview", row => _openServer(row.Id));
        Item("nav.terminal", row => Windows.Terminal(Window.GetWindow(this), row.Server));
        menu.Items.Add(FilesItem(list));
        Item("common.edit", row => EditServer(row.Server));
        Item("common.testConnection", row => TestConnection(row.Server));
        menu.Items.Add(new Separator());

        // Group assignment, rebuilt on open so a group added meanwhile shows.
        var assign = new MenuItem { Header = Strings.Get("group.assign") };
        menu.Opened += (_, _) =>
        {
            assign.Items.Clear();
            var none = new MenuItem { Header = Strings.Get("group.none") };
            none.Click += (_, _) =>
            {
                if (list.SelectedItem is Row row) Monitor.Assign(row.Server, null);
            };
            assign.Items.Add(none);
            foreach (var group in Monitor.Groups)
            {
                var item = new MenuItem { Header = group.Name };
                var captured = group;
                item.Click += (_, _) =>
                {
                    if (list.SelectedItem is Row row) Monitor.Assign(row.Server, captured);
                };
                assign.Items.Add(item);
            }
        };
        menu.Items.Add(assign);

        menu.Items.Add(new Separator());
        Item("common.delete", DeleteServer);
        return menu;
    }

    // MARK: - Actions

    private void AddServer()
    {
        var editor = new ServerEditorWindow(null) { Owner = Window.GetWindow(this) };
        if (editor.ShowDialog() == true) Rebuild();
    }

    private void EditServer(Server server)
    {
        var editor = new ServerEditorWindow(server) { Owner = Window.GetWindow(this) };
        if (editor.ShowDialog() == true) Rebuild();
    }

    private void DeleteServer(Row row)
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("server.deleteConfirm", row.Name))) return;
        Monitor.DeleteServer(row.Server);
        Rebuild();
    }

    private async void TestConnection(Server server)
    {
        try
        {
            await Monitor.TestConnectionAsync(Monitor.Target(server), server.OsKind);
            Ui.Inform(Window.GetWindow(this), Strings.Get("server.connectionOK"));
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }

    private void ImportSshConfig()
    {
        var window = new ImportSshConfigWindow { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() == true) Rebuild();
    }

    private void NewGroup() => EditGroup(null);

    private void EditGroup(MachineGroup? existing)
    {
        var window = new GroupEditorWindow(existing) { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() == true) Rebuild();
    }

    private void DeleteGroup(MachineGroup group)
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("group.deleteConfirm", group.Name))) return;
        Monitor.DeleteGroup(group.Id);
        Rebuild();
    }
}
