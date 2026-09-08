using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using Microsoft.Terminal.Wpf;
using ServerMonitor.App.Theme;

namespace ServerMonitor.App.Terminal;

/// <summary>
/// The terminal widget, and the only place that knows which one it is.
/// </summary>
/// <remarks>
/// R3's containment. <see cref="TerminalControl"/> comes from a package that
/// is not an official Microsoft release — it is the Windows Terminal control
/// republished by a third party — so the risk is that its API moves or the
/// package goes away. Everything that touches its types is in this file and
/// <see cref="ShellStreamConnection"/>; the rest of the app sees a
/// <see cref="TerminalHost"/> with a connection, a theme and a paste method.
/// Swapping in the fallback engine would be a rewrite of these two files.
///
/// The control is an <c>HwndHost</c>, which brings the airspace rule: WPF
/// content cannot be drawn over it. That is why the snippet picker is a
/// toolbar button rather than an overlay, and why there is no scrim over the
/// terminal while it reconnects.
/// </remarks>
internal sealed class TerminalHost : ContentControl
{
    private readonly TerminalControl _terminal = new();
    private ShellStreamConnection? _connection;

    /// <summary>Raised when the shell on the far side finishes.</summary>
    public event Action? Ended;

    public TerminalHost()
    {
        // AutoResize means the control tells the connection its new row and
        // column count as the window changes, which is the whole of SIGWINCH
        // handling from this side.
        _terminal.AutoResize = true;
        Content = _terminal;
        Focusable = true;
        GotFocus += (_, _) => _terminal.Focus();
    }

    public int Rows => _terminal.Rows;
    public int Columns => _terminal.Columns;

    /// <summary>Attaches a shell and starts pumping it.</summary>
    public void Connect(ShellStreamConnection connection)
    {
        Disconnect();
        _connection = connection;
        connection.Ended += () => Ended?.Invoke();
        _terminal.Connection = connection;
        connection.Start();
        // The control only learns its size once it has been laid out, and a
        // shell started before that would come up 80x24 whatever the window
        // is. Nudging it after the first layout pass gets the real size to the
        // far side before the prompt is drawn.
        Dispatcher.BeginInvoke(
            System.Windows.Threading.DispatcherPriority.Loaded,
            new Action(() => _terminal.TriggerResize(new Size(ActualWidth, ActualHeight))));
    }

    public void Disconnect()
    {
        if (_connection is null) return;
        _terminal.Connection = null;
        _connection.Dispose();
        _connection = null;
    }

    /// <summary>Types text as if the user had.</summary>
    /// <remarks>
    /// What a snippet does. Deliberately not "run": the text lands at the
    /// prompt and the user presses Enter, so a snippet with a mistake in it
    /// can be read and corrected first — the macOS build made the same choice.
    /// </remarks>
    public void Type(string text) => _connection?.WriteInput(text);

    public void Paste()
    {
        try
        {
            var text = System.Windows.Clipboard.GetText();
            // Bracketed paste is the shell's job to enable; what matters here
            // is that a copied block of commands does not lose its newlines,
            // and that a stray CR does not double every line.
            if (text.Length > 0) Type(text.Replace("\r\n", "\r").Replace('\n', '\r'));
        }
        catch (Exception)
        {
            // Another process owns the clipboard. Nothing to do but ignore it.
        }
    }

    public string SelectedText => _terminal.GetSelectedText();

    public void Copy()
    {
        var text = SelectedText;
        if (text.Length == 0) return;
        try
        {
            System.Windows.Clipboard.SetText(text);
        }
        catch (Exception)
        {
            // Same.
        }
    }

