using System.Text.Json;

namespace ServerMonitor.Core.Collect;

/// <summary>Where an address is, as far as a public geolocation service knows.</summary>
public sealed class GeoInfo
{
    public string Ip { get; set; } = string.Empty;
    public string CountryCode { get; set; } = string.Empty;
    public string Country { get; set; } = string.Empty;
    public string Region { get; set; } = string.Empty;
    public string City { get; set; } = string.Empty;
    public string Organisation { get; set; } = string.Empty;

    public bool IsEmpty => CountryCode.Length == 0 && City.Length == 0 && Organisation.Length == 0;

    /// <summary>"San Jose, California" — the parts that exist, in reading order.</summary>
    public string Place =>
        string.Join(", ", new[] { City, Region }.Where(p => p.Length > 0));
}

/// <summary>
/// Looks an address up with ipwho.is.
/// </summary>
/// <remarks>
/// Deliberately <em>not</em> wired into the poll loop. Resolving a location
/// means telling a third party which servers this user runs, which is a
/// disclosure they should make on purpose — so it happens only when someone
/// presses the button on the IP card. The result is cached for the session and
/// the country is written back to the server row so the flag survives without
/// asking again.
/// </remarks>
public sealed class GeoLookup
{
    /// <summary>Injected so tests never touch the network.</summary>
    public delegate Task<string> Fetch(Uri url, CancellationToken cancellationToken);

    private readonly Fetch _fetch;

    public GeoLookup(Fetch? fetch = null) => _fetch = fetch ?? DefaultFetch;

    /// <summary>
    /// One client for the process.
    /// </summary>
    /// <remarks>
    /// A new <c>HttpClient</c> per call leaks a socket in TIME_WAIT each time,
    /// which is the standard .NET trap; the button is rate-limited by a human
    /// but the leak is not.
    /// </remarks>
    private static readonly HttpClient Client = new() { Timeout = TimeSpan.FromSeconds(12) };

    private static async Task<string> DefaultFetch(Uri url, CancellationToken cancellationToken)
    {
        using var response = await Client.GetAsync(url, cancellationToken).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw new GeoException($"Lookup service returned {(int)response.StatusCode}");
        }
        return await response.Content.ReadAsStringAsync(cancellationToken).ConfigureAwait(false);
    }

    public async Task<GeoInfo> LookupAsync(string address, CancellationToken cancellationToken = default)
    {
        var trimmed = address.Trim();
        if (trimmed.Length == 0) throw new GeoException("No address to look up");
        var url = new Uri($"https://ipwho.is/{Uri.EscapeDataString(trimmed)}");
        return Parse(await _fetch(url, cancellationToken).ConfigureAwait(false));
    }

    internal static GeoInfo Parse(string json)
    {
        JsonDocument document;
        try
        {
            document = JsonDocument.Parse(json);
        }
        catch (JsonException)
        {
            throw new GeoException("Lookup service sent something unreadable");
        }
        using (document)
        {
            var root = document.RootElement;
            if (root.ValueKind != JsonValueKind.Object)
            {
                throw new GeoException("Lookup service sent something unreadable");
            }
            // The service answers 200 with success:false for a private or
            // bogus address, so the status code alone does not say whether
            // this worked.
            if (root.TryGetProperty("success", out var success)
                && success.ValueKind == JsonValueKind.False)
            {
                throw new GeoException(Text(root, "message") is { Length: > 0 } message
                    ? message
                    : "Address not found");
            }

            var info = new GeoInfo
            {
                Ip = Text(root, "ip"),
                CountryCode = Text(root, "country_code").ToUpperInvariant(),
                Country = Text(root, "country"),
                Region = Text(root, "region"),
                City = Text(root, "city"),
            };
            if (root.TryGetProperty("connection", out var connection)
                && connection.ValueKind == JsonValueKind.Object)
            {
                var org = Text(connection, "org");
                info.Organisation = org.Length > 0 ? org : Text(connection, "isp");
            }
            return info;
        }
    }

    private static string Text(JsonElement element, string name) =>
        element.TryGetProperty(name, out var value) && value.ValueKind == JsonValueKind.String
            ? value.GetString() ?? string.Empty
            : string.Empty;

    /// <summary>
    /// Addresses a public service can say nothing useful about, so the card
    /// can explain that rather than firing a request that will fail.
    /// </summary>
    public static bool IsPrivate(string address)
    {
        var text = address.Trim();
        if (text is "localhost" or "::1") return true;
        if (text.StartsWith("127.", StringComparison.Ordinal)
            || text.StartsWith("10.", StringComparison.Ordinal)
            || text.StartsWith("192.168.", StringComparison.Ordinal)
            || text.StartsWith("169.254.", StringComparison.Ordinal))
        {
            return true;
        }
        // 172.16.0.0 – 172.31.255.255
        var parts = text.Split('.');
        return parts.Length == 4
            && parts[0] == "172"
            && parts[1].ToIntOrNull() is { } second
            && second is >= 16 and <= 31;
    }
}

public sealed class GeoException(string message) : Exception(message);
