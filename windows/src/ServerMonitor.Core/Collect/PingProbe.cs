using System.Net;
using System.Net.NetworkInformation;
using System.Net.Sockets;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// ICMP round trip, measured over a physical interface where possible.
/// </summary>
/// <remarks>
/// Binding to the NIC is the whole point. When a VPN or proxy owns the route,
/// an unbound ping never leaves the machine — the tunnel answers it locally,
/// and a server on another continent appears to reply in under a millisecond.
/// The macOS build gets this with <c>ping -b en0</c>; .NET's <c>Ping</c> has
/// no source-address option, so the same result comes from a raw ICMP socket
/// bound to the chosen interface's address.
///
/// <see cref="System.Net.NetworkInformation.Ping"/> is not used at all. Beyond
/// having no binding, it needs no elevation but also gives no way to tell a
/// tunnel-answered reply from a real one; the raw socket does both. Shelling
/// out to <c>ping.exe</c> is out for R5: its output is localised, so parsing
/// it works on an English Windows and silently returns nothing on a Chinese
/// one.
/// </remarks>
public sealed class PingProbe : ILatencyProbe
{
    public sealed record Reading(double AverageMs, double LossPercent);

    /// <summary>
    /// Below this, a reply from a public address is treated as a local answer
    /// rather than a real round trip (R12).
    /// </summary>
    /// <remarks>
    /// Nothing on the public internet answers in under 2 ms; a TUN interface
    /// answering on the tunnel's behalf does. When this trips, the caller
    /// falls back to the remote-clock figure, which cannot be faked locally
    /// because it requires bytes to reach the host and come back.
    /// </remarks>
    internal const double SuspiciouslyFastMs = 2.0;

    private const int Attempts = 3;

    /// <summary>
    /// How long one echo waits before being counted as lost.
    /// </summary>
    /// <remarks>
    /// Short on purpose. A host that filters ICMP never replies, so three
    /// attempts is three timeouts, and the caller waits for them — at two
    /// seconds each that is six seconds added to a poll on a five-second
    /// cadence. 600 ms is far above any real intercontinental round trip
    /// (~250 ms) while keeping the worst case under two seconds, and
    /// <see cref="TotalBudget"/> bounds it regardless.
    /// </remarks>
    private static readonly TimeSpan PerAttemptTimeout = TimeSpan.FromMilliseconds(600);

    /// <summary>
    /// The whole probe's ceiling, however the attempts go.
    /// </summary>
    /// <remarks>
    /// Belt to the per-attempt braces: DNS can be slow too, and this runs on
    /// the path a fleet walks every few seconds.
    /// </remarks>
    public static readonly TimeSpan TotalBudget = TimeSpan.FromSeconds(2);

    /// <summary>
    /// Measures <paramref name="host"/>, or null when it cannot be measured
    /// honestly.
    /// </summary>
    public async Task<Reading?> MeasureAsync(string host, CancellationToken cancellationToken = default)
    {
        if (host.Length == 0) return null;

        using var budget = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        budget.CancelAfter(TotalBudget);
        try
        {
            return await MeasureWithinBudgetAsync(host, budget.Token).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            // Out of budget. Reporting nothing is right: the caller falls back
            // to the remote-clock figure, which is the honest number for a
            // host that will not answer ICMP anyway.
            return null;
        }
    }

