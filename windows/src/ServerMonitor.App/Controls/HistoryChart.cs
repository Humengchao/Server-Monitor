using System.Globalization;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;

namespace ServerMonitor.App.Controls;

/// <summary>One series on a history chart.</summary>
/// <param name="Label">Shown in the legend.</param>
/// <param name="Values">Oldest first; the x axis is the index.</param>
/// <param name="Colour">Line colour; the fill is the same at low alpha.</param>
/// <param name="Filled">Whether to shade below the line.</param>
public sealed record ChartSeries(
    string Label,
    IReadOnlyList<double> Values,
    Color Colour,
    bool Filled = true);

/// <summary>
/// A filled line chart, drawn by hand (D6).
/// </summary>
/// <remarks>
/// Self-drawn rather than a charting library, for the reason the macOS build
/// gave up on Swift Charts: four charts of a day's polls is the machine
/// screen's whole redraw budget, and a library that lays out a mark per point
/// cannot do it in the time a frame allows. Here each series is one
/// <see cref="StreamGeometry"/>, built once per data change and frozen.
///
/// The data is already thinned to at most 240 points before it arrives (see
/// <c>Database.ReducedSamples</c>), so the geometry is bounded whatever range
/// the user picks.
/// </remarks>
public sealed class HistoryChart : Control
{
    private IReadOnlyList<ChartSeries> _series = [];
    /// <summary>
    /// The y-axis top. Null auto-scales to the data; a fixed value is right
    /// for a percentage, where 0–100 is the meaningful range whatever the
    /// numbers happen to be.
    /// </summary>
    private double? _maximum;
    private Func<double, string> _format = value => value.ToString("0.#", CultureInfo.InvariantCulture);

    public HistoryChart()
    {
        Height = 140;
        // Otherwise the chart's own hit-testing is skipped and the tooltip
        // never appears.
        Background = Brushes.Transparent;
    }

    /// <summary>Replaces the data and redraws.</summary>
    public void Show(
        IReadOnlyList<ChartSeries> series,
        double? maximum = null,
        Func<double, string>? format = null)
    {
        _series = series;
        _maximum = maximum;
        if (format is not null) _format = format;
        InvalidateVisual();
    }

    protected override void OnRender(DrawingContext context)
    {
        var width = ActualWidth;
        var height = ActualHeight;
        if (width <= 1 || height <= 1) return;

        const double LeftGutter = 46;
        const double BottomGutter = 4;
        const double TopPadding = 10;
        var plotWidth = Math.Max(1, width - LeftGutter);
        var plotHeight = Math.Max(1, height - BottomGutter - TopPadding);

        var top = _maximum ?? Ceiling(_series
            .SelectMany(s => s.Values)
            .DefaultIfEmpty(0)
            .Max());
        if (top <= 0) top = 1;

        // Four gridlines and their labels. Drawn first, so the series sit on
        // top of them rather than being crossed by them.
        var gridPen = Ink.Stroke(Palette.Separator, 1);
        for (var step = 0; step <= 4; step++)
        {
            var value = top * step / 4;
            var y = TopPadding + plotHeight - plotHeight * step / 4;
            // Snapped to a whole pixel: a 1px line on a half-pixel boundary is
            // drawn as two half-intensity rows, which reads as a blurry chart.
            y = Math.Round(y) + 0.5;
            context.DrawLine(gridPen, new Point(LeftGutter, y), new Point(width, y));

            var label = new FormattedText(
                _format(value),
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                LabelFace,
                10,
                Ink.Brush(Palette.Tertiary),
                VisualTreeHelper.GetDpi(this).PixelsPerDip);
            context.DrawText(label, new Point(LeftGutter - 6 - label.Width, y - label.Height / 2));
        }

        foreach (var series in _series)
        {
            if (series.Values.Count == 0) continue;
            DrawSeries(context, series, LeftGutter, TopPadding, plotWidth, plotHeight, top);
        }

        if (_series.Count > 1) DrawLegend(context, LeftGutter, width);
    }

    private void DrawSeries(
        DrawingContext context,
        ChartSeries series,
        double left,
        double top,
        double plotWidth,
        double plotHeight,
        double maximum)
    {
        var values = series.Values;
        double X(int index) => values.Count == 1
            ? left + plotWidth
            : left + plotWidth * index / (values.Count - 1);
        double Y(double value) =>
            top + plotHeight - plotHeight * Math.Clamp(value / maximum, 0, 1);

        var line = new StreamGeometry();
        using (var writer = line.Open())
        {
            writer.BeginFigure(new Point(X(0), Y(values[0])), isFilled: false, isClosed: false);
            for (var i = 1; i < values.Count; i++)
            {
                writer.LineTo(new Point(X(i), Y(values[i])), isStroked: true, isSmoothJoin: false);
            }
        }
        line.Freeze();

        if (series.Filled && values.Count > 1)
        {
            // The fill is a separate closed geometry rather than the same one
            // stroked and filled: a closed figure's stroke would draw the
            // baseline and the two verticals as part of the line.
            var fill = new StreamGeometry();
            using (var writer = fill.Open())
            {
                writer.BeginFigure(new Point(X(0), top + plotHeight), isFilled: true, isClosed: true);
                writer.LineTo(new Point(X(0), Y(values[0])), isStroked: false, isSmoothJoin: false);
                for (var i = 1; i < values.Count; i++)
                {
                    writer.LineTo(new Point(X(i), Y(values[i])), isStroked: false, isSmoothJoin: false);
                }
                writer.LineTo(
                    new Point(X(values.Count - 1), top + plotHeight),
                    isStroked: false,
                    isSmoothJoin: false);
            }
            fill.Freeze();
            context.DrawGeometry(
                Ink.Brush(Color.FromArgb(0x2E, series.Colour.R, series.Colour.G, series.Colour.B)),
                null,
                fill);
        }

        context.DrawGeometry(null, Ink.Stroke(series.Colour, 1.6), line);
    }

