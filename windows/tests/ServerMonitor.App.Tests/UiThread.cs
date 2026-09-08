using System.IO;
using System.Windows;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Windows.Threading;

namespace ServerMonitor.App.Tests;

/// <summary>
/// One STA thread with a live dispatcher, shared by every render test.
/// </summary>
/// <remarks>
/// WPF objects have thread affinity and an <see cref="Application"/> may only
/// be built on an STA thread, while xUnit runs tests on MTA pool threads. So
/// the tests do not touch WPF themselves: they hand a closure to
/// <see cref="Run{T}"/> and it runs there.
///
/// The <see cref="Application"/> exists purely to own the resource dictionary.
/// Every style in <c>Theme/Styles.xaml</c> is looked up by key at
/// construction, so a control built without it throws rather than rendering
/// unstyled — which is precisely why these tests catch a missing or misnamed
/// resource that a compile does not.
/// </remarks>
internal static class UiThread
{
    private static Dispatcher? _dispatcher;
    private static readonly Lock Gate = new();

    private static Dispatcher Dispatcher
    {
        get
        {
            lock (Gate)
            {
                if (_dispatcher is { } existing) return existing;

                var ready = new ManualResetEventSlim();
                var thread = new Thread(() =>
                {
                    _dispatcher = Dispatcher.CurrentDispatcher;

                    var app = new Application { ShutdownMode = ShutdownMode.OnExplicitShutdown };
                    // The one dictionary App.xaml merges. Controls.xaml
                    // brings in Styles.xaml itself.
                    app.Resources.MergedDictionaries.Add(new ResourceDictionary
                    {
                        Source = new Uri(
                            "pack://application:,,,/ServerMonitor;component/Theme/Controls.xaml"),
                    });
                    // The brush resources are written at runtime by
                    // App.ApplyTheme, which is not running here — so the same
                    // keys are filled in from the same palette.
                    ServerMonitor.App.Theme.ThemeBrushes.Apply(app.Resources, dark: true);

                    ready.Set();
                    Dispatcher.Run();
                })
                {
                    IsBackground = true,
                    Name = "ui-tests",
                };
                thread.SetApartmentState(ApartmentState.STA);
                thread.Start();
                ready.Wait();
                return _dispatcher!;
            }
        }
    }

    /// <summary>Runs <paramref name="work"/> on the UI thread and returns its result.</summary>
    public static T Run<T>(Func<T> work) => Dispatcher.Invoke(work);

    public static void Run(Action work) => Dispatcher.Invoke(work);

    /// <summary>
    /// Measures, arranges and rasterises an element offscreen.
    /// </summary>
    /// <remarks>
    /// The measure/arrange pair is not optional: <c>OnRender</c> reads
    /// <c>ActualWidth</c>, and every self-drawn control here returns early at
    /// zero size — so an unarranged visual renders as a blank bitmap and the
    /// test would pass while drawing nothing.
    /// </remarks>
    public static Rendered Render(FrameworkElement element, double width, double height)
    {
        return Run(() =>
        {
            element.Measure(new Size(width, height));
            element.Arrange(new Rect(0, 0, width, height));
            element.UpdateLayout();

            var bitmap = new RenderTargetBitmap(
                (int)width, (int)height, 96, 96, PixelFormats.Pbgra32);
            bitmap.Render(element);

            var stride = bitmap.PixelWidth * 4;
            var pixels = new byte[stride * bitmap.PixelHeight];
            bitmap.CopyPixels(pixels, stride, 0);
            return new Rendered(bitmap, pixels, bitmap.PixelWidth, bitmap.PixelHeight);
        });
    }
}

/// <summary>A rasterised element, and what can be asked of the pixels.</summary>
internal sealed record Rendered(
    RenderTargetBitmap Bitmap, byte[] Pixels, int Width, int Height)
{
    /// <summary>How many pixels are not fully transparent.</summary>
    public int Painted
    {
        get
        {
            var count = 0;
            for (var i = 3; i < Pixels.Length; i += 4)
            {
                if (Pixels[i] > 8) count++;
            }
            return count;
        }
    }

    /// <summary>How many distinct colours appear.</summary>
    /// <remarks>
    /// A flat fill and a drawn chart both paint every pixel; only the number
    /// of colours separates "it drew something" from "it drew a rectangle".
    /// </remarks>
    public int Colours
    {
        get
        {
            var seen = new HashSet<uint>();
            for (var i = 0; i + 3 < Pixels.Length; i += 4)
            {
                seen.Add((uint)(Pixels[i] | (Pixels[i + 1] << 8)
                    | (Pixels[i + 2] << 16) | (Pixels[i + 3] << 24)));
            }
            return seen.Count;
        }
    }

    /// <summary>Whether any pixel is close to <paramref name="colour"/>.</summary>
    public bool Contains(System.Windows.Media.Color colour, int tolerance = 12)
    {
        for (var i = 0; i + 3 < Pixels.Length; i += 4)
        {
            if (Pixels[i + 3] < 40) continue;
            if (Math.Abs(Pixels[i] - colour.B) <= tolerance
                && Math.Abs(Pixels[i + 1] - colour.G) <= tolerance
                && Math.Abs(Pixels[i + 2] - colour.R) <= tolerance)
            {
                return true;
            }
        }
        return false;
    }

    /// <summary>
    /// Writes the frame to <c>%SM_RENDER_CARDS%</c> when that is set.
    /// </summary>
    /// <remarks>
    /// The assertions run always; this only dumps the PNGs, for when the
    /// question is "does it look right" rather than "did it draw" — which no
    /// pixel count can answer.
    /// </remarks>
    public void Save(string name)
    {
        var directory = Environment.GetEnvironmentVariable("SM_RENDER_CARDS");
        if (string.IsNullOrWhiteSpace(directory)) return;
        UiThread.Run(() =>
        {
            Directory.CreateDirectory(directory);
            var encoder = new PngBitmapEncoder();
            encoder.Frames.Add(BitmapFrame.Create(Bitmap));
            using var stream = File.Create(Path.Combine(directory, name + ".png"));
            encoder.Save(stream);
        });
    }
}