    private async Task<Reading?> MeasureWithinBudgetAsync(
        string host, CancellationToken cancellationToken)
    {
        IPAddress? destination;
        try
        {
            var addresses = await Dns.GetHostAddressesAsync(host, cancellationToken).ConfigureAwait(false);
            // IPv4 only: the source-binding trick needs a matching family, and
            // every host this app reaches has an A record.
            destination = addresses.FirstOrDefault(a => a.AddressFamily == AddressFamily.InterNetwork);
        }
        catch (Exception error) when (error is SocketException or ArgumentException)
        {
            return null;
        }
        if (destination is null) return null;

        var source = PrimaryPhysicalAddress();
        var samples = new List<double>();
        var lost = 0;
        for (var attempt = 0; attempt < Attempts; attempt++)
        {
            cancellationToken.ThrowIfCancellationRequested();
            var rtt = await OneEchoAsync(destination, source, cancellationToken).ConfigureAwait(false);
            if (rtt is { } value) samples.Add(value); else lost++;
        }

        if (samples.Count == 0) return null;
        var average = samples.Average();
        // A public address answering this fast did not answer from where it
        // claims. Report nothing so the caller uses the remote-clock figure.
        if (average < SuspiciouslyFastMs && !GeoLookup.IsPrivate(destination.ToString()))
        {
            return null;
        }
        return new Reading(average, (double)lost / Attempts * 100);
    }

    /// <summary>
    /// One ICMP echo over a raw socket, timed locally.
    /// </summary>
    /// <remarks>
    /// Returns null rather than throwing on every expected failure — a
    /// filtered host, a socket the OS will not give us, a timeout. Raw sockets
    /// need no elevation for ICMP on Windows, but a hardened machine or a
    /// firewall policy can still refuse, and a monitor must not fall over
    /// because it could not measure latency.
    /// </remarks>
    private static async Task<double?> OneEchoAsync(
        IPAddress destination, IPAddress? source, CancellationToken cancellationToken)
    {
        Socket socket;
        try
        {
            socket = new Socket(AddressFamily.InterNetwork, SocketType.Raw, ProtocolType.Icmp);
        }
        catch (SocketException)
        {
            return null;
        }

        using (socket)
        {
            try
            {
                if (source is not null) socket.Bind(new IPEndPoint(source, 0));
                socket.ReceiveTimeout = (int)PerAttemptTimeout.TotalMilliseconds;

                var identifier = (ushort)Random.Shared.Next(ushort.MaxValue);
                var request = EchoRequest(identifier, sequence: 1);
                var endpoint = new IPEndPoint(destination, 0);

                var started = System.Diagnostics.Stopwatch.GetTimestamp();
                await socket.SendToAsync(request, SocketFlags.None, endpoint, cancellationToken)
                    .ConfigureAwait(false);

                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
                timeout.CancelAfter(PerAttemptTimeout);
                var buffer = new byte[1024];
                while (true)
                {
                    var received = await socket
                        .ReceiveFromAsync(buffer, SocketFlags.None, new IPEndPoint(IPAddress.Any, 0), timeout.Token)
                        .ConfigureAwait(false);
                    // A raw ICMP socket sees every echo reply on the machine,
                    // including other processes'. Match the identifier or keep
                    // waiting, or a busy machine measures somebody else's ping.
                    if (!IsOurReply(buffer, received.ReceivedBytes, identifier, destination)) continue;
                    var elapsed = System.Diagnostics.Stopwatch.GetElapsedTime(started);
                    return elapsed.TotalMilliseconds;
                }
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                throw;
            }
            catch (Exception error) when (error is SocketException or OperationCanceledException)
            {
                // Timed out, filtered, or the bind was refused.
                return null;
            }
        }
    }

    /// <summary>An ICMP echo request with its checksum filled in.</summary>
    internal static byte[] EchoRequest(ushort identifier, ushort sequence)
    {
        // type, code, checksum(2), identifier(2), sequence(2), then payload.
        var packet = new byte[8 + 16];
        packet[0] = 8;      // echo request
        packet[1] = 0;
        BitConverter.TryWriteBytes(packet.AsSpan(4, 2), identifier);
        BitConverter.TryWriteBytes(packet.AsSpan(6, 2), sequence);
        for (var i = 8; i < packet.Length; i++) packet[i] = (byte)'m';
        var checksum = Checksum(packet);
        BitConverter.TryWriteBytes(packet.AsSpan(2, 2), checksum);
        return packet;
    }

