namespace ServerMonitor.Core.Alerts;

/// <summary>Where a notification is handed off.</summary>
/// <remarks>
/// Injected so tests can observe what <em>would</em> be delivered without a
/// notification centre, and so Core does not depend on the Windows toast API.
/// <paramref name="serverId"/> is passed so the App can make clicking the
/// toast open that host.
/// </remarks>
public delegate void AlertDelivery(Guid serverId, string title, string body);
