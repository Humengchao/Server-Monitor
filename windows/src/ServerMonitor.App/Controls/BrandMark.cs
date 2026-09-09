using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;

namespace ServerMonitor.App.Controls;

/// <summary>
/// The app's mark: a gradient tile with a cloud over a server rack.
/// </summary>
/// <remarks>
/// Taken from the web client's brand lockup — <c>.brand-mark</c> in
/// <c>web/frontend/src/index.css</c>: a 42px tile, 13px corner radius, a
/// 135° gradient from #7096FF to #4E5EE9, holding a white
/// <c>CloudServerOutlined</c> glyph. The wordmark and the CONTROL CENTER
/// line beside it are the sidebar's, not this control's.
///
/// The glyph is drawn here rather than imported. Ant's own path was not
/// available to copy (the web client's dependencies are not vendored), so
/// what this reproduces is the mark's composition and weight — an outlined
/// cloud above an outlined rack with two slots — at the same proportions,
/// not Ant's bezier data byte for byte.
///
/// Drawn in <see cref="OnRender"/> for the same reason
/// <see cref="RingGauge"/> is (D6), though the argument is weaker here:
/// there is one of these on screen, not eighteen. The real reason is that a
/// vector mark drawn from geometry is also the source the .ico is generated
/// from, so the sidebar and the taskbar cannot drift apart.
/// </remarks>
public sealed class BrandMark : Control
{
    /// <summary>Keeps the mark out of the tab order — it is a drawing.</summary>
    /// <remarks>
    /// <see cref="Control"/> makes its subclasses focusable; every drawn
    /// control in this app turns that back off, and a logo that could be
    /// tabbed to would be the first stop in the whole window.
    /// </remarks>
    static BrandMark() =>
        FocusableProperty.OverrideMetadata(
            typeof(BrandMark), new FrameworkPropertyMetadata(false));

    /// <summary>The web mark's two gradient stops.</summary>
    public static readonly Color GradientFrom = Color.FromRgb(0x70, 0x96, 0xFF);
    public static readonly Color GradientTo = Color.FromRgb(0x4E, 0x5E, 0xE9);

    /// <summary>13px of corner on a 42px tile, as a fraction.</summary>
    private const double CornerFraction = 13.0 / 42.0;

    /// <summary>
    /// The glyph's share of the tile's edge, which grows as the tile shrinks.
    /// </summary>
    /// <remarks>
    /// At the large sizes this matches the web mark: a 21px font in a 42px
    /// tile, and since an icon font's glyph does not fill its em box, a little
    /// over half once drawn.
    ///
    /// Holding that ratio all the way down does not work, and the first
    /// attempt at this proved it — the 16px frame came out a blue tile with a
    /// grey smudge in it. 56% of 16px is a nine-pixel drawing that has to hold
    /// a cloud, a gap and a two-slot rack: three bands and two gaps inside
    /// nine pixels. The padding is what has to give, because it is the only
    /// part of the picture that carries nothing.
    /// </remarks>
    private static double GlyphFraction(double edge) => edge switch
    {
        >= 64 => 0.56,
        >= 32 => 0.68,
        _ => 0.80,
    };

    /// <summary>
    /// Stroke weight in the glyph's own 0-100 units.
    /// </summary>
    /// <remarks>
    /// Constant here means constant *relative* to the glyph, so it thins in
    /// device pixels as the icon shrinks — 7 units of a nine-pixel glyph is
    /// 0.6 of a pixel, which the rasteriser renders as a grey suggestion of a
    /// line rather than a line. The small sizes are drawn heavier so their
    /// strokes land on something like a whole pixel.
    /// </remarks>
    private static double Weight(double edge) => edge switch
    {
        >= 64 => 7.0,
        >= 32 => 9.0,
        _ => 12.5,
    };

    /// <summary>
    /// Below this edge length the glyph drops its finest detail.
    /// </summary>
    /// <remarks>
    /// A 16px taskbar icon is 16 device pixels at 100% scaling, which leaves
    /// the rack's slot divider and its two indicator dots well under a pixel
    /// each: they do not render as detail, they render as grey. Dropping them
    /// and thickening what remains keeps the silhouette — cloud over rack —
    /// which is the part that is recognisable at that size. Ordinary icon
    /// practice, and the reason a real icon set draws its small sizes
    /// separately rather than scaling one master down.
    /// </remarks>
    private const double DetailFloor = 32;

    public static readonly DependencyProperty EdgeProperty = DependencyProperty.Register(
        nameof(Edge),
        typeof(double),
        typeof(BrandMark),
        new FrameworkPropertyMetadata(42.0, FrameworkPropertyMetadataOptions.AffectsMeasure));

    /// <summary>Width and height of the tile. Square by definition.</summary>
    public double Edge
    {
        get => (double)GetValue(EdgeProperty);
        set => SetValue(EdgeProperty, value);
    }

    protected override Size MeasureOverride(Size available) => new(Edge, Edge);

    protected override void OnRender(DrawingContext dc) => Draw(dc, Edge);

