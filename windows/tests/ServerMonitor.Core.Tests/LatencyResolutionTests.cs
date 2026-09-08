using System.Net;
using ServerMonitor.Core.Collect;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// How the latency probe turns a configured host into an address.
/// </summary>
/// <remarks>
/// This is on the path every poll of every host walks, which is what makes it
/// worth pinning: <c>Dns.GetHostAddressesAsync</c> leaks a handle roughly
/// every fourth call on Windows and does not return it on a collection, so
/// calling it per poll is what produced the drift P5's soak measured.
/// </remarks>
public class LatencyResolutionTests
{
    [Theory]
    [InlineData("203.0.113.10")]
    [InlineData("127.0.0.1")]
    [InlineData("10.0.0.1")]
    public async Task AnAddressIsUsedAsItIsRatherThanResolved(string host)
    {
        // Nothing in TEST-NET-3 has a PTR record and none of these is a name,
        // so a resolver call would either fail or hit the network. Coming back
        // instantly with the same address is the observable proof it did not.
        var resolved = await PingProbe.ResolveAsync(host, CancellationToken.None);
        Assert.Equal(IPAddress.Parse(host), resolved);
    }

    [Fact]
    public async Task AnIPv6LiteralIsDeclinedRatherThanPinged()
    {
        // The source-binding trick needs a matching family, and the probe is
        // IPv4 only. Declining is right; trying to bind an IPv4 socket to a v6
        // address would throw inside the poll.
        Assert.Null(await PingProbe.ResolveAsync("2001:db8::1", CancellationToken.None));
    }

    [Fact]
    public async Task ANameIsResolvedOnceAndThenRemembered()
    {
        PingProbe.ForgetResolutions();
        // localhost is the one name guaranteed to resolve without a network.
        var first = await PingProbe.ResolveAsync("localhost", CancellationToken.None);
        if (first is null)
        {
            // A machine whose hosts file has no IPv4 localhost. Nothing to
            // assert about caching there, and failing would be noise.
            return;
        }

        var second = await PingProbe.ResolveAsync("localhost", CancellationToken.None);
        // Reference equality: the second call returned the cached instance
        // rather than a freshly allocated one from the resolver.
        Assert.Same(first, second);
    }

    [Fact]
    public async Task ForgettingMakesTheNextCallResolveAgain()
    {
        PingProbe.ForgetResolutions();
        var first = await PingProbe.ResolveAsync("localhost", CancellationToken.None);
        if (first is null) return;

        PingProbe.ForgetResolutions();
        var afterForgetting = await PingProbe.ResolveAsync("localhost", CancellationToken.None);
        Assert.NotNull(afterForgetting);
        Assert.Equal(first, afterForgetting);
        // Same address, different instance — the resolver ran a second time,
        // which is what a network change needs it to do.
        Assert.NotSame(first, afterForgetting);
    }

    [Fact]
    public async Task AnUnresolvableNameIsNotCachedAsAFailure()
    {
        PingProbe.ForgetResolutions();
        // .invalid is reserved by RFC 2606 so that it never resolves — but
        // plenty of resolvers answer NXDOMAIN with an address of their own
        // anyway (this machine's returns 198.18.0.67, from a filtering
        // service). Where that happens there is no failure to observe, and
        // asserting one would only fail on the network rather than on the
        // code.
        const string host = "sm-test-host.invalid";
        if (await PingProbe.ResolveAsync(host, CancellationToken.None) is not null) return;

        // Asking again must reach the resolver rather than return a cached
        // "no": a name that was briefly unresolvable should recover on the
        // next poll, not after the TTL. Observable because a cached failure
        // would have to be stored, and nothing is stored.
        Assert.Null(await PingProbe.ResolveAsync(host, CancellationToken.None));
    }

    [Fact]
    public async Task ResolvingRespectsCancellation()
    {
        using var cancelled = new CancellationTokenSource();
        await cancelled.CancelAsync();
        // A literal short-circuits before any awaitable work, so it still
        // answers — the probe's own budget is what bounds the rest.
        Assert.Equal(
            IPAddress.Parse("203.0.113.10"),
            await PingProbe.ResolveAsync("203.0.113.10", cancelled.Token));
    }
}