    /// <summary>The internet checksum (RFC 1071) over a packet.</summary>
    internal static ushort Checksum(ReadOnlySpan<byte> data)
    {
        uint sum = 0;
        var index = 0;
        for (; index + 1 < data.Length; index += 2)
        {
            sum += (uint)(data[index] | (data[index + 1] << 8));
        }
        if (index < data.Length) sum += data[index];
        while ((sum >> 16) != 0) sum = (sum & 0xFFFF) + (sum >> 16);
        return (ushort)~sum;
    }

    /// <summary>
    /// Whether a received datagram is the echo reply we are waiting for.
    /// </summary>
    /// <remarks>
    /// A raw socket delivers the IP header too, and its length is variable
    /// (options), so the ICMP payload does not start at a fixed offset.
    /// </remarks>
    internal static bool IsOurReply(
        ReadOnlySpan<byte> buffer, int length, ushort identifier, IPAddress destination)
    {
        if (length < 20) return false;
        var headerLength = (buffer[0] & 0x0F) * 4;
        if (headerLength < 20 || length < headerLength + 8) return false;
        var icmp = buffer[headerLength..length];
        if (icmp[0] != 0) return false;                  // 0 = echo reply
        var replyId = (ushort)(icmp[4] | (icmp[5] << 8));
        if (replyId != identifier) return false;
        // Source address, bytes 12..16 of the IP header.
        var from = new IPAddress(buffer[12..16].ToArray());
        return from.Equals(destination);
    }

    /// <summary>
    /// The address of the active physical interface, or null when there is no
    /// obvious one.
    /// </summary>
    /// <remarks>
    /// Tunnels are excluded by type and by name: binding to one would
    /// reintroduce exactly the problem this class exists to avoid. Returning
    /// null means "do not bind", which is still useful — on a machine with no
    /// VPN the default route is the physical NIC anyway.
    /// </remarks>
    internal static IPAddress? PrimaryPhysicalAddress()
    {
        try
        {
            var candidates = new List<(int Order, IPAddress Address)>();
            foreach (var nic in NetworkInterface.GetAllNetworkInterfaces())
            {
                if (nic.OperationalStatus != OperationalStatus.Up) continue;
                if (nic.NetworkInterfaceType is NetworkInterfaceType.Loopback
                    or NetworkInterfaceType.Tunnel
                    or NetworkInterfaceType.Ppp) continue;
                if (LooksLikeTunnel(nic.Name) || LooksLikeTunnel(nic.Description)) continue;

                var properties = nic.GetIPProperties();
                // No gateway means the link is up but goes nowhere — a
                // disconnected adapter, a host-only virtual switch.
                if (properties.GatewayAddresses.Count == 0) continue;

                foreach (var unicast in properties.UnicastAddresses)
                {
                    if (unicast.Address.AddressFamily != AddressFamily.InterNetwork) continue;
                    var text = unicast.Address.ToString();
                    // A self-assigned address means the link is up but unusable.
                    if (text.StartsWith("169.254", StringComparison.Ordinal) || text == "0.0.0.0") continue;
                    // Ethernet before Wi-Fi, matching "lowest index first" on
                    // the macOS side, which lands on the built-in NIC.
                    var order = nic.NetworkInterfaceType == NetworkInterfaceType.Ethernet ? 0 : 1;
                    candidates.Add((order, unicast.Address));
                }
            }
            return candidates.OrderBy(c => c.Order).Select(c => c.Address).FirstOrDefault();
        }
        catch (NetworkInformationException)
        {
            return null;
        }
    }

    internal static bool LooksLikeTunnel(string name)
    {
        string[] markers = ["tap", "tun", "wintun", "wireguard", "openvpn", "tailscale", "zerotier", "clash", "utun"];
        var lowered = name.ToLowerInvariant();
        return markers.Any(marker => lowered.Contains(marker, StringComparison.Ordinal));
    }
}
