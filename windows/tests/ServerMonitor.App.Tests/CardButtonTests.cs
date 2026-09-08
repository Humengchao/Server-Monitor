using System.Windows;
using System.Windows.Automation.Peers;
using System.Windows.Automation.Provider;
using System.Windows.Input;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// The dashboard's server card, as something other than a mouse target.
/// </summary>
/// <remarks>
/// It used to be a Border with a MouseLeftButtonUp handler, which is fine for
/// a mouse and nothing else: no tab stop, no Enter, no focus ring, and a
/// screen reader announcing a pane. On the one screen whose purpose is "open
/// the host that went red", that has to work without a mouse — so these check
/// the parts that are invisible until someone needs them.
/// </remarks>
public class CardButtonTests
{
    [Fact]
    public void ACardIsATabStop()
    {
        // Every property read goes through the UI thread too: these objects
        // have thread affinity, and reading one from the test thread throws.
        var focusable = UiThread.Run(() =>
            Ui.ClickableCard(Ui.Text("web-01"), () => { }, name: "web-01").Focusable);
        Assert.True(focusable, "the card cannot be reached by keyboard");
    }

    [Fact]
    public void ACardCarriesItsNameForAScreenReader()
    {
        var card = UiThread.Run(() =>
            Ui.ClickableCard(Ui.Text("web-01"), () => { }, name: "web-01 — offline"));
        Assert.Equal(
            "web-01 — offline",
            UiThread.Run(() => System.Windows.Automation.AutomationProperties.GetName(card)));
    }

    [Fact]
    public void ACardReportsAsAButtonAndCanBeInvoked()
    {
        var clicks = 0;
        var card = UiThread.Run(() => Ui.ClickableCard(Ui.Text("web-01"), () => clicks++, name: "web-01"));

        var peer = UiThread.Run(() => UIElementAutomationPeer.CreatePeerForElement(card));
        Assert.NotNull(peer);
        Assert.Equal(AutomationControlType.Button, UiThread.Run(peer.GetAutomationControlType));

        // The pattern a screen reader uses to press a button.
        var invoke = UiThread.Run(() => peer.GetPattern(PatternInterface.Invoke)) as IInvokeProvider;
        Assert.NotNull(invoke);
        UiThread.Run(invoke.Invoke);
        Assert.Equal(1, clicks);
    }

    [Theory]
    [InlineData(Key.Enter)]
    [InlineData(Key.Space)]
    public void EnterAndSpaceActivateACard(Key key)
    {
        var clicks = 0;
        var card = UiThread.Run(() => Ui.ClickableCard(Ui.Text("web-01"), () => clicks++, name: "web-01"));

        UiThread.Run(() =>
        {
            // Raised on the element rather than typed at the window: there is
            // no focused window offscreen, and what is under test is the
            // handler, not WPF's input routing.
            var source = PresentationSource.FromVisual(card);
            var args = new KeyEventArgs(Keyboard.PrimaryDevice, source ?? Stub(), 0, key)
            {
                RoutedEvent = Keyboard.KeyDownEvent,
            };
            card.RaiseEvent(args);
        });

        Assert.Equal(1, clicks);
    }

    /// <summary>
    /// A presentation source for an element that is not in a window.
    /// </summary>
    /// <remarks>
    /// KeyEventArgs will not take null, and an offscreen visual has no source
    /// of its own — so a throwaway HwndSource stands in. It is never shown.
    /// </remarks>
    private static PresentationSource Stub() =>
        new System.Windows.Interop.HwndSource(new System.Windows.Interop.HwndSourceParameters("offscreen")
        {
            Width = 1,
            Height = 1,
        });

    [Fact]
    public void ACardRendersAndHighlightsWhenFocused()
    {
        var card = UiThread.Run(() => Ui.ClickableCard(
            Ui.Rows(6, Ui.Title("web-01"), Ui.Text("8 cores")), () => { }, name: "web-01"));
        var frame = UiThread.Render(card, 300, 90);
        frame.Save("card-button");
        Assert.True(frame.Painted > 2000, $"only {frame.Painted} pixels painted");

        // Focus repaints the border in the accent colour, which is the only
        // cue a keyboard user gets about where they are.
        UiThread.Run(() =>
        {
            card.RaiseEvent(new KeyboardFocusChangedEventArgs(
                Keyboard.PrimaryDevice, 0, null, card)
            {
                RoutedEvent = UIElement.GotKeyboardFocusEvent,
            });
        });
        var focused = UiThread.Render(card, 300, 90);
        focused.Save("card-button-focused");
        Assert.True(
            focused.Contains(Palette.Accent, tolerance: 30),
            "a focused card shows no accent border");
    }
}
