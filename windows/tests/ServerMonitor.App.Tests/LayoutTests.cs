using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// The card grid's arithmetic, which is where a resize goes wrong.
/// </summary>
/// <remarks>
/// Pure functions on purpose — that is the reason they are static rather than
/// buried inside <c>MeasureOverride</c>, where the only way to check them
/// would be to measure a live panel at a dozen widths.
/// </remarks>
public class LayoutTests
{
    [Theory]
    [InlineData(300, 6, 1)]   // narrower than one column: still one, never zero
    [InlineData(693, 6, 1)]   // 340+14+340 = 694, so 693 is one — but only just
    [InlineData(694, 6, 2)]
    [InlineData(700, 6, 2)]
    [InlineData(1100, 6, 3)]
    [InlineData(1100, 2, 2)]  // never more columns than cards
    [InlineData(1100, 1, 1)]
    [InlineData(0, 6, 1)]
    public void ColumnsFitWithoutEverReachingZero(double width, int items, int expected) =>
        Assert.Equal(expected, StaticGrid.ColumnCount(width, items, 340, 14));

    [Fact]
    public void AColumnIsNeverNarrowerThanTheMinimum()
    {
        // The rounding-down is the risk: a floor that lands below the minimum
        // would let cards clip their own content.
        for (var width = 340.0; width < 2400; width += 1)
        {
            var columns = StaticGrid.ColumnCount(width, 8, 340, 14);
            var columnWidth = StaticGrid.ColumnWidth(width, columns, 340, 14);
            Assert.True(columnWidth >= 340, $"{width}px gave a {columnWidth}px column");
        }
    }

    [Fact]
    public void ColumnsAndGapsNeverOverflowThePane()
    {
        for (var width = 340.0; width < 2400; width += 1)
        {
            var columns = StaticGrid.ColumnCount(width, 8, 340, 14);
            var columnWidth = StaticGrid.ColumnWidth(width, columns, 340, 14);
            var slack = StaticGrid.SlackPerSlot(width, columns, columnWidth, 14);
            var used = (columnWidth * columns) + ((14 + slack) * (columns - 1)) + (slack * 2);
            Assert.True(used <= width + 0.001, $"{width}px laid out {used}px");
        }
    }

    [Fact]
    public void QuantisingHoldsTheWidthStillAcrossSmallDrags()
    {
        // The whole point of the step: a pane dragged from 1200 to 1215 must
        // propose the same column width every frame, so WPF's layout cache
        // answers instead of re-measuring every card.
        var widths = Enumerable.Range(1200, 16)
            .Select(w => StaticGrid.ColumnWidth(w, 3, 340, 14))
            .Distinct()
            .ToList();
        Assert.True(widths.Count <= 2, $"16px of drag produced {widths.Count} widths");
    }

    [Fact]
    public void QuantiseNeverGrowsAWidth()
    {
        foreach (var width in new[] { 0.0, 1, 15, 16, 17, 340, 1919 })
        {
            Assert.True(StaticGrid.Quantise(width) <= width);
        }
    }

    [Fact]
    public void EveryCardLandsInExactlyOneColumn()
    {
        var weights = new double[] { 1, 3, 1, 4, 2, 1, 5, 1 };
        foreach (var columns in new[] { 1, 2, 3, 4 })
        {
            var assignment = StaticGrid.Assign(weights, columns);
            var placed = assignment.SelectMany(c => c).OrderBy(i => i).ToList();
            Assert.Equal(Enumerable.Range(0, weights.Length), placed);
        }
    }

    [Fact]
    public void ColumnsEndAtRoughlyTheSameHeight()
    {
        // The reason the assignment is shortest-first rather than round-robin.
        var weights = new double[] { 5, 1, 1, 1, 4, 1, 1, 1 };
        var assignment = StaticGrid.Assign(weights, 2);
        var totals = assignment.Select(column => column.Sum(i => weights[i])).ToList();
        Assert.True(
            Math.Abs(totals[0] - totals[1]) <= weights.Max(),
            $"columns ended at {totals[0]} and {totals[1]}");
    }

    [Fact]
    public void OneColumnKeepsTheOriginalOrder()
    {
        var assignment = StaticGrid.Assign([3, 1, 2], 1);
        Assert.Equal([0, 1, 2], assignment[0]);
    }

    [Fact]
    public void ATagsColourFollowsTheSharedHashAndNothingElse()
    {
        // Palette.ForTag must be a pure function of Core.Format.TagColorIndex,
        // which is pinned against the macOS build. If the colour depended on
        // anything else, the same tag would be a different colour on each
        // platform — and the index is the only thing crossing over.
        var tags = new[] { "prod", "db", "eu", "staging", "arm64", "", "生产", "web", "cache" };
        foreach (var left in tags)
        {
            foreach (var right in tags)
            {
                var same = Format.TagColorIndex(left, 8) == Format.TagColorIndex(right, 8);
                Assert.Equal(same, Palette.ForTag(left) == Palette.ForTag(right));
            }
        }
    }

    [Fact]
    public void TagColoursAreLiftedForDarkMode()
    {
        // The saturated light-mode values read as muddy on a dark background,
        // so they are brightened — and the brightening must actually happen.
        Palette.Use(dark: false);
        var light = Palette.ForTag("prod");
        Palette.Use(dark: true);
        var dark = Palette.ForTag("prod");
        Assert.True(
            dark.R >= light.R && dark.G >= light.G && dark.B >= light.B && dark != light,
            $"{light} was not lifted to {dark}");
    }
}
