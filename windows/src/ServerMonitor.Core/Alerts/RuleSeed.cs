using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Core.Alerts;

/// <summary>
/// Turns the settings the old threshold service read into rules, once.
/// </summary>
/// <remarks>
/// Run on the launch that advances the schema to v2 and never again, so a user
/// who has tuned CPU/memory/disk limits keeps getting the alerts they were
/// getting, now as rows they can edit. Without this the rule engine would
/// start empty and every existing threshold would silently stop meaning
/// anything.
///
/// Two places to be careful, both about not changing what the user hears:
///
/// <para><b>Per-host overrides.</b> The old service read
/// <c>server.CpuThreshold ?? settings.CpuThreshold</c>, so a host with its own
/// limit was <em>excluded</em> from the global one. A rule is scoped to one
/// host or to all; there is no "all except these", in this engine or in the
/// web's. So when any host overrides a metric, that metric is seeded as one
/// rule per host rather than a global rule — same set of alerts, spelled out.
/// A host whose override is 0 gets no rule, which is exactly what 0 meant.</para>
///
/// <para><b>Duration.</b> The old service fired after three consecutive
/// breaches (~15 s at the default interval) and announced an offline host at
/// once. The rule language's floor is 30 s, so that is what everything is
/// seeded with: the closest thing to the old timing, rather than the engine's
/// own 300 s default, which would be this migration quietly making the user's
/// alerts five minutes later than they were.</para>
/// </remarks>
public static class RuleSeed
{
    /// <summary>
    /// The seeded duration, and the lowest the validator accepts.
    /// </summary>
    public const int SeededDurationSeconds = 30;

    /// <summary>The rules that reproduce these settings.</summary>
    public static List<AlertRule> From(AppSettings settings, IReadOnlyList<Server> servers)
    {
        var seeded = new List<AlertRule>();

        Seed(AlertMetric.Cpu, settings.CpuThreshold, server => server.CpuThreshold);
        Seed(AlertMetric.Memory, settings.MemoryThreshold, server => server.MemoryThreshold);
        Seed(AlertMetric.Disk, settings.DiskThreshold, server => server.DiskThreshold);

        if (settings.NotifyOnOffline)
        {
            seeded.Add(new AlertRule
            {
                Name = Strings.IsChinese ? "主机离线" : "Host offline",
                Metric = AlertMetric.Offline,
                DurationSeconds = SeededDurationSeconds,
            });
        }

        return seeded;

        void Seed(AlertMetric metric, int global, Func<Server, int?> perHost)
        {
            if (!servers.Any(server => perHost(server) is not null))
            {
                if (global > 0) seeded.Add(Rule(metric, global, null));
                return;
            }

            foreach (var server in servers)
            {
                var limit = perHost(server) ?? global;
                if (limit > 0) seeded.Add(Rule(metric, limit, server));
            }
        }
    }

    private static AlertRule Rule(AlertMetric metric, int limit, Server? server) => new()
    {
        Name = Name(metric, limit, server),
        Metric = metric,
        Threshold = limit,
        DurationSeconds = SeededDurationSeconds,
        ServerId = server?.Id,
    };

    /// <summary>
    /// What the seeded rule is called in the list.
    /// </summary>
    /// <remarks>
    /// Written in the language in force at migration, then stored — a rule
    /// name is user text from the moment it exists, and re-translating one the
    /// user may have since renamed would be worse than leaving it.
    /// </remarks>
    private static string Name(AlertMetric metric, int limit, Server? server)
    {
        var what = Strings.AlertMetricName(metric);
        var scope = server is null ? "" : $" · {server.Name}";
        if (Strings.IsChinese) return $"{what} 超过 {limit}%{scope}";
        return $"{char.ToUpperInvariant(what[0])}{what[1..]} above {limit}%{scope}";
    }
}
