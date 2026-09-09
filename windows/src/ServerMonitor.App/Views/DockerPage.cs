using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// Containers, images, volumes, networks and compose projects for one host.
/// </summary>
/// <remarks>
/// The five tables the plan asks for (P4), each one call. The host picker is
/// limited to hosts that reported Docker, which the poll already knows — so
/// opening this page costs nothing until a host is chosen.
/// </remarks>
public sealed class DockerPage : UserControl
{
    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    private readonly Action<Guid> _openServer;
    private readonly StackPanel _root = Ui.Rows(0);
    private readonly StackPanel _body = Ui.Rows(0);

    private Server? _host;
    private Tab _tab = Tab.Containers;
    private bool _loading;

    private List<DockerContainer> _containers = [];
    private Dictionary<string, DockerContainerStats> _stats = [];
    private List<DockerImage> _images = [];
    private List<DockerVolume> _volumes = [];
    private List<DockerNetwork> _networks = [];
    private List<DockerComposeProject> _compose = [];
    private string? _error;

    private enum Tab { Containers, Images, Volumes, Networks, Compose }

    public DockerPage(Action<Guid> openServer)
    {
        _openServer = openServer;
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);

        _host = Monitor.Servers.FirstOrDefault(s => s.HasDocker);
        Rebuild();
        if (_host is not null) _ = LoadAsync();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        var hosts = Monitor.Servers.Where(s => s.HasDocker).ToList();
        if (hosts.Count == 0)
        {
            // Two different empty states, because they need different answers.
            // macOS shows "no host with Docker detected" in both cases, which
            // on a first launch sends the user looking for a Docker problem
            // they do not have: the reason is that there are no hosts at all.
            // The divergence is deliberate.
            _root.Children.Add(Monitor.Servers.Count == 0
                ? Ui.Empty(
                    "dashboard.emptyTitle",
                    "dashboard.empty",
                    FirstRun.Actions(Window.GetWindow(this), Rebuild))
                : Ui.Empty(null, "docker.noHosts"));
            return;
        }

        var picker = Ui.Picker(
            hosts,
            _host,
            server => server.Name,
            server =>
            {
                _host = server;
                _ = LoadAsync();
            });

        // The picker shows a host name and carries no label of its own.
        System.Windows.Automation.AutomationProperties.SetName(
            picker, Strings.Get("nav.machines"));

        var toolbar = Ui.Columns(8,
            picker,
            Ui.Quiet(Strings.Get("common.refresh"), () => _ = LoadAsync()));
        if (_host is { } current)
        {
            toolbar.Children.Add(Ui.Quiet(
                Strings.Get("nav.overview"), () => _openServer(current.Id)));
        }
        toolbar.HorizontalAlignment = HorizontalAlignment.Left;
        toolbar.Margin = new Thickness(0, 0, 0, 12);
        _root.Children.Add(toolbar);

        if (_host is { } host && Monitor.DockerSummaries.TryGetValue(host.Id, out var summary))
        {
            var tiles = Ui.Grid("*,*,*,*,*",
                Tile("docker.engine", summary.EngineVersion, Palette.Accent),
                Tile("docker.images", summary.Images.ToString(System.Globalization.CultureInfo.InvariantCulture), Palette.Accent),
                Tile("docker.running", summary.Running.ToString(System.Globalization.CultureInfo.InvariantCulture), Palette.Online),
                Tile("docker.stopped", summary.Stopped.ToString(System.Globalization.CultureInfo.InvariantCulture), Palette.Tertiary),
                Tile("docker.paused", summary.Paused.ToString(System.Globalization.CultureInfo.InvariantCulture), Palette.Warning));
            var card = Ui.Card(tiles);
            card.Margin = new Thickness(0, 0, 0, 12);
            _root.Children.Add(card);
        }

