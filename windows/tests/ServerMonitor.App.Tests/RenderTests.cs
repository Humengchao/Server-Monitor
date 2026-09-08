using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.App.Theme;
using ServerMonitor.Core;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// Every self-drawn control, rasterised offscreen.
/// </summary>
/// <remarks>
/// A control whose <c>OnRender</c> throws, whose geometry is empty, or that
/// asks for a style key that does not exist compiles perfectly and then draws
/// nothing — the failure only appears on screen. These render each control
/// into a bitmap and check the pixels, which is as close to looking at it as a
/// test gets. They are not screenshot comparisons: exact pixels depend on the
/// machine's text rendering, so the assertions are about what must be true of
/// any correct drawing — it painted, it used the colour it was given, it is
/// not a flat rectangle.
/// </remarks>
public class RenderTests
{
    [Fact]
    public void ARingGaugeDrawsAnArcInItsValueColour()
    {
        var tint = Palette.ForPercent(82);
        var gauge = UiThread.Run(() => new RingGauge
        {
            Value = 82,
            Diameter = 96,
            Thickness = 10,
            Width = 96,
            Height = 96,
        });
        var frame = UiThread.Render(gauge, 96, 96);
        frame.Save("ring-82");

        Assert.True(frame.Painted > 500, $"only {frame.Painted} pixels painted");
        Assert.True(frame.Contains(tint), "the arc is not in the value's colour");
        // The ring is a ring, not a disc: a point inside the track and clear
        // of the centred number must be empty. Up and to the right at 45°,
        // which no digit of "82%" reaches.
        Assert.True(
            frame.Pixels[((((48 - 21) * 96) + 48 + 21) * 4) + 3] < 40,
            "the ring is filled in rather than hollow");
        // And the number is drawn — the gauge is unreadable without it.
        Assert.True(frame.Contains(Palette.Text, tolerance: 24), "the percentage is missing");
    }

    /// <summary>
    /// A country renders as its letters, not as a flag that is not there.
    /// </summary>
    /// <remarks>
    /// The model carries <c>Server.Flag</c>, a regional-indicator pair, which
    /// on macOS is a flag and on Windows is two boxed capitals: no shipped
    /// font has those glyphs. The server detail header asked for it anyway
    /// until this test went in alongside the fix. Asserted on the pixels
    /// because the failure was entirely a rendering one — the string was
    /// exactly what macOS wanted.
    /// </remarks>
    [Fact]
    public void ACountryBadgeDrawsItsLettersAndNoEmoji()
    {
        var badge = UiThread.Run(() => Ui.CountryBadge("cn"));
        var frame = UiThread.Render(badge, 44, 22);
        frame.Save("country-badge");

        // The letters are drawn, in the secondary ink the badge asks for.
        Assert.True(frame.Painted > 120, $"only {frame.Painted} pixels painted");
        Assert.True(frame.Contains(Palette.Secondary, tolerance: 40), "the code is not drawn");

        // Upper-cased on the way in, so a lower-case code in the editor does
        // not show up as a lower-case badge.
        var text = UiThread.Run(() => ((TextBlock)badge.Child).Text);
        Assert.Equal("CN", text);

        // And nothing in the tree is a regional-indicator codepoint, which is
        // what the emoji path put here.
        Assert.DoesNotContain(text, c => c is >= (char)0xD83C and <= (char)0xD83D);
    }

    [Fact]
    public void ARingGaugeAtZeroStillDrawsItsTrack()
    {
        // The track is what makes an idle host look idle rather than broken.
        var frame = UiThread.Render(
            UiThread.Run(() => new RingGauge { Value = 0, Diameter = 72, Width = 72, Height = 72 }),
            72, 72);
        frame.Save("ring-0");
        Assert.True(frame.Painted > 200, $"only {frame.Painted} pixels painted");
    }

