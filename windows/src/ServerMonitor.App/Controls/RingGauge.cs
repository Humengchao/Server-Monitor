using System.Globalization;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using ServerMonitor.App.Theme;

namespace ServerMonitor.App.Controls;

/// <summary>
/// Circular percentage gauge, for CPU and memory on the dashboard cards.
/// </summary>
/// <remarks>
/// A ring rather than a bar because the cards put two of them side by side at
/// a glanceable size, where a bar's fill is much harder to read quickly.
///
/// Drawn in <see cref="OnRender"/> rather than composed from XAML shapes
/// (D6): a dashboard of nine cards is eighteen of these, and eighteen
/// templated controls with an <c>ArcSegment</c> apiece is eighteen subtrees to
/// lay out on every publish, against one <c>DrawingContext</c> call each.
///
/// There is deliberately no animation. Every poll moves every gauge, and on
/// the macOS build an eased arc meant 0.35 s of display-rate frames per gauge
/// per tick — 71% of the main thread's work on a screen with a dozen gauges —
/// for a sweep nobody watches. It snaps, the way Task Manager's do.
/// </remarks>
public sealed class RingGauge : Control
{
    /// <summary>Keeps this drawing out of the tab order.</summary>
    /// <remarks>
    /// Not focusable. This is a drawing, and Control makes its subclasses
    /// focusable by default — so every one of these was a tab stop that did
    /// nothing. Two ring gauges per dashboard card meant moving from one host
    /// to the next took three Tab presses, two of which landed on no visible
    /// focus at all. Measured by pressing Tab and reading the focused element
    /// back; nothing about the screen says it.
    /// </remarks>
    static RingGauge() =>
        FocusableProperty.OverrideMetadata(
            typeof(RingGauge), new FrameworkPropertyMetadata(false));

    public static readonly DependencyProperty ValueProperty = DependencyProperty.Register(
        nameof(Value),
        typeof(double),
        typeof(RingGauge),
        new FrameworkPropertyMetadata(0.0, FrameworkPropertyMetadataOptions.AffectsRender));

    /// <summary>0–100. Clamped when drawn, so a host reporting 103% is not a bug here.</summary>
    public double Value
    {
        get => (double)GetValue(ValueProperty);
        set => SetValue(ValueProperty, value);
    }

    public static readonly DependencyProperty DiameterProperty = DependencyProperty.Register(
        nameof(Diameter),
        typeof(double),
        typeof(RingGauge),
        new FrameworkPropertyMetadata(62.0, FrameworkPropertyMetadataOptions.AffectsMeasure));

    public double Diameter
    {
        get => (double)GetValue(DiameterProperty);
        set => SetValue(DiameterProperty, value);
    }

    public static readonly DependencyProperty ThicknessProperty = DependencyProperty.Register(
        nameof(Thickness),
        typeof(double),
        typeof(RingGauge),
        new FrameworkPropertyMetadata(7.0, FrameworkPropertyMetadataOptions.AffectsRender));

    public double Thickness
    {
        get => (double)GetValue(ThicknessProperty);
        set => SetValue(ThicknessProperty, value);
    }

    protected override Size MeasureOverride(Size constraint) => new(Diameter, Diameter);

    protected override void OnRender(DrawingContext context)
    {
        var value = Math.Clamp(Value, 0, 100);
        var diameter = Diameter;
        var thickness = Thickness;
        var radius = (diameter - thickness) / 2;
        var centre = new Point(diameter / 2, diameter / 2);

        // The track, always a full circle.
        context.DrawEllipse(
            null,
            Ink.Stroke(Palette.IsDark
                ? Color.FromArgb(0x28, 0xFF, 0xFF, 0xFF)
                : Color.FromArgb(0x1A, 0x00, 0x00, 0x00), thickness),
            centre,
            radius,
            radius);

        if (value > 0)
        {
            context.DrawGeometry(
                null, Ink.RoundStroke(Palette.ForPercent(value), thickness), Arc(centre, radius, value));
        }

        // The number, centred. A FormattedText per render is unavoidable — the
        // text changes with the value — but it is one object, not a TextBlock
        // in a template with its own measure pass.
        var label = new FormattedText(
            $"{Math.Round(value, MidpointRounding.AwayFromZero):0}%",
            CultureInfo.InvariantCulture,
            FlowDirection.LeftToRight,
            Typeface,
            diameter * 0.26,
            Ink.Brush(Palette.Text),
            VisualTreeHelper.GetDpi(this).PixelsPerDip);
        context.DrawText(
            label,
            new Point(centre.X - label.Width / 2, centre.Y - label.Height / 2));
    }

