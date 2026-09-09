using System.Net;
using System.Net.Http;
using System.Net.Http.Json;
using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Alerts;

/// <summary>
/// Delivers alert transitions to a user-configured URL.
/// </summary>
/// <remarks>
/// Ported from <c>WebhookNotifier</c> in
/// <c>web/backend/internal/services/alerts.go</c>, payload field for field, so
/// a receiver written against the web client works against this one unchanged.
///
/// Two of its guards are kept and one is deliberately dropped.
///
/// Kept: redirects are not followed. A public URL that 302s into the private
/// network would walk straight past any address check, so the check has to be
/// on the address actually contacted.
///
/// Kept: the scheme must be http or https and the host must be present. A
/// file:// or a URL with no host is a typo, and failing at save time is the
/// difference between "that is not a URL" and a webhook that silently never
/// arrives.
///
/// Dropped: the web refuses hosts that resolve to loopback, private,
/// link-local or CGNAT addresses. That guard exists because a *remote*
/// authenticated user could otherwise aim the server's own network scanner
/// through the alerting pipeline. This client is a desktop app configured by
/// the person sitting at it, who can already reach their own LAN by typing an
/// address into anything — and who quite reasonably wants to webhook the
/// self-hosted receiver on the same network as the servers being watched.
/// Keeping it here would block the normal case to prevent a threat that does
/// not exist on a single-user desktop.
/// </remarks>
public sealed class WebhookNotifier(Action<string>? log = null)
{
    private static readonly TimeSpan Timeout = TimeSpan.FromSeconds(10);

    /// <summary>
    /// One client for the process.
    /// </summary>
    /// <remarks>
    /// A new HttpClient per call leaks a socket in TIME_WAIT each time — the
    /// same reason GeoLookup keeps one.
    /// </remarks>
    private static readonly HttpClient Client = new(
        new HttpClientHandler { AllowAutoRedirect = false })
    {
        Timeout = Timeout,
    };

    /// <summary>What a receiver is sent. The web's field names, exactly.</summary>
    public sealed record Payload(
        string Status,
        string Rule,
        string Server,
        string Metric,
        double Value,
        double Threshold,
        string Comparator,
        string Message,
        DateTime Timestamp);

    /// <summary>
    /// Posts the transition, and never lets a delivery problem reach the caller.
    /// </summary>
    /// <remarks>
    /// Fire-and-forget on purpose: the evaluation loop runs on every poll and
    /// a webhook host that has gone away must not slow it down, let alone
    /// fail it. A failure is logged, which is the only place it can usefully
    /// go — there is no one watching at the moment it happens.
    /// </remarks>
    public void Send(string url, Payload payload)
    {
        if (url.Trim().Length == 0) return;
        _ = Task.Run(async () =>
        {
            try
            {
                await PostAsync(url, payload).ConfigureAwait(false);
            }
            catch (Exception error)
            {
                log?.Invoke($"alerts: webhook for '{payload.Rule}' failed: {error.Message}");
            }
        });
    }

    /// <summary>Posts and throws on anything that went wrong. For tests and for Send.</summary>
    public static async Task PostAsync(string url, Payload payload)
    {
        Validate(url);
        using var request = new HttpRequestMessage(HttpMethod.Post, url.Trim())
        {
            Content = JsonContent.Create(payload),
        };
        request.Headers.UserAgent.ParseAdd("server-monitor-alerts/1");

        using var response = await Client.SendAsync(request).ConfigureAwait(false);
        if ((int)response.StatusCode >= 300)
        {
            throw new HttpRequestException($"webhook responded {(int)response.StatusCode}");
        }
    }

    /// <summary>
    /// Rejects what is not a usable webhook target.
    /// </summary>
    /// <remarks>
    /// Exposed so the rule editor can refuse a bad URL at save time rather
    /// than letting the first alert be the thing that discovers it.
    /// </remarks>
    public static void Validate(string url)
    {
        var target = url.Trim();
        if (target.Length == 0) throw new ArgumentException("webhook URL is empty");
        if (!Uri.TryCreate(target, UriKind.Absolute, out var parsed))
            throw new ArgumentException("invalid webhook URL");
        if (parsed.Scheme != Uri.UriSchemeHttp && parsed.Scheme != Uri.UriSchemeHttps)
            throw new ArgumentException("webhook URL must use http or https");
        if (parsed.Host.Length == 0)
            throw new ArgumentException("webhook URL has no host");
    }

    /// <summary>Builds the payload for one transition.</summary>
    public static Payload For(AlertRule rule, AlertEvent open, bool resolved, string message) =>
        new(
            resolved ? "resolved" : "firing",
            rule.Name,
            open.ServerName,
            rule.Metric.ToString().ToLowerInvariant(),
            open.Value,
            rule.Threshold,
            rule.Comparator,
            message,
            DateTime.UtcNow);
}
