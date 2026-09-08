using System.Net.Sockets;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The offline reason, in the user's language.
/// </summary>
/// <remarks>
/// This sentence is the most-read thing the app produces — under the host name
/// on the machine screen, in every dashboard card's tooltip, and in the body
/// of the toast that says a host went down. It was the exception's own message,
/// so a Chinese UI announced "The connection to the remote server was closed
/// before a valid SSH identification string was received."
///
/// What these check is the shape of the mapping rather than the wording: every
/// failure a monitored host actually produces is recognised, an unknown one
/// still says something, and the one case that must stay verbatim does.
/// </remarks>
public class FailureTextTests
{
    [Fact]
    public void ARecognisedFailureIsTranslated()
    {
        using var zh = new LanguageScope(AppLanguage.Zh);
        var chinese = FailureText.For(new SocketException((int)SocketError.ConnectionRefused));
        Assert.Contains("拒绝", chinese, StringComparison.Ordinal);

        using var en = new LanguageScope(AppLanguage.En);
        var english = FailureText.For(new SocketException((int)SocketError.ConnectionRefused));
        Assert.Contains("refused", english, StringComparison.OrdinalIgnoreCase);
        Assert.NotEqual(chinese, english);
    }

    [Theory]
    [InlineData(SocketError.ConnectionRefused)]
    [InlineData(SocketError.HostNotFound)]
    [InlineData(SocketError.HostUnreachable)]
    [InlineData(SocketError.NetworkUnreachable)]
    [InlineData(SocketError.TimedOut)]
    [InlineData(SocketError.ConnectionReset)]
    public void TheSocketErrorsAHostProducesEachGetTheirOwnSentence(SocketError code)
    {
        // Named individually because they call for different actions: a
        // refusal means the port is wrong, an unreachable network means
        // routing, a name that will not resolve is a typo. One sentence for
        // all three would help with none.
        using var _ = new LanguageScope(AppLanguage.Zh);
        var text = FailureText.For(new SocketException((int)code));
        var raw = new SocketException((int)code).Message;
        Assert.NotEqual(raw, text);
        Assert.NotEmpty(text);
    }

    [Fact]
    public void EverySocketSentenceIsDistinct()
    {
        using var _ = new LanguageScope(AppLanguage.Zh);
        SocketError[] codes =
        [
            SocketError.ConnectionRefused, SocketError.HostNotFound,
            SocketError.HostUnreachable, SocketError.ConnectionReset,
        ];
        var texts = codes.Select(c => FailureText.For(new SocketException((int)c))).ToList();
        Assert.Equal(texts.Count, texts.Distinct(StringComparer.Ordinal).Count());
    }

    [Fact]
    public void TheRenciExceptionsThatReachedTheUiRawAreTranslated()
    {
        using var _ = new LanguageScope(AppLanguage.Zh);

        // These three are what an unreachable or misconfigured host actually
        // threw before this existed.
        var timeout = FailureText.For(
            new Renci.SshNet.Common.SshOperationTimeoutException("Connection has timed out."));
        Assert.DoesNotContain("timed out", timeout, StringComparison.OrdinalIgnoreCase);
        Assert.Contains("超时", timeout, StringComparison.Ordinal);

        var dropped = FailureText.For(new Renci.SshNet.Common.SshConnectionException(
            "The connection to the remote server was closed before a valid SSH identification string was received."));
        Assert.DoesNotContain("identification string", dropped, StringComparison.OrdinalIgnoreCase);

        var auth = FailureText.For(
            new Renci.SshNet.Common.SshAuthenticationException("No suitable authentication method found"));
        Assert.Contains("认证", auth, StringComparison.Ordinal);
    }

    [Fact]
    public void AnAuthenticationFailureIsNotSwallowedByItsBaseType()
    {
        // SshAuthenticationException and SshConnectionException both inherit
        // Renci's SshException, so a switch that matched the base first would
        // report every one of them as a dropped connection.
        using var _ = new LanguageScope(AppLanguage.En);
        var auth = FailureText.For(
            new Renci.SshNet.Common.SshAuthenticationException("nope"));
        var dropped = FailureText.For(
            new Renci.SshNet.Common.SshConnectionException("nope"));
        Assert.NotEqual(auth, dropped);
        Assert.Contains("Authentication", auth, StringComparison.Ordinal);
    }

    [Fact]
    public void TheHostsOwnWordsAreKept()
    {
        // A remote command's stderr is the most informative thing available.
        // "Permission denied (publickey)." in sshd's words beats any sentence
        // written here, so CommandFailed stays verbatim.
        using var _ = new LanguageScope(AppLanguage.Zh);
        var error = SshException.CommandFailed(255, "Permission denied (publickey).");
        Assert.Equal("Permission denied (publickey).", FailureText.For(error));
    }

    [Fact]
    public void AChangedHostKeySaysWhatToDoAboutIt()
    {
        using var _ = new LanguageScope(AppLanguage.En);
        var text = FailureText.For(SshException.HostKeyChanged("web-01"));
        Assert.Contains("known_hosts", text, StringComparison.Ordinal);
        // And does not tell the user to just carry on.
        Assert.Contains("do not connect", text, StringComparison.OrdinalIgnoreCase);
    }

    [Fact]
    public void AMissingCredentialPointsAtTheEditor()
    {
        using var _ = new LanguageScope(AppLanguage.En);
        var text = FailureText.For(SshException.MissingCredential());
        Assert.Contains("machine editor", text, StringComparison.OrdinalIgnoreCase);
    }

    [Fact]
    public void AnUnrecognisedFailureStillSaysSomething()
    {
        // The honest fallback. Inventing a category for an exception nobody
        // has seen would hide the only information there is.
        using var _ = new LanguageScope(AppLanguage.Zh);
        Assert.Equal("something odd", FailureText.For(new InvalidOperationException("something odd")));
    }

    [Fact]
    public void EveryOwnFailureKindIsCovered()
    {
        // A new SshFailure member must not fall through to an English
        // message by default. Each kind is either translated or deliberately
        // verbatim; this fails when one is neither.
        using var _ = new LanguageScope(AppLanguage.Zh);
        foreach (var kind in Enum.GetValues<SshFailure>())
        {
            var error = new SshException(kind, "raw detail", 255, "raw detail");
            var text = FailureText.For(error);
            Assert.NotEmpty(text);
            if (kind == SshFailure.CommandFailed)
            {
                Assert.Equal("raw detail", text);
                continue;
            }
            Assert.True(
                text != "raw detail",
                $"{kind} falls through to the exception's own English message");
        }
    }
}
