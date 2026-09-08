using System.Linq;
using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// A table column that runs out of room.
/// </summary>
/// <remarks>
/// GridViewColumn.DisplayMemberBinding builds a bare TextBlock, which clips.
/// A 58-character host name in a 170px column therefore stopped
/// mid-character with no ellipsis and no way to read the rest — it looked
/// like a drawing fault rather than like truncation. Found by putting an
/// awkward name in the store and looking at the machines table, which is the
/// only way this shows up: every fixture until then had been six characters
/// long.
/// </remarks>
public class TableColumnTests
{
    private sealed record Row(string Name);

    [Fact]
    public void ACellTrimsWithAnEllipsis()
    {
        var text = Realise(
            () => Ui.TextColumn("Name", nameof(Row.Name), 60),
            new Row("kubernetes-worker-node-eu-central-1b-production-cluster-07"),
            60);

        Assert.Equal(
            TextTrimming.CharacterEllipsis,
            UiThread.Run(() => text.TextTrimming));
    }

    [Fact]
    public void ACellKeepsTheThemesTextStyle()
    {
        // Templating the cell replaces the implicit TextBlock style, so the
        // Table.Cell style has to be based on Text.Body or the table would
        // silently lose the app's font and ink.
        var text = Realise(
            () => Ui.TextColumn("Name", nameof(Row.Name), 200), new Row("web-01"), 200);

        // Compared against an ordinary body TextBlock rather than against the
        // style's setters: the theme holds its sizes as DynamicResource, so a
        // setter's Value is the extension and not a number.
        var (cell, body) = UiThread.Run(() =>
        {
            var plain = Ui.Text("web-01");
            plain.Measure(new Size(200, 40));
            return (text.FontSize, plain.FontSize);
        });
        Assert.Equal(body, cell);
        Assert.True(cell > 0, "the cell has no resolved font size");
    }

    [Fact]
    public void OnlyTheColumnsThatAskForItCarryATooltip()
    {
        // A tooltip repeating "Linux" over a cell reading "Linux" is noise;
        // one over a name that is cut in half is the only way to read it.
        var plain = Realise(
            () => Ui.TextColumn("OS", nameof(Row.Name), 80), new Row("Linux"), 80);
        Assert.Null(UiThread.Run(() => plain.ToolTip));

        var full = "kubernetes-worker-node-eu-central-1b-production-cluster-07";
        var named = Realise(
            () => Ui.TextColumn("Name", nameof(Row.Name), 80, tooltip: true), new Row(full), 80);
        Assert.Equal(full, UiThread.Run(() => named.ToolTip));
    }

    /// <summary>
    /// Builds the column's cell for one row and lays it out.
    /// </summary>
    /// <remarks>
    /// The template's bindings only resolve against a real DataContext inside
    /// a loaded tree, so this puts the cell in a ContentControl and arranges
    /// it — otherwise Text and ToolTip both read as null and the assertions
    /// would pass for the wrong reason.
    ///
    /// The column is built inside the same UI-thread call rather than passed
    /// in: a GridViewColumn is a DependencyObject, so one created on the test
    /// thread cannot have its CellTemplate read from the dispatcher.
    /// </remarks>
    private static TextBlock Realise(Func<GridViewColumn> makeColumn, Row row, double width) =>
        UiThread.Run(() =>
        {
            var host = new ContentControl
            {
                ContentTemplate = makeColumn().CellTemplate,
                Content = row,
                Width = width,
            };
            host.Measure(new Size(width, 40));
            host.Arrange(new Rect(0, 0, width, 40));
            host.UpdateLayout();
            host.ApplyTemplate();

            var found = Descendants(host).OfType<TextBlock>().FirstOrDefault();
            Assert.NotNull(found);
            return found!;
        });

    private static IEnumerable<DependencyObject> Descendants(DependencyObject root)
    {
        yield return root;
        var count = System.Windows.Media.VisualTreeHelper.GetChildrenCount(root);
        for (var i = 0; i < count; i++)
        {
            var child = System.Windows.Media.VisualTreeHelper.GetChild(root, i);
            foreach (var found in Descendants(child)) yield return found;
        }
    }
}
