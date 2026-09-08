using System.Net.Sockets;
using ServerMonitor.Core.L10n;

namespace ServerMonitor.Core.Ssh;

/// <summary>
/// Why a host could not be reached, in the user's language.
/// </summary>
/// <remarks>
/// The offline reason is the most-read sentence the app produces: it sits
/// under the host name on the machine screen, in the tooltip on every
/// dashboard card, and in the body of the toast that says a host went down.
/// It used to be <c>error.Message</c> straight from SSH.NET, so a Chinese UI
/// announced "The connection to the remote server was closed before a valid
/// SSH identification string was received." — accurate, English, and jargon
/// even in English.
///
/// Mapped on the exception's <em>type</em>, not its text: library messages
/// change between versions and are not worth pattern-matching. Anything
/// unrecognised falls through to its own message, which is the honest
/// alternative to inventing a category for it — and the raw text always
/// reaches the log regardless, so nothing is lost for diagnosis.
///
/// One case is deliberately left verbatim: <see cref="SshFailure.CommandFailed"/>
/// carries the remote host's own stderr, and "Permission denied (publickey)."
/// in the host's words is worth more than any sentence written here.
/// </remarks>
public static class FailureText
{
    public static string For(Exception error) => error switch
    {
        SshException own => Own(own),

        // Renci's own hierarchy. Derived types first: both of these inherit
        // Renci.SshNet.Common.SshException, so matching the base earlier would
        // swallow them.
        Renci.SshNet.Common.SshAuthenticationException => Zh(
            "认证失败：主机拒绝了提供的密钥或密码。",
            "Authentication failed: the host rejected the key or password offered."),
        Renci.SshNet.Common.SshOperationTimeoutException => TimedOut,
        Renci.SshNet.Common.SshConnectionException => Zh(
            "连接在 SSH 握手完成前被断开。可能是端口不是 SSH 服务，或有中间设备拦截。",
            "The connection dropped before the SSH handshake finished — the port may not be SSH, or something in between is intercepting it."),

        SocketException socket => Socket(socket),

        // A poll that was cancelled says nothing about the host; the caller
        // filters these out before it gets here, and this is the belt.
        OperationCanceledException => Zh("采集已取消。", "The collection was cancelled."),

        _ => error.Message,
    };

    private static string Own(SshException error) => error.Kind switch
    {
        // The host's own words. Nothing written here beats them.
        SshFailure.CommandFailed => error.Message,

        SshFailure.TimedOut => TimedOut,
        SshFailure.AuthFailed => Zh(
            $"认证失败：{error.Message}", $"Authentication failed: {error.Message}"),
        SshFailure.HostKeyChanged => Zh(
            "主机密钥已变更。如果这是预期的，请从 %USERPROFILE%\\.ssh\\known_hosts 里删掉旧条目；否则不要连接。",
            "The host key has changed. If that is expected, remove the old entry from %USERPROFILE%\\.ssh\\known_hosts; if not, do not connect."),
        SshFailure.MissingCredential => Zh(
            "这台主机没有可用的 SSH 凭据，请在机器编辑器里补上。",
            "This host has no usable SSH credential — add one in the machine editor."),
        SshFailure.LaunchFailed => Zh(
            $"无法启动 ssh：{error.Message}", $"Could not start ssh: {error.Message}"),
        _ => error.Message,
    };

    /// <summary>
    /// The socket errors a monitored host actually produces.
    /// </summary>
    /// <remarks>
    /// Named individually because they call for different actions: a refusal
    /// means the port is wrong or sshd is down, an unreachable network means
    /// routing or a firewall, and a name that will not resolve is a typo or a
    /// DNS problem. "A socket error occurred" would cover all three and help
    /// with none.
    /// </remarks>
    private static string Socket(SocketException error) => error.SocketErrorCode switch
    {
        SocketError.ConnectionRefused => Zh(
            "连接被拒绝：该端口上没有 SSH 服务在监听。",
            "Connection refused: nothing is listening for SSH on that port."),
        SocketError.HostNotFound or SocketError.NoData => Zh(
            "无法解析主机名。", "The host name could not be resolved."),
        SocketError.HostUnreachable or SocketError.NetworkUnreachable => Zh(
            "网络不可达：可能是路由或防火墙的问题。",
            "The network is unreachable — routing or a firewall."),
        SocketError.TimedOut => TimedOut,
        SocketError.ConnectionReset => Zh(
            "连接被对端重置。", "The connection was reset by the host."),
        _ => error.Message,
    };

    private static string TimedOut => Zh(
        "连接超时：主机没有在 10 秒内应答。",
        "Connection timed out: the host did not answer within 10 seconds.");

    private static string Zh(string chinese, string english) =>
        Strings.IsChinese ? chinese : english;
}
