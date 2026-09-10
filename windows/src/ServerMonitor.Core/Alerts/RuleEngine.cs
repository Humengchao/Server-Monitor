using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Alerts;

/// <summary>
/// Turns poll results into notifications, one rule at a time.
/// </summary>
/// <remarks>
/// Ported from <c>web/backend/internal/services/alerts.go</c>. The threshold
/// service this replaces hard-coded three metrics with a fixed "breach for
/// three polls" shape; this evaluates a stored set of rules, each with its own
/// metric, comparator, threshold, duration and scope, and records an event for
/// every firing and every resolution.
///
/// The state machine is keyed (rule, server), as the web's is. A value that
/// stays on the wrong side of the comparator for the rule's whole duration
/// opens an event, and that transition delivers the notification exactly once;
/// coming back resolves it. The web keeps a <c>firing</c> set and restores it
/// at startup from events that never resolved, so a condition that was already
/// up when the process died is not announced a second time on the next launch.
/// <see cref="Restore"/> is that.
///
/// Everything the engine needs from the outside is a delegate: which rules
/// exist, where a rule's webhook lives (not in the database — see
/// <see cref="AlertRule"/>), how to record an event, and how to notify. Core
/// stays free of both the store's concrete type and the Windows toast API,
/// which is what lets the whole state machine be tested without either.
/// </remarks>
public sealed class RuleEngine(
    Func<IReadOnlyList<AlertRule>> rules,
    Func<AlertRule, string?> webhookUrl,
    Action<AlertEvent> recordOpen,
    Action<AlertEvent> recordResolve,
    AlertDelivery deliver,
    Action<string>? log = null)
{
    private readonly Dictionary<(Guid Rule, Guid Server), DateTime> _since = [];
    private readonly HashSet<(Guid Rule, Guid Server)> _firing = [];
    private readonly HashSet<Guid> _rejected = [];

    /// <summary>What is firing right now, so the UI can badge a rule.</summary>
    public IReadOnlyCollection<(Guid Rule, Guid Server)> Firing => _firing;

    /// <summary>
    /// Called after each poll result, whether the host answered or not.
    /// </summary>
    /// <remarks>
    /// <paramref name="snapshot"/> is null for a host that failed, which is
    /// exactly when an offline rule has something to say and every other kind
    /// has nothing — a threshold cannot be judged against a reading that was
    /// never taken.
    /// </remarks>
    public void Evaluate(Server server, ServerStatus status, MetricSnapshot? snapshot)
        => Evaluate(server, status, snapshot, DateTime.UtcNow);

    /// <summary>For tests that need a known clock.</summary>
    public void Evaluate(Server server, ServerStatus status, MetricSnapshot? snapshot, DateTime now)
    {
        foreach (var rule in rules())
        {
            if (!rule.Enabled || !Usable(rule)) continue;

            // A null ServerId is a rule for every host; otherwise this host is
            // in scope only by name.
            if (rule.ServerId is { } scoped && scoped != server.Id) continue;

            var key = (rule.Id, server.Id);
            var value = ValueFor(rule, status, snapshot);
            var breached = !double.IsNaN(value) && Breaches(rule, value);

            if (!breached)
            {
                _since.Remove(key);
                if (_firing.Remove(key)) Transition(rule, server, value, now, opening: false);
                continue;
            }

            if (!_since.TryGetValue(key, out var since))
            {
                _since[key] = now;
                continue;
            }

            // Strictly "for the whole duration": a rule with a 300 s duration
            // has not fired at 299 s, which is the difference between this and
            // the counted-polls rule it replaces.
            if ((now - since).TotalSeconds < rule.DurationSeconds) continue;
            if (_firing.Add(key)) Transition(rule, server, value, now, opening: true);
        }
    }

    /// <summary>
    /// Rebuilds the firing set from events that were never resolved.
    /// </summary>
    /// <remarks>
    /// Called once at startup with the open rows. Without it, every condition
    /// that was up when the app closed would be announced again on launch, and
    /// its earlier event would stay open for ever because nothing would ever
    /// see it transition.
    /// </remarks>
    public void Restore(IEnumerable<AlertEvent> unresolved)
    {
        foreach (var open in unresolved)
        {
            _firing.Add((open.RuleId, open.ServerId));
            _since[(open.RuleId, open.ServerId)] = open.StartedAt;
        }
    }

    /// <summary>Forgets a host, so a deleted server leaves nothing firing.</summary>
    public void Forget(Guid serverId)
    {
        foreach (var key in _firing.Where(k => k.Server == serverId).ToList()) _firing.Remove(key);
        foreach (var key in _since.Keys.Where(k => k.Server == serverId).ToList()) _since.Remove(key);
    }

    /// <summary>
    /// Whether a rule can be judged at all, complaining once if it cannot.
    /// </summary>
    /// <remarks>
    /// A row can be edited by hand or arrive from a future version. One
    /// unusable rule must not take the whole pass down with it, and the
    /// complaint is worth exactly once — this runs on every poll.
    /// </remarks>
    private bool Usable(AlertRule rule)
    {
        try
        {
            RuleValidation.Normalize(rule);
            return true;
        }
        catch (ArgumentException error)
        {
            if (_rejected.Add(rule.Id))
            {
                log?.Invoke($"alerts: rule '{rule.Name}' is unusable ({error.Message}); skipped");
            }
            return false;
        }
    }

    /// <summary>
    /// The number this rule compares, or NaN when there is nothing to compare.
    /// </summary>
    /// <remarks>
    /// Offline is the one metric judged on the status rather than a reading,
    /// and it is the one that has an answer when the snapshot is missing.
    /// </remarks>
    private static double ValueFor(AlertRule rule, ServerStatus status, MetricSnapshot? snapshot)
    {
        if (rule.Metric == AlertMetric.Offline) return status.IsOnline ? 0 : 1;
        if (!status.IsOnline || snapshot is null) return double.NaN;

        return rule.Metric switch
        {
            AlertMetric.Cpu => snapshot.CpuPercent,
            AlertMetric.Memory => snapshot.MemoryPercent,
            AlertMetric.Disk => snapshot.DiskPercent,
            AlertMetric.Load1 => snapshot.Load1,
            AlertMetric.Latency => snapshot.LatencyMs,
            _ => double.NaN,
        };
    }

    private static bool Breaches(AlertRule rule, double value) =>
        rule.Comparator == "<" ? value < rule.Threshold : value > rule.Threshold;

    private void Transition(AlertRule rule, Server server, double value, DateTime now, bool opening)
    {
        var summary = Summarise(rule, server, value);
        var record = new AlertEvent
        {
            RuleId = rule.Id,
            RuleName = rule.Name,
            Metric = rule.Metric,
            ServerId = server.Id,
            ServerName = server.Name,
            Value = value,
            Comparator = rule.Comparator,
            Threshold = rule.Threshold,
            DurationSeconds = rule.DurationSeconds,
            Message = summary,
            StartedAt = now,
            ResolvedAt = opening ? null : now,
        };

        if (opening) recordOpen(record);
        else recordResolve(record);

        var state = opening ? Strings.AlertOpen : Strings.AlertResolved;
        deliver(server.Id, rule.Name, summary + Environment.NewLine + state);

        if (webhookUrl(rule) is { } url && url.Trim().Length > 0)
        {
            new WebhookNotifier(log).Send(
                url, WebhookNotifier.For(rule, record, resolved: !opening, summary));
        }
    }

    /// <summary>
    /// The sentence recorded on the event and shown in the notification.
    /// </summary>
    /// <remarks>
    /// Rendered here in the current language and also stored, because the UI
    /// re-renders history from the structured fields so it follows a language
    /// change — and falls back to this text for a rule that has since been
    /// deleted. The web does exactly this and says so on its Message field.
    /// </remarks>
    private static string Summarise(AlertRule rule, Server server, double value)
    {
        if (rule.Metric == AlertMetric.Offline)
        {
            return Strings.IsChinese ? $"{server.Name} 离线" : $"{server.Name} is offline";
        }

        var unit = Strings.AlertUnit(rule.Metric);
        var name = Strings.AlertMetricName(rule.Metric);
        return $"{server.Name} {name} {value:0.#}{unit} {rule.Comparator} {rule.Threshold:0.#}{unit}";
    }
}
