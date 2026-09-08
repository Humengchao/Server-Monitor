using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Platform;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Store;

namespace ServerMonitor.App.Views;

public sealed class SettingsPage : UserControl
{
    private static AppSettings Settings => App.Current.Settings;

    public SettingsPage()
    {
        var body = Ui.Rows(0,
            Section("settings.general", General()),
            Section("settings.alerts", Alerts()),
            Section("settings.terminal", Terminal()),
            Section("settings.storage", Storage()));
        body.Margin = new Thickness(20, 0, 20, 20);
        body.MaxWidth = 760;
        body.HorizontalAlignment = HorizontalAlignment.Left;
        Content = Ui.Scroll(body);
    }

    private static UIElement Section(string titleKey, UIElement content)
    {
        var card = Ui.Card(Ui.Rows(10, Ui.Title(Strings.Get(titleKey)), content));
        card.Margin = new Thickness(0, 0, 0, 14);
        return card;
    }

    // MARK: - General

    private UIElement General()
    {
        var launchToggle = Ui.Toggle(
            Strings.Get("settings.launchAtLogin"),
            StartupRegistration.IsEnabled,
            value =>
            {
                // The registry is the source of truth, not the setting: if the
                // write is refused, the toggle goes back rather than claiming
                // a state that is not real.
                if (StartupRegistration.Set(value))
                {
                    Settings.LaunchAtLogin = value;
                }
                else
                {
                    Ui.Complain(Window.GetWindow(this), Strings.Get("common.error"));
                }
            });

        return Ui.Rows(0,
            Ui.Field("settings.pollInterval", Ui.Picker(
                AppSettings.AllowedIntervals,
                Settings.PollInterval,
                seconds => $"{seconds:0} {Strings.Get("settings.seconds")}",
                seconds => Settings.PollInterval = seconds), "settings.pollIntervalHelp"),

            Ui.Field("settings.retention", Ui.Picker(
                AppSettings.AllowedRetention,
                Settings.RetentionDays,
                days => $"{days} {Strings.Get("settings.days")}",
                days => Settings.RetentionDays = days), "settings.retentionHelp"),

            Ui.Field("settings.language", Ui.Picker(
                new[] { AppLanguage.System, AppLanguage.Zh, AppLanguage.En },
                Settings.Language,
                language => language switch
                {
                    AppLanguage.Zh => "中文",
                    AppLanguage.En => "English",
                    _ => Strings.Get("settings.languageSystem"),
                },
                language => Settings.Language = language)),

            Ui.Field("settings.general", Ui.Picker(
                new[] { AppTheme.System, AppTheme.Light, AppTheme.Dark },
                Settings.Theme,
                theme => theme switch
                {
                    AppTheme.Light => Strings.IsChinese ? "浅色" : "Light",
                    AppTheme.Dark => Strings.IsChinese ? "深色" : "Dark",
                    _ => Strings.Get("settings.languageSystem"),
                },
                theme => Settings.Theme = theme)),

            Ui.Toggle(
                Strings.IsChinese ? "使用 Mica 窗口材质" : "Use the Mica window material",
                Settings.UseMicaBackdrop,
                value => Settings.UseMicaBackdrop = value),
            Ui.Wrapped(
                Strings.IsChinese
                    ? "让桌面壁纸透过窗口背景。默认关闭：Mica 需要窗口自身透明，而在虚拟显示器、部分远程串流环境下系统并不会真正绘制这层材质，窗口内容区会整片变黑——而且系统 API 在这种情况下依然返回成功，程序无法自己发现。打开后若窗口变黑，关掉即可恢复。"
                    : "Lets the desktop wallpaper show through the window background. Off by default: Mica needs the window itself to be transparent, and on a virtual display or some remote-streaming setups Windows never composites the material, leaving the client area entirely black — while the API still reports success, so the app cannot detect it. If the window goes black, turn this back off.",
                "Text.Tertiary"),

            // D3 in the UI: which transport hosts use by default, with the
            // trade-off stated rather than left to the README.
            Ui.Field("nav.settings", Ui.Picker(
                new[] { TransportKind.Library, TransportKind.OpenSshExe },
                Settings.Transport,
                kind => kind == TransportKind.Library
                    ? (Strings.IsChinese ? "内置 SSH（长连接）" : "Built-in SSH (persistent)")
                    : "ssh.exe",
                kind => Settings.Transport = kind)),

            Ui.Wrapped(
                Strings.IsChinese
                    ? "内置 SSH 为每台主机保持一条长连接，等价于 macOS 端的 ControlMaster；Windows 自带的 ssh.exe 不支持连接复用，每次采集都是一次完整握手与登录，主机的 auth.log 会因此增长很快。需要 Match 块、证书、PKCS#11 或真正的 ssh-agent 的主机可以在机器编辑器里单独切换。"
                    : "Built-in SSH keeps one connection per host, the equivalent of ControlMaster on macOS. Windows' own ssh.exe cannot reuse connections, so every poll is a full handshake and login — which grows a host's auth.log quickly. A host needing Match blocks, a certificate, PKCS#11 or a real ssh-agent can be switched on its own in the machine editor.",
                "Text.Tertiary"),

            Ui.Separator(),
            launchToggle,
            Ui.Wrapped(Strings.Get("settings.launchAtLoginHelp"), "Text.Tertiary"),

            Ui.Toggle(
                Strings.IsChinese ? "关闭窗口时驻留在通知区域" : "Keep running in the notification area when closed",
                Settings.CloseToTray,
                value => Settings.CloseToTray = value),
            Ui.Wrapped(
                Strings.IsChinese
                    ? "关闭窗口后继续在后台采集与告警；从通知区域图标可以重新打开或退出。"
                    : "Collecting and alerting carry on after the window is closed; the notification-area icon reopens it or quits.",
                "Text.Tertiary"),

            Ui.Separator(),
            Ui.Wrapped(Strings.Get("settings.credentialsNoteLocal"), "Text.Tertiary"));
    }

