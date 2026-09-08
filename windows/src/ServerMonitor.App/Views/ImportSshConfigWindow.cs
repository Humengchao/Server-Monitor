using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;

namespace ServerMonitor.App.Views;

/// <summary>
/// Adopts hosts from <c>%USERPROFILE%\.ssh\config</c> without retyping them.
/// </summary>
/// <remarks>
/// The imported rows use the alias as their credential, so OpenSSH's whole
/// Host block applies — HostName, User, Port, IdentityFile, ProxyJump and
/// anything else the user configured — and no key material is copied anywhere.
/// </remarks>
public sealed class ImportSshConfigWindow : Window
{
    private readonly List<(SshConfigHost Host, CheckBox Box, bool AlreadyAdded)> _rows = [];
    private readonly TextBlock _summary = Ui.Wrapped("", "Text.Caption");

    /// <summary>The primary button, disabled until something is chosen.</summary>
    private Button? _import;

    public ImportSshConfigWindow()
    {
        Title = Strings.Get("import.title");
        Width = 640;
        Height = 560;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        var discovered = SshConfig.FromDefaultLocation().Discover();
        var existingAliases = App.Current.Monitor.Servers
            .Where(s => s.AuthKind == AuthKind.SshConfigAlias)
            .Select(s => s.SshAlias)
            .ToHashSet(StringComparer.OrdinalIgnoreCase);

        Content = discovered.Count == 0 ? BuildEmpty() : Build(discovered, existingAliases);
    }

    private UIElement BuildEmpty()
    {
        var close = Ui.Button(Strings.Get("common.close"), Close);
        var panel = Ui.Rows(12,
            Ui.Empty("import.none", "import.noneHelp"),
            Ui.Caption(SshConfig.DefaultConfigPath),
            close);
        foreach (var child in panel.Children.OfType<FrameworkElement>())
        {
            child.HorizontalAlignment = HorizontalAlignment.Center;
        }
        panel.Margin = new Thickness(20);
        return panel;
    }

