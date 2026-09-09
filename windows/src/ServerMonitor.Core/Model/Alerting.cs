namespace ServerMonitor.Core.Model;

/// <summary>
/// One configurable alert rule, as the web client's engine defines it.
/// </summary>
/// <remarks>
/// Ported from <c>web/backend/internal/models/alert.go</c> (AlertRule) with the
/// per-user column dropped — this client has no users, so the rule belongs to
/// the machine. A null <see cref="ServerId"/> means "all hosts".
///
/// <see cref="WebhookUrl"/> is deliberately <em>not</em> in the database: a
/// rule stored on disk is a fact, and a webhook URL is a credential-shaped
/// thing the user would not expect to be readable from the file. The rule
/// table stores everything the engine needs to judge, and the URL lives in the
/// same credential store the SSH passwords do.
/// </remarks>
public sealed class AlertRule
{
    public Guid Id { get; set; } = Guid.NewGuid();
    public string Name { get; set; } = string.Empty;
    public AlertMetric Metric { get; set; } = AlertMetric.Cpu;
    /// <summary>"&gt;" or "&lt;". The web keeps it a string; so does this.</summary>
    public string Comparator { get; set; } = ">";
    public double Threshold { get; set; }
    /// <summary>Seconds the value must stay on the wrong side before it fires.</summary>
    public int DurationSeconds { get; set; } = 300;
    public bool Enabled { get; set; } = true;
    public Guid? ServerId { get; set; }
    /// <summary>Set by list queries; the UI badges a rule without a second call.</summary>
    public int FiringCount { get; set; }
}

/// <summary>What a rule can watch.</summary>
/// <remarks>
/// The web is a string list; an enum is the same set with the typo ruled out
/// at compile time. The mapping is one-to-one and both are kept in sync here.
/// </remarks>
public enum AlertMetric
{
    Cpu,
    Memory,
    Disk,
    Load1,
    Latency,
    Offline,
}

/// <summary>
/// One firing (or resolved) of a rule against one host.
/// </summary>
/// <remarks>
/// <see cref="Message"/> is the summary recorded when the event opened. The UI
/// prefers to re-render from the structured fields so the text follows the
/// selected language, like the web's Alerts page does; this stays the fallback
/// for a rule that has since been deleted.
/// </remarks>
public sealed class AlertEvent
{
    public long Id { get; set; }
    public Guid RuleId { get; set; }
    public string RuleName { get; set; } = string.Empty;
    public AlertMetric Metric { get; set; }
    public Guid ServerId { get; set; }
    public string ServerName { get; set; } = string.Empty;
    public double Value { get; set; }
    public string Comparator { get; set; } = ">";
    public double Threshold { get; set; }
    public int DurationSeconds { get; set; }
    public string Message { get; set; } = string.Empty;
    public DateTime StartedAt { get; set; } = DateTime.UtcNow;
    public DateTime? ResolvedAt { get; set; }
}
