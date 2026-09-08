using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// All hosts at a glance: three counters over a grid of server cards.
/// </summary>
public sealed class DashboardPage : UserControl, ISearchable
{
    private readonly Action<Guid> _openServer;
    private readonly WrapPanel _cards = new();
    private readonly StackPanel _counters = Ui.Columns(14);
    private readonly Grid _root = new();
    private string _query = string.Empty;

    private static MonitorService Monitor => App.Current.Monitor;

    public DashboardPage(Action<Guid> openServer)
    {
        _openServer = openServer;

        _root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        _root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(_counters, 0);
        _counters.Margin = new Thickness(20, 6, 20, 14);
        _root.Children.Add(_counters);

        var scroll = Ui.Scroll(_cards);
        _cards.Margin = new Thickness(20, 0, 20, 20);
        Grid.SetRow(scroll, 1);
        _root.Children.Add(scroll);

        Content = _root;

        Monitor.Published += Rebuild;
        Unloaded += (_, _) => Monitor.Published -= Rebuild;
        Rebuild();
    }

    public void Search(string query)
    {
        _query = query.Trim();
        Rebuild();
    }

    /// <summary>
    /// The hosts the search leaves visible.
    /// </summary>
    /// <remarks>
    /// Tags are searchable because they are only useful if searching for one
    /// finds the machines wearing it.
    /// </remarks>
    private List<Server> Visible()
    {
        if (_query.Length == 0) return [.. Monitor.Servers];
        return Monitor.Servers.Where(server =>
                server.Name.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || server.Host.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || server.Username.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || server.Tags.Any(tag => tag.Contains(_query, StringComparison.OrdinalIgnoreCase)))
            .ToList();
    }

    /// <summary>
    /// Rebuilds the whole grid.
    /// </summary>
    /// <remarks>
    /// Rebuilt rather than data-bound, once per publish. That is one pass over
    /// nine cards, which is what the tick barrier exists to make it — and it
    /// is why the cards themselves hold no state: there is nothing to preserve
    /// across a rebuild, so there is nothing to get stale.
    /// </remarks>
    private void Rebuild()
    {
        _counters.Children.Clear();
        _cards.Children.Clear();

        if (Monitor.Servers.Count == 0)
        {
            _counters.Visibility = Visibility.Collapsed;
            // The same two offers the machines page makes, so a first launch
            // shows them wherever the user happens to land.
            _cards.Children.Add(Ui.Empty(
                "dashboard.emptyTitle", "dashboard.empty", FirstRun.Actions(Window.GetWindow(this), Rebuild)));
            return;
        }
        _counters.Visibility = Visibility.Visible;

        var statuses = Monitor.Servers
            .Select(s => Monitor.Status.TryGetValue(s.Id, out var status) ? status : ServerStatus.Unknown)
            .ToList();
        var online = statuses.Count(s => s.IsOnline);
        var offline = statuses.Count(s => s.Kind == StatusKind.Offline);

        _counters.Children.Add(Counter("dashboard.total", Monitor.Servers.Count, Palette.Accent));
        _counters.Children.Add(Counter("dashboard.online", online, Palette.Online));
        _counters.Children.Add(Counter("dashboard.offline", offline, Palette.Offline));

        foreach (var server in Visible()) _cards.Children.Add(CardFor(server));
    }


    private UIElement Counter(string key, int value, Color tint)
    {
        var number = Ui.Number(value.ToString(System.Globalization.CultureInfo.InvariantCulture), 26);
        number.VerticalAlignment = VerticalAlignment.Center;
        var card = Ui.Card(Ui.Rows(8,
            Ui.Caption(Strings.Get(key)),
            Ui.Columns(8, Ui.Dot(tint, 9), number)));
        card.Width = 180;
        return card;
    }

