using ServerMonitor.Core.Collect;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Core.Alerts;

/// <summary>Where a notification is handed off.</summary>
/// <remarks>
/// Injected so tests can observe what <em>would</em> be delivered without a
/// notification centre, and so Core does not depend on the Windows toast API.
/// <paramref name="serverId"/> is passed so the App can make clicking the
/// toast open that host.
/// </remarks>
public delegate void AlertDelivery(Guid serverId, string title, string body);

/// <summary>
/// Turns poll results into desktop notifications.
/// </summary>
/// <remarks>
/// The value of an always-on monitor is that it tells you <em>without</em>
/// being looked at, so the rules here are tuned against noise rather than
/// latency: a threshold must be breached for several consecutive polls before
/// it fires, and each alert then goes quiet for a cooldown period.
/// </remarks>
/// <param name="log">
/// Optional trace of what the thresholds decided. An alert that does not
/// arrive is otherwise undiagnosable from outside: "the limit was never
/// crossed", "it was crossed but not for long enough" and "it fired and
/// Windows dropped the toast" look identical, and each needs a different
/// fix. Written at most once per metric per poll.
/// </param>
public sealed class AlertService(
    AppSettings settings, AlertDelivery deliver, Action<string>? log = null)
{
    public enum Metric { Cpu, Memory, Disk }

    /// <summary>Consecutive breaching polls per server and metric.</summary>
    private readonly Dictionary<string, int> _breachRun = [];
    /// <summary>When each alert last fired, so a persistent problem does not repeat.</summary>
    private readonly Dictionary<string, DateTime> _lastFired = [];
    /// <summary>Previous status per server, to detect transitions rather than states.</summary>
    private readonly Dictionary<Guid, bool> _previousOnline = [];

    /// <summary>
    /// A metric must breach this many polls in a row before it alerts.
    /// </summary>
    /// <remarks>
    /// At the default 5-second interval that is ~15 seconds of sustained load,
    /// which filters out the spike from a build or a backup starting.
    /// </remarks>
    private const int SustainedPolls = 3;
    private static readonly TimeSpan Cooldown = TimeSpan.FromMinutes(15);

    /// <summary>
    /// Called after each poll. Compares against the previous result and emits
    /// at most one notification per condition per cooldown.
    /// </summary>
    public void Evaluate(Server server, ServerStatus status, MetricSnapshot? snapshot)
    {
        if (!settings.NotificationsEnabled)
        {
            log?.Invoke("alerts: off, nothing evaluated");
            return;
        }

        var isOnline = status.IsOnline;
        var hadPrevious = _previousOnline.TryGetValue(server.Id, out var wasOnline);
        _previousOnline[server.Id] = isOnline;

        // A transition, not a state: without the "had previous" check the
        // first poll of a host that is already down would fire an alert for
        // something the user has not been told changed.
        if (settings.NotifyOnOffline && hadPrevious && wasOnline != isOnline)
        {
            if (isOnline)
            {
                Send(server.Id, $"recovered:{server.Id}", server.Name, Strings.Recovered, ignoreCooldown: true);
            }
            else
            {
                var reason = status.Reason;
                Send(
                    server.Id,
                    $"offline:{server.Id}",
                    server.Name,
                    reason.Length == 0 ? Strings.Offline : $"{Strings.Offline} — {reason}",
                    ignoreCooldown: true);
            }
        }

        // Thresholds only make sense for a host that answered.
        if (!isOnline || snapshot is null)
        {
            foreach (var metric in Enum.GetValues<Metric>()) _breachRun[$"{metric}:{server.Id}"] = 0;
            return;
        }

        // A server's own limit wins; null means it follows the global setting.
        Check(Metric.Cpu, snapshot.CpuPercent, server.CpuThreshold ?? settings.CpuThreshold, server);
        Check(Metric.Memory, snapshot.MemoryPercent, server.MemoryThreshold ?? settings.MemoryThreshold, server);
        Check(Metric.Disk, snapshot.DiskPercent, server.DiskThreshold ?? settings.DiskThreshold, server);
    }

    private void Check(Metric metric, double value, int limit, Server server)
    {
        var key = $"{metric}:{server.Id}";
        if (limit <= 0)
        {
            // 0 is "off for this metric", which is a different thing from
            // null — see Server.CpuThreshold.
            _breachRun[key] = 0;
            return;
        }
        if (value < limit)
        {
            // Recovering resets the run, so the next breach must build up again.
            _breachRun[key] = 0;
            return;
        }
        var run = _breachRun.GetValueOrDefault(key) + 1;
        _breachRun[key] = run;
        log?.Invoke(
            $"alerts: {server.Name} {metric} {value:F1}% over {limit}%, "
            + $"run {run}/{SustainedPolls}");
        // Exactly at the threshold, not at or above it: the cooldown handles
        // repetition, and firing on every later poll would defeat it.
        if (run != SustainedPolls) return;

        Send(server.Id, key, server.Name, Strings.Threshold(metric, value, limit));
    }

    public void Forget(Guid serverId)
    {
        _previousOnline.Remove(serverId);
        foreach (var metric in Enum.GetValues<Metric>())
        {
            _breachRun.Remove($"{metric}:{serverId}");
            _lastFired.Remove($"{metric}:{serverId}");
        }
        _lastFired.Remove($"offline:{serverId}");
        _lastFired.Remove($"recovered:{serverId}");
    }

    private void Send(Guid serverId, string key, string title, string body, bool ignoreCooldown = false)
    {
        if (!ignoreCooldown
            && _lastFired.TryGetValue(key, out var last)
            && DateTime.UtcNow - last < Cooldown)
        {
            return;
        }
        _lastFired[key] = DateTime.UtcNow;
        deliver(serverId, title, body);
    }
}