    /// <summary>
    /// Paints the mark into <paramref name="dc"/> at the given edge length.
    /// </summary>
    /// <remarks>
    /// Static and size-parameterised so the icon generator can render the
    /// same drawing at 16 through 256 without instantiating a control on a
    /// UI thread it does not have.
    /// </remarks>
    internal static void Draw(DrawingContext dc, double edge)
    {
        if (edge <= 0) return;

        var tile = new RectangleGeometry(
            new Rect(0, 0, edge, edge), edge * CornerFraction, edge * CornerFraction);
        dc.DrawGeometry(Tile, null, tile);

        // The glyph is authored in a 0-100 box and scaled onto the tile, so
        // every coordinate below reads as a percentage of the glyph rather
        // than a pixel count at one particular size.
        var glyph = edge * GlyphFraction(edge);
        var inset = (edge - glyph) / 2;
        dc.PushTransform(new TranslateTransform(inset, inset));
        dc.PushTransform(new ScaleTransform(glyph / 100.0, glyph / 100.0));
        DrawGlyph(dc, detailed: edge >= DetailFloor, weight: Weight(edge));
        dc.Pop();
        dc.Pop();
    }

    /// <summary>
    /// The cloud and the rack, in a 0-100 box.
    /// </summary>
    /// <remarks>
    /// Pen widths are in the same 0-100 units as the shapes, so a stroke
    /// keeps its proportion at every size rather than being a hairline at 256
    /// and a blob at 16.
    /// </remarks>
    private static void DrawGlyph(DrawingContext dc, bool detailed, double weight)
    {
        var white = Theme.Ink.Brush(Colors.White);
        if (!detailed)
        {
            DrawSolidGlyph(dc, white);
            return;
        }

        var pen = RoundPen(weight);
        dc.DrawGeometry(null, pen, Cloud);
        dc.DrawGeometry(null, pen, new RectangleGeometry(new Rect(16, 54, 68, 38), 8, 8));

        // The divider between the rack's two slots, and the indicator lamp in
        // each — the detail that says "server" rather than "box".
        dc.DrawLine(pen, new Point(16, 73), new Point(84, 73));
        dc.DrawEllipse(white, null, new Point(29, 63.5), 4, 4);
        dc.DrawEllipse(white, null, new Point(29, 82.5), 4, 4);
    }

    /// <summary>
    /// The same silhouette, filled instead of outlined, for the small frames.
    /// </summary>
    /// <remarks>
    /// Thickening the outline was not enough and the 16px frame proved it: at
    /// that size the cloud's ring is about 1.6px of stroke around 1.8px of
    /// hole, so it closes up and the whole glyph renders as one grey blob.
    /// An outline needs three things to survive — two walls and a gap — where
    /// a filled shape needs one. So the small sizes keep the composition, the
    /// proportions and the silhouette, and give up the hollow.
    ///
    /// This is what an icon set means by drawing its small sizes separately.
    /// It is a departure from the web mark, which is outlined at every size
    /// because the web never renders it at sixteen pixels.
    /// </remarks>
    private static void DrawSolidGlyph(DrawingContext dc, Brush white)
    {
        dc.DrawGeometry(white, null, Cloud);
        // Two bars rather than a bordered box: at this size the box's border
        // and its contents are the same pixel.
        dc.DrawGeometry(white, null, new RectangleGeometry(new Rect(16, 54, 68, 17), 5, 5));
        dc.DrawGeometry(white, null, new RectangleGeometry(new Rect(16, 76, 68, 17), 5, 5));
    }

    /// <summary>One frozen pen per weight — see the note on Ink.</summary>
    private static readonly Dictionary<double, Pen> Pens = [];
    private static readonly Lock PenGate = new();

    private static Pen RoundPen(double width)
    {
        lock (PenGate)
        {
            if (Pens.TryGetValue(width, out var existing)) return existing;
            var pen = new Pen(Theme.Ink.Brush(Colors.White), width)
            {
                StartLineCap = PenLineCap.Round,
                EndLineCap = PenLineCap.Round,
                LineJoin = PenLineJoin.Round,
            };
            pen.Freeze();
            Pens[width] = pen;
            return pen;
        }
    }

    /// <summary>
    /// The cloud's outline as one closed path.
    /// </summary>
    /// <remarks>
    /// Built by unioning two lobes and a base rather than drawn as a path:
    /// stroking a <see cref="GeometryGroup"/> would outline each part
    /// separately and draw the circles' buried halves straight across the
    /// cloud. <see cref="Geometry.Combine"/> gives the silhouette, which is
    /// the only edge there should be.
    /// </remarks>
    private static readonly Geometry Cloud = BuildCloud();

    private static Geometry BuildCloud()
    {
        var small = new EllipseGeometry(new Point(36, 30), 16, 16);
        var large = new EllipseGeometry(new Point(61, 26), 20, 20);
        var union = Geometry.Combine(small, large, GeometryCombineMode.Union, null);
        var basin = new RectangleGeometry(new Rect(20, 30, 61, 14), 7, 7);
        var cloud = Geometry.Combine(union, basin, GeometryCombineMode.Union, null);
        cloud.Freeze();
        return cloud;
    }

    /// <summary>The tile's fill, at 135° as the web's gradient is.</summary>
    private static readonly Brush Tile = BuildTileBrush();

    private static Brush BuildTileBrush()
    {
        var brush = new LinearGradientBrush(
            GradientFrom, GradientTo, new Point(0, 0), new Point(1, 1));
        brush.Freeze();
        return brush;
    }
}
