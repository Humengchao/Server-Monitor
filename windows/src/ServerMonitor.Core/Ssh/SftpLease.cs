using Renci.SshNet;

namespace ServerMonitor.Core.Ssh;

/// <summary>Keeps a pooled connection alive for the complete SFTP operation.</summary>
public sealed class SftpLease : IDisposable
{
    private Action? _release;
    public ISftpClient Client { get; }
    internal SftpLease(ISftpClient client, Action release)
    {
        Client = client;
        _release = release;
    }
    public void Dispose() => Interlocked.Exchange(ref _release, null)?.Invoke();
}

internal sealed class SftpActivity
{
    private readonly object _gate = new();
    private readonly Func<DateTime> _now;
    private int _active;
    private DateTime _lastUsed;

    internal SftpActivity(Func<DateTime>? now = null)
    {
        _now = now ?? (() => DateTime.UtcNow);
        _lastUsed = _now();
    }

    public SftpLease Acquire(ISftpClient client)
    {
        lock (_gate) _active++;
        return new SftpLease(client, () =>
        {
            lock (_gate)
            {
                _lastUsed = _now();
                _active--;
            }
        });
    }

    public bool CanSweep(DateTime now, TimeSpan idleTimeout, bool connected)
    {
        lock (_gate) return _active == 0 && (!connected || now - _lastUsed >= idleTimeout);
    }
}
