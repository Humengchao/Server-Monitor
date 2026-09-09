using ServerMonitor.App.Views;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// Which address the IP-location card asks about.
/// </summary>
/// <remarks>
/// The card took the first address the host reported about itself. On a cloud
/// VM that is the NAT address — 10.x, plus whatever bridges Docker has made —
/// so the card said "a private address, no public service can locate it" and
/// withheld its lookup button, directly underneath the public endpoint the
/// same card had already printed one line above. It read as the app
/// contradicting itself, and it did this on nine of eleven real hosts.
///
/// macOS reads the endpoint and its comment says why: "what the app actually
/// connects to, which is the address whose location is meaningful — not the
/// host's own LAN addresses". The port had inverted it.
/// </remarks>
public class IpLocationTests
{
    private static Server Alias(string host) => new()
    {
        Name = "web-01",
        AuthKind = AuthKind.SshConfigAlias,
        SshAlias = "web-01",
        Username = "root",
        Host = host,
    };

    [Fact]
    public void TheEndpointIsWhatGetsLookedUp()
    {
        Assert.Equal("203.0.113.10", ServerDetailPage.LocatableAddress(Alias("203.0.113.10")));
    }

    [Fact]
    public void APublicEndpointIsNotCalledPrivate()
    {
        // The visible symptom, asserted end to end: with the endpoint chosen,
        // the card takes its lookup branch instead of its "private" one.
        Assert.False(GeoLookup.IsPrivate(ServerDetailPage.LocatableAddress(Alias("203.0.113.10"))));
    }

    [Fact]
    public void AnUnresolvedHostFallsBackToItsAlias()
    {
        // Before the first poll there is no address at all. The alias is at
        // least something to show, and is what macOS falls back to.
        Assert.Equal("web-01", ServerDetailPage.LocatableAddress(Alias(string.Empty)));
    }

    [Fact]
    public void AHostThatReallyIsOnTheLanStillSaysSo()
    {
        // The message is not wrong, only misapplied: a server actually reached
        // at a LAN address must keep it, or the card would offer a lookup that
        // cannot succeed.
        Assert.True(GeoLookup.IsPrivate(ServerDetailPage.LocatableAddress(Alias("192.168.1.20"))));
    }
}
