using System.IO;
using System.Windows;
using System.Windows.Media;
using System.Windows.Media.Imaging;

namespace ServerMonitor.App.Controls;

/// <summary>
/// Builds the application icon from <see cref="BrandMark"/>.
/// </summary>
/// <remarks>
/// The .ico has to be a file on disk before the compiler runs — MSBuild's
/// <c>ApplicationIcon</c> embeds it into the executable — so it cannot be
/// produced at run time and is committed under <c>Assets/</c>. The risk in a
/// committed binary is that it stops matching the drawing it came from, and
/// nobody notices because nothing reads both. A test renders these frames and
/// compares them against the committed file, which is what keeps the sidebar's
/// mark and the taskbar's the same picture.
///
/// No image tooling is involved. ICO is a header, a directory, and one payload
/// per frame; PNG payloads have been legal since Vista and are what every
/// icon over 48px uses anyway, so <see cref="PngBitmapEncoder"/> is the whole
/// of the encoding.
/// </remarks>
internal static class BrandIcon
{
    /// <summary>
    /// The frames Windows actually asks for.
    /// </summary>
    /// <remarks>
    /// 16 is the taskbar and the title bar, 24 and 32 Alt-Tab and the desktop,
    /// 48 Explorer's medium icons, 256 its extra-large ones and the installer.
    /// 20 and 40 exist for 125% and 250% scaling of the 16px slot, which is
    /// what a high-DPI laptop actually shows.
    /// </remarks>
    internal static readonly int[] Sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256];

    /// <summary>One frame, rasterised.</summary>
    internal static RenderTargetBitmap Frame(int size)
    {
        var visual = new DrawingVisual();
        using (var dc = visual.RenderOpen())
        {
            BrandMark.Draw(dc, size);
        }

        var bitmap = new RenderTargetBitmap(size, size, 96, 96, PixelFormats.Pbgra32);
        bitmap.Render(visual);
        bitmap.Freeze();
        return bitmap;
    }

    /// <summary>The whole .ico, ready to write.</summary>
    internal static byte[] Build()
    {
        var payloads = Sizes.Select(Png).ToList();

        using var stream = new MemoryStream();
        using var writer = new BinaryWriter(stream);

        // ICONDIR: reserved, type 1 (icon), count.
        writer.Write((ushort)0);
        writer.Write((ushort)1);
        writer.Write((ushort)Sizes.Length);

        // The directory is fixed-width, so every payload's offset is known
        // before any of them is written.
        var offset = 6 + (16 * Sizes.Length);
        for (var i = 0; i < Sizes.Length; i++)
        {
            // 256 does not fit in a byte and is encoded as 0 — the one piece
            // of this format that is not what it looks like.
            writer.Write((byte)(Sizes[i] >= 256 ? 0 : Sizes[i]));
            writer.Write((byte)(Sizes[i] >= 256 ? 0 : Sizes[i]));
            writer.Write((byte)0);      // palette entries; none, it is truecolour
            writer.Write((byte)0);      // reserved
            writer.Write((ushort)1);    // colour planes
            writer.Write((ushort)32);   // bits per pixel
            writer.Write(payloads[i].Length);
            writer.Write(offset);
            offset += payloads[i].Length;
        }

        foreach (var payload in payloads) writer.Write(payload);
        writer.Flush();
        return stream.ToArray();
    }

    private static byte[] Png(int size)
    {
        var encoder = new PngBitmapEncoder();
        encoder.Frames.Add(BitmapFrame.Create(Frame(size)));
        using var stream = new MemoryStream();
        encoder.Save(stream);
        return stream.ToArray();
    }
}