    // MARK: - Alerts

    private UIElement Alerts()
    {
        var thresholds = Ui.Rows(0,
            ThresholdField("settings.cpuThreshold", Settings.CpuThreshold, v => Settings.CpuThreshold = v),
            ThresholdField("settings.memoryThreshold", Settings.MemoryThreshold, v => Settings.MemoryThreshold = v),
            ThresholdField("settings.diskThreshold", Settings.DiskThreshold, v => Settings.DiskThreshold = v));
        thresholds.IsEnabled = Settings.NotificationsEnabled;

        var offline = Ui.Toggle(
            Strings.Get("settings.notifyOffline"),
            Settings.NotifyOnOffline,
            value => Settings.NotifyOnOffline = value);
        offline.IsEnabled = Settings.NotificationsEnabled;

        var enabled = Ui.Toggle(
            Strings.Get("settings.notificationsEnabled"),
            Settings.NotificationsEnabled,
            value =>
            {
                Settings.NotificationsEnabled = value;
                // The dependent controls are greyed rather than hidden, so the
                // panel does not change height as it is toggled.
                thresholds.IsEnabled = value;
                offline.IsEnabled = value;
            });

        return Ui.Rows(0,
            enabled,
            offline,
            Ui.Separator(),
            thresholds,
            Ui.Wrapped(Strings.Get("settings.thresholdHelp"), "Text.Tertiary"));
    }

    private static UIElement ThresholdField(string key, int current, Action<int> onChange) =>
        Ui.Field(key, Ui.Picker(
            AppSettings.ThresholdChoices,
            current,
            value => value == 0 ? Strings.Get("settings.thresholdOff") : $"{value}%",
            onChange));

    // MARK: - Terminal

    private static UIElement Terminal() => Ui.Rows(0,
        Ui.Field("settings.terminalFont", Ui.Picker(
            AppSettings.TerminalFonts,
            Settings.TerminalFontName,
            name => name,
            name => Settings.TerminalFontName = name)),
        Ui.Field("settings.terminalFontSize", Ui.Picker(
            AppSettings.TerminalFontSizes,
            Settings.TerminalFontSize,
            size => size.ToString("0", System.Globalization.CultureInfo.InvariantCulture),
            size => Settings.TerminalFontSize = size)));

    // MARK: - Storage

    private UIElement Storage()
    {
        var databasePath = Core.Store.Database.DefaultPath;
        var logs = System.IO.Path.Combine(Core.Store.Database.DefaultDirectory, "logs");

        return Ui.Rows(8,
            Ui.Caption(databasePath),
            Ui.Caption(logs),
            Ui.Columns(8,
                Ui.Button(
                    Strings.IsChinese ? "打开数据目录" : "Open data folder",
                    () => Reveal(Core.Store.Database.DefaultDirectory)),
                Ui.Button(Strings.Get("history.clear"), ClearHistory)),
            Ui.Separator(),
            Ui.Columns(8, Ui.Danger(Strings.Get("menubar.quit"), () => App.Current.QuitApp())));
    }

    private void ClearHistory()
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("history.clearConfirm"))) return;
        App.Current.Monitor.Database.ClearSessionHistory();
        Ui.Inform(Window.GetWindow(this), Strings.Get("common.save"));
    }

    private void Reveal(string path)
    {
        try
        {
            System.IO.Directory.CreateDirectory(path);
            System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo("explorer.exe")
            {
                ArgumentList = { path },
                UseShellExecute = true,
            });
        }
        catch (Exception error)
        {
            Ui.Complain(Window.GetWindow(this), error.Message);
        }
    }
}