    /// <summary>
    /// One host: identity and latency, host facts, then the live metrics.
    /// </summary>
    /// <remarks>
    /// Every state is pinned to the same height. An offline card that
    /// collapsed to two lines of text was ~55px shorter than its neighbours on
    /// the macOS build, which made the grid reflow every time a host went
    /// down.
    /// </remarks>
    private UIElement CardFor(Server server)
    {
        var status = Monitor.Status.TryGetValue(server.Id, out var value) ? value : ServerStatus.Unknown;
        var snapshot = Monitor.Latest.TryGetValue(server.Id, out var latest) ? latest : null;

        var metrics = new Border
        {
            MinHeight = 88,
            Child = MetricsFor(status, snapshot),
        };

        var card = Ui.ClickableCard(
            Ui.Rows(10, Header(server, status, snapshot), Facts(server, snapshot), Ui.Separator(), metrics),
            () => _openServer(server.Id),
            // The host's name and state, so tabbing through the dashboard
            // reads as "web-01, offline" rather than as ten unnamed panes.
            name: $"{server.Name} — {StatusWord(status.Kind)}");
        // Right-click for the two things you would otherwise open the detail
        // page to reach.
        card.ContextMenu = CardMenu(server);
        card.Width = 400;
        card.Margin = new Thickness(0, 0, 14, 14);
        return card;
    }

    /// <summary>
    /// The state in a word, for the card's accessible name.
    /// </summary>
    /// <remarks>
    /// Localised, because it is read aloud. The visible badge says the same
    /// thing in colour and shape, which a screen reader cannot see.
    /// </remarks>
    private static string StatusWord(StatusKind kind) => kind switch
    {
        StatusKind.Online => Strings.Get("common.online"),
        StatusKind.Offline => Strings.Get("common.offline"),
        StatusKind.Polling => Strings.Get("common.connecting"),
        _ => Strings.Get("common.unknown"),
    };

    private ContextMenu CardMenu(Server server)
    {
        var menu = new ContextMenu();

        void Item(string header, Action action)
        {
            var item = new MenuItem { Header = header };
            item.Click += (_, _) => action();
            menu.Items.Add(item);
        }

        Item(Strings.Get("nav.overview"), () => _openServer(server.Id));
        Item(Strings.Get("nav.terminal"), () => Windows.Terminal(Window.GetWindow(this), server));
        Item("SFTP", () => Windows.Files(Window.GetWindow(this), server));
        return menu;
    }

    private UIElement Header(Server server, ServerStatus status, MetricSnapshot? snapshot)
    {
        var line = Ui.Columns(8);

        if (server.CountryCode.Length > 0)
        {
            line.Children.Add(Ui.CountryBadge(server.CountryCode));
        }

        var name = Ui.Title(server.Name);
        name.VerticalAlignment = VerticalAlignment.Center;
        line.Children.Add(name);

        if (server.Tags.Count > 0)
        {
            var chips = Ui.TagChips(server.Tags, 3);
            chips.Margin = new Thickness(8, 0, 0, 0);
            line.Children.Add(chips);
        }

        var badge = Badge(server, status, snapshot);
        var row = Ui.Grid("*,auto", line, badge);
        badge.HorizontalAlignment = HorizontalAlignment.Right;
        return row;
    }

    private static FrameworkElement Badge(Server server, ServerStatus status, MetricSnapshot? snapshot)
    {
        switch (status.Kind)
        {
            case StatusKind.Online:
                {
                    var milliseconds = snapshot?.LatencyMs ?? 0;
                    var latency = Ui.Number(Format.Latency(milliseconds), 13, Palette.ForLatency(milliseconds));
                    latency.VerticalAlignment = VerticalAlignment.Center;
                    return Ui.Columns(6, Ui.Dot(Palette.Online), latency);
                }
            case StatusKind.Offline:
                {
                    var label = Ui.Text(Strings.Get("common.offline"));
                    label.Foreground = Ink.Brush(Palette.Offline);
                    label.VerticalAlignment = VerticalAlignment.Center;
                    var badge = Ui.Columns(6, Ui.Dot(Palette.Offline), label);
                    // The reason is spelled out on the detail page; on a card
                    // it has to fit in a tooltip or the card loses its fixed
                    // height.
                    ToolTipService.SetToolTip(badge, status.Reason);
                    return badge;
                }
            default:
                return Ui.Spinner();
        }
    }

