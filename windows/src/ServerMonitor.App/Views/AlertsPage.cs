using System.Globalization;
using System.Windows;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.App.Views;

/// <summary>
/// The alert rules, and what they have fired.
/// </summary>
/// <remarks>
/// The surface over the rule engine ported from the web client. Rules are
/// judged continuously by <c>App.Monitor.Alerts</c>; this page only reads and
/// edits them. Its one live value — the firing badge — comes from the engine's
/// in-memory firing set rather than a second pass over the readings, so the
/// badge and the notification can never disagree.
///
/// Both the rule summary and the history row are rendered from the stored
/// fields rather than from saved text, so they follow a language change. The
/// event's own <c>Message</c> is kept as the fallback for a rule that has
/// since been deleted, which is what the web does too.
/// </remarks>
public sealed class AlertsPage : UserControl
{
    private readonly StackPanel _root = Ui.Rows(0);

    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    public AlertsPage()
    {
        _root.Margin = new Thickness(20, 0, 20, 20);
        Content = Ui.Scroll(_root);
        Rebuild();
    }

    private void Rebuild()
    {
        _root.Children.Clear();

        var rules = Monitor.Database.AllAlertRules();
        var firing = Monitor.Alerts?.Firing.ToList() ?? [];

        var add = Ui.Accent(Strings.Get("alert.new"), () => Edit(null));

        if (rules.Count == 0)
        {
            _root.Children.Add(Ui.Empty(null, "alert.empty", add));
        }
        else
        {
            add.HorizontalAlignment = HorizontalAlignment.Left;
            add.Margin = new Thickness(0, 0, 0, 14);
            _root.Children.Add(add);
            foreach (var rule in rules)
            {
                _root.Children.Add(RuleCard(rule, firing.Count(f => f.Rule == rule.Id)));
            }
        }

        var historyHeading = Ui.Caption(Strings.Get("alert.history"));
        historyHeading.Margin = new Thickness(0, 18, 0, 8);
        _root.Children.Add(Ui.Separator());
        _root.Children.Add(historyHeading);
        AddHistory();
    }

    private UIElement RuleCard(AlertRule rule, int firingCount)
    {
        var name = Ui.Title(rule.Name);
        name.VerticalAlignment = VerticalAlignment.Center;

        var badges = Ui.Columns(6);
        badges.VerticalAlignment = VerticalAlignment.Center;
        if (firingCount > 0)
        {
            badges.Children.Add(Ui.TagChip(
                firingCount == 1
                    ? Strings.Get("alert.firing")
                    : Strings.Get("alert.firingCount", firingCount.ToString(CultureInfo.InvariantCulture))));
        }
        if (!rule.Enabled) badges.Children.Add(Ui.Caption(Strings.Get("alert.disabled")));

        var actions = Ui.Columns(6,
            Ui.Quiet(Strings.Get("common.edit"), () => Edit(rule)),
            Ui.Danger(Strings.Get("common.delete"), () => Delete(rule)));
        actions.HorizontalAlignment = HorizontalAlignment.Right;

        var card = Ui.Card(Ui.Rows(6,
            Ui.Grid("auto,*,auto", name, badges, actions),
            Ui.Caption(Describe(rule))));
        card.Margin = new Thickness(0, 0, 0, 10);
        return card;
    }

    /// <summary>The sentence that says what a rule watches.</summary>
    public static string Describe(AlertRule rule)
    {
        var scope = rule.ServerId is { } id
            ? Monitor.Server(id)?.Name ?? Strings.Get("alert.ruleDeleted")
            : Strings.Get("alert.allHosts");
        var duration = Durations.Say(rule.DurationSeconds);

        if (rule.Metric == AlertMetric.Offline)
        {
            return Strings.IsChinese
                ? $"{scope}：离线持续 {duration}"
                : $"{scope}: offline for {duration}";
        }

        var metric = Strings.AlertMetricName(rule.Metric);
        var unit = Strings.AlertUnit(rule.Metric);
        var threshold = rule.Threshold.ToString("0.#", CultureInfo.InvariantCulture);
        var comparator = Comparators.Say(rule.Comparator);
        return Strings.IsChinese
            ? $"{scope}：{metric} {comparator} {threshold}{unit}，持续 {duration}"
            : $"{scope}: {metric} {comparator} {threshold}{unit} for {duration}";
    }

