using System.Net.NetworkInformation;
using Microsoft.Win32;

namespace ServerMonitor.App.Platform;

/// <summary>
/// Sleep, wake, network changes and energy-saver mode.
/// </summary>
/// <remarks>
/// The Windows side of the macOS build's <c>NWPathMonitor</c> and
/// <c>didWakeNotification</c>. Both matter for the same reason: on a laptop the
/// usual explanation for every host failing at once is this machine — a closed
/// lid, a train, a different Wi-Fi — not nine servers. Without these, the
/// backoff built up during the outage keeps the fleet dark for up to five
/// minutes after the network is already back.
/// </remarks>
public sealed class SystemWatchers : IDisposable
{
    /// <summary>Raised on wake from sleep, and when the network returns.</summary>
    public event Action<string>? RetryEverything;

    /// <summary>
    /// Null until the first network event, so starting up is not mistaken for
    /// a network that just came back.
    /// </summary>
    private bool? _networkWasAvailable;

    private readonly Action<Action> _dispatch;
    private bool _disposed;

    public SystemWatchers(Action<Action> dispatch)
    {
        _dispatch = dispatch;
        SystemEvents.PowerModeChanged += OnPowerModeChanged;
        NetworkChange.NetworkAvailabilityChanged += OnNetworkAvailabilityChanged;
        NetworkChange.NetworkAddressChanged += OnNetworkAddressChanged;
    }

    private void OnPowerModeChanged(object sender, PowerModeChangedEventArgs e)
    {
        if (e.Mode != PowerModes.Resume) return;
        // Waking is its own signal, independent of the network: the fleet
        // almost certainly did not go down while the lid was shut. The network
        // path is often "available" before anything actually routes — the link
        // is up, DHCP and DNS are not — so the first ticks after a lid-open
        // fail without any network transition to clear the backoff they build.
        _dispatch(() => RetryEverything?.Invoke("woke from sleep"));
    }

    private void OnNetworkAvailabilityChanged(object? sender, NetworkAvailabilityEventArgs e)
    {
        _dispatch(() =>
        {
            var previous = _networkWasAvailable;
            _networkWasAvailable = e.IsAvailable;
            // A transition to available, not the state itself. The first event
            // is just the current state.
            if (previous is null || !e.IsAvailable || previous == true) return;
            RetryEverything?.Invoke("network came back");
        });
    }

    private void OnNetworkAddressChanged(object? sender, EventArgs e)
    {
        // A changed address is a changed network — moving between Wi-Fi and a
        // dock, or a VPN coming up. Every host's route may be different now,
        // and any connection held is against the old one.
        _dispatch(() =>
        {
            if (_networkWasAvailable == false) return;
            RetryEverything?.Invoke("network address changed");
        });
    }

    /// <summary>
    /// Whether Windows is in energy-saver mode.
    /// </summary>
    /// <remarks>
    /// The counterpart of <c>isLowPowerModeEnabled</c>. Read through
    /// <c>SystemInformation.PowerStatus</c> rather than
    /// <c>PowerManager.EnergySaverStatus</c>, which the plan named (§5): that
    /// one is a WinRT API needing a Windows App SDK reference the app
    /// otherwise does not take, for a boolean that
    /// <c>SYSTEM_POWER_STATUS.SystemStatusFlag</c> already carries.
    /// </remarks>
    public static bool IsEnergySaverOn()
    {
        try
        {
            // 1 means battery saver is on. The property is documented as
            // "reserved" on older Windows, where it reads 0 — which is the
            // safe answer: poll at the normal cadence.
            return System.Windows.Forms.SystemInformation.PowerStatus.BatteryLifePercent is > 0
                && GetSystemPowerStatus(out var status)
                && status.SystemStatusFlag == 1;
        }
        catch (Exception)
        {
            return false;
        }
    }

    [System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)]
    private struct SYSTEM_POWER_STATUS
    {
        public byte ACLineStatus;
        public byte BatteryFlag;
        public byte BatteryLifePercent;
        public byte SystemStatusFlag;
        public uint BatteryLifeTime;
        public uint BatteryFullLifeTime;
    }

    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
    [return: System.Runtime.InteropServices.MarshalAs(System.Runtime.InteropServices.UnmanagedType.Bool)]
    private static extern bool GetSystemPowerStatus(out SYSTEM_POWER_STATUS status);

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        SystemEvents.PowerModeChanged -= OnPowerModeChanged;
        NetworkChange.NetworkAvailabilityChanged -= OnNetworkAvailabilityChanged;
        NetworkChange.NetworkAddressChanged -= OnNetworkAddressChanged;
    }
}