    private UIElement Build(List<SshConfigHost> hosts, HashSet<string> existingAliases)
    {
        var list = Ui.Rows(0);

        foreach (var host in hosts)
        {
            var already = existingAliases.Contains(host.Alias);
            var box = new CheckBox
            {
                // Nothing is pre-selected, which is what macOS does (its
                // `chosen` set starts empty) and what this got wrong: it
                // ticked every host not already added, so opening the dialog
                // and pressing the primary button adopted the entire ssh
                // config. On a config with forty hosts that is forty servers
                // and forty SSH connections nobody asked for. "Select all" is
                // one click away for people who do want that.
                //
                // A host already added is shown but disabled, so the list is
                // a complete picture of the config rather than a filtered one
                // that leaves the user wondering where an alias went.
                IsChecked = false,
                IsEnabled = !already,
                VerticalAlignment = VerticalAlignment.Center,
            };
            // Named, or a screen reader announces an unlabelled checkbox: the
            // alias it belongs to is a sibling TextBlock, which the tick has
            // no relationship to as far as automation is concerned.
            System.Windows.Automation.AutomationProperties.SetName(box, host.Alias);
            box.Checked += (_, _) => RefreshSummary();
            box.Unchecked += (_, _) => RefreshSummary();

            var notes = new List<string>();
            if (already) notes.Add(Strings.Get("import.alreadyAdded"));
            if (!host.HasReadableKey && host.IdentityFile is not null)
            {
                notes.Add(Strings.Get("import.noKey"));
            }
            if (host.ProxyJump.Length > 0) notes.Add($"ProxyJump {host.ProxyJump}");

            var detail = $"{host.User}@{host.HostName}"
                + (host.Port == 22 ? "" : $":{host.Port}");

            var text = Ui.Rows(2,
                Ui.Text(host.Alias),
                Ui.Caption(notes.Count > 0 ? $"{detail} · {string.Join(" · ", notes)}" : detail));

            var row = Ui.Grid("auto,*", box, text);
            row.Margin = new Thickness(0, 4, 0, 4);
            ((FrameworkElement)text).Margin = new Thickness(10, 0, 0, 0);
            list.Children.Add(row);

            _rows.Add((host, box, already));
        }

        var selectAll = Ui.Quiet(Strings.Get("import.selectAll"), () =>
        {
            // Only the ones that can be imported; toggling a disabled box
            // would do nothing and look broken.
            var target = _rows.Any(r => !r.AlreadyAdded && r.Box.IsChecked != true);
            foreach (var row in _rows.Where(r => !r.AlreadyAdded)) row.Box.IsChecked = target;
        });
        selectAll.HorizontalAlignment = HorizontalAlignment.Left;

        _import = Ui.Accent(Strings.Get("import.action"), Import);
        var buttons = Ui.Columns(8,
            Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); }),
            _import);
        buttons.HorizontalAlignment = HorizontalAlignment.Right;

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });

        var header = Ui.Rows(6,
            Ui.Wrapped(Strings.Get("import.subtitle"), "Text.Secondary"),
            selectAll);
        Grid.SetRow(header, 0);
        root.Children.Add(header);

        var scroll = Ui.Scroll(list);
        Grid.SetRow(scroll, 1);
        scroll.Margin = new Thickness(0, 10, 0, 10);
        root.Children.Add(scroll);

        var footer = Ui.Grid("*,auto", _summary, buttons);
        Grid.SetRow(footer, 2);
        root.Children.Add(footer);

        root.Margin = new Thickness(20);
        RefreshSummary();
        return root;
    }

    private void RefreshSummary()
    {
        var selected = _rows.Count(r => r.Box.IsChecked == true);
        _summary.Text = Strings.Get(
            "group.machines", selected.ToString(System.Globalization.CultureInfo.InvariantCulture));
        _summary.VerticalAlignment = VerticalAlignment.Center;
        // Disabled while nothing is chosen, as on macOS. An enabled primary
        // button over an empty selection is an invitation to press it and
        // wonder what happened.
        if (_import is not null) _import.IsEnabled = selected > 0;
    }

    private void Import()
    {
        var monitor = App.Current.Monitor;
        var sortIndex = monitor.Database.NextSortIndex();
        var imported = 0;
        var skipped = new List<string>();

        foreach (var (host, box, already) in _rows)
        {
            if (already || box.IsChecked != true) continue;
            try
            {
                monitor.AddServer(new Server
                {
                    Name = host.Alias,
                    // Both are kept: the alias is what gets dialled, and the
                    // resolved address is what the ping probe and the IP card
                    // need without re-reading the config.
                    SshAlias = host.Alias,
                    Host = host.HostName,
                    Port = host.Port,
                    Username = host.User,
                    AuthKind = AuthKind.SshConfigAlias,
                    SortIndex = sortIndex++,
                });
                imported++;
            }
            catch (Exception)
            {
                // One host that will not store must not take the rest of the
                // import with it. Without this the loop threw out of the
                // handler on the first failure: the hosts before it were
                // already saved, the ones after were not, the window never
                // closed, and the user got a stack trace instead of a count.
                // Reported below rather than swallowed, which is the whole
                // difference.
                skipped.Add(host.Alias);
            }
        }

        DialogResult = true;
        Close();

        if (ResultMessage(imported, skipped) is { } message) Ui.Inform(Owner, message);
    }

    /// <summary>
    /// What to tell the user afterwards, or null when there is nothing to say.
    /// </summary>
    /// <remarks>
    /// Both halves, because either can happen alone: a clean import of three
    /// hosts, three hosts that would not store, or some of each. Reporting
    /// only the count — which is what this did — left a user who asked for
    /// five and got three with no idea which two were missing or why the
    /// number moved.
    /// </remarks>
    internal static string? ResultMessage(int imported, IReadOnlyList<string> skipped)
    {
        var lines = new List<string>();
        if (imported > 0)
        {
            lines.Add(Strings.Get(
                "import.done",
                imported.ToString(System.Globalization.CultureInfo.InvariantCulture)));
        }
        if (skipped.Count > 0)
        {
            lines.Add(Strings.Get("import.skipped", string.Join(", ", skipped)));
        }
        return lines.Count == 0 ? null : string.Join("\n\n", lines);
    }
}