    /// <summary>
    /// The filled arc, starting at twelve o'clock and going clockwise.
    /// </summary>
    /// <remarks>
    /// A <see cref="StreamGeometry"/> rather than a <c>PathFigure</c> tree:
    /// it is write-once, freezable, and allocates one object rather than a
    /// figure plus a segment plus their collections.
    /// </remarks>
    private static Geometry Arc(Point centre, double radius, double value)
    {
        // A full ring cannot be one arc segment — start and end coincide and
        // the sweep is ambiguous — so it is an ellipse instead.
        if (value >= 99.95)
        {
            var full = new EllipseGeometry(centre, radius, radius);
            full.Freeze();
            return full;
        }

        var sweep = 2 * Math.PI * (value / 100);
        var start = new Point(centre.X, centre.Y - radius);
        var end = new Point(
            centre.X + radius * Math.Sin(sweep),
            centre.Y - radius * Math.Cos(sweep));

        var geometry = new StreamGeometry();
        using (var writer = geometry.Open())
        {
            writer.BeginFigure(start, isFilled: false, isClosed: false);
            writer.ArcTo(
                end,
                new Size(radius, radius),
                rotationAngle: 0,
                isLargeArc: value > 50,
                SweepDirection.Clockwise,
                isStroked: true,
                isSmoothJoin: false);
        }
        geometry.Freeze();
        return geometry;
    }

    /// <summary>
    /// The face for the numbers.
    /// </summary>
    /// <remarks>
    /// Tabular figures, so a gauge going 9% → 10% → 9% does not jitter as the
    /// digits change width — the same reason the macOS build asks for
    /// <c>monospacedDigit</c>.
    /// </remarks>
    private static readonly Typeface Typeface = new(
        new FontFamily("Segoe UI"), FontStyles.Normal, FontWeights.SemiBold, FontStretches.Normal);
}

/// <summary>
/// A small horizontal bar, for per-core and per-mount rows.
/// </summary>
/// <remarks>
/// The counterpart of the ring where there are twenty of them in a column and
/// a ring apiece would be unreadable — and where the row already has a label,
/// so the bar only has to convey magnitude.
/// </remarks>
public sealed class MeterBar : Control
{
    /// <summary>Keeps this drawing out of the tab order.</summary>
    /// <remarks>
    /// Not focusable. This is a drawing, and Control makes its subclasses
    /// focusable by default — so every one of these was a tab stop that did
    /// nothing. Two ring gauges per dashboard card meant moving from one host
    /// to the next took three Tab presses, two of which landed on no visible
    /// focus at all. Measured by pressing Tab and reading the focused element
    /// back; nothing about the screen says it.
    /// </remarks>
    static MeterBar() =>
        FocusableProperty.OverrideMetadata(
            typeof(MeterBar), new FrameworkPropertyMetadata(false));

    public static readonly DependencyProperty ValueProperty = DependencyProperty.Register(
        nameof(Value),
        typeof(double),
        typeof(MeterBar),
        new FrameworkPropertyMetadata(0.0, FrameworkPropertyMetadataOptions.AffectsRender));

    public double Value
    {
        get => (double)GetValue(ValueProperty);
        set => SetValue(ValueProperty, value);
    }

    /// <summary>
    /// Override the threshold colouring — for a bar that measures something
    /// where "high" is not "bad", such as one interface's share of traffic.
    /// </summary>
    public static readonly DependencyProperty TintProperty = DependencyProperty.Register(
        nameof(Tint),
        typeof(Color?),
        typeof(MeterBar),
        new FrameworkPropertyMetadata(null, FrameworkPropertyMetadataOptions.AffectsRender));

    public Color? Tint
    {
        get => (Color?)GetValue(TintProperty);
        set => SetValue(TintProperty, value);
    }

    protected override void OnRender(DrawingContext context)
    {
        var width = ActualWidth;
        var height = ActualHeight;
        if (width <= 0 || height <= 0) return;

        var radius = height / 2;
        var track = new RectangleGeometry(new Rect(0, 0, width, height), radius, radius);
        track.Freeze();
        context.DrawGeometry(
            Ink.Brush(Palette.IsDark
                ? Color.FromArgb(0x28, 0xFF, 0xFF, 0xFF)
                : Color.FromArgb(0x18, 0x00, 0x00, 0x00)),
            null,
            track);

        var value = Math.Clamp(Value, 0, 100);
        if (value <= 0) return;
        // Never narrower than its own height: a 1% bar drawn to scale is a
        // sliver that reads as zero, and "nearly nothing" and "nothing" are
        // different answers.
        var filled = Math.Max(height, width * value / 100);
        var fill = new RectangleGeometry(new Rect(0, 0, filled, height), radius, radius);
        fill.Freeze();
        context.DrawGeometry(Ink.Brush(Tint ?? Palette.ForPercent(value)), null, fill);
    }
}
