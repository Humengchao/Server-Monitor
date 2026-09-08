using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core.L10n;

namespace ServerMonitor.App.Views;

/// <summary>What a message box is for.</summary>
public enum MessageKind
{
    /// <summary>Something happened, and there is nothing to decide.</summary>
    Info,
    /// <summary>Something failed.</summary>
    Error,
    /// <summary>Something is about to happen unless the user says otherwise.</summary>
    Confirm,
}

/// <summary>
/// The app's own message box.
/// </summary>
/// <remarks>
/// Replaces <c>MessageBox.Show</c>, which is wrong here in two ways that are
/// obvious the moment you look at one.
///
/// It does not follow the app's theme. Windows' own message box stays light
/// whatever the app does, so in dark mode every confirmation and every error
/// in this app was a bright white rectangle over a dark window.
///
/// And its buttons are labelled by Windows, not by the app: with the app set
/// to English on a Chinese Windows, an English sentence came with a 确定
/// button. The language setting exists precisely so the app's language need
/// not match the system's, and the buttons are part of the app's language.
/// The macOS build has this for free, because SwiftUI resolves alert buttons
/// from the app's own localisation.
///
/// One deliberate exception stays on the native box: the last-resort handler
/// for an unhandled exception in App.OnStartup. That one must not depend on
/// the app's resource dictionaries or its own window machinery, because those
/// may be exactly what failed.
/// </remarks>
public sealed class MessageWindow : Window
{
    private bool _accepted;

    private MessageWindow(
        Window? owner, MessageKind kind, string message, string? title, string? confirmLabel)
    {
        Owner = owner;
        Title = title ?? (kind == MessageKind.Error
            ? Strings.Get("common.error")
            : Strings.Get("app.title"));
        SizeToContent = SizeToContent.Height;
        Width = 460;
        MinHeight = 150;
        ResizeMode = ResizeMode.NoResize;
        ShowInTaskbar = false;
        WindowStartupLocation = owner is null
            ? WindowStartupLocation.CenterScreen
            : WindowStartupLocation.CenterOwner;
        Background = Ink.Brush(Palette.Background);

        Content = BuildContent(kind, message, confirmLabel, accepted =>
        {
            _accepted = accepted;
            Close();
        }, out var primary);
        // Enter and Escape, because a dialog nobody can dismiss from the
        // keyboard is worse than no dialog.
        primary.IsDefault = true;
        Loaded += (_, _) => primary.Focus();
        PreviewKeyDown += (_, e) =>
        {
            if (e.Key != Key.Escape) return;
            _accepted = false;
            e.Handled = true;
            Close();
        };
    }

    /// <summary>
    /// The dialog's body, separately so it can be rendered in a test.
    /// </summary>
    /// <remarks>
    /// A Window cannot be rasterised offscreen the way the render tests do it,
    /// and the part worth asserting on — that it paints, in the theme's own
    /// colours, with the right buttons — is all in here. Static, and told how
    /// to finish rather than closing the window itself, so a test can build it
    /// without a window at all.
    /// </remarks>
    /// <param name="confirmLabel">
    /// What the confirming button says. A named action — "Delete" — is
    /// clearer than "OK" and is what the macOS build uses for the same
    /// dialogs; null falls back to OK.
    /// </param>
    internal static FrameworkElement BuildContent(
        MessageKind kind,
        string message,
        string? confirmLabel,
        Action<bool> finish,
        out Button primary)
    {
        var text = Ui.Wrapped(message, "Text.Body");
        text.Margin = new Thickness(0, 0, 0, 18);
        if (kind == MessageKind.Error) text.Foreground = Ink.Brush(Palette.Offline);

        var buttons = Ui.Columns(8);
        buttons.HorizontalAlignment = HorizontalAlignment.Right;

        if (kind == MessageKind.Confirm)
        {
            var cancel = Ui.Button(Strings.Get("common.cancel"), () => finish(false));
            cancel.IsCancel = true;
            buttons.Children.Add(cancel);

            // No shared key says "OK" — the table is asserted to match the
            // macOS one key for key — so the fallback is inline, like the
            // other Windows-only strings in the views.
            primary = Ui.Accent(
                confirmLabel ?? (Strings.IsChinese ? "确定" : "OK"), () => finish(true));
        }
        else
        {
            primary = Ui.Accent(Strings.Get("common.close"), () => finish(true));
        }
        buttons.Children.Add(primary);

        var body = Ui.Rows(0, text, buttons);
        body.Margin = new Thickness(20);
        return body;
    }

    /// <summary>Shows the dialog. True when the user accepted.</summary>
    private static bool Show(
        Window? owner, MessageKind kind, string message, string? title, string? confirmLabel = null)
    {
        owner ??= Application.Current?.MainWindow;
        // An owner that is not visible cannot own a modal window: WPF throws
        // rather than showing it. That happens whenever something reports a
        // failure while the window sits hidden in the notification area.
        if (owner is not null && !owner.IsVisible) owner = null;

        var dialog = new MessageWindow(owner, kind, message, title, confirmLabel);
        dialog.ShowDialog();
        return dialog._accepted;
    }

    public static void Inform(Window? owner, string message, string? title = null) =>
        Show(owner, MessageKind.Info, message, title);

    public static void Complain(Window? owner, string message) =>
        Show(owner, MessageKind.Error, message, null);

    public static bool Confirm(
        Window? owner, string message, string? title = null, string? confirmLabel = null) =>
        Show(owner, MessageKind.Confirm, message, title, confirmLabel);
}
