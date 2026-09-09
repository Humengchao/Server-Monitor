using System.IO;
using System.Windows.Media;
using ServerMonitor.App.Controls;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// The app's mark, and the icon generated from it.
/// </summary>
/// <remarks>
/// The mark is taken from the web client's brand lockup, so what is worth
/// asserting is that it still looks like that one: the two gradient stops,
/// white on top of them, and rounded corners rather than a square.
///
/// The icon matters more than it looks. It is a committed binary that MSBuild
/// embeds into the executable, so nothing at run time reads it and nothing
/// would notice it drifting away from the drawing it came from. That is what
/// <see cref="TheCommittedIconIsTheCurrentMark"/> is for.
/// </remarks>
public class BrandMarkTests
{
    /// <summary>Where the committed icon lives, from the test's bin directory.</summary>
    private static string IconPath => Path.GetFullPath(Path.Combine(
        AppContext.BaseDirectory,
        "..", "..", "..", "..", "..",
        "src", "ServerMonitor.App", "Assets", "ServerMonitor.ico"));

    private static BrandMark Mark(double edge) =>
        UiThread.Run(() => new BrandMark { Edge = edge });

    [Fact]
    public void TheMarkPaintsTheWebsGradient()
    {
        var frame = UiThread.Render(Mark(128), 128, 128);
        frame.Save("brand-128");

        Assert.True(
            frame.Contains(BrandMark.GradientFrom, tolerance: 24),
            "the light end of the gradient is missing");
        Assert.True(
            frame.Contains(BrandMark.GradientTo, tolerance: 24),
            "the dark end of the gradient is missing");
        Assert.True(frame.Contains(Colors.White, tolerance: 10), "the glyph is not drawn");
    }

    [Fact]
    public void TheTileHasRoundedCorners()
    {
        // The corner radius is a third of the edge, so a square tile and this
        // one differ by an obvious amount of empty pixel — which is also the
        // cheapest way to prove the tile geometry is being used at all.
        var frame = UiThread.Render(Mark(64), 64, 64);
        Assert.True(frame.Painted < 64 * 64, "the tile fills its whole box, so it is square");
        Assert.True(frame.Painted > 64 * 64 * 0.8, "too little is painted for a filled tile");
    }

    [Fact]
    public void EverySizeDrawsSomething()
    {
        // A glyph authored at one size and scaled can vanish at another: the
        // strokes fall below a pixel and the rasteriser drops them. 16 is the
        // one that would go first, and the one Windows shows most.
        foreach (var size in BrandIcon.Sizes)
        {
            var frame = UiThread.Render(Mark(size), size, size);
            Assert.True(
                frame.Contains(Colors.White, tolerance: 60),
                $"nothing white survived at {size}px — the glyph is gone");
        }
    }

    [Fact]
    public void TheSmallSizesDropTheDetailAndTheLargeOnesKeepIt()
    {
        // Two indicator dots and a divider, about a pixel each at 16px. Their
        // presence is the difference between a rack and a grey smudge, so the
        // small frames leave them out — which shows up as fewer distinct
        // colours per pixel painted than the same drawing at a size that can
        // hold them.
        var small = UiThread.Render(Mark(16), 16, 16);
        var large = UiThread.Render(Mark(64), 64, 64);
        Assert.True(large.Colours > small.Colours, "the large frame is no richer than the small one");
    }

    [Fact]
    public void TheCommittedIconIsTheCurrentMark()
    {
        // The drift guard. On a mismatch the fresh bytes go to artifacts/ —
        // gitignored — rather than over the source file, so regenerating stays
        // something a person decides to do.
        var fresh = UiThread.Run(BrandIcon.Build);
        Assert.True(File.Exists(IconPath), $"no icon at {IconPath}");
        var committed = File.ReadAllBytes(IconPath);

        if (committed.AsSpan().SequenceEqual(fresh)) return;

        var artifacts = Path.GetFullPath(Path.Combine(
            AppContext.BaseDirectory, "..", "..", "..", "..", "..", "artifacts"));
        Directory.CreateDirectory(artifacts);
        var written = Path.Combine(artifacts, "ServerMonitor.ico");
        File.WriteAllBytes(written, fresh);
        Assert.Fail(
            $"BrandMark has changed and {IconPath} no longer matches it. "
            + $"The regenerated icon is at {written}; copy it over the committed one.");
    }

    [Fact]
    public void TheIconCarriesEverySizeWindowsAsksFor()
    {
        var bytes = UiThread.Run(BrandIcon.Build);

        Assert.Equal(0, BitConverter.ToUInt16(bytes, 0));
        Assert.Equal(1, BitConverter.ToUInt16(bytes, 2));
        Assert.Equal(BrandIcon.Sizes.Length, BitConverter.ToUInt16(bytes, 4));

        for (var i = 0; i < BrandIcon.Sizes.Length; i++)
        {
            var entry = 6 + (16 * i);
            // 256 is written as 0: the field is one byte wide and the format
            // spends its only escape value on the largest size anyone uses.
            var expected = BrandIcon.Sizes[i] >= 256 ? 0 : BrandIcon.Sizes[i];
            Assert.Equal(expected, bytes[entry]);
            Assert.Equal(expected, bytes[entry + 1]);
            Assert.Equal(32, BitConverter.ToUInt16(bytes, entry + 6));

            var length = BitConverter.ToInt32(bytes, entry + 8);
            var offset = BitConverter.ToInt32(bytes, entry + 12);
            Assert.True(length > 0, $"the {BrandIcon.Sizes[i]}px frame is empty");
            Assert.True(offset + length <= bytes.Length, "a frame points past the end of the file");
            // PNG's magic, so a frame that failed to encode cannot pass as one
            // that did.
            Assert.Equal(0x89, bytes[offset]);
            Assert.Equal((byte)'P', bytes[offset + 1]);
        }
    }
}
