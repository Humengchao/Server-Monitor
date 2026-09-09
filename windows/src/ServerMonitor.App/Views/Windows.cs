using System.Windows;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;

namespace ServerMonitor.App.Views;

/// <summary>
/// Opening a terminal or a file browser, from anywhere that lists a host.
/// </summary>
/// <remarks>
/// One place because four screens offer it — the dashboard card's menu, the
/// machines table, the detail page and the container list — and because the
/// two of them share a precondition worth stating once: both need the session
/// itself, which only the library transport has (D3). A host pinned to
/// ssh.exe is told that here rather than failing later with a null reference.
///
/// That one place is also why moving both out of windows and into the session
/// dock changed nothing at the four call sites: the names and the arguments
/// are what they were, and only these two bodies know that a session is no
/// longer a window.
/// </remarks>
internal static class Windows
{
    public static void Terminal(Window? owner, Server server, string? command = null, string? title = null)
    {
        if (!CanReach(owner, server)) return;
        Open(server, SessionKind.Terminal, command, title);
    }

    public static void Files(Window? owner, Server server)
    {
        if (!CanReach(owner, server)) return;
        Open(server, SessionKind.Sftp);
    }

    /// <summary>
    /// Hands the request to the dock in the main window.
    /// </summary>
    /// <remarks>
    /// The window can be hidden in the notification area — closing it does
    /// that by default — and a session opened into a window nobody can see is
    /// worse than no session, so it is brought up first. In practice every
    /// caller is a screen inside that window, but the tray menu is one
    /// keystroke away from adding another.
    /// </remarks>
    private static void Open(Server server, SessionKind kind, string? command = null, string? title = null)
    {
        App.Current.ShowWindow();
        App.Current.Shell?.Dock.Open(server, kind, command, title);
    }

    /// <summary>
    /// Whether the interactive features are available for this host.
    /// </summary>
    /// <remarks>
    /// The ssh.exe transport runs one command per process; there is no session
    /// to open a shell channel or an SFTP subsystem on. Giving that host a
    /// terminal would mean a second, differently configured connection —
    /// exactly what R13's escape hatch exists to avoid — so the answer is a
    /// sentence saying which setting to change.
    /// </remarks>
    private static bool CanReach(Window? owner, Server server)
    {
        var settings = App.Current.Settings;
        var forced = settings.Transport == TransportKind.OpenSshExe
            || settings.ForceOpenSshExe.Contains(server.Id);
        if (!forced && App.Current.LibraryTransport is not null) return true;

        Ui.Inform(owner, Strings.IsChinese
            ? "终端与 SFTP 需要内置 SSH 传输：它们复用采集用的那条连接，而 ssh.exe 每次只能跑一条命令。请在设置里把传输方式改回「内置 SSH」，或在机器编辑器里取消这台主机的 ssh.exe 选项。"
            : "The terminal and SFTP need the built-in SSH transport: both ride on the connection the poll already holds, and ssh.exe runs one command per process. Switch the transport back to built-in SSH in Settings, or untick ssh.exe for this host in the machine editor.");
        return false;
    }
}
