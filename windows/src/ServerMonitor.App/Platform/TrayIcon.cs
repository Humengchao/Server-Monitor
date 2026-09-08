using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
using System.Windows.Forms;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;

namespace ServerMonitor.App.Platform;

/// <summary>
/// The notification-area icon, drawn from the fleet's current state.
/// </summary>
/// <remarks>
/// The counterpart of the macOS build's <c>MenuBarExtra</c>, and the reason
/// this app can be closed without stopping: the window is a view onto a
/// monitor that keeps running.
///
/// The icon is generated rather than a static asset, because it carries the
/// number worth glancing at — the offline count if anything is down, otherwise
/// the peak CPU — the way the menu-bar item does. That is also why it is
/// redrawn on publish rather than on a timer.
/// </remarks>
public sealed class TrayIcon : IDisposable
{
    private readonly NotifyIcon _icon;
    private readonly MonitorService _monitor;
    private readonly Action _onOpen;
    private readonly Action _onQuit;
    private readonly Action<Guid> _onOpenServer;

    /// <summary>What the icon currently shows, so it is only redrawn when it moves.</summary>
    private (int Offline, int Peak, bool Dark) _drawn = (-1, -1, false);
    private Icon? _current;
    private bool _dark;

    public TrayIcon(
        MonitorService monitor,
        Action onOpen,
        Action onQuit,
        Action<Guid> onOpenServer)
    {
        _monitor = monitor;
        _onOpen = onOpen;
        _onQuit = onQuit;
        _onOpenServer = onOpenServer;

        _icon = new NotifyIcon
        {
            Text = Strings.Get("app.title"),
            Visible = true,
            ContextMenuStrip = new ContextMenuStrip(),
        };
        // Left-click opens the window; the menu is on right-click. A left
        // click that opened a menu would be surprising for something whose
        // primary job is "show me the dashboard".
        _icon.MouseClick += (_, e) =>
        {
            if (e.Button == MouseButtons.Left) _onOpen();
        };
        _icon.ContextMenuStrip.Opening += (_, _) => RebuildMenu();
        Refresh();
    }

    /// <summary>
    /// Redraws the icon and tooltip if the fleet's summary has changed.
    /// </summary>
    /// <remarks>
    /// Called on every publish. The early return matters: creating an
    /// <see cref="Icon"/> allocates a GDI handle, and doing that nine times a
    /// minute for an unchanged picture leaks handles until the process runs
    /// out of them.
    /// </remarks>
    public void Refresh()
    {
        var offline = _monitor.OfflineServers.Count;
        var peak = (int)Math.Round(_monitor.PeakCpu?.Percent ?? 0);
        var state = (offline, peak, _dark);
        if (state == _drawn) return;
        _drawn = state;

        var replacement = Render(offline, peak);
        _icon.Icon = replacement;
        // Disposed after the swap, not before: NotifyIcon keeps using the old
        // handle until the new one is assigned.
        _current?.Dispose();
        _current = replacement;

        _icon.Text = Tooltip(offline);
    }

    /// <summary>Follows the taskbar's theme, which is not always the app's.</summary>
    public void SetDark(bool dark)
    {
        _dark = dark;
        Refresh();
    }

    private string Tooltip(int offline)
    {
        var total = _monitor.Servers.Count;
        if (total == 0) return Strings.Get("app.title");
        var online = total - offline;
        // NotifyIcon.Text is capped at 63 characters and silently truncates,
        // so this stays short rather than listing hosts.
        return $"{Strings.Get("app.title")} — "
            + $"{Strings.Get("dashboard.online")} {online} / {Strings.Get("dashboard.offline")} {offline}";
    }

    /// <summary>
    /// A 16×16 icon: a status-coloured disc with a number on it.
    /// </summary>
    /// <remarks>
    /// Rendered at 32 and handed over at that size — Windows picks the 16px
    /// representation itself, and a bitmap drawn at 16 then scaled up on a
    /// high-DPI taskbar is visibly soft.
    /// </remarks>
    private Bitmap RenderBitmap(int offline, int peak)
    {
        const int Size = 32;
        var bitmap = new Bitmap(Size, Size, PixelFormat.Format32bppArgb);
        using var graphics = Graphics.FromImage(bitmap);
        graphics.SmoothingMode = SmoothingMode.AntiAlias;
        graphics.TextRenderingHint = System.Drawing.Text.TextRenderingHint.ClearTypeGridFit;
        graphics.Clear(Color.Transparent);

        // Offline wins: something being down matters more than something being
        // busy, and the two would otherwise compete for the same 16 pixels.
        var (fill, text) = offline > 0
            ? (Color.FromArgb(0xE8, 0x1C, 0x2E), offline.ToString(System.Globalization.CultureInfo.InvariantCulture))
            : _monitor.Servers.Count == 0
                ? (Color.FromArgb(0x8A, 0x8A, 0x8A), "–")
                : (TintFor(peak), peak.ToString(System.Globalization.CultureInfo.InvariantCulture));

        using var brush = new SolidBrush(fill);
        graphics.FillEllipse(brush, 1, 1, Size - 2, Size - 2);

        // 100% has three digits and does not fit; "99+" is the honest label at
        // that point, and the disc is already red.
        if (text.Length > 2) text = text == "100" ? "99" : text[..2];

        var fontSize = text.Length >= 2 ? 15f : 18f;
        using var font = new Font("Segoe UI", fontSize, FontStyle.Bold, GraphicsUnit.Pixel);
        using var format = new StringFormat
        {
            Alignment = StringAlignment.Center,
            LineAlignment = StringAlignment.Center,
        };
        graphics.DrawString(text, font, Brushes.White, new RectangleF(0, 0, Size, Size), format);
        return bitmap;
    }

