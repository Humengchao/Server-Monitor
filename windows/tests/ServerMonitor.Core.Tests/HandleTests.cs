using System.Diagnostics;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// What repeatedly failing to reach a host costs.
/// </summary>
/// <remarks>
/// The case that runs forever: a host that is down is retried on every tick,
/// and anything the failed attempt forgets to release accumulates for as long
/// as the app is open. P5's background soak is what turns that into a visible
/// number — 30 minutes against five unreachable hosts climbed by about three
/// handles a minute — and this is the same question asked in a second rather
/// than half an hour.
///
/// The bound is loose but no longer arbitrary. It was 40, which was really a
/// noise budget: the count belongs to the process, so whatever else xUnit was
/// running in parallel landed in the measurement window, and the test failed
/// about one solution run in ten at 54 handles. With this assembly serialised
/// (see AssemblyInfo.cs) the measured delta is 0 across repeated runs, so what
/// is left is headroom for the runtime's own timers and thread-pool waits
/// rather than for other tests.
/// </remarks>
public class HandleTests
{
    /// <summary>
    /// Port 1 on the loopback: nothing listens there, and a refusal comes back
    /// in microseconds rather than after a connect timeout.
    /// </summary>
    private static SshTarget Unreachable() =>
        new(Guid.NewGuid(), "127.0.0.1", 1, "nobody", SshCredential.Password);

    [Fact]
    public async Task AHundredFailedConnectionsDoNotLeakHandles()
    {
        await using var transport = new SshNetTransport(new InMemoryCredentialStore());

        // A first round to let the runtime settle: the first connection
        // attempt initialises SSH.NET's algorithm tables and the socket stack,
        // which is a one-off cost and not a leak.
        for (var i = 0; i < 10; i++) await Attempt(transport);

        var before = Handles();
        for (var i = 0; i < 100; i++) await Attempt(transport);
        var after = Handles();

        // Before the fix this was a handle per attempt: the client that failed
        // to connect kept the socket it had opened, and only the catch's hop
        // release ran. 100 attempts leaked ~100 handles. Measured at 0 now, so
        // 20 is a fifth of what the original bug cost and twenty times the
        // observed value.
        Assert.True(
            after - before < 20,
            $"100 failed connections added {after - before} handles ({before} -> {after})");
    }

    [Fact]
    public async Task AFailedConnectionReportsRatherThanHangs()
    {
        await using var transport = new SshNetTransport(new InMemoryCredentialStore());
        var stopwatch = Stopwatch.StartNew();
        var error = await Record.ExceptionAsync(() =>
            transport.RunAsync("echo hello", Unreachable(), 5));
        stopwatch.Stop();

        Assert.NotNull(error);
        // A refused connection must not wait out the command timeout: the poll
        // loop's tick cap depends on a dead host failing fast.
        Assert.True(stopwatch.Elapsed < TimeSpan.FromSeconds(5), $"took {stopwatch.Elapsed}");
    }

    private static async Task Attempt(SshNetTransport transport)
    {
        try
        {
            await transport.RunAsync("echo hello", Unreachable(), 5);
        }
        catch (Exception)
        {
            // The point of the test.
        }
    }

    private static int Handles()
    {
        // Collect first, so a handle waiting on a finalizer is not counted as
        // one that leaked — the distinction that matters is whether releasing
        // it depends on the GC at all.
        GC.Collect();
        GC.WaitForPendingFinalizers();
        GC.Collect();
        using var self = Process.GetCurrentProcess();
        self.Refresh();
        return self.HandleCount;
    }
}