    [Theory]
    [InlineData(0)]
    [InlineData(1)]
    [InlineData(50)]
    [InlineData(99.9)]
    [InlineData(100)]
    [InlineData(140)]
    [InlineData(-5)]
    [InlineData(double.NaN)]
    public void ARingGaugeSurvivesEveryValue(double value)
    {
        // Out-of-range and NaN reach here for real: a percentage computed from
        // a host that reported zero total memory is NaN, and a rate sampled
        // across a counter reset can exceed 100.
        var frame = UiThread.Render(
            UiThread.Run(() => new RingGauge { Value = value, Diameter = 64, Width = 64, Height = 64 }),
            64, 64);
        Assert.True(frame.Painted > 100, $"{value} drew only {frame.Painted} pixels");
    }

    [Fact]
    public void AMeterBarFillsInProportion()
    {
        int Painted(double value) => UiThread.Render(
            UiThread.Run(() => new MeterBar { Value = value, Width = 200, Height = 6 }),
            200, 6).Painted;

        var quarter = Painted(25);
        var full = Painted(100);
        Assert.True(quarter < full, $"25% painted {quarter}, 100% painted {full}");
        Assert.True(full > 600, $"a full bar painted only {full} pixels");
    }

    [Fact]
    public void AHistoryChartDrawsALineAndItsFill()
    {
        var values = Enumerable.Range(0, 240)
            .Select(i => 40 + (30 * Math.Sin(i / 12.0)))
            .ToList();
        var chart = UiThread.Run(() =>
        {
            var control = new HistoryChart { Width = 600, Height = 140 };
            control.Show(
                [new ChartSeries("cpu", values, Palette.Accent)],
                maximum: 100,
                format: Format.Percent);
            return control;
        });
        var frame = UiThread.Render(chart, 600, 140);
        frame.Save("chart-cpu");

        Assert.True(frame.Painted > 5000, $"only {frame.Painted} pixels painted");
        // Many colours because the fill is a gradient under an antialiased
        // line; a solid block or an empty frame would be a handful.
        Assert.True(frame.Colours > 20, $"only {frame.Colours} distinct colours");
        Assert.True(frame.Contains(Palette.Accent, tolerance: 30), "the line is not in its colour");
    }

    [Fact]
    public void AHistoryChartWithNoDataDrawsAnEmptyState()
    {
        var chart = UiThread.Run(() =>
        {
            var control = new HistoryChart { Width = 400, Height = 140 };
            control.Show([]);
            return control;
        });
        // An empty chart must not throw and must not be blank: a host polled
        // once has no history, and a blank rectangle reads as a bug.
        var frame = UiThread.Render(chart, 400, 140);
        frame.Save("chart-empty");
        Assert.True(frame.Painted > 20, "an empty chart drew nothing at all");
    }

    [Fact]
    public void AHistoryChartWithOneFlatSeriesDoesNotDivideByZero()
    {
        // Every value identical means the auto-scaled range is zero, which is
        // the classic place a chart produces NaN coordinates and draws nothing.
        var chart = UiThread.Run(() =>
        {
            var control = new HistoryChart { Width = 400, Height = 140 };
            control.Show([new ChartSeries("flat", [7, 7, 7, 7, 7], Palette.Online)]);
            return control;
        });
        var frame = UiThread.Render(chart, 400, 140);
        frame.Save("chart-flat");
        Assert.True(frame.Painted > 200, $"a flat series drew only {frame.Painted} pixels");
    }

    [Fact]
    public void ABarChartDrawsBothDirections()
    {
        var chart = UiThread.Run(() =>
        {
            var control = new BarChart { Width = 500, Height = 160 };
            control.Show([
                ("Mon", 4_000_000_000d, 900_000_000d),
                ("Tue", 2_500_000_000d, 300_000_000d),
                ("Wed", 6_100_000_000d, 1_200_000_000d),
            ]);
            return control;
        });
        var frame = UiThread.Render(chart, 500, 160);
        frame.Save("chart-traffic");

        Assert.True(frame.Painted > 2000, $"only {frame.Painted} pixels painted");
        Assert.True(frame.Contains(Palette.Accent, tolerance: 40), "the download bars are missing");
    }

