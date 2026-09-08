using System.Windows;
using System.Windows.Automation;
using System.Windows.Automation.Peers;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// The sidebar's group labels, as a screen reader sees them.
/// </summary>
/// <remarks>
/// "Resources" and "Toolbox" are items of the nav ListBox because the sidebar
/// is one list, and they are disabled so they cannot become the current page.
/// That left them announced as two disabled entries among the nine real ones.
/// A heading level is what distinguishes them; this asserts the annotation is
/// actually on the item and not on some wrapper that a ListBox discards.
/// </remarks>
public class NavHeaderTests
{
    [Fact]
    public void AGroupLabelIsAHeading()
    {
        var header = UiThread.Run(() => new NavHeader { Content = "Resources" });
        Assert.Equal(
            AutomationHeadingLevel.Level2,
            UiThread.Run(() => AutomationProperties.GetHeadingLevel(header)));
    }

    [Fact]
    public void APageItemIsNotAHeading()
    {
        // The contrast is the whole point: if everything in the list were a
        // heading, heading navigation would land on all nine.
        var item = UiThread.Run(() => new ListBoxItem { Content = "Machines" });
        Assert.Equal(
            AutomationHeadingLevel.None,
            UiThread.Run(() => AutomationProperties.GetHeadingLevel(item)));
    }

    [Fact]
    public void AGroupLabelSurvivesBeingPutInTheList()
    {
        // The mechanism that failed first time round was an
        // OnCreateAutomationPeer override, because a ListBox builds its own
        // item peers and never consults the container's. HeadingLevel is a
        // property on the element, so it comes through the wrapper — asserted
        // here through the ListBox's peer rather than off the bare item.
        var (headingOfLabel, headingOfPage) = UiThread.Run(() =>
        {
            var list = new ListBox();
            var header = new NavHeader { Content = "Resources", IsEnabled = false };
            var page = new ListBoxItem { Content = "Machines" };
            list.Items.Add(header);
            list.Items.Add(page);
            // Arrange so the containers are generated and the peer has children.
            list.Measure(new Size(200, 400));
            list.Arrange(new Rect(0, 0, 200, 400));
            list.UpdateLayout();

            var peer = UIElementAutomationPeer.CreatePeerForElement(list);
            var children = peer?.GetChildren();
            Assert.NotNull(children);
            Assert.Equal(2, children!.Count);
            return (children[0].GetHeadingLevel(), children[1].GetHeadingLevel());
        });

        Assert.Equal(AutomationHeadingLevel.Level2, headingOfLabel);
        Assert.Equal(AutomationHeadingLevel.None, headingOfPage);
    }
}
