using System.Windows;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;

namespace ServerMonitor.App.Views;

/// <summary>
/// What an empty app offers: add a host, or adopt the ones ssh already knows.
/// </summary>
/// <remarks>
/// One place because three screens show it. The dashboard and the machines
/// page each had their own copy — near-identical, and already diverging: one
/// wrapped the config parse in a try and the other did it in a helper, so a
/// broken ~/.ssh/config would have been survivable on one screen and not the
/// other. The container page is the third caller, and was the reason to stop
/// copying it.
///
/// Importing from <c>%USERPROFILE%\.ssh\config</c> used to live only in the
/// machines toolbar, which is drawn once there is at least one host — so the
/// one person who most wants it, somebody with a config full of hosts and an
/// empty app, could not find it. When the file has hosts in it the button says
/// how many, because "import" alone does not tell you whether it will find
/// anything.
/// </remarks>
internal static class FirstRun
{
    /// <summary>
    /// The action row, centred.
    /// </summary>
    /// <param name="owner">Parent for the dialogs, so they centre correctly.</param>
    /// <param name="afterChange">
    /// Called when a dialog saved something, so the calling page can rebuild.
    /// </param>
    public static UIElement Actions(Window? owner, Action afterChange)
    {
        var row = Ui.Columns(8, Ui.Accent(Strings.Get("server.add"), () =>
        {
            // Both open a window rather than navigating: routing through
            // navigation would mean threading callbacks into pages that
            // already have the one they need.
            var editor = new ServerEditorWindow(null) { Owner = owner };
            if (editor.ShowDialog() == true) afterChange();
        }));

        var discovered = Discoverable();
        if (discovered > 0)
        {
            row.Children.Add(Ui.Button(
                $"{Strings.Get("import.title")} ({discovered})",
                () =>
                {
                    var window = new ImportSshConfigWindow { Owner = owner };
                    if (window.ShowDialog() == true) afterChange();
                }));
        }

        row.HorizontalAlignment = HorizontalAlignment.Center;
        return row;
    }

    /// <summary>
    /// How many hosts <c>%USERPROFILE%\.ssh\config</c> offers, or zero if it
    /// cannot be read.
    /// </summary>
    /// <remarks>
    /// Parsed on each call rather than cached: the file is small, this runs
    /// once per rebuild of an empty page, and a stale count on a first-run
    /// screen would be worse than no count.
    /// </remarks>
    private static int Discoverable()
    {
        try
        {
            return Core.Ssh.SshConfig.FromDefaultLocation().Discover().Count;
        }
        catch (Exception)
        {
            // No config, or an unreadable one. Nothing to offer.
            return 0;
        }
    }
}