    private void AddHistory()
    {
        var events = Monitor.Database.AlertEvents();
        if (events.Count == 0)
        {
            _root.Children.Add(Ui.Tertiary(Strings.Get("alert.historyEmpty")));
            return;
        }

        var clear = Ui.Danger(Strings.Get("alert.clear"), ClearHistory);
        clear.HorizontalAlignment = HorizontalAlignment.Left;
        clear.Margin = new Thickness(0, 0, 0, 12);
        _root.Children.Add(clear);

        var list = new ListView();
        ScrollViewer.SetVerticalScrollBarVisibility(list, ScrollBarVisibility.Disabled);
        var view = new GridView { AllowsColumnReorder = false };

        void Column(string headerKey, string path, double width, bool tooltip = false) =>
            view.Columns.Add(Ui.TextColumn(Strings.Get(headerKey), path, width, tooltip));

        Column("history.server", nameof(Row.Server), 150, tooltip: true);
        Column("alert.name", nameof(Row.Rule), 150, tooltip: true);
        Column("alert.condition", nameof(Row.Condition), 210, tooltip: true);
        Column("alert.raisedAt", nameof(Row.Raised), 160);
        Column("history.duration", nameof(Row.Status), 130);
        list.View = view;

        foreach (var record in events)
        {
            list.Items.Add(new Row(
                record.ServerName,
                record.RuleName,
                Recount(record),
                // Local time, like the session history: this is a record of
                // what happened on the user's own watch.
                record.StartedAt.ToLocalTime().ToString(
                    "yyyy-MM-dd HH:mm:ss", CultureInfo.CurrentCulture),
                record.ResolvedAt is null
                    ? Strings.Get("alert.ongoing")
                    : Strings.Get("alert.for", Durations.Say(
                        (int)Math.Max(0, (record.ResolvedAt.Value - record.StartedAt).TotalSeconds)))));
        }

        var card = Ui.Card(list, 8);
        card.Margin = new Thickness(0, 0, 0, 10);
        _root.Children.Add(card);
    }

    /// <summary>
    /// What an event says, re-rendered in the current language.
    /// </summary>
    /// <remarks>
    /// The stored <c>Message</c> is the fallback rather than the source: it was
    /// written in whatever language was in force when the alert fired, and a
    /// user who has since switched should not find half their history in the
    /// other one. An offline event has no numbers of its own, so there the
    /// stored sentence is all there is.
    /// </remarks>
    private static string Recount(AlertEvent record)
    {
        if (record.Metric == AlertMetric.Offline)
        {
            return record.Message.Length > 0
                ? record.Message
                : Strings.IsChinese ? $"{record.ServerName} 离线" : $"{record.ServerName} is offline";
        }

        var metric = Strings.AlertMetricName(record.Metric);
        var unit = Strings.AlertUnit(record.Metric);
        var comparator = Comparators.Say(record.Comparator);
        var threshold = record.Threshold.ToString("0.#", CultureInfo.InvariantCulture);
        var value = record.Value.ToString("0.#", CultureInfo.InvariantCulture);
        return Strings.IsChinese
            ? $"{metric} {comparator} {threshold}{unit}（当时 {value}{unit}）"
            : $"{metric} {comparator} {threshold}{unit} (was {value}{unit})";
    }

    private void ClearHistory()
    {
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("alert.clearConfirm"))) return;
        Monitor.Database.ClearAlertHistory();
        Rebuild();
    }

    private void Edit(AlertRule? rule)
    {
        var window = new AlertRuleEditorWindow(rule) { Owner = Window.GetWindow(this) };
        if (window.ShowDialog() != true) return;
        App.Current.InvalidateAlertRules();
        Rebuild();
    }

    private void Delete(AlertRule rule)
    {
        // The events stay: they are a record of what happened, and the rule
        // going away does not unhappen them. Said out loud so a user deleting
        // a rule to tidy the history is not surprised.
        if (!Ui.Confirm(Window.GetWindow(this), Strings.Get("alert.deleteConfirm", rule.Name))) return;
        Monitor.Database.DeleteAlertRule(rule.Id);
        App.Current.Credentials.DeleteWebhook(rule.Id);
        App.Current.InvalidateAlertRules();
        Rebuild();
    }

    /// <summary>One history row, shaped for the GridView's bindings.</summary>
    public sealed record Row(string Server, string Rule, string Condition, string Raised, string Status);
}

/// <summary>"&gt;" and "&lt;" in words, shared by the card, the row and the editor.</summary>
public static class Comparators
{
    public static string Say(string comparator) => comparator == "<"
        ? Strings.IsChinese ? "低于" : "below"
        : Strings.IsChinese ? "超过" : "above";
}

