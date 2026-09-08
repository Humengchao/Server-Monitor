using System.Windows;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// Every resource key the views ask for by name.
/// </summary>
/// <remarks>
/// The views look styles and brushes up as strings, so a key that was renamed
/// or never written compiles and then throws
/// <c>ResourceReferenceKeyNotFoundException</c> the first time that page is
/// opened — which is how <c>Text.Secondary</c> was missed until the app was
/// launched by hand.
///
/// The list is the set of literals the App project passes to
/// <c>FindResource</c> or to a <c>style</c> parameter. It is not derived
/// automatically: adding a new key here when you add one to a view is the
/// price of catching the next one in a second rather than on a page nobody
/// opens until after release.
/// </remarks>
public class ResourceTests
{
    private static readonly string[] Keys =
    [
        "Brush.Accent", "Brush.Background", "Brush.Border", "Brush.Card",
        "Brush.CardHover", "Brush.Offline", "Brush.Online", "Brush.Secondary",
        "Brush.Selection", "Brush.Separator", "Brush.Sidebar", "Brush.Tertiary",
        "Brush.Text", "Brush.Warning",

        "Button.Accent", "Button.Danger", "Button.Quiet", "Button.Standard",

        "Card", "ColumnHeader", "Row", "Table",
        "Input", "Input.Password", "Picker", "Picker.Item", "Toggle",
        "SidebarItem",

        "Font.Display", "Font.Mono", "Font.UI",
        "Size.Body", "Size.Caption", "Size.Headline", "Size.Title",

        "Text.Body", "Text.Caption", "Text.Control", "Text.Headline",
        "Text.Mono", "Text.Number", "Text.Secondary", "Text.Tertiary",
        "Text.Title",
    ];

    [Fact]
    public void EveryKeyTheViewsAskForResolves()
    {
        var missing = UiThread.Run(() => Keys
            .Where(key => Application.Current.TryFindResource(key) is null)
            .ToList());
        Assert.Empty(missing);
    }

    [Fact]
    public void EveryThemedBrushIsRewrittenOnAThemeChange()
    {
        // ThemeBrushes.Apply writes the palette into these keys; one it does
        // not write keeps the light value baked into Styles.xaml and stays
        // light forever in dark mode.
        var stale = UiThread.Run(() =>
        {
            var resources = Application.Current.Resources;
            var light = Keys.Where(k => k.StartsWith("Brush.", StringComparison.Ordinal))
                .ToDictionary(k => k, k => resources[k]?.ToString());
            ServerMonitor.App.Theme.ThemeBrushes.Apply(resources, dark: false);
            var afterLight = Keys.Where(k => k.StartsWith("Brush.", StringComparison.Ordinal))
                .ToDictionary(k => k, k => resources[k]?.ToString());
            ServerMonitor.App.Theme.ThemeBrushes.Apply(resources, dark: true);
            var afterDark = Keys.Where(k => k.StartsWith("Brush.", StringComparison.Ordinal))
                .ToDictionary(k => k, k => resources[k]?.ToString());
            _ = light;
            return afterLight.Where(pair => afterDark[pair.Key] == pair.Value)
                .Select(pair => pair.Key)
                .ToList();
        });

        // Brush.Unknown aside, no brush is meant to be the same colour in both
        // themes — one that is has been left out of ThemeBrushes.Apply.
        Assert.Empty(stale);
    }
}
