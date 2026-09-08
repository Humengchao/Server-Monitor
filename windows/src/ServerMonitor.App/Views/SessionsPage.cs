using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// Terminal and SFTP session history.
/// </summary>
/// <remarks>
/// Rows survive the server they refer to: the id is nulled on delete and the
/// name column carries the label, so history does not silently shrink when a
/// host is removed.
/// </remarks>
public sealed class SessionsPage : UserControl
{
    private readonly StackPanel _root = Ui.Rows(0);

    public SessionsPage()
    {
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);
        Rebuild();
    }

    private sealed record Row(string Server, string Kind, string Started, string Duration);

    private void Rebuild()
    {
        _root.Children.Clear();
        var records = App.Current.Monitor.Database.RecentSessions();

        if (records.Count == 0)
        {
            _root.Children.Add(Ui.Empty("nav.sessions", "history.empty"));
            return;
        }

        var clear = Ui.Danger(Strings.Get("history.clear"), Clear);
        clear.HorizontalAlignment = HorizontalAlignment.Left;
        clear.Margin = new Thickness(0, 0, 0, 14);
        _root.Children.Add(clear);

        var list = new ListView();
        ScrollViewer.SetVerticalScrollBarVisibility(list, ScrollBarVisibility.Disabled);
        var view = new GridView { AllowsColumnReorder = false };

        void Column(string headerKey, string path, double width) =>
            view.Columns.Add(new GridViewColumn
            {
                Header = Strings.Get(headerKey),
                DisplayMemberBinding = new System.Windows.Data.Binding(path),
                Width = width,
            });

        Column("history.server", nameof(Row.Server), 200);
        Column("history.kind", nameof(Row.Kind), 100);
        Column("history.started", nameof(Row.Started), 190);
        Column("history.duration", nameof(Row.Duration), 120);
        list.View = view;

        foreach (var record in records)
        {
            list.Items.Add(new Row(
                record.ServerName,
                record.Kind == SessionKind.Sftp ? Strings.Get("nav.sftp") : Strings.Get("nav.terminal"),
                // Local time here, unlike the vnStat axis: this is a record of
                // what *this* user did, so their own clock is the right one.
                record.StartedAt.ToLocalTime().ToString(
                    "yyyy-MM-dd HH:mm:ss", System.Globalization.CultureInfo.CurrentCulture),
                record.IsOpen
                    ? Strings.Get("history.open")
                    : Describe(record.Duration ?? TimeSpan.Zero)));
        }

        _root.Children.Add(Ui.Card(list, 8));
    }

    private static string Describe(TimeSpan duration)
    {
        if (duration.TotalSeconds < 1) return "—";
        if (duration.TotalHours >= 1)
        {
            return Strings.IsChinese
                ? $"{(int)duration.TotalHours} 小时 {duration.Minutes} 分"
                : $"{(int)duration.TotalHours}h {duration.Minutes}m";
        }
        if (duration.TotalMinutes >= 1)
        {
            return Strings.IsChinese
                ? $"{(int)duration.TotalMinutes} 分 {duration.Seconds} 秒"
                : $"{(int)duration.TotalMinutes}m {duration.Seconds}s";
        }
        return Strings.IsChinese ? $"{duration.Seconds} 秒" : $"{duration.Seconds}s";
    }

    private void Clear()
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("history.clearConfirm"))) return;
        App.Current.Monitor.Database.ClearSessionHistory();
        Rebuild();
    }
}
