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
/// Everything one host is doing: the status cards, then the history charts.
/// </summary>
/// <remarks>
/// The card set matches the macOS machine screen so the two can be compared
/// side by side (plan §7 P3): CPU with its per-core rows and the user/system/
/// nice/iowait/steal split, load, memory, storage per mount, network per
/// interface, processes, host info, GPU, IP location, Docker and vnStat.
///
/// The cards are laid out by <see cref="StaticGrid"/> rather than a lazy grid,
/// and rebuilt from the snapshot on each publish. The grid's column assignment
/// is weighted so the two long cards (processes, per-core) do not both land in
/// the same column.
/// </remarks>
public sealed class ServerDetailPage : UserControl
{
    private static MonitorService Monitor => App.Current.Monitor;

    private readonly Guid _serverId;
    private readonly Action _goBack;
    private readonly StaticGrid _cards = new();
    private readonly StackPanel _root = Ui.Rows(0);
    private readonly StackPanel _charts = Ui.Rows(0);

    /// <summary>Which history window the charts show.</summary>
    private TimeSpan _range = TimeSpan.FromHours(1);
    private CancellationTokenSource? _chartLoad;

    /// <summary>Set once the host answers, so the cards stop rebuilding empty.</summary>
    private bool _showAllCores;
    private bool _showVirtualInterfaces;
    private string _processFilter = string.Empty;

    /// <summary>
    /// The vnStat report, fetched on demand rather than with the poll.
    /// </summary>
    /// <remarks>
    /// It changes on the hour, not every five seconds, and asking for it in
    /// the collection batch would cost every host a JSON parse for a card only
    /// this screen shows.
    /// </remarks>
    private VnstatOutcome? _traffic;
    private bool _trafficLoading;
    private TrafficGranularity _granularity = TrafficGranularity.Hour;

    private GeoInfo? _geo;
    private string? _geoError;
    private bool _geoLoading;

    public ServerDetailPage(Guid serverId, Action goBack)
    {
        _serverId = serverId;
        _goBack = goBack;

        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);

        Monitor.Published += Rebuild;
        Unloaded += (_, _) => Closing();

