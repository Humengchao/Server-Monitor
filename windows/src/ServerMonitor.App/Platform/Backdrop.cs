using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Media;

namespace ServerMonitor.App.Platform;

/// <summary>
/// Mica, and the dark title bar that has to come with it.
/// </summary>
/// <remarks>
/// This is the one thing D1 wanted WPF-UI for that is not just styling, and it
/// is a few dozen lines of <c>DwmSetWindowAttribute</c>. WPF's own Fluent theme
/// has not landed Mica (F6).
///
/// Everything here degrades rather than fails: on Windows 10 the attributes are
/// unknown and the calls return an error code, which <see cref="Apply"/>
/// reports so the window paints a solid colour instead — which is exactly what
/// D8 says should happen.
/// </remarks>
public static class Backdrop
{
    /// <summary>
    /// Applies the Mica backdrop and matches the title bar to the theme.
    /// </summary>
    /// <param name="wantsBackdrop">
    /// Whether the user asked for Mica. Off by default — see
    /// <c>AppSettings.UseMicaBackdrop</c> for why the texture is opt-in.
    /// </param>
    /// <returns>
    /// Whether the backdrop was accepted. False means the caller must paint an
    /// opaque background: a transparent WPF window over a backdrop that was
    /// refused is not translucent, it is black.
    /// </returns>
    /// <summary>
    /// Paints one window's title bar to match the theme.
    /// </summary>
    /// <remarks>
    /// Separate from <see cref="Apply"/>, and applied to every window rather
    /// than only the main one. It used to be part of Apply, which only
    /// MainWindow calls — so in dark mode the terminal, the file browser, all
    /// four editors and every dialog had a light title bar over dark content.
    ///
    /// The attribute has worked since Windows 10 1809; on anything older the
    /// call fails and is ignored, which is why its result is discarded. It
    /// needs a window handle, so the caller has to be past
    /// SourceInitialized — App registers a class handler on Window.Loaded to
    /// get that for free, for windows that do not exist yet included.
    /// </remarks>
    public static void ApplyTitleBar(Window window, bool dark)
    {
        var handle = new WindowInteropHelper(window).Handle;
        if (handle == IntPtr.Zero) return;
        var useDark = dark ? 1 : 0;
        _ = DwmSetWindowAttribute(handle, DWMWA_USE_IMMERSIVE_DARK_MODE, ref useDark, sizeof(int));
    }

    public static bool Apply(Window window, bool dark, bool wantsBackdrop)
    {
        var handle = new WindowInteropHelper(window).Handle;
        if (handle == IntPtr.Zero) return false;

        // The title bar first, and regardless of the backdrop: a light title
        // bar over a dark window is worse than no Mica at all.
        ApplyTitleBar(window, dark);

        if (!wantsBackdrop || !IsSupported)
        {
            // Put the frame back where it was, in case the backdrop was on a
            // moment ago: the extension survives the attribute being cleared.
            var flat = new Margins();
            _ = DwmExtendFrameIntoClientArea(handle, ref flat);
            var none = DWMSBT_NONE;
            _ = DwmSetWindowAttribute(handle, DWMWA_SYSTEMBACKDROP_TYPE, ref none, sizeof(int));
            return false;
        }

        var backdrop = DWMSBT_MAINWINDOW;
        if (DwmSetWindowAttribute(handle, DWMWA_SYSTEMBACKDROP_TYPE, ref backdrop, sizeof(int)) != 0)
        {
            return false;
        }

        // The step without which the whole thing looks broken: the system
        // backdrop is drawn behind the *frame*, and the client area is still
        // painted by the window — as black, once WPF stops filling it. A
        // negative margin extends the frame across the entire client area, so
        // the backdrop is what shows through the transparent background.
        var margins = new Margins { Left = -1, Top = -1, Right = -1, Bottom = -1 };
        return DwmExtendFrameIntoClientArea(handle, ref margins) == 0;
    }

    /// <summary>
    /// Whether the composited backdrop is worth asking for.
    /// </summary>
    /// <remarks>
    /// Build 22621 is Windows 11 22H2, the first with a stable
    /// <c>DWMWA_SYSTEMBACKDROP_TYPE</c>. Earlier Windows 11 builds used an
    /// undocumented attribute number that later changed meaning; asking for it
    /// there is how apps ended up with an all-black client area.
    /// </remarks>
    public static bool IsSupported =>
        Environment.OSVersion.Version.Build >= 22621;

    /// <summary>The opaque window background, for when Mica is not showing.</summary>
    public static Brush SolidBackground(bool dark) => new SolidColorBrush(dark
        ? Color.FromRgb(0x20, 0x20, 0x20)
        : Color.FromRgb(0xF3, 0xF3, 0xF3));

    private const int DWMWA_USE_IMMERSIVE_DARK_MODE = 20;
    private const int DWMWA_SYSTEMBACKDROP_TYPE = 38;

    /// <summary>Mica. (2 is Mica, 3 is acrylic, 4 is "tabbed" Mica Alt.)</summary>
    private const int DWMSBT_MAINWINDOW = 2;

    /// <summary>No backdrop at all, for turning it back off.</summary>
    private const int DWMSBT_NONE = 1;

    [StructLayout(LayoutKind.Sequential)]
    private struct Margins
    {
        public int Left;
        public int Right;
        public int Top;
        public int Bottom;
    }

    [DllImport("dwmapi.dll")]
    private static extern int DwmSetWindowAttribute(
        IntPtr window, int attribute, ref int value, int size);

    [DllImport("dwmapi.dll")]
    private static extern int DwmExtendFrameIntoClientArea(IntPtr window, ref Margins margins);
}
