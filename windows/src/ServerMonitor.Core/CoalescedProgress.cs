namespace ServerMonitor.Core;

/// <summary>At most one queued UI callback, carrying the newest progress.</summary>
public sealed class CoalescedProgress<T>(Action<Action> post, Action<T> report) : IProgress<T>, IDisposable
{
    private readonly object _gate = new();
    private T? _latest;
    private bool _queued;
    private bool _disposed;

    public void Report(T value)
    {
        lock (_gate)
        {
            if (_disposed) return;
            _latest = value;
            if (_queued) return;
            _queued = true;
        }
        post(Deliver);
    }

    private void Deliver()
    {
        T value;
        lock (_gate)
        {
            _queued = false;
            if (_disposed) return;
            value = _latest!;
        }
        report(value);
    }

    public void Dispose()
    {
        lock (_gate) { _disposed = true; _latest = default; }
    }
}
