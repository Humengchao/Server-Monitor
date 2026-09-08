using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// Saved commands, runnable against any host.
/// </summary>
public sealed class SnippetsPage : UserControl, ISearchable
{
    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    private readonly StackPanel _root = Ui.Rows(0);
    private string _query = string.Empty;

    public SnippetsPage()
    {
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);
        Rebuild();
    }

    public void Search(string query)
    {
        _query = query.Trim();
        Rebuild();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        var add = Ui.Accent(Strings.Get("snippet.new"), () => Edit(null));
        if (Monitor.Snippets.Count == 0)
        {
            _root.Children.Add(Ui.Empty("nav.snippets", "snippet.empty", add));
            return;
        }

        add.HorizontalAlignment = HorizontalAlignment.Left;
        add.Margin = new Thickness(0, 0, 0, 14);
        _root.Children.Add(add);

        var visible = Monitor.Snippets.Where(s =>
                _query.Length == 0
                || s.Name.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || s.Command.Contains(_query, StringComparison.OrdinalIgnoreCase)
                || s.Category.Contains(_query, StringComparison.OrdinalIgnoreCase))
            .ToList();

        // Grouped by the free-text category, which is what the picker in the
        // terminal groups by too.
        foreach (var group in visible.GroupBy(s => s.Category))
        {
            if (group.Key.Length > 0)
            {
                var heading = Ui.Caption(group.Key);
                heading.Margin = new Thickness(0, 8, 0, 4);
                _root.Children.Add(heading);
            }
            foreach (var snippet in group) _root.Children.Add(CardFor(snippet));
        }
    }

    private UIElement CardFor(Snippet snippet)
    {
        var heading = Ui.Columns(10, Ui.Title(snippet.Name));
        if (snippet.UseCount > 0)
        {
            heading.Children.Add(Ui.Tertiary(Strings.Get(
                "snippet.runCount",
                snippet.UseCount.ToString(System.Globalization.CultureInfo.InvariantCulture))));
        }
        foreach (var child in heading.Children.OfType<FrameworkElement>())
        {
            child.VerticalAlignment = VerticalAlignment.Center;
        }

        var actions = Ui.Columns(6,
            Ui.Quiet(Strings.Get("snippet.run"), () => Run(snippet)),
            Ui.Quiet(Strings.Get("snippet.copy"), () => Ui.Copy(snippet.Command)),
            Ui.Quiet(Strings.Get("common.edit"), () => Edit(snippet)),
            Ui.Danger(Strings.Get("common.delete"), () => Delete(snippet)));
        actions.HorizontalAlignment = HorizontalAlignment.Right;

        var command = Ui.Mono(snippet.Command);
        command.TextWrapping = TextWrapping.Wrap;
        command.TextTrimming = TextTrimming.None;

        var children = new List<UIElement>
        {
            Ui.Grid("*,auto", heading, actions),
            command,
        };
        if (snippet.Notes.Length > 0) children.Add(Ui.Wrapped(snippet.Notes, "Text.Tertiary"));

        var card = Ui.Card(Ui.Rows(6, [.. children]));
        card.Margin = new Thickness(0, 0, 0, 10);
        return card;
    }

    private void Edit(Snippet? existing)
    {
        var window = new SnippetEditorWindow(existing) { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() == true) Rebuild();
    }

    private void Delete(Snippet snippet)
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("keys.deleteConfirm", snippet.Name))) return;
        Monitor.DeleteSnippet(snippet.Id);
        Rebuild();
    }

    /// <summary>
    /// Runs a snippet on a host the user picks, and shows what it said.
    /// </summary>
    /// <remarks>
    /// The output window is the point: a snippet whose result vanishes is a
    /// snippet you cannot tell succeeded.
    /// </remarks>
    private void Run(Snippet snippet)
    {
        if (Monitor.Servers.Count == 0)
        {
            Ui.Inform(Window.GetWindow(this), Strings.Get("dashboard.empty"));
            return;
        }
        var window = new SnippetRunWindow(snippet) { Owner = Window.GetWindow(this) };
        window.ShowDialog();
        Rebuild();
    }
}

