using System.Linq;
using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.App.Views;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// The app's own message box, which replaced MessageBox.Show.
/// </summary>
/// <remarks>
/// Two things were wrong with the native box, and both are visible the moment
/// you look at one. It ignores the app's theme — Windows' message box stays
/// light whatever the app does, so in dark mode every confirmation and every
/// error was a white rectangle over a dark window. And Windows labels its
/// buttons in the *system* language, so an app set to English on a Chinese
/// Windows showed an English sentence over a 确定 button.
///
/// These assert the parts a screenshot cannot: which buttons exist, what they
/// are called in each language, and which one answers yes.
/// </remarks>
public class MessageWindowTests
{
    private static FrameworkElement Build(
        MessageKind kind,
        string message,
        out Button primary,
        Action<bool>? finish = null,
        string? confirmLabel = null)
    {
        Button? found = null;
        var content = UiThread.Run(() => MessageWindow.BuildContent(
            kind, message, confirmLabel, finish ?? (_ => { }), out found));
        primary = found!;
        return content;
    }

    [Fact]
    public void AnInfoDialogHasOneButton()
    {
        var content = Build(MessageKind.Info, "Something happened.", out _);
        var buttons = UiThread.Run(() => Buttons(content));
        var only = Assert.Single(buttons);
        Assert.Equal(Strings.Get("common.close"), UiThread.Run(() => only.Content));
    }

    [Fact]
    public void AConfirmDialogOffersBothAnswers()
    {
        var content = Build(MessageKind.Confirm, "Delete web-01?", out var primary);
        var buttons = UiThread.Run(() => Buttons(content));
        Assert.Equal(2, buttons.Count);

        // Cancel first, the primary last — the Windows order, so muscle memory
        // does not delete something.
        Assert.Equal(Strings.Get("common.cancel"), UiThread.Run(() => buttons[0].Content));
        Assert.Same(primary, buttons[1]);
        Assert.True(UiThread.Run(() => buttons[0].IsCancel), "Escape does not cancel");
    }

    [Fact]
    public void OnlyThePrimaryButtonMeansYes()
    {
        var answers = new List<bool>();
        var content = Build(MessageKind.Confirm, "Delete web-01?", out _, answers.Add);
        var buttons = UiThread.Run(() => Buttons(content));

        UiThread.Run(() => buttons[0].RaiseEvent(
            new RoutedEventArgs(System.Windows.Controls.Primitives.ButtonBase.ClickEvent)));
        UiThread.Run(() => buttons[1].RaiseEvent(
            new RoutedEventArgs(System.Windows.Controls.Primitives.ButtonBase.ClickEvent)));

        Assert.Equal([false, true], answers);
    }

    [Fact]
    public void TheButtonsAreInTheAppsLanguageNotTheSystems()
    {
        // The whole point of replacing the native box. A language setting that
        // the buttons ignore is a language setting that is half applied.
        using (new LanguageScope(AppLanguage.En))
        {
            Build(MessageKind.Confirm, "x", out var english);
            Assert.Equal("OK", UiThread.Run(() => english.Content));
            Build(MessageKind.Info, "x", out var close);
            Assert.Equal("Close", UiThread.Run(() => close.Content));
        }

        using (new LanguageScope(AppLanguage.Zh))
        {
            Build(MessageKind.Confirm, "x", out var chinese);
            Assert.Equal("确定", UiThread.Run(() => chinese.Content));
            Build(MessageKind.Info, "x", out var close);
            Assert.Equal("关闭", UiThread.Run(() => close.Content));
        }
    }

    [Fact]
    public void ANamedActionReplacesOk()
    {
        // "Delete" says what the button does; "OK" makes the reader work it
        // out from the sentence above. macOS labels these dialogs the same
        // way, and the editor's delete is the first caller.
        Build(MessageKind.Confirm, "Delete web-01?", out var primary,
            confirmLabel: Strings.Get("common.delete"));
        Assert.Equal(Strings.Get("common.delete"), UiThread.Run(() => primary.Content));
    }

    [Fact]
    public void AnErrorReadsAsAnError()
    {
        // The message itself carries the colour, because the native box's icon
        // is the thing being replaced and a red sentence is what is left.
        var content = Build(MessageKind.Error, "It went wrong.", out _);
        var colour = UiThread.Run(() =>
            ((System.Windows.Media.SolidColorBrush)FirstText(content).Foreground).Color);
        Assert.Equal(Palette.Offline, colour);
    }

    [Fact]
    public void ADialogPaintsInTheThemesColours()
    {
        // The failure that started this: a dialog that does not follow the
        // theme. Rendered rather than inspected, because "it is dark" is a
        // property of the pixels.
        var content = Build(MessageKind.Info, "Something happened, at some length.", out _);
        var frame = UiThread.Render(content, 460, 130);
        frame.Save("dialog-info");

        Assert.True(frame.Painted > 1500, $"only {frame.Painted} pixels painted");
        // The accent button is the one thing that must stand out.
        Assert.True(frame.Contains(Palette.Accent, tolerance: 30), "no accent button drawn");
        Assert.True(frame.Contains(Palette.Text, tolerance: 40), "the message is not drawn");
    }

    /// <summary>
    /// Switches the UI language for one block.
    /// </summary>
    /// <remarks>
    /// The Core tests have their own; it is internal to that assembly. Safe
    /// to set process-wide here because this assembly runs serially — see
    /// AssemblyInfo.cs.
    /// </remarks>
    private sealed class LanguageScope : IDisposable
    {
        private readonly AppLanguage _previous = Strings.Language;

        public LanguageScope(AppLanguage language) => Strings.Language = language;

        public void Dispose() => Strings.Language = _previous;
    }

    private static List<Button> Buttons(FrameworkElement content) =>
        Descendants(content).OfType<Button>().ToList();

    private static TextBlock FirstText(FrameworkElement content) =>
        Descendants(content).OfType<TextBlock>().First();

    private static IEnumerable<FrameworkElement> Descendants(FrameworkElement root)
    {
        yield return root;
        if (root is not Panel panel) yield break;
        foreach (var child in panel.Children.OfType<FrameworkElement>())
        {
            foreach (var found in Descendants(child)) yield return found;
        }
    }
}
