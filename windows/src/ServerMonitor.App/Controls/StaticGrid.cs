using System.Windows;
using System.Windows.Controls;

namespace ServerMonitor.App.Controls;

/// <summary>
/// A non-lazy adaptive grid, laid out as balanced columns.
/// </summary>
/// <remarks>
/// The machine screen's card container. Deliberately not virtualised, and the
/// macOS build's comment explains why better than a rewrite would: a lazy grid
/// materialises only what is visible and estimates the rest, so scrolling up
/// re-creates cards at their real height, the scroll offset is corrected to
/// compensate, and the view jumps back down — with a card that loads
/// asynchronously and changes height when it does, that becomes a loop the
/// user cannot scroll out of.
///
/// Ten cards do not need laziness. Columns rather than rows so cards of very
/// different heights do not stretch each other.
/// </remarks>
public sealed class StaticGrid : Panel
{
    /// <summary>Narrowest a column may be before dropping to fewer columns.</summary>
    public double MinimumColumnWidth { get; set; } = 340;

    public double Spacing { get; set; } = 14;

    /// <summary>
    /// Relative heights, one per child, steering which column each lands in.
    /// </summary>
    /// <remarks>
    /// A rough weight (1 for a short card, 4 for a tall one) rather than a
    /// measured height: the real height is not known until after the layout it
    /// is meant to inform, and the assignment only has to be good enough that
    /// the columns end at roughly the same place.
    /// </remarks>
    public List<double> Weights { get; } = [];

    /// <summary>
    /// Width step the columns quantise to.
    /// </summary>
    /// <remarks>
    /// 16px, from the macOS build's measurement: during a live resize the pane
    /// width changes by a pixel or two per frame, and with the column width
    /// quantised most of those frames leave every card's proposed size
    /// unchanged, so the whole tree comes from WPF's layout cache. Measured on
    /// the full machine screen there: median 21 ms per frame unquantised, 6 ms
    /// quantised.
    /// </remarks>
    public const double WidthStep = 16;

    /// <summary>Rounds a width down to the step.</summary>
    public static double Quantise(double width, double step = WidthStep) =>
        width <= 0 || step <= 1 ? width : Math.Floor(width / step) * step;

    /// <summary>
    /// How many columns fit.
    /// </summary>
    /// <remarks>
    /// Never more than there are children: three columns for two cards leaves
    /// an empty third of the pane.
    /// </remarks>
    public static int ColumnCount(double available, int items, double minimum, double spacing)
    {
        if (available <= 0 || items <= 0) return 1;
        return Math.Max(1, Math.Min(items, (int)((available + spacing) / (minimum + spacing))));
    }

    /// <summary>Each column's exact width, quantised.</summary>
    public static double ColumnWidth(
        double available, int columns, double minimum, double spacing, double step = WidthStep)
    {
        if (available <= 0 || columns <= 0) return minimum;
        var exact = (available - spacing * (columns - 1)) / columns;
        return step > 1 ? Math.Max(minimum, Math.Floor(exact / step) * step) : exact;
    }

    /// <summary>
    /// What the rounding left over, spread across every slot.
    /// </summary>
    /// <remarks>
    /// Both outer margins and each gap, so the grid still fills the pane and
    /// no single gap absorbs it all. With two columns the slack can reach
    /// 30px; in one gap that is a 14→44px gap visibly breathing during a drag,
    /// spread over three slots it is ≤10px everywhere.
    /// </remarks>
    public static double SlackPerSlot(
        double available, int columns, double columnWidth, double spacing)
    {
        if (available <= 0 || columns <= 0) return 0;
        var slack = available - columnWidth * columns - spacing * (columns - 1);
        return Math.Max(0, slack / (columns + 1));
    }

    /// <summary>
    /// Which child indices go in which column.
    /// </summary>
    /// <remarks>
    /// Each card, in order, joins the column that is currently shortest by
    /// weight. Round-robin put a tall card beside a short one and left the
    /// short column with a hole the height of the difference; this keeps the
    /// columns' bottoms close together. Ties go to the leftmost, so a fresh row
    /// still fills left to right.
    /// </remarks>
    public static List<List<int>> Assign(IReadOnlyList<double> weights, int columns)
    {
        if (columns <= 1) return [[.. Enumerable.Range(0, weights.Count)]];
        var result = new List<List<int>>();
        for (var i = 0; i < columns; i++) result.Add([]);
        var heights = new double[columns];

        for (var index = 0; index < weights.Count; index++)
        {
            var shortest = 0;
            for (var column = 1; column < columns; column++)
            {
                if (heights[column] < heights[shortest]) shortest = column;
            }
            result[shortest].Add(index);
            heights[shortest] += weights[index];
        }
        return result;
    }

    private double WeightAt(int index) =>
        index < Weights.Count ? Math.Max(0.1, Weights[index]) : 1;

    protected override Size MeasureOverride(Size available)
    {
        var count = InternalChildren.Count;
        if (count == 0) return new Size(0, 0);

        var width = double.IsInfinity(available.Width) ? MinimumColumnWidth : available.Width;
        var columns = ColumnCount(width, count, MinimumColumnWidth, Spacing);
        var columnWidth = ColumnWidth(width, columns, MinimumColumnWidth, Spacing);
        var assignment = Assign(
            [.. Enumerable.Range(0, count).Select(WeightAt)], columns);

        var columnHeights = new double[columns];
        for (var column = 0; column < columns; column++)
        {
            foreach (var index in assignment[column])
            {
                var child = InternalChildren[index];
                child.Measure(new Size(columnWidth, double.PositiveInfinity));
                if (columnHeights[column] > 0) columnHeights[column] += Spacing;
                columnHeights[column] += child.DesiredSize.Height;
            }
        }
        return new Size(width, columnHeights.Length == 0 ? 0 : columnHeights.Max());
    }

    protected override Size ArrangeOverride(Size final)
    {
        var count = InternalChildren.Count;
        if (count == 0) return final;

        var columns = ColumnCount(final.Width, count, MinimumColumnWidth, Spacing);
        var columnWidth = ColumnWidth(final.Width, columns, MinimumColumnWidth, Spacing);
        var slack = SlackPerSlot(final.Width, columns, columnWidth, Spacing);
        var gap = Spacing + slack;
        var assignment = Assign(
            [.. Enumerable.Range(0, count).Select(WeightAt)], columns);

        var tallest = 0.0;
        for (var column = 0; column < columns; column++)
        {
            var x = slack + column * (columnWidth + gap);
            var y = 0.0;
            foreach (var index in assignment[column])
            {
                var child = InternalChildren[index];
                child.Arrange(new Rect(x, y, columnWidth, child.DesiredSize.Height));
                y += child.DesiredSize.Height + Spacing;
            }
            tallest = Math.Max(tallest, y);
        }
        return new Size(final.Width, tallest);
    }
}