/// <summary>A duration in seconds said the way the pickers offer it.</summary>
/// <remarks>
/// Not <c>SessionsPage.Describe</c>: that one narrates an elapsed session
/// ("2h 14m"), while these are the round numbers a rule is configured with,
/// and "1 小时" reads better than "1 小时 0 分" on every one of them.
/// </remarks>
public static class Durations
{
    public static string Say(int seconds)
    {
        if (seconds >= 3600 && seconds % 3600 == 0)
        {
            var hours = seconds / 3600;
            return Strings.IsChinese ? $"{hours} 小时" : $"{hours}h";
        }
        if (seconds >= 60 && seconds % 60 == 0)
        {
            var minutes = seconds / 60;
            return Strings.IsChinese ? $"{minutes} 分钟" : $"{minutes}m";
        }
        return Strings.IsChinese ? $"{seconds} 秒" : $"{seconds}s";
    }
}

/// <summary>The add/edit dialog for one rule.</summary>
/// <remarks>
/// The comparator and threshold rows are built once and hidden for an offline
/// rule rather than rebuilt on every change of the metric picker. Rebuilding
/// is what threw on the identity editor: <see cref="Ui.Field"/> wraps its
/// control in a fresh Grid, but the control is the same instance, and WPF
/// refuses to adopt one that still has a parent.
/// </remarks>
public sealed class AlertRuleEditorWindow : Window
{
    /// <summary>
    /// The durations offered, from the validator's floor to half a day.
    /// </summary>
    /// <remarks>
    /// A picker rather than a number box: the useful values are these, and a
    /// free field invites 7 seconds (refused) or 7 days (also refused) and
    /// makes the user discover the bounds by being told no.
    /// </remarks>
    private static readonly int[] DurationChoices =
        [30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200];

    private readonly AlertRule _rule;
    private readonly bool _isNew;
    private readonly TextBox _name = Ui.Input();
    private readonly TextBox _threshold = Ui.Input();
    private readonly TextBox _webhook = Ui.Input();
    private readonly CheckBox _enabled;
    private readonly TextBlock _error = Ui.Wrapped("", "Text.Caption");
    private readonly Grid _comparatorRow;
    private readonly Grid _thresholdRow;
    private readonly TextBlock _offlineNote = Ui.Wrapped(Strings.Get("alert.offlineNoThreshold"), "Text.Tertiary");

    private AlertMetric _metric;
    private string _comparator;
    private int _duration;
    private Guid? _scope;

    private static Core.Collect.MonitorService Monitor => App.Current.Monitor;

    public AlertRuleEditorWindow(AlertRule? existing)
    {
        _isNew = existing is null;
        _rule = existing is null
            ? new AlertRule { Threshold = 90, DurationSeconds = 300 }
            : Copy(existing);

        _metric = _rule.Metric;
        _comparator = _rule.Comparator;
        _duration = Nearest(_rule.DurationSeconds);
        _scope = _rule.ServerId;
        _name.Text = _rule.Name;
        _threshold.Text = _rule.Threshold.ToString("0.#", CultureInfo.InvariantCulture);
        // Shown as it is, not masked. It is a destination the user typed on
        // their own machine, and an editor that hides what it will save is how
        // a URL gets silently replaced by a row of dots.
        _webhook.Text = App.Current.Credentials.GetWebhook(_rule.Id) ?? "";
        _enabled = Ui.Toggle(Strings.Get("alert.enabled"), _rule.Enabled, _ => { });

        Title = Strings.Get(_isNew ? "alert.new" : "common.edit");
        Width = 560;
        SizeToContent = SizeToContent.Height;
        WindowStartupLocation = WindowStartupLocation.CenterOwner;
        ResizeMode = ResizeMode.NoResize;
        Background = (System.Windows.Media.Brush)FindResource("Brush.Background");

        _error.Foreground = Theme.Ink.Brush(Theme.Palette.Offline);
        _error.Visibility = Visibility.Collapsed;

        _comparatorRow = Ui.Field("alert.condition", Ui.Picker(
            new[] { ">", "<" }, _comparator, Comparators.Say, c => _comparator = c));
        _thresholdRow = Ui.Field("alert.threshold", _threshold);

        // Guid.Empty stands for "all hosts". A sentinel row rather than a
        // nullable picker because ComboBox selection is by instance, and the
        // list has to contain the selected value for it to show up selected.
        var hosts = new List<(Guid Id, string Name)> { (Guid.Empty, Strings.Get("alert.allHosts")) };
        hosts.AddRange(Monitor.Servers.Select(s => (s.Id, s.Name)));
        var scopeRow = Ui.Field("alert.scope", Ui.Picker(
            hosts,
            hosts.FirstOrDefault(h => h.Id == (_scope ?? Guid.Empty)),
            host => host.Name,
            host => _scope = host.Id == Guid.Empty ? null : host.Id));

        var buttons = Ui.Columns(8,
            Ui.Cancels(Ui.Button(Strings.Get("common.cancel"), () => { DialogResult = false; Close(); })),
            Ui.Default(Ui.Accent(Strings.Get("common.save"), Save)));
        buttons.HorizontalAlignment = HorizontalAlignment.Right;
        buttons.Margin = new Thickness(0, 14, 0, 0);

        var root = Ui.Rows(0,
            Ui.Field("alert.name", _name),
            Ui.Field("alert.metric", Ui.Picker(
                Enum.GetValues<AlertMetric>(),
                _metric,
                Strings.AlertMetricName,
                metric =>
                {
                    _metric = metric;
                    RefreshMetricFields();
                })),
            _comparatorRow,
            _thresholdRow,
            _offlineNote,
            Ui.Field("alert.duration", Ui.Picker(
                DurationChoices, _duration, Durations.Say, d => _duration = d),
                "alert.durationHelp"),
            scopeRow,
            Ui.Field("alert.webhook", _webhook, "alert.webhookHelp"),
            _enabled,
            _error,
            buttons);
        root.Margin = new Thickness(20);
        Content = root;

        RefreshMetricFields();
    }

