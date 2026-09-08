using System.Windows.Media;
using ServerMonitor.Core;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Theme;

/// <summary>
/// Every colour the app draws with, per theme.
/// </summary>
/// <remarks>
/// Code rather than a resource dictionary because most of the surface is drawn
/// into a <see cref="DrawingContext"/> (D6), where a
/// <c>DynamicResource</c> is not reachable — and because the status thresholds
/// have to agree between the ring gauge, the tray icon and the tables, which
/// is easier to hold in one file than across three templates.
///
/// The XAML side reads these through <c>ThemeBrushes</c>, so there is still
/// exactly one definition of "card background".
/// </remarks>
public static class Palette
{
    public static bool IsDark { get; private set; }

    public static void Use(bool dark) => IsDark = dark;

    // MARK: - Surfaces

    public static Color Background => IsDark ? Rgb(0x1C, 0x1C, 0x1C) : Rgb(0xF3, 0xF3, 0xF3);

    /// <summary>
    /// The card surface. Slightly translucent so Mica shows through, which is
    /// the whole point of asking for it.
    /// </summary>
    public static Color Card => IsDark ? Argb(0xD8, 0x2B, 0x2B, 0x2B) : Argb(0xE0, 0xFF, 0xFF, 0xFF);

    public static Color CardHover => IsDark ? Argb(0xEE, 0x33, 0x33, 0x33) : Argb(0xF6, 0xFF, 0xFF, 0xFF);

    public static Color Sidebar => IsDark ? Argb(0x40, 0x00, 0x00, 0x00) : Argb(0x50, 0xFF, 0xFF, 0xFF);

    public static Color Border => IsDark ? Argb(0x30, 0xFF, 0xFF, 0xFF) : Argb(0x24, 0x00, 0x00, 0x00);

    public static Color Separator => IsDark ? Argb(0x1E, 0xFF, 0xFF, 0xFF) : Argb(0x14, 0x00, 0x00, 0x00);

    // MARK: - Text

    public static Color Text => IsDark ? Rgb(0xF2, 0xF2, 0xF2) : Rgb(0x18, 0x18, 0x18);
    public static Color Secondary => IsDark ? Rgb(0xA6, 0xA6, 0xA6) : Rgb(0x60, 0x60, 0x60);
    public static Color Tertiary => IsDark ? Rgb(0x76, 0x76, 0x76) : Rgb(0x8C, 0x8C, 0x8C);

    // MARK: - Accents and status

    public static Color Accent => IsDark ? Rgb(0x5B, 0xA9, 0xF5) : Rgb(0x0F, 0x6C, 0xBD);
    public static Color Online => IsDark ? Rgb(0x3E, 0xC4, 0x6D) : Rgb(0x14, 0x8F, 0x45);
    public static Color Offline => IsDark ? Rgb(0xF2, 0x5A, 0x67) : Rgb(0xD1, 0x1F, 0x2E);
    public static Color Warning => IsDark ? Rgb(0xF0, 0xB0, 0x2E) : Rgb(0xB8, 0x7A, 0x00);
    public static Color Unknown => Tertiary;

    /// <summary>
    /// The colour for a utilisation percentage.
    /// </summary>
    /// <remarks>
    /// The same four bands the macOS ring gauge uses, and the same ones the
    /// tray icon reads — so a host that looks amber in the window does not look
    /// green in the notification area.
    /// </remarks>
    public static Color ForPercent(double value) => value switch
    {
        < 50 => Online,
        < 70 => Warning,
        < 85 => IsDark ? Rgb(0xE8, 0x8A, 0x2A) : Rgb(0xC4, 0x5E, 0x00),
        _ => Offline,
    };

    /// <summary>
    /// Green under 100 ms, amber to 300 ms, orange beyond — the point where an
    /// interactive SSH session starts to feel laggy.
    /// </summary>
    public static Color ForLatency(double milliseconds) => milliseconds switch
    {
        < 100 => Online,
        < 300 => Warning,
        _ => IsDark ? Rgb(0xE8, 0x8A, 0x2A) : Rgb(0xC4, 0x5E, 0x00),
    };

    // MARK: - Named colours shared with the macOS build

    /// <summary>
    /// The eight-colour tag palette, in the order the macOS build lists it.
    /// </summary>
    /// <remarks>
    /// Order is load-bearing: <see cref="Format.TagColorIndex"/> hashes a tag
    /// to an index into this, and both clients must land on the same one so
    /// "prod" is the same colour on a Mac and here. Do not reorder.
    /// </remarks>
    private static readonly (byte R, byte G, byte B)[] TagPalette =
    [
        (0x2F, 0x86, 0xE0),   // blue
        (0x2E, 0xA0, 0x4E),   // green
        (0xE0, 0x7C, 0x1F),   // orange
        (0x8B, 0x5C, 0xD6),   // purple
        (0xD8, 0x4C, 0x92),   // pink
        (0x1F, 0x9E, 0xA0),   // teal
        (0x5A, 0x5F, 0xD6),   // indigo
        (0x9C, 0x6B, 0x3F),   // brown
    ];

