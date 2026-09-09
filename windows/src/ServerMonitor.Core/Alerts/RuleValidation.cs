using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Alerts;

/// <summary>
/// What makes an alert rule writable.
/// </summary>
/// <remarks>
/// Ported from <c>normalizeAlertRule</c> in
/// <c>web/backend/internal/handlers/alert.go</c>, including its two non-obvious
/// choices. Percent metrics are clamped so a rule can never be written in a way
/// that makes it impossible (or trivial) to satisfy simply by typing a number
/// outside the range. And an offline rule has no number to compare against —
/// the threshold is unused and the comparator is forced to "&gt;", because
/// "still off for a while" is the only shape that one has.
///
/// Kept as a pure function and not a method on the model for the same reason
/// the web keeps it in a handler: it is an entry-point policy, and something a
/// test should be able to call without a rule to mutate.
/// </remarks>
public static class RuleValidation
{
    public const int MinDurationSeconds = 30;
    public const int MaxDurationSeconds = 24 * 60 * 60;
    public const int DefaultDurationSeconds = 300;
    public const int MaxNameLength = 128;

    /// <summary>
    /// Normalises a rule in place, or throws <see cref="ArgumentException"/>
    /// with the same message the web uses.
    /// </summary>
    public static void Normalize(AlertRule rule)
    {
        rule.Name = rule.Name.Trim();
        if (rule.Name.Length == 0)
            throw new ArgumentException("rule name is required");
        if (rule.Name.Length > MaxNameLength)
            throw new ArgumentException("rule name is too long");

        if (rule.Comparator != ">" && rule.Comparator != "<")
            rule.Comparator = ">";

        // The web stores the metric as a lowercase string and validates the
        // string; an enum cannot be misspelled, so any value it holds is one
        // the engine can judge and there is nothing to reject.
        switch (rule.Metric)
        {
            case AlertMetric.Cpu:
            case AlertMetric.Memory:
            case AlertMetric.Disk:
                if (rule.Threshold < 0 || rule.Threshold > 100)
                    throw new ArgumentException(
                        $"threshold for {rule.Metric} must be between 0 and 100");
                break;
            case AlertMetric.Offline:
                rule.Threshold = 0;
                rule.Comparator = ">";
                break;
            default:
                if (rule.Threshold < 0)
                    throw new ArgumentException("threshold must not be negative");
                break;
        }

        if (rule.DurationSeconds == 0) rule.DurationSeconds = DefaultDurationSeconds;
        if (rule.DurationSeconds < MinDurationSeconds
            || rule.DurationSeconds > MaxDurationSeconds)
        {
            throw new ArgumentException(
                $"duration must be between {MinDurationSeconds} and {MaxDurationSeconds} seconds");
        }
    }
}
