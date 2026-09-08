using System.Windows;
using System.Windows.Automation;
using System.Windows.Automation.Peers;
using System.Windows.Automation.Provider;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Media;
using ServerMonitor.App.Theme;

namespace ServerMonitor.App.Controls;

/// <summary>
/// A card that behaves like a button.
/// </summary>
/// <remarks>
/// The dashboard's server cards were a <see cref="Border"/> with a
/// <c>MouseLeftButtonUp</c> handler, which works for a mouse and for nothing
/// else: no tab stop, no Enter, no focus ring, and a screen reader announcing
/// a pane with some text in it rather than something you can activate. On a
/// screen whose whole purpose is "click the host that went red", that is the
/// one control that has to be reachable without a mouse.
///
/// A real <see cref="Button"/> with a template would also do it, but its
/// content model fights the card: the hover and focus visuals here repaint the
/// same brushes the card already owns, and a templated button would need those
/// brushes threaded through a template that exists for one control.
/// </remarks>
internal sealed class CardButton : Border
{
    private readonly Action _onClick;
    private bool _pressedHere;

    public CardButton(Action onClick, string? name)
    {
        _onClick = onClick;

        Cursor = Cursors.Hand;
        Focusable = true;
        if (name is { Length: > 0 })
        {
            AutomationProperties.SetName(this, name);
        }

        MouseEnter += (_, _) => Paint(hot: true);
        MouseLeave += (_, _) => Paint(hot: IsKeyboardFocused);
        GotKeyboardFocus += (_, _) => Paint(hot: true);
        LostKeyboardFocus += (_, _) => Paint(hot: IsMouseOver);

        // Press and release both have to land here. A drag that starts on one
        // card and ends on another used to activate the second one.
        MouseLeftButtonDown += (_, _) =>
        {
            _pressedHere = true;
            Focus();
        };
        MouseLeftButtonUp += (_, _) =>
        {
            if (!_pressedHere) return;
            _pressedHere = false;
            Activate();
        };
        MouseLeave += (_, _) => _pressedHere = false;
    }

    private void Paint(bool hot)
    {
        Background = (Brush)Application.Current.FindResource(hot ? "Brush.CardHover" : "Brush.Card");
        BorderBrush = hot
            ? Ink.Brush(Palette.Accent)
            : (Brush)Application.Current.FindResource("Brush.Border");
    }

    /// <summary>Runs the card's action, from wherever it was asked for.</summary>
    public void Activate() => _onClick();

    protected override void OnKeyDown(KeyEventArgs e)
    {
        // Space and Enter, which is what every other button on Windows does.
        if (e.Key is Key.Enter or Key.Space)
        {
            e.Handled = true;
            Activate();
            return;
        }
        base.OnKeyDown(e);
    }

    /// <summary>
    /// Reports as a button to assistive technology, and can be invoked by it.
    /// </summary>
    protected override AutomationPeer OnCreateAutomationPeer() => new CardPeer(this);

    private sealed class CardPeer(CardButton owner) : FrameworkElementAutomationPeer(owner), IInvokeProvider
    {
        protected override AutomationControlType GetAutomationControlTypeCore() =>
            AutomationControlType.Button;

        protected override string GetClassNameCore() => nameof(CardButton);

        protected override bool IsContentElementCore() => true;

        protected override bool IsControlElementCore() => true;

        public override object? GetPattern(PatternInterface patternInterface) =>
            patternInterface == PatternInterface.Invoke ? this : base.GetPattern(patternInterface);

        /// <summary>What a screen reader's "activate" does.</summary>
        public void Invoke() => owner.Dispatcher.Invoke(owner.Activate);
    }
}
