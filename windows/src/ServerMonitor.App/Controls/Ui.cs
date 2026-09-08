using System.Windows;
using System.Windows.Controls;
using System.Windows.Documents;
using System.Windows.Input;
using System.Windows.Media;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;
using ServerMonitor.Core.L10n;

namespace ServerMonitor.App.Controls;

/// <summary>
/// Factories for the handful of shapes every page is built from.
/// </summary>
/// <remarks>
/// The pages are assembled in C# rather than XAML. Two reasons, both about
/// this app specifically: most of what they contain is drawn
/// (<see cref="RingGauge"/>, <see cref="HistoryChart"/>, <see cref="StaticGrid"/>),
/// so a XAML page would mostly be a list of placeholders; and the machine
/// screen rebuilds its cards from a snapshot on every publish, which in XAML
/// means either a template per card shape or a pile of converters.
///
/// Everything here uses <c>DynamicResource</c> for colour, so a theme switch
/// repaints without rebuilding — see <c>App.ApplyTheme</c>.
/// </remarks>
internal static class Ui
{
    // MARK: - Text

    public static TextBlock Text(string text, string style = "Text.Body") => new()
    {
        Text = text,
        Style = (Style)Application.Current.FindResource(style),
        TextTrimming = TextTrimming.CharacterEllipsis,
    };

    public static TextBlock Caption(string text) => Text(text, "Text.Caption");
    public static TextBlock Tertiary(string text) => Text(text, "Text.Tertiary");
    public static TextBlock Title(string text) => Text(text, "Text.Title");
    public static TextBlock Headline(string text) => Text(text, "Text.Headline");
    public static TextBlock Mono(string text) => Text(text, "Text.Mono");

    /// <summary>
    /// A path or other value the user will want to copy.
    /// </summary>
    /// <remarks>
    /// A read-only TextBox rather than a TextBlock, because a WPF TextBlock
    /// cannot be selected at all — the macOS settings screen turns selection
    /// on explicitly for the same path, and a path shown but not copyable is
    /// something the user has to retype by hand. Styled down to look like the
    /// caption it replaces: no border, no background, no caret of its own.
    /// </remarks>
    public static TextBox Selectable(string text)
    {
        var box = new TextBox
        {
            Text = text,
            IsReadOnly = true,
            IsReadOnlyCaretVisible = false,
            BorderThickness = new Thickness(0),
            Background = System.Windows.Media.Brushes.Transparent,
            Padding = new Thickness(0),
            Margin = new Thickness(0),
            Foreground = Ink.Brush(Theme.Palette.Secondary),
            FontSize = 11,
            // The paths are long and the settings card is not wide; wrapping
            // beats a box the user has to scroll sideways to read.
            TextWrapping = TextWrapping.Wrap,
            HorizontalAlignment = HorizontalAlignment.Stretch,
        };
        // The template's own placeholder and focus chrome are for input
        // fields; this is a label that happens to be selectable.
        box.SetValue(FrameworkElement.FocusVisualStyleProperty, null);
        return box;
    }

    /// <summary>A number, with tabular figures so it does not jitter as it changes.</summary>
    public static TextBlock Number(string text, double size = 13, Color? colour = null)
    {
        var block = Text(text, "Text.Number");
        block.FontSize = size;
        if (colour is { } value) block.Foreground = Ink.Brush(value);
        return block;
    }

    /// <summary>A label with the text localised from a key.</summary>
    public static TextBlock Label(string key) => Caption(Strings.Get(key));

    public static TextBlock Wrapped(string text, string style = "Text.Body")
    {
        var block = Text(text, style);
        block.TextTrimming = TextTrimming.None;
        block.TextWrapping = TextWrapping.Wrap;
        return block;
    }

    // MARK: - Layout

    public static StackPanel Stack(
        Orientation orientation = Orientation.Vertical,
        double spacing = 0,
        params UIElement[] children)
    {
        var panel = new SpacedStack { Orientation = orientation, Spacing = spacing };
        foreach (var child in children) panel.Children.Add(child);
        return panel;
    }