        // Opening the screen polls the host at once, so the process card fills
        // in about a second rather than waiting out the rest of the interval.
        Monitor.SetDetailVisible(_serverId, true);
        _ = LoadTrafficAsync();
        _ = LoadChartsAsync();
        Rebuild();
    }

    /// <summary>
    /// Tells the service this screen is gone, so its host stops paying for the
    /// process list.
    /// </summary>
    public void Closing()
    {
        Monitor.Published -= Rebuild;
        Monitor.SetDetailVisible(_serverId, false);
        _chartLoad?.Cancel();
    }

    // MARK: - Layout

    private void Rebuild()
    {
        var server = Monitor.Server(_serverId);
        if (server is null)
        {
            // Deleted while open. Going back is better than showing a screen
            // for a host that no longer exists.
            _goBack();
            return;
        }

        var status = Monitor.Status.TryGetValue(_serverId, out var value)
            ? value
            : ServerStatus.Unknown;
        var snapshot = Monitor.Latest.TryGetValue(_serverId, out var latest) ? latest : null;

        _root.Children.Clear();
        _root.Children.Add(Header(server, status, snapshot));

        _cards.Children.Clear();
        _cards.Weights.Clear();
        _cards.Margin = new Thickness(0, 14, 0, 0);

        void Add(UIElement? card, double weight)
        {
            if (card is null) return;
            _cards.Children.Add(card);
            _cards.Weights.Add(weight);
        }

        if (snapshot is not null)
        {
            Add(CpuCard(snapshot), 2 + (_showAllCores ? snapshot.CoreLoads.Count * 0.12 : 0.6));
            Add(MemoryCard(snapshot), 1.6);
            Add(StorageCard(snapshot), 1 + snapshot.Filesystems.Count * 0.25);
            Add(NetworkCard(snapshot), 1 + snapshot.Interfaces.Count * 0.2);
            Add(HostInfoCard(server, snapshot), 1.6);
            // The whole GPU card is omitted on a host with no card, rather
            // than shown with zeroes — the reason the parser reports an empty
            // status rather than a default one.
            if (snapshot.Gpu.IsPresent) Add(GpuCard(snapshot), 1.4 + snapshot.Gpu.Gpus.Count * 0.6);
            Add(DockerCard(server), 1.2);
            Add(IpLocationCard(server, snapshot), 1.4);
            // Processes last and heaviest: it is the tallest card, so the
            // weighting sends it to whichever column has room.
            Add(ProcessCard(snapshot), 4);
            Add(TrafficCard(server), 3);
        }
        else
        {
            var waiting = Ui.Card(Ui.Rows(10,
                Ui.Title(Strings.Get("card.awaitingData")),
                Ui.Spinner()));
            Add(waiting, 1);
        }

        _root.Children.Add(_cards);
        _root.Children.Add(ChartsSection());
    }

    private UIElement Header(Server server, ServerStatus status, MetricSnapshot? snapshot)
    {
        var identity = Ui.Columns(10);
        if (server.Flag.Length > 0)
        {
            var flag = Ui.Text(server.Flag);
            flag.FontSize = 18;
            flag.FontFamily = new FontFamily("Segoe UI Emoji");
            identity.Children.Add(flag);
        }
        identity.Children.Add(Ui.Headline(server.Name));
        if (server.Tags.Count > 0) identity.Children.Add(Ui.TagChips(server.Tags));
        foreach (var child in identity.Children.OfType<FrameworkElement>())
        {
            child.VerticalAlignment = VerticalAlignment.Center;
        }

        var badge = status.Kind switch
        {
            StatusKind.Online => Ui.Columns(6,
                Ui.Dot(Palette.Online),
                Ui.Number(
                    Format.Latency(snapshot?.LatencyMs ?? 0),
                    13,
                    Palette.ForLatency(snapshot?.LatencyMs ?? 0))),
            StatusKind.Offline => Ui.Columns(6,
                Ui.Dot(Palette.Offline),
                Ui.Text(Strings.Get("common.offline"))),
            _ => (Panel)Ui.Columns(6, Ui.Spinner()),
        };
        foreach (var child in badge.Children.OfType<FrameworkElement>())
        {
            child.VerticalAlignment = VerticalAlignment.Center;
        }

        var actions = Ui.Columns(6,
            Ui.Quiet(Strings.Get("nav.terminal"), OpenTerminal),
            Ui.Quiet("SFTP", OpenFiles),
            Ui.Quiet(Strings.Get("common.refresh"), () => _ = Monitor.PollAllAsync()),
            Ui.Quiet(Strings.Get("common.edit"), Edit),
            Ui.Quiet(Strings.Get("nav.dashboard"), _goBack));
        actions.HorizontalAlignment = HorizontalAlignment.Right;

        var top = Ui.Grid("*,auto,auto", identity, badge, actions);
        badge.Margin = new Thickness(12, 0, 12, 0);

        var children = new List<UIElement> { top, Ui.Caption(server.DisplayTarget) };

        // Offline: the reason belongs at the top, spelled out in full. On a
        // card it only fits in a tooltip; here there is room to read it.
        if (status.Kind == StatusKind.Offline && status.Reason.Length > 0)
        {
            var reason = Ui.Wrapped(status.Reason, "Text.Caption");
            reason.Foreground = Ink.Brush(Palette.Offline);
            children.Add(reason);
        }
        if (server.Notes.Length > 0) children.Add(Ui.Wrapped(server.Notes, "Text.Tertiary"));

        return Ui.Rows(6, [.. children]);
    }

    private void OpenTerminal()
    {
        if (Monitor.Server(_serverId) is { } server) Windows.Terminal(Window.GetWindow(this), server);
    }

    private void OpenFiles()
    {
        if (Monitor.Server(_serverId) is { } server) Windows.Files(Window.GetWindow(this), server);
    }

    private void Edit()
    {
        var server = Monitor.Server(_serverId);
        if (server is null) return;
        var editor = new ServerEditorWindow(server) { Owner = Window.GetWindow(this) };
        if (editor.ShowDialog() == true) Rebuild();
    }

    // MARK: - Cards

    private static UIElement Card(string titleKey, params UIElement[] content)
    {
        var children = new List<UIElement> { Ui.Title(Strings.Get(titleKey)) };
        children.AddRange(content);
        return Ui.Card(Ui.Rows(8, [.. children]));
    }

    /// <summary>A label on the left, a value on the right.</summary>
    private static UIElement Row(string label, string value, Color? tint = null)
    {
        var left = Ui.Caption(label);
        left.VerticalAlignment = VerticalAlignment.Center;
        var right = Ui.Number(value, 13, tint);
        right.HorizontalAlignment = HorizontalAlignment.Right;
        right.VerticalAlignment = VerticalAlignment.Center;
        var grid = Ui.Grid("*,auto", left, right);
        grid.Margin = new Thickness(0, 1, 0, 1);
        return grid;
    }

    /// <summary>A label, a bar, and a value — the per-core and per-mount shape.</summary>
    private static UIElement MeterRow(string label, double percent, string value, Color? tint = null)
    {
        var name = Ui.Caption(label);
        name.VerticalAlignment = VerticalAlignment.Center;
        name.TextTrimming = TextTrimming.CharacterEllipsis;

        var bar = new MeterBar
        {
            Value = percent,
            Height = 6,
            Margin = new Thickness(8, 0, 8, 0),
            VerticalAlignment = VerticalAlignment.Center,
        };
        if (tint is { } colour) bar.Tint = colour;

        var text = Ui.Number(value, 12);
        text.HorizontalAlignment = HorizontalAlignment.Right;
        text.VerticalAlignment = VerticalAlignment.Center;

        var grid = Ui.Grid("110,*,auto", name, bar, text);
        grid.Margin = new Thickness(0, 2, 0, 2);
        return grid;
    }

    private UIElement CpuCard(MetricSnapshot snapshot)
    {
        var gauge = new RingGauge
        {
            Value = snapshot.CpuPercent,
            Diameter = 76,
            VerticalAlignment = VerticalAlignment.Center,
        };

        var facts = Ui.Rows(2,
            // An em dash rather than a zero: a host that has never answered,
            // or whose last poll failed, has no core count — and "0 cores"
            // states a fact we do not have.
            Row(Strings.Get("card.cores"),
                snapshot.Cores > 0
                    ? snapshot.Cores.ToString(System.Globalization.CultureInfo.InvariantCulture)
                    : "—"),
            Row(Strings.Get("metric.load"),
                $"{Format.Load(snapshot.Load1)}  {Format.Load(snapshot.Load5)}  {Format.Load(snapshot.Load15)}"));
        facts.VerticalAlignment = VerticalAlignment.Center;
        facts.Margin = new Thickness(14, 0, 0, 0);

        var children = new List<UIElement> { Ui.Grid("auto,*", gauge, facts) };

        // The split is Linux-only: Windows does not report it, and the card
        // leaves the row out rather than drawing five zeroes.
        if (snapshot.CpuBreakdown.IsReported)
        {
            var breakdown = snapshot.CpuBreakdown;
            children.Add(Ui.Separator());
            children.Add(Ui.Rows(1,
                Row("user", Format.Percent(breakdown.User)),
                Row("system", Format.Percent(breakdown.System)),
                Row("nice", Format.Percent(breakdown.Nice)),
                // iowait and steal are the two that change what you do about
                // a busy box, so they are tinted when they are non-trivial.
                Row("iowait", Format.Percent(breakdown.Iowait),
                    breakdown.Iowait > 10 ? Palette.Warning : null),
                Row("steal", Format.Percent(breakdown.Steal),
                    breakdown.Steal > 5 ? Palette.Warning : null)));
        }

        if (snapshot.CoreLoads.Count > 0)
        {
            children.Add(Ui.Separator());
            var cores = Ui.Rows(0);
            // Eight rows unless asked for more: a 96-core host would otherwise
            // make this card taller than the window.
            var shown = _showAllCores ? snapshot.CoreLoads : snapshot.CoreLoads.Take(8).ToList();
            foreach (var core in shown)
            {
                cores.Children.Add(MeterRow(
                    $"CPU {core.Index}", core.Percent, Format.Percent(core.Percent)));
            }
            children.Add(cores);
            if (snapshot.CoreLoads.Count > 8)
            {
                var toggle = Ui.Quiet(
                    _showAllCores ? Strings.Get("common.collapse") : Strings.Get("card.showAllCores"),
                    () =>
                    {
                        _showAllCores = !_showAllCores;
                        Rebuild();
                    });
                toggle.HorizontalAlignment = HorizontalAlignment.Left;
                children.Add(toggle);
            }
        }

        return Card("card.cpuUsage", [.. children]);
    }

    private static UIElement MemoryCard(MetricSnapshot snapshot)
    {
        var memory = snapshot.Memory;
        var children = new List<UIElement>
        {
            Ui.Grid("auto,*",
                new RingGauge
                {
                    Value = snapshot.MemoryPercent,
                    Diameter = 76,
                    VerticalAlignment = VerticalAlignment.Center,
                },
                new Border
                {
                    Margin = new Thickness(14, 0, 0, 0),
                    VerticalAlignment = VerticalAlignment.Center,
                    Child = Ui.Rows(2,
                        Row(Strings.Get("card.used"), Format.Bytes(snapshot.MemoryUsed)),
                        Row(Strings.Get("dashboard.total"), Format.Bytes(snapshot.MemoryTotal))),
                }),
        };

        // Windows exposes no buffers/cache split, so those rows appear only
        // when the host actually reported them.
        if (memory.Buffers > 0 || memory.Cached > 0)
        {
            children.Add(Ui.Separator());
            children.Add(Ui.Rows(1,
                Row(Strings.Get("card.buffers"), Format.Bytes(memory.Buffers)),
                Row(Strings.Get("card.cached"), Format.Bytes(memory.Cached)),
                Row(Strings.Get("card.free"), Format.Bytes(memory.Free))));
        }

        if (memory.HasSwap)
        {
            children.Add(Ui.Separator());
            children.Add(MeterRow(
                Strings.Get("card.swap"),
                memory.SwapPercent,
                Format.Usage(memory.SwapUsed, memory.SwapTotal)));
        }

        return Card("card.memoryUsage", [.. children]);
    }

    private static UIElement StorageCard(MetricSnapshot snapshot)
    {
        if (snapshot.Filesystems.Count == 0)
        {
            return Card("card.storage", Ui.Tertiary(Strings.Get("card.noFilesystems")));
        }
        var rows = Ui.Rows(0);
        foreach (var mount in snapshot.Filesystems)
        {
            rows.Children.Add(MeterRow(
                mount.Mount,
                mount.Percent,
                Format.Usage(mount.Used, mount.Total)));
        }
        return Card("card.storage", rows);
    }

    private UIElement NetworkCard(MetricSnapshot snapshot)
    {
        var children = new List<UIElement>
        {
            Ui.Grid("*,*",
                Ui.Rows(2,
                    Ui.Caption(Strings.Get("metric.download")),
                    Ui.Number(Format.Rate(snapshot.NetRxRate), 15)),
                Ui.Rows(2,
                    Ui.Caption(Strings.Get("metric.upload")),
                    Ui.Number(Format.Rate(snapshot.NetTxRate), 15))),
            Ui.Separator(),
            Ui.Grid("*,*",
                Ui.Rows(2,
                    Ui.Caption(Strings.Get("metric.trafficTotal")),
                    Ui.Caption($"↓ {Format.Bytes(snapshot.NetRxTotal)}  ↑ {Format.Bytes(snapshot.NetTxTotal)}")),
                Ui.Rows(2,
                    Ui.Caption(Strings.Get("metric.diskIO")),
                    Ui.Caption($"R {Format.Rate(snapshot.DiskReadRate)}  W {Format.Rate(snapshot.DiskWriteRate)}"))),
        };

        // Container bridges and veth pairs are hidden by default: on a Docker
        // host they outnumber the real links several to one.
        var interfaces = _showVirtualInterfaces
            ? snapshot.Interfaces
            : snapshot.Interfaces.Where(i => !i.IsVirtual).ToList();

        if (interfaces.Count > 0)
        {
            children.Add(Ui.Separator());
            var rows = Ui.Rows(1);
            var busiest = interfaces.Max(i => Math.Max(i.RxRate, i.TxRate));
            foreach (var nic in interfaces)
            {
                var share = busiest > 0 ? (nic.RxRate + nic.TxRate) / (busiest * 2) * 100 : 0;
                rows.Children.Add(MeterRow(
                    nic.Name,
                    share,
                    $"↓ {Format.Rate(nic.RxRate)}  ↑ {Format.Rate(nic.TxRate)}",
                    // A busy interface is not a problem, so this bar is not
                    // coloured by threshold the way a utilisation bar is.
                    Palette.Accent));
            }
            children.Add(rows);
        }

        if (snapshot.Interfaces.Any(i => i.IsVirtual))
        {
            var toggle = Ui.Quiet(
                _showVirtualInterfaces
                    ? Strings.Get("common.collapse")
                    : Strings.Get("card.showVirtual"),
                () =>
                {
                    _showVirtualInterfaces = !_showVirtualInterfaces;
                    Rebuild();
                });
            toggle.HorizontalAlignment = HorizontalAlignment.Left;
            children.Add(toggle);
        }

        return Card("card.network", [.. children]);
    }

    private UIElement ProcessCard(MetricSnapshot snapshot)
    {
        if (snapshot.Processes.Count == 0)
        {
            return Card("card.processes", Ui.Tertiary(Strings.Get("card.noProcesses")));
        }

        var filter = Ui.Input(_processFilter);
        ToolTipService.SetToolTip(filter, Strings.Get("card.processFilter"));
        filter.TextChanged += (_, _) =>
        {
            _processFilter = filter.Text;
            RefreshProcessRows();
        };

        _processRows = Ui.Rows(0);
        RefreshProcessRows(snapshot);

        var children = new List<UIElement> { filter, _processRows };

        // Windows reports total CPU seconds per process rather than a
        // percentage, and saying so is better than a column that silently
        // means something different per platform.
        if (snapshot.Identity.OsName.Contains("Windows", StringComparison.OrdinalIgnoreCase))
        {
            children.Add(Ui.Wrapped(Strings.Get("card.processCPUSeconds"), "Text.Tertiary"));
        }

        return Card("card.processes", [.. children]);
    }

    private StackPanel _processRows = Ui.Rows(0);

    private void RefreshProcessRows(MetricSnapshot? snapshot = null)
    {
        snapshot ??= Monitor.Latest.TryGetValue(_serverId, out var latest) ? latest : null;
        if (snapshot is null) return;

        _processRows.Children.Clear();
        var query = _processFilter.Trim();
        var processes = snapshot.Processes.AsEnumerable();
        if (query.Length > 0)
        {
            processes = processes.Where(p =>
                p.Command.Contains(query, StringComparison.OrdinalIgnoreCase)
                || p.User.Contains(query, StringComparison.OrdinalIgnoreCase)
                || p.Pid.ToString(System.Globalization.CultureInfo.InvariantCulture).Contains(query, StringComparison.Ordinal));
        }

        foreach (var process in processes.Take(25))
        {
            var command = Ui.Caption(process.Command);
            command.TextTrimming = TextTrimming.CharacterEllipsis;
            ToolTipService.SetToolTip(command, $"{process.Pid}  {process.User}\n{process.Command}");
            command.VerticalAlignment = VerticalAlignment.Center;

            var cpu = Ui.Number(Format.Percent(process.CpuPercent), 12);
            cpu.HorizontalAlignment = HorizontalAlignment.Right;
            var memory = Ui.Number(Format.Bytes(process.ResidentBytes), 12);
            memory.HorizontalAlignment = HorizontalAlignment.Right;

            var row = Ui.Grid("*,58,74", command, cpu, memory);
            row.Margin = new Thickness(0, 2, 0, 2);
            _processRows.Children.Add(row);
        }

        if (_processRows.Children.Count == 0)
        {
            _processRows.Children.Add(Ui.Tertiary(Strings.Get("card.noProcesses")));
        }
    }

    private static UIElement HostInfoCard(Server server, MetricSnapshot snapshot)
    {
        var identity = snapshot.Identity;
        var rows = Ui.Rows(1);

        void Maybe(string labelKey, string value)
        {
            if (value.Length == 0) return;
            var label = Ui.Caption(Strings.Get(labelKey));
            var text = Ui.Caption(value);
            text.HorizontalAlignment = HorizontalAlignment.Right;
            text.TextTrimming = TextTrimming.CharacterEllipsis;
            ToolTipService.SetToolTip(text, value);
            rows.Children.Add(Ui.Grid("110,*", label, text));
        }

        Maybe("card.hostname", identity.Hostname);
        Maybe("card.os", identity.OsName);
        Maybe("card.kernel", identity.Kernel);
        Maybe("card.arch", identity.Architecture);
        Maybe("metric.cpu", identity.CpuModel);
        if (snapshot.UptimeSeconds > 0)
        {
            Maybe("metric.uptime", Format.Uptime(snapshot.UptimeSeconds, Strings.IsChinese));
        }
        if (identity.Addresses.Count > 0)
        {
            Maybe("card.addresses", string.Join(", ", identity.Addresses));
        }
        if (server.DockerVersion.Length > 0) Maybe("docker.engine", server.DockerVersion);

        return Card("card.machineInfo", rows);
    }

    private static UIElement GpuCard(MetricSnapshot snapshot)
    {
        var gpu = snapshot.Gpu;
        var children = new List<UIElement>();

        if (gpu.DriverVersion.Length > 0)
        {
            children.Add(Row("Driver", gpu.DriverVersion));
        }
        if (gpu.CudaVersion.Length > 0) children.Add(Row("CUDA", gpu.CudaVersion));

        foreach (var card in gpu.Gpus)
        {
            children.Add(Ui.Separator());
            children.Add(Ui.Text(card.Name));
            children.Add(MeterRow(
                Strings.Get("gpu.utilization"),
                card.UtilizationPercent,
                Format.Percent(card.UtilizationPercent)));
            children.Add(MeterRow(
                Strings.Get("metric.memory"),
                card.MemoryPercent,
                Format.Usage(card.MemoryUsed, card.MemoryTotal)));

            // Only what the card actually reported: nvidia-smi prints [N/A]
            // for a fan a datacentre part does not have, and 0% there would
            // read as "fan stopped".
            var extras = new List<string>();
            if (card.TemperatureC is { } temperature)
            {
                extras.Add($"{temperature:0} °C");
            }
            if (card.FanPercent is { } fan) extras.Add($"fan {fan:0}%");
            if (card.PowerDrawW is { } draw)
            {
                extras.Add(card.PowerLimitW is { } limit
                    ? $"{draw:0} / {limit:0} W"
                    : $"{draw:0} W");
            }
            if (extras.Count > 0) children.Add(Ui.Tertiary(string.Join("   ", extras)));
        }

        foreach (var process in gpu.Processes.Take(6))
        {
            children.Add(Row(process.Name, Format.Bytes(process.MemoryUsed)));
        }

        return Card("metric.cpu", [.. children]);
    }

    private UIElement DockerCard(Server server)
    {
        if (!server.HasDocker)
        {
            return Card("nav.docker", Ui.Tertiary(Strings.Get("docker.unavailable")));
        }

        var children = new List<UIElement>();
        if (Monitor.DockerSummaries.TryGetValue(_serverId, out var summary))
        {
            var tiles = Ui.Grid("*,*,*,*",
                Tile("docker.images", summary.Images, Palette.Accent),
                Tile("docker.running", summary.Running, Palette.Online),
                Tile("docker.stopped", summary.Stopped, Palette.Tertiary),
                Tile("docker.paused", summary.Paused, Palette.Warning));
            children.Add(tiles);
            children.Add(Row(Strings.Get("docker.engine"), summary.EngineVersion));
        }
        else
        {
            children.Add(Row(Strings.Get("docker.engine"), server.DockerVersion));
            children.Add(Ui.Spinner());
        }
        return Card("nav.docker", [.. children]);
    }

    private static UIElement Tile(string key, int value, Color tint)
    {
        var number = Ui.Number(
            value.ToString(System.Globalization.CultureInfo.InvariantCulture), 20, tint);
        number.HorizontalAlignment = HorizontalAlignment.Center;
        var label = Ui.Tertiary(Strings.Get(key));
        label.HorizontalAlignment = HorizontalAlignment.Center;
        return Ui.Rows(2, number, label);
    }

    /// <summary>
    /// Where the host is, looked up only when asked.
    /// </summary>
    /// <remarks>
    /// Deliberately not part of the poll. Resolving a location tells a third
    /// party which servers this user runs, which is a disclosure they should
    /// make on purpose — so the card says what pressing the button will do.
    /// </remarks>
    private UIElement IpLocationCard(Server server, MetricSnapshot snapshot)
    {
        var address = snapshot.Identity.Addresses.FirstOrDefault()
            ?? (server.Host.Length > 0 ? server.Host : string.Empty);

        var children = new List<UIElement> { Row(Strings.Get("card.endpoint"), server.DisplayTarget) };

        if (_geo is { } geo && !geo.IsEmpty)
        {
            if (geo.Place.Length > 0) children.Add(Row(Strings.Get("card.city"), geo.Place));
            if (geo.Country.Length > 0) children.Add(Row(Strings.Get("card.os"), geo.Country));
            if (geo.Organisation.Length > 0)
            {
                children.Add(Row(Strings.Get("card.org"), geo.Organisation));
            }
        }
        else if (_geoLoading)
        {
            children.Add(Ui.Tertiary(Strings.Get("card.lookingUp")));
            children.Add(Ui.Spinner());
        }
        else if (_geoError is { } error)
        {
            var text = Ui.Wrapped(error, "Text.Caption");
            text.Foreground = Ink.Brush(Palette.Offline);
            children.Add(text);
        }
        else if (address.Length == 0)
        {
            children.Add(Ui.Tertiary(Strings.Get("card.locationUnknown")));
        }
        else if (GeoLookup.IsPrivate(address))
        {
            children.Add(Ui.Tertiary(Strings.Get("card.privateAddress")));
        }
        else
        {
            var button = Ui.Button(Strings.Get("card.lookUp"), () => _ = LookUpAsync(address));
            button.HorizontalAlignment = HorizontalAlignment.Left;
            children.Add(button);
            children.Add(Ui.Tertiary(Strings.Get("card.lookupNotice")));
        }

        return Card("card.ipLocation", [.. children]);
    }

    private async Task LookUpAsync(string address)
    {
        _geoLoading = true;
        _geoError = null;
        Rebuild();
        try
        {
            _geo = await Monitor.Geo.LookupAsync(address);
            // The country is written back so the flag survives without asking
            // again — one column, so a lookup finishing a poll behind cannot
            // undo the poll.
            if (_geo.CountryCode.Length == 2)
            {
                Monitor.UpdateCountryCode(_serverId, _geo.CountryCode);
            }
        }
        catch (Exception error)
        {
            _geoError = error.Message;
        }
        finally
        {
            _geoLoading = false;
            Rebuild();
        }
    }

    // MARK: - Traffic (vnStat)

    private UIElement TrafficCard(Server server)
    {
        var children = new List<UIElement>();

        if (_trafficLoading)
        {
            children.Add(Ui.Spinner());
        }
        else if (_traffic is null)
        {
            children.Add(Ui.Tertiary(Strings.Get("traffic.unavailable")));
        }
        else if (_traffic.Kind == VnstatOutcomeKind.NotInstalled)
        {
            children.Add(Ui.Wrapped(Strings.Get("traffic.notInstalled")));
            children.Add(Ui.Wrapped(Strings.Get("traffic.installHint"), "Text.Tertiary"));
            children.Add(Ui.Link(Strings.Get("traffic.installLink"), "https://humdi.net/vnstat/"));
            var install = Ui.Button(Strings.Get("traffic.install"), () => _ = InstallVnstatAsync(server));
            install.HorizontalAlignment = HorizontalAlignment.Left;
            children.Add(install);
        }
        else if (_traffic.Report is { } report)
        {
            if (report.IsCollecting)
            {
                children.Add(Ui.Wrapped(Strings.Get("traffic.collecting")));
                children.Add(Ui.Wrapped(Strings.Get("traffic.collectingHint"), "Text.Tertiary"));
            }
            else if (report.PrimaryInterface is { } nic)
            {
                var picker = Ui.Picker(
                    Enum.GetValues<TrafficGranularity>(),
                    _granularity,
                    granularity => Strings.Get(granularity switch
                    {
                        TrafficGranularity.FiveMinute => "traffic.fiveMinute",
                        TrafficGranularity.Hour => "traffic.hour",
                        TrafficGranularity.Day => "traffic.day",
                        TrafficGranularity.Month => "traffic.month",
                        _ => "traffic.year",
                    }),
                    granularity =>
                    {
                        _granularity = granularity;
                        Rebuild();
                    });
                picker.HorizontalAlignment = HorizontalAlignment.Left;
                children.Add(Ui.Grid("auto,*", picker, Ui.Caption(nic.DisplayName)));

                var buckets = nic.Buckets(_granularity)
                    .TakeLast(_granularity.ShownCount())
                    .Select(b => (Label: LabelFor(b.Date, _granularity), Down: (double)b.Rx, Up: (double)b.Tx))
                    .ToList();

                if (buckets.Count == 0)
                {
                    children.Add(Ui.Tertiary(Strings.Get("traffic.collecting")));
                }
                else
                {
                    var chart = new BarChart();
                    chart.Show(buckets);
                    children.Add(chart);
                    children.Add(Row(
                        Strings.Get("traffic.shown"),
                        $"↓ {Format.Bytes(buckets.Sum(b => b.Down))}  ↑ {Format.Bytes(buckets.Sum(b => b.Up))}"));
                }
                children.Add(Row(
                    Strings.Get("traffic.allTime"),
                    $"↓ {Format.Bytes(nic.TotalRx)}  ↑ {Format.Bytes(nic.TotalTx)}"));
            }
        }

        return Card("traffic.title", [.. children]);
    }

    /// <summary>
    /// The bucket's own label, in the host's clock.
    /// </summary>
    /// <remarks>
    /// Formatted from the UTC components the parser built, deliberately not
    /// converted to local time: the axis should show the hour vnStat recorded,
    /// so "14:00" on the server reads as "14:00" here whatever this machine's
    /// zone is.
    /// </remarks>
    private static string LabelFor(DateTime date, TrafficGranularity granularity) => granularity switch
    {
        TrafficGranularity.FiveMinute or TrafficGranularity.Hour =>
            date.ToString("HH:mm", System.Globalization.CultureInfo.InvariantCulture),
        TrafficGranularity.Day =>
            date.ToString("MM-dd", System.Globalization.CultureInfo.InvariantCulture),
        TrafficGranularity.Month =>
            date.ToString("yyyy-MM", System.Globalization.CultureInfo.InvariantCulture),
        _ => date.ToString("yyyy", System.Globalization.CultureInfo.InvariantCulture),
    };

    private async Task LoadTrafficAsync()
    {
        var server = Monitor.Server(_serverId);
        if (server is null) return;
        _trafficLoading = true;
        try
        {
            var output = await Monitor.RunAsync(VnstatParser.Command, server, 30);
            _traffic = VnstatParser.Parse(output);
        }
        catch (Exception)
        {
            // An unreachable host is already reported by its status; this card
            // says nothing rather than repeating it.
            _traffic = null;
        }
        finally
        {
            _trafficLoading = false;
            Rebuild();
        }
    }

    /// <summary>
    /// Installs vnStat, in two steps.
    /// </summary>
    /// <remarks>
    /// The probe is read-only and only asks which package manager the host has
    /// and whether we are root. The exact command that would run is then shown
    /// and nothing is installed until the user confirms it — changing software
    /// on somebody's server is a thing they should see happen.
    /// </remarks>
    private async Task InstallVnstatAsync(Server server)
    {
        var owner = Window.GetWindow(this);
        VnstatInstaller.Plan? plan;
        try
        {
            var probe = await Monitor.RunAsync(VnstatInstaller.ProbeCommand, server, 30);
            plan = VnstatInstaller.PlanFromProbe(probe);
        }
        catch (Exception error)
        {
            Ui.Complain(owner, error.Message);
            return;
        }

        if (plan is null)
        {
            Ui.Complain(owner, Strings.Get("traffic.installNoPackageManager"));
            return;
        }

        var confirmed = Ui.Confirm(
            owner,
            $"{Strings.Get("traffic.installConfirmBody")}\n\n{plan.DisplayCommand}",
            Strings.Get("traffic.installConfirmTitle", server.Name));
        if (!confirmed) return;

        _trafficLoading = true;
        Rebuild();
        try
        {
            var output = await Monitor.RunAsync(VnstatInstaller.RemoteScript(plan), server, 300);
            var outcome = VnstatInstaller.OutcomeFrom(output);
            switch (outcome.Kind)
            {
                case VnstatInstaller.OutcomeKind.Installed:
                    Ui.Inform(owner, Strings.Get("traffic.installed"));
                    await LoadTrafficAsync();
                    return;
                case VnstatInstaller.OutcomeKind.NeedsSudoPassword:
                    Ui.Complain(
                        owner,
                        $"{Strings.Get("traffic.installNeedsSudo")}\n\n{plan.DisplayCommand}");
                    break;
                default:
                    Ui.Complain(owner, $"{Strings.Get("traffic.installFailed")}\n\n{outcome.Detail}");
                    break;
            }
        }
        catch (Exception error)
        {
            Ui.Complain(owner, error.Message);
        }
        finally
        {
            _trafficLoading = false;
            Rebuild();
        }
    }

    // MARK: - History charts

    private UIElement ChartsSection()
    {
        _charts.Margin = new Thickness(0, 14, 0, 0);
        return _charts;
    }

    private async Task LoadChartsAsync()
    {
        _chartLoad?.Cancel();
        _chartLoad = new CancellationTokenSource();
        var token = _chartLoad.Token;

        // Bucketed by SQLite to 240 points and read off the UI thread: a day
        // of five-second polls is ~17,000 rows, and decoding those on the
        // thread that draws is a visible hitch every refresh.
        var samples = await Monitor.ChartHistoryAsync(_serverId, DateTime.UtcNow - _range);
        if (token.IsCancellationRequested) return;

        _charts.Children.Clear();
        _charts.Children.Add(RangePicker());

        if (samples.Count < 2)
        {
            _charts.Children.Add(Ui.Card(Ui.Tertiary(Strings.Get("card.awaitingData"))));
            return;
        }

        var grid = new StaticGrid { MinimumColumnWidth = 420, Margin = new Thickness(0, 10, 0, 0) };

        void Add(string titleKey, IReadOnlyList<ChartSeries> series, double? maximum, Func<double, string> format)
        {
            var chart = new HistoryChart();
            chart.Show(series, maximum, format);
            grid.Children.Add(Ui.Card(Ui.Rows(6, Ui.Title(Strings.Get(titleKey)), chart)));
            grid.Weights.Add(1);
        }

        Add("metric.cpu",
            [new ChartSeries("CPU", [.. samples.Select(s => s.CpuPercent)], Palette.Accent)],
            // Fixed 0–100: for a percentage that is the meaningful range, and
            // auto-scaling makes a quiet host's noise look like load.
            100,
            value => $"{value:0}%");

        Add("metric.memory",
            [new ChartSeries("Memory", [.. samples.Select(s => s.MemoryPercent)], Palette.Online)],
            100,
            value => $"{value:0}%");

        Add("metric.network",
            [
                new ChartSeries(Strings.Get("metric.download"), [.. samples.Select(s => s.NetRxRate)], Palette.Accent),
                new ChartSeries(Strings.Get("metric.upload"), [.. samples.Select(s => s.NetTxRate)], Palette.Online, Filled: false),
            ],
            null,
            Format.Bytes);

        Add("metric.diskIO",
            [
                new ChartSeries(Strings.Get("metric.read"), [.. samples.Select(s => s.DiskReadRate)], Palette.Warning),
                new ChartSeries(Strings.Get("metric.write"), [.. samples.Select(s => s.DiskWriteRate)], Palette.Offline, Filled: false),
            ],
            null,
            Format.Bytes);

        _charts.Children.Add(grid);
    }

    private UIElement RangePicker()
    {
        var ranges = new (string Key, TimeSpan Span)[]
        {
            ("range.15m", TimeSpan.FromMinutes(15)),
            ("range.1h", TimeSpan.FromHours(1)),
            ("range.6h", TimeSpan.FromHours(6)),
            ("range.24h", TimeSpan.FromHours(24)),
        };

        var row = Ui.Columns(6);
        foreach (var (key, span) in ranges)
        {
            var isCurrent = span == _range;
            var button = Ui.Button(
                Strings.Get(key),
                () =>
                {
                    _range = span;
                    _ = LoadChartsAsync();
                },
                isCurrent ? "Button.Accent" : "Button.Quiet");
            row.Children.Add(button);
        }
        row.HorizontalAlignment = HorizontalAlignment.Left;
        return row;
    }
}
