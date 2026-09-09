using ServerMonitor.Core.Alerts;
using ServerMonitor.Core.Model;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The webhook target check and the payload a receiver sees.
/// </summary>
/// <remarks>
/// The payload is asserted field for field because its whole purpose is to
/// match the web client's, so a receiver written against one works against the
/// other. If this drifts, nothing in either codebase notices.
/// </remarks>
public class WebhookTests
{
    private static AlertRule Rule() => new()
    {
        Name = "cpu high",
        Metric = AlertMetric.Cpu,
        Comparator = ">",
        Threshold = 90,
        DurationSeconds = 300,
    };

    private static AlertEvent Event() => new()
    {
        ServerName = "web-01",
        Value = 97.4,
    };

    [Theory]
    [InlineData("https://example.com/hook")]
    [InlineData("http://192.168.1.10:9000/alerts")]
    public void AUsableTargetIsAccepted(string url) => WebhookNotifier.Validate(url);

    [Fact]
    public void APrivateAddressIsAllowedHere()
    {
        // The web backend refuses these, because there a *remote* user could
        // aim the server's own network scanner through the alerting pipeline.
        // This is a desktop app configured by the person at the keyboard, who
        // can already reach their LAN by typing an address into anything — and
        // whose webhook receiver is very likely on the same network as the
        // machines being watched. So the scheme-and-host checks stay and the
        // address-rangeness check does not.
        WebhookNotifier.Validate("http://10.0.0.5/hook");
        WebhookNotifier.Validate("http://localhost:8080/hook");
    }

    [Theory]
    [InlineData("")]
    [InlineData("   ")]
    [InlineData("not a url")]
    [InlineData("file:///etc/passwd")]
    [InlineData("ftp://example.com/hook")]
    public void AnythingThatIsNotAnHttpUrlIsRefused(string url) =>
        Assert.Throws<ArgumentException>(() => WebhookNotifier.Validate(url));

    [Fact]
    public void TheFiringPayloadMatchesTheWebs()
    {
        var payload = WebhookNotifier.For(Rule(), Event(), resolved: false, "web-01 CPU 97.4% > 90%");

        Assert.Equal("firing", payload.Status);
        Assert.Equal("cpu", payload.Metric);
        Assert.Equal(97.4, payload.Value);
        Assert.Equal(90, payload.Threshold);
        Assert.Equal(">", payload.Comparator);
        Assert.Equal("web-01 CPU 97.4% > 90%", payload.Message);
        Assert.Equal("web-01", payload.Server);
    }

    [Fact]
    public void ResolvingSaysSoInTheStatus()
    {
        // The one field a receiver switches on. The web uses these two exact
        // words.
        Assert.Equal(
            "resolved",
            WebhookNotifier.For(Rule(), Event(), resolved: true, "recovered").Status);
    }

    [Fact]
    public void TheMetricIsLowercaseLikeTheWebsStringEnum()
    {
        // Windows models the metric as an enum and the web as a lowercase
        // string; the wire is the web's.
        var rule = Rule();
        rule.Metric = AlertMetric.Load1;
        Assert.Equal("load1", WebhookNotifier.For(rule, Event(), false, "x").Metric);
    }
}