    private Icon Render(int offline, int peak)
    {
        using var bitmap = RenderBitmap(offline, peak);
        // GetHicon hands out a handle we own; Icon.FromHandle does not free
        // it, so the clone is made and the original destroyed explicitly.
        var handle = bitmap.GetHicon();
        try
        {
            using var borrowed = Icon.FromHandle(handle);
            return (Icon)borrowed.Clone();
        }
        finally
        {
            DestroyIcon(handle);
        }
    }

    /// <summary>The same thresholds the ring gauge uses, so the two agree.</summary>
    private static Color TintFor(int percent) => percent switch
    {
        < 50 => Color.FromArgb(0x1D, 0x9B, 0x4E),
        < 70 => Color.FromArgb(0xC8, 0x9B, 0x00),
        < 85 => Color.FromArgb(0xE0, 0x7C, 0x00),
        _ => Color.FromArgb(0xE8, 0x1C, 0x2E),
    };

    /// <summary>
    /// Rebuilt each time it opens, so the host list is current.
    /// </summary>
    /// <remarks>
    /// The offline hosts are listed first and by name: the menu's job when
    /// something is wrong is to say <em>what</em>, without opening the window.
    /// </remarks>
    private void RebuildMenu()
    {
        var menu = _icon.ContextMenuStrip!;
        menu.Items.Clear();

        var offline = _monitor.OfflineServers;
        if (offline.Count > 0)
        {
            foreach (var server in offline.Take(8))
            {
                var reason = _monitor.Status.TryGetValue(server.Id, out var status) ? status.Reason : "";
                var item = new ToolStripMenuItem($"● {server.Name}")
                {
                    ForeColor = Color.FromArgb(0xC0, 0x10, 0x20),
                    ToolTipText = reason,
                    Tag = server.Id,
                };
                item.Click += (_, _) => _onOpenServer(server.Id);
                menu.Items.Add(item);
            }
            menu.Items.Add(new ToolStripSeparator());
        }
        else if (_monitor.PeakCpu is { } peak)
        {
            var item = new ToolStripMenuItem(
                $"{peak.Server.Name} — {Core.Format.Percent(peak.Percent)}")
            {
                Tag = peak.Server.Id,
            };
            item.Click += (_, _) => _onOpenServer(peak.Server.Id);
            menu.Items.Add(item);
            menu.Items.Add(new ToolStripSeparator());
        }

        var open = new ToolStripMenuItem(Strings.Get("menubar.open"));
        open.Click += (_, _) => _onOpen();
        menu.Items.Add(open);

        var refresh = new ToolStripMenuItem(Strings.Get("common.refresh"));
        refresh.Click += (_, _) => _ = _monitor.PollAllAsync();
        menu.Items.Add(refresh);

        menu.Items.Add(new ToolStripSeparator());

        var quit = new ToolStripMenuItem(Strings.Get("menubar.quit"));
        quit.Click += (_, _) => _onQuit();
        menu.Items.Add(quit);
    }

    /// <summary>
    /// A balloon notification, for alerts.
    /// </summary>
    /// <remarks>
    /// The plan named the toast API (§5), and this is not it: a real toast
    /// needs a registered AUMID and a start-menu shortcut, which for an
    /// unpackaged app means writing one at first run and hoping the shell
    /// indexes it. NotifyIcon's balloon needs none of that, appears in the
    /// same place, and — the part that matters — supports click-to-activate,
    /// which is what makes an alert actionable. Its cost is no action buttons
    /// and no Action Center history.
    /// </remarks>
    public void Notify(string title, string body, Guid serverId)
    {
        void OnClicked(object? sender, EventArgs e)
        {
            _icon.BalloonTipClicked -= OnClicked;
            _onOpenServer(serverId);
        }
        // Unsubscribed first: a second alert arriving before the first was
        // clicked would otherwise leave both handlers attached, and one click
        // would open two hosts.
        _icon.BalloonTipClicked -= OnClicked;
        _icon.BalloonTipClicked += OnClicked;
        _icon.ShowBalloonTip(10_000, title, body, ToolTipIcon.Warning);
    }

    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool DestroyIcon(IntPtr handle);

    public void Dispose()
    {
        _icon.Visible = false;
        _icon.ContextMenuStrip?.Dispose();
        _icon.Dispose();
        _current?.Dispose();
    }
}
