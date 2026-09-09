using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// What the session dock needs from whatever it is hosting.
/// </summary>
/// <remarks>
/// Two kinds of pane live in the dock and they have almost nothing in common
/// — one is an HwndHost wrapping a shell, the other an ordinary WPF file
/// browser. This is the whole of what the dock knows about either: what to
/// write on the tab, which host it belongs to, and how to shut it down.
///
/// The macOS build's SessionHost switches on a `kind` enum instead. An
/// interface is the same idea in a language that has them, and it keeps the
/// dock from acquiring a reason to know what a terminal is.
/// </remarks>
internal interface ISessionPane
{
    /// <summary>The tab's text.</summary>
    string Label { get; }

    /// <summary>
    /// How a tab is named.
    /// </summary>
    /// <remarks>
    /// Here rather than in each pane so the two cannot drift, and static so a
    /// test can ask what a tab would say without building the pane — a
    /// terminal pane reaches for the app's settings the moment it is
    /// constructed, which is not something a unit test has.
    /// </remarks>
    /// <param name="title">
    /// The container list's own label for an exec session. "Terminal · web-01"
    /// on three container shells at once names none of them.
    /// </param>
    internal static string LabelFor(SessionKind kind, string serverName, string? title = null) =>
        title is { Length: > 0 }
            ? title
            : $"{Strings.Get(kind == SessionKind.Terminal ? "nav.terminal" : "nav.sftp")} · {serverName}";

    /// <summary>Which host, so a second request for it can find this pane.</summary>
    Guid ServerId { get; }

    /// <summary>
    /// Disconnects and closes the session history row.
    /// </summary>
    /// <remarks>
    /// Called by the dock when the tab is closed or the app is shutting down.
    /// A window used to do this in <c>Closed</c>; a pane is never closed on
    /// its own, so it has to be told.
    /// </remarks>
    void CloseSession();
}
