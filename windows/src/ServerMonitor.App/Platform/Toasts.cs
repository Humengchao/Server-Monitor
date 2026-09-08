using Microsoft.Toolkit.Uwp.Notifications;

namespace ServerMonitor.App.Platform;

/// <summary>
/// Windows toast notifications for alerts.
/// </summary>
/// <remarks>
/// The plan asks for <c>ToastNotificationManagerCompat</c> (§D: "Windows
/// Toast, CommunityToolkit's ToastNotificationManagerCompat, mature for
/// unpackaged apps; register a Start Menu shortcut on first run; clicking
/// activates the host"). What was built instead was
/// <c>NotifyIcon.ShowBalloonTip</c>, and on Windows 11 that shows
/// <em>nothing at all</em> — no toast, no error, no entry under Settings ›
/// Notifications, because the balloon API is routed through the toast system
/// and an unpackaged app with no registered AUMID has nowhere to route it.
///
/// So every threshold and every offline alert this app raised was discarded
/// in silence. It was invisible from the outside twice over: alerts were not
/// logged either, and nothing in the app disagreed with "there was nothing
/// to report". Found against real hosts by setting a threshold the machines
/// were actually breaching and watching the screen at the moment the log
/// said an alert had fired.
///
/// The compat layer does the registration itself on first use — AUMID in
/// HKCU, a Start Menu shortcut, and a COM activator so a click reaches the
/// running process rather than starting a second one.
/// </remarks>
internal static class Toasts
{
    /// <summary>The argument a click carries back, so it can open the host.</summary>
    private const string ServerArgument = "server";

    /// <summary>
    /// Whether toasts are usable. False on a machine where registration
    /// fails, in which case the caller falls back to the tray balloon.
    /// </summary>
    /// <remarks>
    /// Resolved once, and never allowed to throw out of here: a notification
    /// that cannot be delivered must not take down the poll that produced it.
    /// </remarks>
    public static bool Available { get; private set; } = true;

    /// <summary>
    /// Routes clicks to <paramref name="openServer"/>.
    /// </summary>
    /// <remarks>
    /// Called once at startup. The handler runs on a thread-pool thread, so
    /// the caller marshals to the UI itself.
    /// </remarks>
    public static void OnClicked(Action<Guid> openServer)
    {
        try
        {
            ToastNotificationManagerCompat.OnActivated += activation =>
            {
                var arguments = ToastArguments.Parse(activation.Argument);
                if (!arguments.TryGetValue(ServerArgument, out var raw)) return;
                if (Guid.TryParse(raw, out var serverId)) openServer(serverId);
            };
        }
        catch (Exception)
        {
            // No COM activator on this machine. Toasts may still appear; they
            // just will not navigate, which is better than refusing to alert.
        }
    }

    /// <summary>
    /// Shows one alert. False when it could not be delivered.
    /// </summary>
    public static bool Show(string title, string body, Guid serverId)
    {
        if (!Available) return false;
        try
        {
            new ToastContentBuilder()
                .AddArgument(ServerArgument, serverId.ToString())
                .AddText(title)
                .AddText(body)
                .Show();
            return true;
        }
        catch (Exception)
        {
            // Registration refused, or the notification platform is
            // unavailable — a Server SKU without the shell, a locked-down
            // policy. Remembered so every later alert goes straight to the
            // fallback instead of throwing again per poll.
            Available = false;
            return false;
        }
    }

    /// <summary>
    /// Clears this app's notifications on the way out.
    /// </summary>
    /// <remarks>
    /// Otherwise yesterday's "web-01 offline" sits in the action centre after
    /// the host has been fine for a day.
    /// </remarks>
    public static void Clear()
    {
        try
        {
            ToastNotificationManagerCompat.History.Clear();
        }
        catch (Exception)
        {
            // Nothing to clear, or no platform to clear it on.
        }
    }
}
