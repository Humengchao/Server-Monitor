namespace ServerMonitor.Core;

/// <summary>
/// Text helpers every parser here leans on.
/// </summary>
public static class Text
{
    /// <summary>
    /// Splits text into lines on any kind of line break, dropping empty ones.
    /// </summary>
    /// <remarks>
    /// Every parser in this assembly reads output from a remote host, so none
    /// of them may assume LF: Windows hosts answer entirely in CRLF, and a
    /// plain <c>Split('\n')</c> leaves a stray <c>\r</c> on the end of every
    /// line — which then fails to parse as a number, or becomes part of a
    /// hostname. Empty lines are dropped, matching the Swift
    /// <c>split(whereSeparator: \.isNewline)</c> the parsers were ported from;
    /// several sections rely on that (the key=value ones do not, which is
    /// exactly why they are keyed rather than positional).
    /// </remarks>
    public static List<string> Lines(this string? text)
    {
        var result = new List<string>();
        if (string.IsNullOrEmpty(text)) return result;

        var start = 0;
        for (var i = 0; i <= text.Length; i++)
        {
            if (i != text.Length && text[i] != '\n' && text[i] != '\r') continue;
            if (i > start) result.Add(text[start..i]);
            // \r\n is one break, not two: without this a CRLF document would
            // yield an empty line between every real one — harmless where
            // empties are dropped, but the same loop is the basis for
            // KeepEmptyLines below, where it is not.
            if (i != text.Length && text[i] == '\r' && i + 1 < text.Length && text[i + 1] == '\n') i++;
            start = i + 1;
        }
        return result;
    }

    /// <summary>
    /// The same split, keeping empty lines. Used where position matters —
    /// section splitting, which must not collapse a section that came back
    /// blank.
    /// </summary>
    public static List<string> KeepEmptyLines(this string? text)
    {
        var result = new List<string>();
        if (text is null) return result;

        var start = 0;
        for (var i = 0; i <= text.Length; i++)
        {
            if (i != text.Length && text[i] != '\n' && text[i] != '\r') continue;
            result.Add(text[start..i]);
            if (i != text.Length && text[i] == '\r' && i + 1 < text.Length && text[i + 1] == '\n') i++;
            start = i + 1;
        }
        return result;
    }

    /// <summary>
    /// Splits on runs of whitespace, dropping empties — the shape almost every
    /// <c>/proc</c> file and shell tool prints.
    /// </summary>
    public static string[] Fields(this string line) =>
        line.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);

    /// <summary>
    /// Parses a decimal integer, or 0. Total on purpose: a partially readable
    /// host should still report the metrics it did return, so a malformed
    /// field is a zero rather than an exception that loses the whole poll.
    /// </summary>
    public static long ToLong(this string? text) =>
        long.TryParse(text?.Trim(), System.Globalization.NumberStyles.Integer,
            System.Globalization.CultureInfo.InvariantCulture, out var value)
            ? value
            : 0;

    /// <summary>
    /// Parses a floating-point value, or 0. Invariant culture: the host prints
    /// <c>0.52</c> whatever the *local* machine's decimal separator is, and on
    /// a Chinese or German Windows the current culture would read that as 52.
    /// </summary>
    public static double ToDouble(this string? text) =>
        double.TryParse(text?.Trim(), System.Globalization.NumberStyles.Float,
            System.Globalization.CultureInfo.InvariantCulture, out var value)
            ? value
            : 0;

    /// <summary>
    /// Parses a floating-point value, or null — for a figure the host may
    /// legitimately not have (nvidia-smi prints <c>[N/A]</c> for a fan a
    /// datacentre card does not have, and 0% there would read as "fan
    /// stopped").
    /// </summary>
    public static double? ToDoubleOrNull(this string? text) =>
        double.TryParse(text?.Trim(), System.Globalization.NumberStyles.Float,
            System.Globalization.CultureInfo.InvariantCulture, out var value)
            ? value
            : null;

    public static int ToInt(this string? text) =>
        int.TryParse(text?.Trim(), System.Globalization.NumberStyles.Integer,
            System.Globalization.CultureInfo.InvariantCulture, out var value)
            ? value
            : 0;

    public static int? ToIntOrNull(this string? text) =>
        int.TryParse(text?.Trim(), System.Globalization.NumberStyles.Integer,
            System.Globalization.CultureInfo.InvariantCulture, out var value)
            ? value
            : null;

    /// <summary>
    /// Splits a <c>key=value</c> line at the first <c>=</c>. A value may
    /// itself contain <c>=</c> (a base64 tail, an adapter GUID), so only the
    /// first separator counts.
    /// </summary>
    public static bool TryKeyValue(this string line, out string key, out string value)
    {
        var index = line.IndexOf('=');
        if (index < 0)
        {
            key = value = string.Empty;
            return false;
        }
        key = line[..index].Trim();
        value = line[(index + 1)..].Trim();
        return true;
    }

    /// <summary>
    /// Quotes a value for a POSIX shell. Remote paths, container ids and
    /// public keys all reach a shell and none of them are ours, so nothing is
    /// interpolated raw.
    /// </summary>
    public static string ShellQuote(this string value) =>
        "'" + value.Replace("'", "'\\''") + "'";
}
