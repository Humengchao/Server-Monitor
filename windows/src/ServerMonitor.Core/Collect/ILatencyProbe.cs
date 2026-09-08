namespace ServerMonitor.Core.Collect;

/// <summary>
/// Measures network round trip to a host.
/// </summary>
/// <remarks>
/// An interface so the collector's tests do not fire real ICMP at addresses
/// that do not exist — three timeouts per poll is both slow and a measurement
/// of nothing.
/// </remarks>
public interface ILatencyProbe
{
    /// <summary>
    /// The round trip, or null when it cannot be measured honestly — the host
    /// filters ICMP, no socket was available, or a tunnel answered locally
    /// (R12). The caller then falls back to the remote-clock figure.
    /// </summary>
    Task<PingProbe.Reading?> MeasureAsync(string host, CancellationToken cancellationToken = default);
}

/// <summary>
/// A probe that measures nothing, so latency comes from the remote clocks.
/// </summary>
/// <remarks>
/// Used by the tests, and available to the app for a machine where raw sockets
/// are refused outright — the clock method always works, it just reads higher
/// because it includes any proxy hop.
/// </remarks>
public sealed class NoLatencyProbe : ILatencyProbe
{
    public Task<PingProbe.Reading?> MeasureAsync(
        string host, CancellationToken cancellationToken = default) =>
        Task.FromResult<PingProbe.Reading?>(null);
}
