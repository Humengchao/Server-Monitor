namespace ServerMonitor.Core.Collect;

public enum StatusKind { Unknown, Polling, Online, Offline }

/// <summary>Whether the last collection attempt for a server succeeded.</summary>
/// <remarks>
/// A struct with a kind rather than four classes: it is compared for equality
/// on every publish, and a value type makes that free and allocation-free on
/// the path a nine-host fleet walks every five seconds.
/// </remarks>
public readonly record struct ServerStatus(StatusKind Kind, DateTime At, string Reason)
{
    public static readonly ServerStatus Unknown = new(StatusKind.Unknown, default, "");
    public static readonly ServerStatus Polling = new(StatusKind.Polling, default, "");
    public static ServerStatus Online(DateTime at) => new(StatusKind.Online, at, "");
    public static ServerStatus Offline(string reason) => new(StatusKind.Offline, DateTime.UtcNow, reason);

    public bool IsOnline => Kind == StatusKind.Online;

    /// <summary>Whether the card for this server shows anything but a spinner.</summary>
    public bool HasVerdict => Kind is StatusKind.Online or StatusKind.Offline;
}
