using System.Windows;
using System.Windows.Media;

namespace ServerMonitor.App.Theme;

/// <summary>
/// Writes the themed brushes into a resource dictionary.
/// </summary>
/// <remarks>
/// Separate from <c>App.ApplyTheme</c> so the render tests can theme a bare
/// <see cref="ResourceDictionary"/> without an <c>App</c>: the styles in
/// <c>Styles.xaml</c> reference these keys as <c>DynamicResource</c>, so a
/// control built against a dictionary missing them renders with WPF's
/// defaults and the test would be checking the wrong thing.
/// </remarks>
public static class ThemeBrushes
{
    public static void Apply(ResourceDictionary resources, bool dark)
    {
        Palette.Use(dark);
        Ink.Reset();

        void Set(string key, Color colour) => resources[key] = new SolidColorBrush(colour);

        Set("Brush.Background", Palette.Background);
        Set("Brush.Card", Palette.Card);
        Set("Brush.CardHover", Palette.CardHover);
        Set("Brush.Sidebar", Palette.Sidebar);
        Set("Brush.Border", Palette.Border);
        Set("Brush.Separator", Palette.Separator);
        Set("Brush.Text", Palette.Text);
        Set("Brush.Secondary", Palette.Secondary);
        Set("Brush.Tertiary", Palette.Tertiary);
        Set("Brush.Accent", Palette.Accent);
        Set("Brush.Online", Palette.Online);
        Set("Brush.Offline", Palette.Offline);
        Set("Brush.Warning", Palette.Warning);
        Set("Brush.Selection", Color.FromArgb(
            (byte)(dark ? 0x38 : 0x1A), Palette.Accent.R, Palette.Accent.G, Palette.Accent.B));
    }
}