    private void DrawLegend(DrawingContext context, double left, double width)
    {
        var x = left + 4;
        foreach (var series in _series)
        {
            context.DrawEllipse(Ink.Brush(series.Colour), null, new Point(x + 3, 6), 3, 3);
            var label = new FormattedText(
                series.Label,
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                LabelFace,
                10,
                Ink.Brush(Palette.Secondary),
                VisualTreeHelper.GetDpi(this).PixelsPerDip);
            context.DrawText(label, new Point(x + 10, 0));
            x += 10 + label.Width + 12;
            if (x > width - 40) return;
        }
    }

    /// <summary>
    /// A round number at or above the peak, so the axis labels read cleanly.
    /// </summary>
    /// <remarks>
    /// Without this the top label is whatever the highest sample happened to
    /// be — "7.34 MB/s" — and the chart's scale appears to jitter between
    /// refreshes even when the data barely moved.
    /// </remarks>
    internal static double Ceiling(double peak)
    {
        if (peak <= 0) return 1;
        var magnitude = Math.Pow(10, Math.Floor(Math.Log10(peak)));
        foreach (var step in new[] { 1.0, 2.0, 2.5, 5.0, 10.0 })
        {
            if (peak <= magnitude * step) return magnitude * step;
        }
        return magnitude * 10;
    }

    private static readonly Typeface LabelFace = new(
        new FontFamily("Segoe UI"), FontStyles.Normal, FontWeights.Normal, FontStretches.Normal);
}

/// <summary>
/// A bar chart, for the vnStat traffic buckets.
/// </summary>
/// <remarks>
/// Bars rather than a line because the buckets are discrete totals — "how
/// much moved in this hour" — and a line between them implies a rate that was
/// never measured.
/// </remarks>
public sealed class BarChart : Control
{
    private IReadOnlyList<(string Label, double Down, double Up)> _bars = [];

    public BarChart()
    {
        Height = 150;
        Background = Brushes.Transparent;
    }

    public void Show(IReadOnlyList<(string Label, double Down, double Up)> bars)
    {
        _bars = bars;
        InvalidateVisual();
    }

    protected override void OnRender(DrawingContext context)
    {
        var width = ActualWidth;
        var height = ActualHeight;
        if (width <= 1 || height <= 1 || _bars.Count == 0) return;

        const double LeftGutter = 56;
        const double LabelHeight = 16;
        var plotWidth = Math.Max(1, width - LeftGutter);
        var plotHeight = Math.Max(1, height - LabelHeight - 8);

        var top = HistoryChart.Ceiling(_bars.Max(b => Math.Max(b.Down, b.Up)));
        if (top <= 0) top = 1;

        var gridPen = Ink.Stroke(Palette.Separator, 1);
        for (var step = 0; step <= 2; step++)
        {
            var y = Math.Round(8 + plotHeight - plotHeight * step / 2) + 0.5;
            context.DrawLine(gridPen, new Point(LeftGutter, y), new Point(width, y));
            var label = new FormattedText(
                Format.Bytes(top * step / 2),
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                Face,
                10,
                Ink.Brush(Palette.Tertiary),
                VisualTreeHelper.GetDpi(this).PixelsPerDip);
            context.DrawText(label, new Point(LeftGutter - 6 - label.Width, y - label.Height / 2));
        }

        // Two bars per bucket, side by side. Gaps scale with the count so 10
        // buckets and 30 buckets both fill the width.
        var slot = plotWidth / _bars.Count;
        var barWidth = Math.Max(2, slot * 0.32);
        var downBrush = Ink.Brush(Palette.Accent);
        var upBrush = Ink.Brush(Palette.Online);

        for (var i = 0; i < _bars.Count; i++)
        {
            var (label, down, up) = _bars[i];
            var centre = LeftGutter + slot * i + slot / 2;

            void Bar(double value, double offset, Brush brush)
            {
                var barHeight = plotHeight * Math.Clamp(value / top, 0, 1);
                if (barHeight <= 0) return;
                context.DrawRectangle(
                    brush,
                    null,
                    new Rect(centre + offset, 8 + plotHeight - barHeight, barWidth, barHeight));
            }

            Bar(down, -barWidth - 1, downBrush);
            Bar(up, 1, upBrush);

            // Every label on a 30-day chart overlaps, so they thin out.
            var every = Math.Max(1, (int)Math.Ceiling(_bars.Count / 12.0));
            if (i % every != 0) continue;
            var text = new FormattedText(
                label,
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                Face,
                9,
                Ink.Brush(Palette.Tertiary),
                VisualTreeHelper.GetDpi(this).PixelsPerDip);
            context.DrawText(text, new Point(centre - text.Width / 2, height - LabelHeight + 2));
        }
    }

    private static readonly Typeface Face = new(
        new FontFamily("Segoe UI"), FontStyles.Normal, FontWeights.Normal, FontStretches.Normal);
}