    /// <summary>
    /// A copy, so cancelling leaves the list's row untouched.
    /// </summary>
    /// <remarks>
    /// <see cref="AlertRule"/> has no Clone of its own — unlike Server, it is
    /// never edited through a shared instance anywhere else — so the editor
    /// makes its own rather than adding a method with one caller.
    /// </remarks>
    private static AlertRule Copy(AlertRule source) => new()
    {
        Id = source.Id,
        Name = source.Name,
        Metric = source.Metric,
        Comparator = source.Comparator,
        Threshold = source.Threshold,
        DurationSeconds = source.DurationSeconds,
        Enabled = source.Enabled,
        ServerId = source.ServerId,
    };

    /// <summary>
    /// The offered duration closest to a stored one.
    /// </summary>
    /// <remarks>
    /// A seeded rule is 30 s and every rule made here is one of the choices,
    /// but a hand-edited row could hold anything, and a picker that cannot
    /// show its own value silently rewrites it to the first option on save.
    /// </remarks>
    private static int Nearest(int seconds) =>
        DurationChoices.MinBy(choice => Math.Abs(choice - seconds));

    private void RefreshMetricFields()
    {
        var offline = _metric == AlertMetric.Offline;
        _comparatorRow.Visibility = offline ? Visibility.Collapsed : Visibility.Visible;
        _thresholdRow.Visibility = offline ? Visibility.Collapsed : Visibility.Visible;
        _offlineNote.Visibility = offline ? Visibility.Visible : Visibility.Collapsed;
    }

    private void Save()
    {
        if (_name.Text.Trim().Length == 0)
        {
            Complain(Strings.Get("alert.required"));
            return;
        }

        var threshold = 0d;
        if (_metric != AlertMetric.Offline)
        {
            if (!double.TryParse(
                    _threshold.Text.Trim(), NumberStyles.Float, CultureInfo.InvariantCulture, out threshold))
            {
                Complain(Strings.Get("alert.thresholdInvalid"));
                return;
            }
            var percent = _metric is AlertMetric.Cpu or AlertMetric.Memory or AlertMetric.Disk;
            if (threshold < 0 || (percent && threshold > 100))
            {
                Complain(Strings.Get(percent ? "alert.thresholdPercent" : "alert.thresholdInvalid"));
                return;
            }
        }

        var webhook = _webhook.Text.Trim();
        if (webhook.Length > 0)
        {
            try
            {
                // Checked here so the first alert is not what discovers a
                // typo'd address.
                WebhookNotifier.Validate(webhook);
            }
            catch (ArgumentException)
            {
                Complain(Strings.Get("alert.webhookInvalid"));
                return;
            }
        }

        _rule.Name = _name.Text.Trim();
        _rule.Metric = _metric;
        _rule.Comparator = _comparator;
        _rule.Threshold = threshold;
        _rule.DurationSeconds = _duration;
        _rule.Enabled = _enabled.IsChecked == true;
        _rule.ServerId = _scope;

        try
        {
            // The same function the engine uses to decide a rule is judgeable.
            // Reached only if a check above was wrong, and its message is
            // developer English, so it is logged rather than shown.
            RuleValidation.Normalize(_rule);
        }
        catch (ArgumentException error)
        {
            App.Log($"alerts: refused a rule from the editor ({error.Message})");
            Complain(Strings.Get("alert.required"));
            return;
        }

        Monitor.Database.Save(_rule);
        if (webhook.Length > 0) App.Current.Credentials.SetWebhook(_rule.Id, webhook);
        else App.Current.Credentials.DeleteWebhook(_rule.Id);

        DialogResult = true;
        Close();
    }

    private void Complain(string message)
    {
        _error.Text = message;
        _error.Visibility = Visibility.Visible;
    }
}