    /// <summary>Cores, memory, disk and uptime.</summary>
    /// <remarks>
    /// A host never reached has zeroes in its row, not measurements. Printing
    /// "0 Cores" states a fact we do not have, so those read as an em dash.
    /// </remarks>
    private static UIElement Facts(Server server, MetricSnapshot? snapshot)
    {
        var chinese = Strings.IsChinese;
        var row = Ui.Columns(16,
            Ui.Fact("", server.Cores > 0
                ? $"{server.Cores} {Strings.Get("metric.cores")}"
                : "—"),
            Ui.Fact("", server.MemoryTotal > 0 ? Format.Bytes(server.MemoryTotal) : "—"),
            Ui.Fact("", server.DiskTotal > 0 ? Format.Bytes(server.DiskTotal) : "—"));

        if (snapshot is { UptimeSeconds: > 0 })
        {
            row.Children.Add(Ui.Fact("", Format.UptimeDays(snapshot.UptimeSeconds, chinese)));
        }
        return row;
    }

    private static UIElement MetricsFor(ServerStatus status, MetricSnapshot? snapshot)
    {
        // Offline first: `Latest` keeps the last good snapshot, and gauges
        // drawn from it look like a live reading of a host that is not
        // answering.
        if (status.Kind == StatusKind.Offline)
        {
            var reason = Ui.Wrapped(status.Reason, "Text.Caption");
            reason.Foreground = Ink.Brush(Palette.Offline);
            reason.MaxHeight = 34;
            var stack = Ui.Rows(4, reason);
            if (snapshot is { UptimeSeconds: > 0 })
            {
                stack.Children.Add(Ui.Tertiary(Strings.Get(
                    "dashboard.lastSeen",
                    Format.Uptime(snapshot.UptimeSeconds, Strings.IsChinese))));
            }
            return stack;
        }

        if (snapshot is null)
        {
            var waiting = Ui.Spinner();
            waiting.VerticalAlignment = VerticalAlignment.Center;
            return waiting;
        }

        var row = Ui.Grid("88,88,*,*",
            Gauge("metric.cpu", snapshot.CpuPercent),
            Gauge("metric.memory", snapshot.MemoryPercent),
            RatePair("metric.network", "", snapshot.NetTxRate, "", snapshot.NetRxRate),
            RatePair("metric.disk", "", snapshot.DiskReadRate, "", snapshot.DiskWriteRate));
        return row;
    }

    private static UIElement Gauge(string key, double value)
    {
        var label = Ui.Caption(Strings.Get(key));
        label.HorizontalAlignment = HorizontalAlignment.Center;
        var gauge = new RingGauge { Value = value, HorizontalAlignment = HorizontalAlignment.Center };
        return Ui.Rows(6, label, gauge);
    }

    /// <summary>An up/down or read/write pair, as shown beside the gauges.</summary>
    private static UIElement RatePair(
        string key, string firstGlyph, double first, string secondGlyph, double second)
    {
        UIElement Row(string glyph, double value)
        {
            var icon = Ui.Tertiary(glyph);
            icon.FontFamily = new FontFamily("Segoe Fluent Icons, Segoe MDL2 Assets");
            icon.FontSize = 11;
            icon.Width = 14;
            icon.VerticalAlignment = VerticalAlignment.Center;

            var (amount, unit) = Format.RateParts(value);
            var number = Ui.Number(amount, 13);
            number.VerticalAlignment = VerticalAlignment.Center;
            var suffix = Ui.Tertiary(unit);
            suffix.VerticalAlignment = VerticalAlignment.Bottom;
            suffix.Margin = new Thickness(0, 0, 0, 1);
            return Ui.Columns(4, icon, number, suffix);
        }

        var stack = Ui.Rows(6,
            Ui.Caption(Strings.Get(key)),
            Row(firstGlyph, first),
            Row(secondGlyph, second));
        stack.Margin = new Thickness(10, 0, 0, 0);
        return stack;
    }
}
