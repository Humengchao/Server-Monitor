namespace ServerMonitor.App.Views;

/// <summary>What closing the main window should do.</summary>
public enum CloseAction
{
    /// <summary>Cancel the close, hide the window, keep collecting.</summary>
    HideToTray,
    /// <summary>Let the window close, and end the process with it.</summary>
    QuitApp,
}

/// <summary>
/// The close decision, pulled out of the window so it can be asserted.
/// </summary>
/// <remarks>
/// Three lines that were wrong in a way nothing could see. The app runs with
/// ShutdownMode="OnExplicitShutdown", because a tray app must survive its own
/// window being closed — so <c>base.OnClosing</c> destroys the window and
/// leaves the process running. With "keep running in the notification area"
/// switched off, that produced an app with no window at all: still polling,
/// reachable only through the tray icon, and the tray's own "show window"
/// then threw, because a closed WPF Window cannot be shown again.
///
/// Nobody had seen it because the setting was unreachable: settings never
/// persisted, and CloseToTray defaults to true, so the false branch had never
/// run outside a session where someone unticked the box by hand.
///
/// There is deliberately no third case for "a quit is already under way".
/// That was the first attempt — on the assumption that Application.Shutdown
/// would be blocked by this handler cancelling the close it asks for — and
/// measuring showed Shutdown closes its windows ignoring Cancel, so the case
/// never arises. Re-entry is handled where it actually happens, by a guard in
/// QuitApp.
/// </remarks>
internal static class CloseBehaviour
{
    public static CloseAction For(bool closeToTray) =>
        closeToTray ? CloseAction.HideToTray : CloseAction.QuitApp;
}
