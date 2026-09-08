using System.Globalization;

namespace ServerMonitor.Core;

/// <summary>
/// Display helpers shared by the tiles, charts and tables.
/// </summary>
/// <remarks>
/// In Core rather than the App because the CLI prints the same figures, and
/// because these are pure functions worth unit-testing. Every format string
/// uses the invariant culture: a Chinese Windows would otherwise render
/// "3.2 GB" as "3,2 GB" in some places and not others depending on which
/// thread built the string.
/// </remarks>
public static class Format
{
    private static readonly CultureInfo Invariant = CultureInfo.InvariantCulture;

    /// <summary>
    /// Binary byte sizes: 1 KB = 1024 B, matching what the shell tools report.
    /// </summary>
    public static string Bytes(long value) => Bytes((double)value);

    public static string Bytes(double value)
    {
        if (!double.IsFinite(value) || value <= 0) return "0 B";
        string[] units = ["B", "KB", "MB", "GB", "TB", "PB"];
        var index = Math.Min((int)(Math.Log(value) / Math.Log(1024)), units.Length - 1);
        var amount = value / Math.Pow(1024, index);
        return index == 0
            ? string.Format(Invariant, "{0:F0} {1}", amount, units[index])
            : string.Format(Invariant, "{0:F1} {1}", amount, units[index]);
    }

    public static string Rate(double bytesPerSecond) => $"{Bytes(bytesPerSecond)}/s";

    /// <summary>
    /// Rate split into number and unit so a card can typeset them at different
    /// weights, e.g. ("5.66", "K/s").
    /// </summary>
    public static (string Amount, string Unit) RateParts(double bytesPerSecond)
    {
        if (!double.IsFinite(bytesPerSecond) || bytesPerSecond <= 0) return ("0", "B/s");
        string[] units = ["B", "K", "M", "G", "T"];
        var index = Math.Min((int)(Math.Log(bytesPerSecond) / Math.Log(1024)), units.Length - 1);
        var amount = bytesPerSecond / Math.Pow(1024, index);
        // Three significant figures keeps the column width stable.
        var text = amount >= 100 ? amount.ToString("F0", Invariant)
            : amount >= 10 ? amount.ToString("F1", Invariant)
            : amount.ToString("F2", Invariant);
        return (text, $"{units[index]}/s");
    }

    /// <summary>Whole days, as the cards show uptime ("126 Days").</summary>
    public static string UptimeDays(long seconds, bool chinese)
    {
        var days = Math.Max(0, seconds / 86_400);
        return chinese ? $"{days} 天" : $"{days} Days";
    }

    /// <summary>
    /// Regional-indicator flag for an ISO 3166-1 alpha-2 code, or "" if unset.
    /// </summary>
    public static string Flag(string? countryCode)
    {
        var code = (countryCode ?? string.Empty).Trim().ToUpperInvariant();
        if (code.Length != 2 || !code.All(char.IsLetter)) return string.Empty;
        // Each letter maps to its regional indicator symbol, which is above
        // the BMP — so each one is a surrogate pair, not a char.
        return string.Concat(code.Select(c => char.ConvertFromUtf32(0x1F1E6 + (c - 'A'))));
    }

    public static string Percent(double value) =>
        string.Format(Invariant, "{0:F1}%", Math.Clamp(value, 0, 100));

    public static string Load(double value) => value.ToString("F2", Invariant);

    public static string Latency(double milliseconds) =>
        milliseconds <= 0 ? "—" : string.Format(Invariant, "{0:F0} ms", milliseconds);

    /// <summary>Compact uptime, e.g. "12d 4h" or "3h 20m".</summary>
    public static string Uptime(long seconds, bool chinese)
    {
        if (seconds <= 0) return "—";
        var days = seconds / 86_400;
        var hours = seconds % 86_400 / 3600;
        var minutes = seconds % 3600 / 60;
        if (days > 0) return chinese ? $"{days} 天 {hours} 小时" : $"{days}d {hours}h";
        if (hours > 0) return chinese ? $"{hours} 小时 {minutes} 分" : $"{hours}h {minutes}m";
        return chinese ? $"{minutes} 分钟" : $"{minutes}m";
    }

    /// <summary>"3.2 GB / 16.0 GB"</summary>
    public static string Usage(long used, long total) =>
        total > 0 ? $"{Bytes(used)} / {Bytes(total)}" : Bytes(used);

    /// <summary>
    /// A tag's colour index, stable across launches and across the two clients.
    /// </summary>
    /// <remarks>
    /// FNV-1a over the lowercased text, taken modulo the palette size. The
    /// hash is spelled out rather than using <c>string.GetHashCode</c> for two
    /// reasons: .NET randomises string hashing per process, so a tag would
    /// change colour on every launch; and the macOS build computes exactly
    /// this, so "prod" is the same colour on both clients. Do not "improve"
    /// the constants.
    /// </remarks>
    public static int TagColorIndex(string tag, int paletteSize)
    {
        if (paletteSize <= 0) return 0;
        var hash = 0xcbf29ce484222325UL;
        foreach (var b in System.Text.Encoding.UTF8.GetBytes(tag.ToLowerInvariant()))
        {
            hash = (hash ^ b) * 0x100000001b3UL;
        }
        return (int)(hash % (ulong)paletteSize);
    }
}
