using System.Windows;
using System.Windows.Controls;

namespace ServerMonitor.App.Controls;

/// <summary>
/// A <see cref="StackPanel"/> that puts a gap between its children.
/// </summary>
/// <remarks>
/// WPF's StackPanel has no Spacing — that is a WinUI property — so the gap has
/// to be a margin on each child. Doing it here rather than in the helper that
/// builds the panel is the point: most of the pages create a row empty and
/// then add to it (a badge that only some hosts get, a button that depends on
/// the state), and children added that way used to come out with no gap at
/// all, which is how a country code ended up glued to a hostname.
///
/// A margin the caller set itself is left alone, so a child that wants to sit
/// closer or further still can.
/// </remarks>
internal sealed class SpacedStack : StackPanel
{
    public double Spacing { get; set; }

    protected override Size MeasureOverride(Size constraint)
    {
        if (Spacing > 0) ApplySpacing();
        return base.MeasureOverride(constraint);
    }

    private void ApplySpacing()
    {
        var gap = Orientation == Orientation.Vertical
            ? new Thickness(0, Spacing, 0, 0)
            : new Thickness(Spacing, 0, 0, 0);

        var first = true;
        foreach (var child in InternalChildren)
        {
            if (child is not FrameworkElement element) continue;
            var wanted = first ? default : gap;
            first = false;
            if (element.Margin == wanted) continue;
            // Only ours to change: anything else was set deliberately.
            if (element.Margin != default && element.Margin != gap) continue;
            element.Margin = wanted;
        }
    }
}
