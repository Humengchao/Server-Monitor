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

    // MARK: - The machine card's address row

    private static HostIdentity Reporting(params string[] addresses) =>
        new() { Addresses = [.. addresses] };

    [Fact]
    public void ANattedHostGetsItsPublicAddressBack()
    {
        // What the user saw: a machine card listing 10.0.18.16 and two Docker
        // bridges while the app was talking to the host on a public address it
        // knew and did not print. `hostname -I` cannot report an address the
        // host does not hold, so the endpoint is the only source for it.
        var shown = ServerDetailPage.MachineAddresses(
            Alias("203.0.113.10"), Reporting("10.0.18.16", "172.17.0.1", "172.18.0.1"));

        Assert.Equal("203.0.113.10", shown[0]);
        Assert.Equal(["203.0.113.10", "10.0.18.16", "172.17.0.1", "172.18.0.1"], shown);
    }

    [Fact]
    public void AHostWithItsOwnPublicAddressIsLeftAlone()
    {
        // The case that hid the bug: a host whose NIC holds a routable address
        // reports it itself, and prepending a duplicate would be the only
        // change visible.
        var shown = ServerDetailPage.MachineAddresses(
            Alias("203.0.113.10"), Reporting("203.0.113.10", "2604:a00:101:1::1"));

        Assert.Equal(["203.0.113.10", "2604:a00:101:1::1"], shown);
    }

    [Fact]
    public void ALanEndpointIsNotAnnouncedAsThoughItWerePublic()
    {
        // A host reached across the office. Its endpoint is already among the
        // addresses it reports, and even when it is not, it says nothing the
        // row does not already say.
        var shown = ServerDetailPage.MachineAddresses(
            Alias("192.168.1.20"), Reporting("192.168.1.20"));
        Assert.Equal(["192.168.1.20"], shown);
    }

    [Fact]
    public void AnUnresolvedAliasIsNotPrintedAsAnAddress()
    {
        // Before the first poll the endpoint is whatever the editor holds, and
        // for an ssh-config host that is a name. "web-01" under "IP addresses"
        // would be worse than the gap it fills.
        var shown = ServerDetailPage.MachineAddresses(
            Alias(string.Empty), Reporting("10.0.18.16"));
        Assert.Equal(["10.0.18.16"], shown);

        var named = new Server { Name = "web-01", Host = "web-01.example.com" };
        Assert.Equal(["10.0.18.16"], ServerDetailPage.MachineAddresses(named, Reporting("10.0.18.16")));
    }

    [Fact]
    public void AHostThatHasNotReportedYetStillShowsWhereItIs()
    {
        // First poll not in yet, or a Windows host that sends no address list:
        // the endpoint on its own is better than an empty row.
        Assert.Equal(
            ["203.0.113.10"],
            ServerDetailPage.MachineAddresses(Alias("203.0.113.10"), Reporting()));
    }

    [Theory]
    [InlineData("8")]          // TryParse says yes and hands back 0.0.0.8
    [InlineData("1.2.3")]      // 1.2.0.3
    [InlineData("010.1.1.1")]  // read as octal: 8.1.1.1
    public void ShorthandThatIsNotAnAddressIsNotPrintedAsOne(string host)
    {
        // IPAddress.TryParse accepts all three and returns something quite
        // different from what was written — measured, not assumed. Without the
        // round-trip check a host named "8" would appear under "IP addresses"
        // as an address nobody typed.
        var odd = new Server { Name = "odd", Host = host };
        Assert.Equal(["10.0.18.16"], ServerDetailPage.MachineAddresses(odd, Reporting("10.0.18.16")));
    }
}