    public static StackPanel Rows(double spacing, params UIElement[] children) =>
        Stack(Orientation.Vertical, spacing, children);

    public static StackPanel Columns(double spacing, params UIElement[] children) =>
        Stack(Orientation.Horizontal, spacing, children);

    /// <summary>
    /// A grid with the given column widths.
    /// </summary>
    /// <remarks>
    /// <c>"*"</c> is a star column, <c>"auto"</c> sizes to content, a number
    /// is a fixed width — the same shorthand XAML uses, so the intent reads
    /// the same way.
    /// </remarks>
    public static Grid Grid(string columns, params UIElement[] children)
    {
        var grid = new Grid();
        var index = 0;
        foreach (var spec in columns.Split(',', StringSplitOptions.RemoveEmptyEntries))
        {
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = Length(spec.Trim()) });
        }
        foreach (var child in children)
        {
            System.Windows.Controls.Grid.SetColumn(child, Math.Min(index++, grid.ColumnDefinitions.Count - 1));
            grid.Children.Add(child);
        }
        return grid;
    }

    private static GridLength Length(string spec) => spec switch
    {
        "*" => new GridLength(1, GridUnitType.Star),
        "auto" => GridLength.Auto,
        _ when spec.EndsWith('*') =>
            new GridLength(spec[..^1].ToDouble(), GridUnitType.Star),
        _ => new GridLength(spec.ToDouble()),
    };

    /// <summary>The card chrome every surface shares.</summary>
    public static Border Card(UIElement content, double padding = 14)
    {
        var border = new Border
        {
            Style = (Style)Application.Current.FindResource("Card"),
            Padding = new Thickness(padding),
            Child = content,
        };
        return border;
    }

    /// <summary>A card that responds to hover and click, for the dashboard.</summary>
    /// <summary>
    /// A card you can click, tab to, and activate from the keyboard.
    /// </summary>
    /// <param name="name">
    /// What assistive technology should call it — the host's name, for the
    /// dashboard. Without it a screen reader announces the card's contents and
    /// no identity.
    /// </param>
    public static Border ClickableCard(
        UIElement content, Action onClick, double padding = 14, string? name = null)
    {
        var card = new CardButton(onClick, name)
        {
            Style = (Style)Application.Current.FindResource("Card"),
            Padding = new Thickness(padding),
            Child = content,
        };
        return card;
    }

    public static Border Separator() => new()
    {
        Height = 1,
        Background = (Brush)Application.Current.FindResource("Brush.Separator"),
        Margin = new Thickness(0, 8, 0, 8),
    };

    public static ScrollViewer Scroll(UIElement content) => new()
    {
        Content = content,
        VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
        HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
        // Otherwise a mouse wheel over a card's own list scrolls the page
        // instead, which makes a long process table unusable.
        CanContentScroll = false,
        Padding = new Thickness(0),
    };

    /// <summary>
    /// A command wrapping an action.
    /// </summary>
    /// <remarks>
    /// Hand-written rather than the toolkit's <c>RelayCommand</c>: these are
    /// key bindings that are always enabled, and a generated command per
    /// shortcut buys nothing.
    /// </remarks>
    internal sealed class Command(Action action) : System.Windows.Input.ICommand
    {
        public event EventHandler? CanExecuteChanged;
        public bool CanExecute(object? parameter) => true;
        public void Execute(object? parameter) => action();
        // Never raised — these are always available. Declared to satisfy the
        // interface, and the compiler's unused-event warning with it.
        internal void Refresh() => CanExecuteChanged?.Invoke(this, EventArgs.Empty);
    }

    // MARK: - Controls

    public static Button Button(string text, Action onClick, string style = "Button.Standard")
    {
        var button = new Button
        {
            Content = text,
            Style = (Style)Application.Current.FindResource(style),
        };
        button.Click += (_, _) => onClick();
        return button;
    }

    public static Button Accent(string text, Action onClick) =>
        Button(text, onClick, "Button.Accent");

    public static Button Quiet(string text, Action onClick) =>
        Button(text, onClick, "Button.Quiet");

    public static Button Danger(string text, Action onClick) =>
        Button(text, onClick, "Button.Danger");

    public static TextBox Input(string value = "", double width = double.NaN)
    {
        var box = new TextBox { Text = value };
        if (!double.IsNaN(width))
        {
            box.Width = width;
            // A fixed width with the default Stretch alignment leaves WPF to
            // centre the box in its cell, so the port and country fields sat
            // in the middle of a column every other input starts at the left
            // of. Narrow does not mean centred.
            box.HorizontalAlignment = HorizontalAlignment.Left;
        }
        return box;
    }

    public static PasswordBox Secret(string placeholder = "")
    {
        var box = new PasswordBox();
        if (placeholder.Length > 0) ToolTipService.SetToolTip(box, placeholder);
        return box;
    }

    public static ComboBox Picker<T>(
        IEnumerable<T> options, T? selected, Func<T, string> label, Action<T> onChange)
    {
        var picker = new ComboBox();
        var list = options.ToList();
        foreach (var option in list)
        {
            picker.Items.Add(new ComboBoxItem { Content = label(option), Tag = option });
        }
        picker.SelectedIndex = selected is null ? 0 : Math.Max(0, list.IndexOf(selected));
        picker.SelectionChanged += (_, _) =>
        {
            if (picker.SelectedItem is ComboBoxItem { Tag: T value }) onChange(value);
        };
        return picker;
    }

    public static CheckBox Toggle(string text, bool value, Action<bool> onChange)
    {
        var box = new CheckBox { Content = text, IsChecked = value };
        box.Checked += (_, _) => onChange(true);
        box.Unchecked += (_, _) => onChange(false);
        return box;
    }

    /// <summary>
    /// A toggle whose new state has to be accepted before it sticks.
    /// </summary>
    /// <remarks>
    /// For the settings whose real home is somewhere outside the app — the
    /// startup entry lives in the registry, and the write can be refused by
    /// group policy. Returning false from <paramref name="onChange"/> puts the
    /// box back, so it shows what is true rather than what was clicked.
    ///
    /// Assigning IsChecked inside the handler raises the opposite event, which
    /// would call back in and, for a setting that is failing, open a second
    /// dialog on the way back. The flag is what stops that.
    /// </remarks>
    public static CheckBox Toggle(string text, bool value, Func<bool, bool> onChange)
    {
        var box = new CheckBox { Content = text, IsChecked = value };
        var reverting = false;

        void Handle(bool wanted)
        {
            if (reverting) return;
            if (onChange(wanted)) return;
            reverting = true;
            box.IsChecked = !wanted;
            reverting = false;
        }

        box.Checked += (_, _) => Handle(true);
        box.Unchecked += (_, _) => Handle(false);
        return box;
    }

    /// <summary>A labelled row for the editors and settings, from string keys.</summary>
    public static Grid Field(string labelKey, UIElement control, string? helpKey = null) =>
        FieldText(
            Strings.Get(labelKey),
            control,
            helpKey is null ? null : Strings.Get(helpKey));

    /// <summary>
    /// The same row, from text that is already in the right language.
    /// </summary>
    /// <remarks>
    /// For the settings that exist only on Windows — the SSH transport, the
    /// data paths — which have no key in the shared table and must not get
    /// one, since the table is asserted to match the macOS side key for key.
    /// Passing such a label to <see cref="Field"/> appears to work only
    /// because a missing key renders as itself, which makes a real typo
    /// indistinguishable from a deliberate literal.
    /// </remarks>
    public static Grid FieldText(string label, UIElement control, string? help = null)
    {
        var labelBlock = Caption(label);
        labelBlock.VerticalAlignment = VerticalAlignment.Center;
        labelBlock.Margin = new Thickness(0, 0, 12, 0);
        labelBlock.TextTrimming = TextTrimming.None;
        labelBlock.TextWrapping = TextWrapping.Wrap;

        var right = help is null
            ? control
            : Rows(4, control, Wrapped(help, "Text.Tertiary"));

        var grid = Grid("150,*", labelBlock, right);
        grid.Margin = new Thickness(0, 0, 0, 10);
        return grid;
    }

    // MARK: - Chips and pills

    /// <summary>
    /// A country, as a two-letter badge.
    /// </summary>
    /// <remarks>
    /// Not a flag: Windows ships no glyphs for regional-indicator pairs in any
    /// font, so <c>Format.Flag</c>'s emoji renders as the two letters anyway —
    /// on macOS it is a flag, here it was two bare capitals jammed against the
    /// hostname. A badge makes that deliberate rather than broken.
    /// </remarks>
    public static Border CountryBadge(string countryCode)
    {
        var text = Caption(countryCode.ToUpperInvariant());
        text.Foreground = Theme.Ink.Brush(Theme.Palette.Secondary);
        return new Border
        {
            Background = Theme.Ink.Brush(Theme.Palette.Separator),
            CornerRadius = new CornerRadius(4),
            Padding = new Thickness(5, 1, 5, 2),
            VerticalAlignment = VerticalAlignment.Center,
            Child = text,
        };
    }

    /// <summary>A tag chip, coloured from the tag's own text.</summary>
    public static Border TagChip(string tag)
    {
        var colour = Palette.ForTag(tag);
        var text = Text(tag);
        text.FontSize = 10;
        text.FontWeight = FontWeights.Medium;
        text.Foreground = Ink.Brush(colour);
        return new Border
        {
            Background = Ink.Brush(Color.FromArgb(0x2A, colour.R, colour.G, colour.B)),
            CornerRadius = new CornerRadius(8),
            Padding = new Thickness(6, 1, 6, 2),
            Child = text,
            VerticalAlignment = VerticalAlignment.Center,
        };
    }

    /// <summary>
    /// Up to <paramref name="limit"/> chips, then a "+n" for the rest.
    /// </summary>
    public static StackPanel TagChips(IReadOnlyList<string> tags, int limit = 4)
    {
        var panel = Stack(Orientation.Horizontal, 4);
        foreach (var tag in tags.Take(limit)) panel.Children.Add(TagChip(tag));
        if (tags.Count > limit)
        {
            var more = Tertiary($"+{tags.Count - limit}");
            more.VerticalAlignment = VerticalAlignment.Center;
            more.Margin = new Thickness(4, 0, 0, 0);
            panel.Children.Add(more);
        }
        return panel;
    }

    /// <summary>A coloured dot, for status.</summary>
    public static System.Windows.Shapes.Ellipse Dot(Color colour, double size = 8) => new()
    {
        Width = size,
        Height = size,
        Fill = Ink.Brush(colour),
        VerticalAlignment = VerticalAlignment.Center,
    };

    /// <summary>Icon-and-value pair for a card's facts row.</summary>
    public static StackPanel Fact(string glyph, string value)
    {
        var icon = Tertiary(glyph);
        icon.FontFamily = new FontFamily("Segoe Fluent Icons, Segoe MDL2 Assets");
        icon.FontSize = 11;
        icon.VerticalAlignment = VerticalAlignment.Center;
        var text = Caption(value);
        text.VerticalAlignment = VerticalAlignment.Center;
        return Columns(5, icon, text);
    }

    /// <summary>
    /// A "nothing here yet" panel.
    /// </summary>
    /// <remarks>
    /// An empty page that explains what to do next, rather than a blank area
    /// that reads as a loading failure.
    /// </remarks>
    /// <param name="titleKey">
    /// A heading, or null where it would only repeat the page title.
    /// </param>
    /// <remarks>
    /// The macOS build passes the page's own key here for four of these
    /// screens, and it works there: its ContentUnavailableView draws the title
    /// beside an SF Symbol, and the page name lives in the window's toolbar.
    /// On Windows the page name is a large heading at the top of the content
    /// area, so the same call put the word "Containers" in 24pt and then again
    /// in 15pt ninety pixels below it. Those screens pass null, and the
    /// message becomes the heading.
    /// </remarks>
    public static Border Empty(string? titleKey, string bodyKey, UIElement? action = null)
    {
        // With no heading of its own the message has to carry the weight, or
        // the screen is one line of grey text adrift in an empty pane.
        var body = Wrapped(
            Strings.Get(bodyKey), titleKey is null ? "Text.Title" : "Text.Secondary");
        body.TextAlignment = TextAlignment.Center;

        var children = new List<UIElement>();
        if (titleKey is not null) children.Add(Title(Strings.Get(titleKey)));
        children.Add(body);
        if (action is not null) children.Add(action);

        var stack = Rows(10, [.. children]);
        stack.HorizontalAlignment = HorizontalAlignment.Center;
        stack.MaxWidth = 420;
        foreach (var child in stack.Children.OfType<FrameworkElement>())
        {
            child.HorizontalAlignment = HorizontalAlignment.Center;
        }
        return new Border { Padding = new Thickness(24, 90, 24, 24), Child = stack };
    }

    /// <summary>A hyperlink that opens in the default browser.</summary>
    public static TextBlock Link(string text, string url)
    {
        var link = new Hyperlink(new Run(text)) { NavigateUri = new Uri(url) };
        link.RequestNavigate += (_, e) =>
        {
            try
            {
                System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(e.Uri.ToString())
                {
                    UseShellExecute = true,
                });
            }
            catch (Exception)
            {
                // No default browser, or the user cancelled the association
                // prompt. Nothing useful to do.
            }
            e.Handled = true;
        };
        var block = new TextBlock(link)
        {
            Style = (Style)Application.Current.FindResource("Text.Caption"),
        };
        return block;
    }

    /// <summary>
    /// A spinner.
    /// </summary>
    /// <remarks>
    /// Deliberately the only animation in the app: it means "this is still
    /// happening", which cannot be conveyed statically. Everything else snaps
    /// (see <see cref="RingGauge"/>).
    /// </remarks>
    public static FrameworkElement Spinner(double size = 16)
    {
        var bar = new ProgressBar
        {
            IsIndeterminate = true,
            Width = size * 5,
            Height = 3,
            Foreground = Ink.Brush(Palette.Accent),
            Background = Brushes.Transparent,
            BorderThickness = new Thickness(0),
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Center,
        };
        return bar;
    }

    /// <summary>Confirm-or-cancel, in the app's language.</summary>
    public static bool Confirm(
        Window? owner, string message, string? title = null, string? confirmLabel = null) =>
        Views.MessageWindow.Confirm(owner, message, title, confirmLabel);

    // MessageWindow rather than MessageBox.Show: Windows' own box ignores the
    // app's theme, so in dark mode every dialog was a white rectangle over a
    // dark window, and Windows labels its buttons in the *system* language,
    // so an English app on a Chinese Windows had a 确定 button. See
    // MessageWindow for the one place that deliberately still uses the
    // native box.
    public static void Inform(Window? owner, string message, string? title = null) =>
        Views.MessageWindow.Inform(owner, message, title);

    public static void Complain(Window? owner, string message) =>
        Views.MessageWindow.Complain(owner, message);

    /// <summary>Copies to the clipboard, swallowing the transient failures.</summary>
    public static bool Copy(string text)
    {
        // The clipboard is a shared, single-owner resource: another process
        // holding it open makes this throw, and retrying once is what every
        // Windows app ends up doing.
        for (var attempt = 0; attempt < 2; attempt++)
        {
            try
            {
                Clipboard.SetText(text);
                return true;
            }
            catch (Exception)
            {
                Thread.Sleep(60);
            }
        }
        return false;
    }
}