    [Fact]
    public void ACardOfEveryTextStyleRenders()
    {
        // The one test that catches a renamed style key: every one of these is
        // a FindResource by string, which the compiler cannot check.
        var card = UiThread.Run(() => Ui.Card(Ui.Rows(6,
            Ui.Headline("web-01"),
            Ui.Title("web-01"),
            Ui.Text("Ubuntu 24.04.1 LTS"),
            Ui.Caption("8 cores"),
            Ui.Tertiary("192.0.2.10"),
            Ui.Mono("SHA256:0000000000000000000000000000000000000000000"),
            Ui.Number("41.2%", 22, Palette.Warning),
            Ui.TagChips(["prod", "db", "eu"]),
            Ui.Columns(6, Ui.Dot(Palette.Online), Ui.Text("online")),
            Ui.Fact("", "12 days"),
            Ui.Separator(),
            Ui.Link("docs", "https://example.invalid"))));
        var frame = UiThread.Render(card, 360, 400);
        frame.Save("card-text");

        Assert.True(frame.Painted > 4000, $"only {frame.Painted} pixels painted");
        Assert.True(frame.Contains(Palette.Warning, tolerance: 40), "the big number is not tinted");
    }

    [Fact]
    public void EveryButtonStyleRenders()
    {
        var row = UiThread.Run(() => Ui.Columns(8,
            Ui.Button("Standard", () => { }),
            Ui.Accent("Accent", () => { }),
            Ui.Quiet("Quiet", () => { }),
            Ui.Danger("Danger", () => { })));
        var frame = UiThread.Render(row, 420, 40);
        frame.Save("buttons");
        Assert.True(frame.Painted > 1500, $"only {frame.Painted} pixels painted");
        Assert.True(frame.Contains(Palette.Accent, tolerance: 20), "the accent button is not accented");
    }

    [Fact]
    public void ADangerousButtonIsActuallyRed()
    {
        // Regression: the implicit TextBlock style reaches the label a
        // ContentPresenter builds from a string, and a style setter beats
        // inheritance — so this button's own Foreground was ignored and
        // "Delete" rendered in the ordinary text colour. See Text.Control.
        var frame = UiThread.Render(
            UiThread.Run(() => Ui.Danger("Delete", () => { })), 120, 34);
        frame.Save("button-danger");
        Assert.True(frame.Contains(Palette.Offline, tolerance: 24), "the label is not in the danger colour");
        Assert.False(frame.Contains(Palette.Text, tolerance: 6), "the label is in the ordinary text colour");
    }

    [Fact]
    public void AFormOfEveryInputRenders()
    {
        var form = UiThread.Run(() => Ui.Rows(0,
            Ui.Field("server.name", Ui.Input("web-01")),
            Ui.Field("server.password", Ui.Secret()),
            Ui.Field("server.port", Ui.Input("22", 70), "server.osHelp"),
            Ui.Toggle("Start with Windows", value: true, _ => { }),
            Ui.Picker(new[] { "a", "b" }, "a", s => s, _ => { })));
        var frame = UiThread.Render(form, 420, 320);
        frame.Save("form");
        Assert.True(frame.Painted > 2000, $"only {frame.Painted} pixels painted");
    }

    [Fact]
    public void AGridOfCardsLandsInColumnsThatBothFill()
    {
        var grid = UiThread.Run(() =>
        {
            var panel = new StaticGrid { MinimumColumnWidth = 340, Spacing = 14 };
            for (var i = 0; i < 6; i++)
            {
                panel.Weights.Add(i % 2 == 0 ? 1 : 3);
                panel.Children.Add(Ui.Card(Ui.Rows(4,
                    Ui.Title($"host-{i}"),
                    new RingGauge { Value = i * 15, Diameter = 60 })));
            }
            return panel;
        });
        var frame = UiThread.Render(grid, 760, 600);
        frame.Save("grid");

        // 760px fits two 340px columns. Both halves must have paint in them: a
        // broken assignment piles every card into the first column.
        int PaintedIn(int fromX, int toX)
        {
            var count = 0;
            for (var y = 0; y < frame.Height; y++)
            {
                for (var x = fromX; x < toX; x++)
                {
                    if (frame.Pixels[((((y * frame.Width) + x) * 4) + 3)] > 8) count++;
                }
            }
            return count;
        }

        Assert.True(PaintedIn(0, 380) > 2000, "the left column is empty");
        Assert.True(PaintedIn(380, 760) > 2000, "the right column is empty");
    }
}
