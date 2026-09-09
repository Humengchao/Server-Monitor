using System.Globalization;
using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Store;

namespace ServerMonitor.Core.L10n;

/// <summary>
/// Two-language UI strings.
/// </summary>
/// <remarks>
/// A dictionary rather than .NET resources (.resx + satellite assemblies) for
/// the reason the macOS build hand-rolled its table too: the language is
/// switchable at <em>runtime</em>, not only at launch, and satellite
/// assemblies would put half the app's text in files that a single-file
/// publish then has to carry as separate DLLs.
///
/// In Core rather than the App so the CLI and the alert service can use it —
/// alerts are produced off the view tree, where the macOS build needed a
/// separate <c>L10nBridge</c> with its own duplicated strings.
/// </remarks>
public static partial class Strings
{
    /// <summary>
    /// What "follow the system" resolves to.
    /// </summary>
    /// <remarks>
    /// Read once. A process's UI culture is fixed at launch, and
    /// <see cref="Get"/> runs for every label on every render — the macOS
    /// build found the equivalent lookup showing up in its profile for a value
    /// that never changes.
    /// </remarks>
    private static readonly AppLanguage SystemLanguage =
        CultureInfo.CurrentUICulture.TwoLetterISOLanguageName
            .Equals("zh", StringComparison.OrdinalIgnoreCase)
            ? AppLanguage.Zh
            : AppLanguage.En;

    private static AppLanguage _language = AppLanguage.System;

    /// <summary>
    /// The selected language. Set from settings; the App re-renders on change.
    /// </summary>
    public static AppLanguage Language
    {
        get => _language;
        set => _language = value;
    }

    /// <summary>The language actually in effect.</summary>
    public static AppLanguage Resolved =>
        _language == AppLanguage.System ? SystemLanguage : _language;

    public static bool IsChinese => Resolved == AppLanguage.Zh;

    /// <summary>
    /// The string for a key.
    /// </summary>
    /// <remarks>
    /// A missing key returns the key itself rather than throwing: a label
    /// reading <c>card.something</c> is a visible bug that ships, whereas an
    /// exception here would take down whichever pane is rendering. The
    /// key-coverage test is what stops one reaching a build.
    /// </remarks>
    public static string Get(string key) =>
        Table.TryGetValue(key, out var pair)
            ? IsChinese ? pair.Zh : pair.En
            : key;

    /// <summary>Single-placeholder interpolation, e.g. <c>Get("group.machines", "3")</c>.</summary>
    public static string Get(string key, string argument) =>
        Get(key).Replace("{}", argument, StringComparison.Ordinal);

    /// <summary>
    /// Every key in the table, so a test can walk it against the Swift side.
    /// </summary>
    public static IReadOnlyCollection<string> Keys => Table.Keys;

    // MARK: - Alert text
    //
    // Reached from AlertService, which runs off the view tree. These read
    // Resolved like everything else rather than keeping their own copy of the
    // language, which is what the macOS build's L10nBridge had to do.

    public static string Offline => IsChinese ? "已离线" : "Offline";
    public static string Recovered => IsChinese ? "已恢复在线" : "Back online";

    public static string AlertOpen => IsChinese ? "告警触发" : "Alert raised";
    public static string AlertResolved => IsChinese ? "已恢复" : "Resolved";

    /// <summary>
    /// The unit a metric's value is reported in.
    /// </summary>
    /// <returns>Empty for the dimensionless metrics.</returns>
    public static string AlertUnit(AlertMetric metric) => metric switch
    {
        AlertMetric.Cpu or AlertMetric.Memory or AlertMetric.Disk => "%",
        AlertMetric.Latency => "ms",
        _ => "",
    };

    /// <summary>What a metric is called in a message.</summary>
    public static string AlertMetricName(AlertMetric metric) => metric switch
    {
        AlertMetric.Cpu => IsChinese ? "CPU" : "CPU",
        AlertMetric.Memory => IsChinese ? "内存" : "memory",
        AlertMetric.Disk => IsChinese ? "磁盘" : "disk",
        AlertMetric.Load1 => IsChinese ? "负载" : "load",
        AlertMetric.Latency => IsChinese ? "延迟" : "latency",
        _ => IsChinese ? "离线" : "offline",
    };

    public static string Threshold(AlertService.Metric metric, double value, int limit)
    {
        var name = metric switch
        {
            AlertService.Metric.Cpu => "CPU",
            AlertService.Metric.Memory => IsChinese ? "内存" : "Memory",
            _ => IsChinese ? "磁盘" : "Disk",
        };
        var rounded = (int)Math.Round(value);
        return IsChinese
            ? $"{name} 持续超过 {limit}%（当前 {rounded}%）"
            : $"{name} above {limit}% (now {rounded}%)";
    }
}