public sealed class SnippetEditorWindow : Window
{
    private readonly Snippet _snippet;
    private readonly TextBox _name = Ui.Input();
    private readonly TextBox _category = Ui.Input();
    private readonly TextBox _command = Ui.Input();
    private readonly TextBox _notes = Ui.Input();
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");

    public SnippetEditorWindow(Snippet? existing)
    {
        _snippet = existing?.Clone() ?? new Snippet();
        _name.Text = _snippet.Name;
        _category.Text = _snippet.Category;
        _command.Text = _snippet.Command;
        _notes.Text = _snippet.Notes;

        // A command is often several lines, so the box is a real editor rather
        // than a single-line field that hides everything past the first.
        _command.AcceptsReturn = true;
        _command.MinHeight = 110;
        _command.TextWrapping = TextWrapping.Wrap;
        _command.VerticalContentAlignment = VerticalAlignment.Top;
        _command.FontFamily = (System.Windows.Media.FontFamily)FindResource("Font.Mono");

        Title = Strings.Get(existing is null ? "snippet.new" : "common.edit");
        Width = 620;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        var buttons = Ui.Columns(8,
            Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); }),
            Ui.Accent(Strings.Get("common.save"), Save));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0,
            Ui.Field("snippet.name", _name),
            Ui.Field("snippet.category", _category),
            Ui.Field("snippet.command", _command),
            Ui.Field("server.notes", _notes),
            _error,
            buttons);
        root.Margin = new Thickness(20);
        Content = root;
    }

    private void Save()
    {
        var name = _name.Text.Trim();
        var command = _command.Text.Trim();
        if (name.Length == 0 || command.Length == 0)
        {
            _error.Text = Strings.Get("snippet.required");
            _error.Visibility = Visibility.Visible;
            return;
        }
        _snippet.Name = name;
        _snippet.Command = command;
        _snippet.Category = _category.Text.Trim();
        _snippet.Notes = _notes.Text.Trim();
        App.Current.Monitor.Save(_snippet);
        DialogResult = true;
        Close();
    }
}

/// <summary>Runs one snippet on one host and shows the output.</summary>
public sealed class SnippetRunWindow : Window
{
    private readonly Snippet _snippet;
    private readonly TextBox _output;
    private Server _target;

    public SnippetRunWindow(Snippet snippet)
    {
        _snippet = snippet;
        _target = App.Current.Monitor.Servers[0];

        Title = snippet.Name;
        Width = 760;
        Height = 520;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _output = Ui.Input();
        _output.IsReadOnly = true;
        _output.AcceptsReturn = true;
        _output.VerticalContentAlignment = VerticalAlignment.Top;
        _output.FontFamily = (System.Windows.Media.FontFamily)FindResource("Font.Mono");
        _output.VerticalScrollBarVisibility = ScrollBarVisibility.Auto;
        _output.TextWrapping = TextWrapping.NoWrap;
        _output.HorizontalScrollBarVisibility = ScrollBarVisibility.Auto;

        var picker = Ui.Picker(
            App.Current.Monitor.Servers.ToList(),
            _target,
            server => server.Name,
            server => _target = server);

        var run = Ui.Accent(Strings.Get("snippet.run"), () => _ = RunAsync());

        var command = Ui.Mono(snippet.Command);
        command.TextWrapping = TextWrapping.Wrap;
        command.TextTrimming = TextTrimming.None;

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });

        var top = Ui.Grid("*,auto", picker, run);
        Grid.SetRow(top, 0);
        root.Children.Add(top);

        var commandCard = Ui.Card(command);
        commandCard.Margin = new Thickness(0, 10, 0, 10);
        Grid.SetRow(commandCard, 1);
        root.Children.Add(commandCard);

        Grid.SetRow(_output, 2);
        root.Children.Add(_output);

        root.Margin = new Thickness(20);
        Content = root;
    }

    private async Task RunAsync()
    {
        _output.Text = "…";
        IsEnabled = false;
        try
        {
            var output = await App.Current.Monitor.RunAsync(_snippet, _target);
            _output.Text = output.Trim().Length == 0 ? Strings.Get("snippet.noOutput") : output;
        }
        catch (Exception error)
        {
            _output.Text = error.Message;
        }
        finally
        {
            IsEnabled = true;
        }
    }
}