    public static Color ForTag(string tag)
    {
        var (r, g, b) = TagPalette[Format.TagColorIndex(tag, TagPalette.Length)];
        // Lifted in dark mode: the saturated values above are chosen for a
        // light background and read as muddy against a dark one.
        return IsDark ? Rgb(Lift(r), Lift(g), Lift(b)) : Rgb(r, g, b);
    }

    private static byte Lift(byte channel) => (byte)Math.Min(255, channel + 40);

    /// <summary>The group colours, by the name stored on the row.</summary>
    public static Color ForGroup(string colorName)
    {
        var (r, g, b) = colorName switch
        {
            "green" => (0x2E, 0xA0, 0x4E),
            "orange" => (0xE0, 0x7C, 0x1F),
            "purple" => (0x8B, 0x5C, 0xD6),
            "pink" => (0xD8, 0x4C, 0x92),
            "teal" => (0x1F, 0x9E, 0xA0),
            "red" => (0xD1, 0x3B, 0x3B),
            "gray" => (0x7A, 0x7A, 0x7A),
            _ => (0x2F, 0x86, 0xE0),
        };
        return IsDark
            ? Rgb(Lift((byte)r), Lift((byte)g), Lift((byte)b))
            : Rgb((byte)r, (byte)g, (byte)b);
    }

    public static Color ForStatus(ServerMonitor.Core.Collect.StatusKind kind) => kind switch
    {
        ServerMonitor.Core.Collect.StatusKind.Online => Online,
        ServerMonitor.Core.Collect.StatusKind.Offline => Offline,
        _ => Unknown,
    };

    private static Color Rgb(byte r, byte g, byte b) => Color.FromRgb(r, g, b);
    private static Color Rgb(int r, int g, int b) => Color.FromRgb((byte)r, (byte)g, (byte)b);
    private static Color Argb(byte a, byte r, byte g, byte b) => Color.FromArgb(a, r, g, b);
    private static Color Argb(int a, int r, int g, int b) =>
        Color.FromArgb((byte)a, (byte)r, (byte)g, (byte)b);
}

/// <summary>
/// Frozen brushes and pens for the drawing code.
/// </summary>
/// <remarks>
/// Every one is <see cref="Freezable.Freeze"/>d and cached. An unfrozen brush
/// created inside <c>OnRender</c> is the single easiest way to make a
/// self-drawn WPF control slow: each one allocates, registers for change
/// notification, and cannot be shared across threads — and this renders a
/// dozen gauges and four charts on every publish.
/// </remarks>
public static class Ink
{
    private static readonly Dictionary<uint, SolidColorBrush> Brushes = [];
    private static readonly Dictionary<(uint Colour, double Width), Pen> Pens = [];
    private static readonly Lock Gate = new();

    public static SolidColorBrush Brush(Color colour)
    {
        var key = Key(colour);
        lock (Gate)
        {
            if (Brushes.TryGetValue(key, out var existing)) return existing;
            var brush = new SolidColorBrush(colour);
            brush.Freeze();
            Brushes[key] = brush;
            return brush;
        }
    }

    public static Pen Stroke(Color colour, double width)
    {
        var key = (Key(colour), width);
        lock (Gate)
        {
            if (Pens.TryGetValue(key, out var existing)) return existing;
            var pen = new Pen(Brush(colour), width);
            pen.Freeze();
            Pens[key] = pen;
            return pen;
        }
    }

    /// <summary>A pen with round caps, for the gauge arc.</summary>
    public static Pen RoundStroke(Color colour, double width)
    {
        var key = (Key(colour), -width);
        lock (Gate)
        {
            if (Pens.TryGetValue(key, out var existing)) return existing;
            var pen = new Pen(Brush(colour), width)
            {
                StartLineCap = PenLineCap.Round,
                EndLineCap = PenLineCap.Round,
            };
            pen.Freeze();
            Pens[key] = pen;
            return pen;
        }
    }

    /// <summary>
    /// Clears the cache after a theme change.
    /// </summary>
    /// <remarks>
    /// The colours are keyed by value, so stale entries would be correct but
    /// unreachable — this is about not holding both palettes' brushes for the
    /// life of the process.
    /// </remarks>
    public static void Reset()
    {
        lock (Gate)
        {
            Brushes.Clear();
            Pens.Clear();
        }
    }

    private static uint Key(Color colour) =>
        ((uint)colour.A << 24) | ((uint)colour.R << 16) | ((uint)colour.G << 8) | colour.B;
}