    /// <summary>
    /// Repaints the terminal in the app's colours.
    /// </summary>
    /// <remarks>
    /// The palette is the standard sixteen, because that is what the programs
    /// on the far side assume — a scheme of our own would make <c>ls</c>
    /// colours and every TUI look wrong. Only the background, foreground,
    /// selection and cursor follow the app's theme.
    /// </remarks>
    public void ApplyTheme(bool dark, string fontFamily, int fontSize)
    {
        var theme = new TerminalTheme
        {
            DefaultBackground = Bgr(dark ? Color.FromRgb(0x1B, 0x1B, 0x1B) : Color.FromRgb(0xFB, 0xFB, 0xFB)),
            DefaultForeground = Bgr(dark ? Color.FromRgb(0xE6, 0xE6, 0xE6) : Color.FromRgb(0x1A, 0x1A, 0x1A)),
            DefaultSelectionBackground = Bgr(Palette.Accent),
            CursorStyle = CursorStyle.BlinkingBar,
            ColorTable = dark ? DarkTable : LightTable,
        };
        _terminal.SetTheme(theme, fontFamily, (short)fontSize, Palette.Accent);
    }

    /// <summary>
    /// A colour in the packed form the control wants: 0x00BBGGRR.
    /// </summary>
    /// <remarks>
    /// Reversed from the ARGB every other Windows API uses, and silently — a
    /// theme built with the channels the usual way round looks plausible and
    /// has red and blue swapped.
    /// </remarks>
    private static uint Bgr(Color colour) =>
        (uint)(colour.R | (colour.G << 8) | (colour.B << 16));

    private static readonly uint[] DarkTable =
    [
        Bgr(Color.FromRgb(0x0C, 0x0C, 0x0C)), Bgr(Color.FromRgb(0xC5, 0x0F, 0x1F)),
        Bgr(Color.FromRgb(0x13, 0xA1, 0x0E)), Bgr(Color.FromRgb(0xC1, 0x9C, 0x00)),
        Bgr(Color.FromRgb(0x00, 0x37, 0xDA)), Bgr(Color.FromRgb(0x88, 0x17, 0x98)),
        Bgr(Color.FromRgb(0x3A, 0x96, 0xDD)), Bgr(Color.FromRgb(0xCC, 0xCC, 0xCC)),
        Bgr(Color.FromRgb(0x76, 0x76, 0x76)), Bgr(Color.FromRgb(0xE7, 0x48, 0x56)),
        Bgr(Color.FromRgb(0x16, 0xC6, 0x0C)), Bgr(Color.FromRgb(0xF9, 0xF1, 0xA5)),
        Bgr(Color.FromRgb(0x3B, 0x78, 0xFF)), Bgr(Color.FromRgb(0xB4, 0x00, 0x9E)),
        Bgr(Color.FromRgb(0x61, 0xD6, 0xD6)), Bgr(Color.FromRgb(0xF2, 0xF2, 0xF2)),
    ];

    private static readonly uint[] LightTable =
    [
        Bgr(Color.FromRgb(0x1A, 0x1A, 0x1A)), Bgr(Color.FromRgb(0xC5, 0x0F, 0x1F)),
        Bgr(Color.FromRgb(0x0D, 0x7C, 0x0B)), Bgr(Color.FromRgb(0x9A, 0x7A, 0x00)),
        Bgr(Color.FromRgb(0x00, 0x37, 0xDA)), Bgr(Color.FromRgb(0x88, 0x17, 0x98)),
        Bgr(Color.FromRgb(0x1F, 0x76, 0xB0)), Bgr(Color.FromRgb(0x76, 0x76, 0x76)),
        Bgr(Color.FromRgb(0x4A, 0x4A, 0x4A)), Bgr(Color.FromRgb(0xA8, 0x0D, 0x1A)),
        Bgr(Color.FromRgb(0x0A, 0x66, 0x08)), Bgr(Color.FromRgb(0x7A, 0x5F, 0x00)),
        Bgr(Color.FromRgb(0x1A, 0x4F, 0xC4)), Bgr(Color.FromRgb(0x6E, 0x12, 0x7A)),
        Bgr(Color.FromRgb(0x17, 0x5E, 0x8C)), Bgr(Color.FromRgb(0x2B, 0x2B, 0x2B)),
    ];
}