        _root.Children.Add(Tabs());
        _root.Children.Add(_body);
        RebuildBody();
    }

    private static UIElement Tile(string key, string value, System.Windows.Media.Color tint)
    {
        var number = Ui.Number(value.Length == 0 ? "—" : value, 18, tint);
        number.HorizontalAlignment = HorizontalAlignment.Center;
        var label = Ui.Tertiary(Strings.Get(key));
        label.HorizontalAlignment = HorizontalAlignment.Center;
        return Ui.Rows(2, number, label);
    }

    private UIElement Tabs()
    {
        var row = Ui.Columns(6);
        void Add(Tab tab, string key, int count)
        {
            var label = count > 0 ? $"{Strings.Get(key)} ({count})" : Strings.Get(key);
            row.Children.Add(Ui.Button(
                label,
                () =>
                {
                    _tab = tab;
                    Rebuild();
                },
                _tab == tab ? "Button.Accent" : "Button.Quiet"));
        }
        Add(Tab.Containers, "docker.containers", _containers.Count);
        Add(Tab.Images, "docker.images", _images.Count);
        Add(Tab.Volumes, "docker.volumes", _volumes.Count);
        Add(Tab.Networks, "docker.networks", _networks.Count);
        Add(Tab.Compose, "docker.compose", _compose.Count);
        row.HorizontalAlignment = HorizontalAlignment.Left;
        row.Margin = new Thickness(0, 0, 0, 12);
        return row;
    }

    private void RebuildBody()
    {
        _body.Children.Clear();

        if (_loading)
        {
            _body.Children.Add(Ui.Card(Ui.Spinner()));
            return;
        }
        if (_error is { } error)
        {
            var text = Ui.Wrapped(error, "Text.Caption");
            text.Foreground = Ink.Brush(Palette.Offline);
            _body.Children.Add(Ui.Card(text));
            return;
        }

        _body.Children.Add(_tab switch
        {
            Tab.Containers => Containers(),
            Tab.Images => Images(),
            Tab.Volumes => Volumes(),
            Tab.Networks => Networks(),
            _ => Compose(),
        });
    }

    /// <summary>A table built from rows of already-formatted strings.</summary>
    private static UIElement Table(
        string emptyKey,
        (string HeaderKey, double Width)[] columns,
        IReadOnlyList<string[]> rows,
        Func<int, ContextMenu>? menuFor = null)
    {
        if (rows.Count == 0) return Ui.Card(Ui.Tertiary(Strings.Get(emptyKey)));

        var grid = Ui.Rows(0);
        var header = new Grid();
        var widths = string.Join(",", columns.Select(c => c.Width.ToString(
            System.Globalization.CultureInfo.InvariantCulture)));

        UIElement Line(string[] cells, bool isHeader)
        {
            var elements = cells.Select((cell, index) =>
            {
                var text = isHeader ? Ui.Caption(cell) : Ui.Text(cell);
                if (!isHeader) text.FontSize = 12;
                text.TextTrimming = TextTrimming.CharacterEllipsis;
                ToolTipService.SetToolTip(text, cell);
                text.Margin = new Thickness(0, 0, 8, 0);
                return (UIElement)text;
            }).ToArray();
            var line = Ui.Grid(widths, elements);
            line.Margin = new Thickness(0, isHeader ? 0 : 3, 0, isHeader ? 6 : 3);
            return line;
        }

        grid.Children.Add(Line([.. columns.Select(c => Strings.Get(c.HeaderKey))], isHeader: true));
        grid.Children.Add(Ui.Separator());

        for (var index = 0; index < rows.Count; index++)
        {
            var line = Line(rows[index], isHeader: false);
            if (menuFor is not null && line is FrameworkElement element)
            {
                element.ContextMenu = menuFor(index);
            }
            grid.Children.Add(line);
        }
        _ = header;
        return Ui.Card(grid);
    }

    private UIElement Containers()
    {
        var rows = _containers.Select(container =>
        {
            _stats.TryGetValue(container.ShortId, out var stats);
            return new[]
            {
                container.Name,
                container.Image,
                container.State,
                container.Status,
                stats is null ? "—" : Core.Format.Percent(stats.CpuPercent),
                stats?.MemoryUsage ?? "—",
            };
        }).ToList();

        return Table(
            "docker.empty",
            [
                ("docker.name", 190),
                ("docker.image", 200),
                ("docker.state", 80),
                ("docker.status", 160),
                ("metric.cpu", 70),
                ("docker.memUsage", 140),
            ],
            rows,
            ContainerMenu);
    }

    private ContextMenu ContainerMenu(int index)
    {
        var menu = new ContextMenu();
        if (index >= _containers.Count) return menu;
        var container = _containers[index];

        void Item(string key, Action action)
        {
            var item = new MenuItem { Header = Strings.Get(key) };
            item.Click += (_, _) => action();
            menu.Items.Add(item);
        }

        // Only the transitions that make sense from the current state: a
        // "Start" on a running container does nothing and looks broken.
        if (container.IsRunning)
        {
            Item("docker.stop", () => _ = PerformAsync(ContainerAction.Stop, container));
            Item("docker.restart", () => _ = PerformAsync(ContainerAction.Restart, container));
        }
        else
        {
            Item("docker.start", () => _ = PerformAsync(ContainerAction.Start, container));
        }
        menu.Items.Add(new Separator());
        if (container.IsRunning)
        {
            // A shell in the container, through the same terminal window a
            // host gets: docker exec -it needs a pty, which is exactly what
            // the shell channel is.
            var shell = new MenuItem { Header = Strings.IsChinese ? "容器 Shell" : "Container shell" };
            shell.Click += (_, _) => OpenShell(container);
            menu.Items.Add(shell);
        }
        Item("docker.logs", () => _ = ShowLogsAsync(container));
        Item("common.copy", () => Ui.Copy(container.Id));
        return menu;
    }

    private void OpenShell(DockerContainer container)
    {
        if (_host is not { } host) return;
        Windows.Terminal(
            Window.GetWindow(this),
            host,
            DockerClient.ExecShellCommand(container.Id),
            $"{container.Name} — {host.Name}");
    }

    private async Task PerformAsync(ContainerAction action, DockerContainer container)
    {
        if (_host is not { } host) return;
        _loading = true;
        RebuildBody();
        try
        {
            await Monitor.Docker.PerformAsync(action, container.Id, Monitor.Target(host));
            await LoadAsync();
        }
        catch (Exception error)
        {
            _error = FailureText.For(error);
            _loading = false;
            RebuildBody();
        }
    }

    private async Task ShowLogsAsync(DockerContainer container)
    {
        if (_host is not { } host) return;
        try
        {
            var logs = await Monitor.Docker.LogsAsync(container.Id, Monitor.Target(host));
            var window = new LogWindow(container.Name, logs) { Owner = Window.GetWindow(this) };
            window.Show();
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), FailureText.For(error));
        }
    }

    private UIElement Images() => Table(
        "docker.noImages",
        [("docker.repository", 240), ("docker.tag", 140), ("docker.size", 100), ("docker.created", 140)],
        _images.Select(image => new[]
        {
            image.IsDangling ? image.DisplayName : image.Repository,
            image.IsDangling ? Strings.Get("docker.dangling") : image.Tag,
            image.Size,
            image.Created,
        }).ToList());

    private UIElement Volumes() => Table(
        "docker.noVolumes",
        [("docker.name", 240), ("docker.driver", 100), ("docker.mountpoint", 320)],
        _volumes.Select(volume => new[] { volume.Name, volume.Driver, volume.Mountpoint }).ToList());

    private UIElement Networks() => Table(
        "docker.noNetworks",
        [("docker.name", 200), ("docker.driver", 110), ("docker.scope", 100), ("docker.status", 120)],
        _networks.Select(network => new[]
        {
            network.Name,
            network.Driver,
            network.Scope,
            network.IsBuiltIn ? Strings.Get("docker.builtIn") : "",
        }).ToList());

    private UIElement Compose() => Table(
        "docker.noCompose",
        [("docker.name", 200), ("docker.status", 200), ("docker.configPath", 320)],
        _compose.Select(project => new[]
        {
            project.Name,
            project.Status,
            project.Directory,
        }).ToList());

    // MARK: - Loading

    private async Task LoadAsync()
    {
        if (_host is not { } host) return;
        _loading = true;
        _error = null;
        RebuildBody();

        try
        {
            var target = Monitor.Target(host);
            var docker = Monitor.Docker;
            // Sequentially, not in parallel: they share one connection, and
            // six concurrent channel opens against a host is a burst it has no
            // reason to absorb for a page the user is reading anyway.
            _containers = await docker.ListContainersAsync(target);
            _stats = await docker.StatsAsync(target);
            _images = await docker.ListImagesAsync(target);
            _volumes = await docker.ListVolumesAsync(target);
            _networks = await docker.ListNetworksAsync(target);
            _compose = await docker.ListComposeProjectsAsync(target);
            await Monitor.RefreshDockerSummaryAsync(host);
        }
        catch (Exception error)
        {
            _error = FailureText.For(error);
        }
        finally
        {
            _loading = false;
            Rebuild();
        }
    }
}

/// <summary>Container logs, in a scrollable monospaced window.</summary>
public sealed class LogWindow : Window
{
    public LogWindow(string title, string logs)
    {
        Title = title;
        Width = 900;
        Height = 600;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        var box = Ui.Input(logs.Length == 0 ? Strings.Get("snippet.noOutput") : logs);
        box.IsReadOnly = true;
        box.AcceptsReturn = true;
        box.TextWrapping = TextWrapping.NoWrap;
        box.VerticalContentAlignment = VerticalAlignment.Top;
        box.FontFamily = (System.Windows.Media.FontFamily)FindResource("Font.Mono");
        box.VerticalScrollBarVisibility = ScrollBarVisibility.Auto;
        box.HorizontalScrollBarVisibility = ScrollBarVisibility.Auto;
        box.Margin = new Thickness(16);
        // Scrolled to the end: the interesting part of a log is what happened
        // last.
        Loaded += (_, _) =>
        {
            box.CaretIndex = box.Text.Length;
            box.ScrollToEnd();
        };

        Content = box;
    }
}
