using System.Windows.Automation;
using System.Windows.Controls;

namespace ServerMonitor.App.Controls;

/// <summary>
/// A group label in the sidebar — "Resources", "Toolbox".
/// </summary>
/// <remarks>
/// The sidebar is one ListBox, so its group labels have to be items too, and
/// they are disabled and unfocusable to keep them out of the keyboard path.
/// That is enough for a mouse and a keyboard and wrong for a screen reader:
/// the labels reported as two disabled, unselected entries in a list of nine,
/// as though the app had greyed out two of its own pages.
///
/// HeadingLevel is what fixes that. A screen reader reads it to announce a
/// heading and to offer heading-to-heading navigation, so the labels read as
/// headings over the items beneath them rather than as two of them.
///
/// The control type stays ListItem, and cannot be changed from here: a
/// ListBox does not use its containers' own peers for its children. It builds
/// ListBoxItemAutomationPeer wrappers from the items itself, so an
/// OnCreateAutomationPeer override on the item is simply never consulted —
/// verified against the live automation tree, where the override changed
/// nothing and HeadingLevel came through. Getting Group as the control type
/// would mean splitting the sidebar into one ListBox per section and keeping
/// selection coordinated across them, which is a lot of new failure modes for
/// a property most screen readers do not announce once a heading level is set.
/// </remarks>
internal sealed class NavHeader : ListBoxItem
{
    public NavHeader() =>
        AutomationProperties.SetHeadingLevel(this, AutomationHeadingLevel.Level2);
}
