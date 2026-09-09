using ServerMonitor.App.Views;
using ServerMonitor.Core.L10n;
using ServerMonitor.Core.Model;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// Which tab a request for a session lands on.
/// </summary>
/// <remarks>
/// Terminal and SFTP used to open windows of their own, and a window manager
/// answered this question: a second click made a second window and the user
/// dealt with it. A dock has to decide, and the decision is the part worth
/// pinning down — the rest of the dock is plumbing.
///
/// Two things pull against each other. Clicking Terminal twice on the same
/// host should not quietly leave two identical shells behind, since nothing
/// on screen would say there were two. But asking a container for a shell
/// must never join one somebody is already using, because the command runs
/// the moment the session opens and it would run into whatever they were
/// typing.
/// </remarks>
public class SessionDockTests
{
    private static readonly Guid HostA = Guid.NewGuid();
    private static readonly Guid HostB = Guid.NewGuid();

    [Fact]
    public void TheFirstRequestHasNothingToJoin()
    {
        Assert.Equal(-1, SessionDock.IndexToReuse([], HostA, SessionKind.Terminal, hasCommand: false));
    }

    [Fact]
    public void AskingTwiceForTheSameHostFindsTheTabAlreadyOpen()
    {
        var open = new[] { (HostA, SessionKind.Terminal) };
        Assert.Equal(0, SessionDock.IndexToReuse(open, HostA, SessionKind.Terminal, hasCommand: false));
    }

    [Fact]
    public void TerminalAndSftpOnOneHostAreTwoTabs()
    {
        // They show different things and are used together — the point of the
        // dock holding several at once.
        var open = new[] { (HostA, SessionKind.Terminal) };
        Assert.Equal(-1, SessionDock.IndexToReuse(open, HostA, SessionKind.Sftp, hasCommand: false));
    }

    [Fact]
    public void AnotherHostIsAnotherTab()
    {
        var open = new[] { (HostA, SessionKind.Terminal) };
        Assert.Equal(-1, SessionDock.IndexToReuse(open, HostB, SessionKind.Terminal, hasCommand: false));
    }

    [Fact]
    public void AContainerShellNeverJoinsSomeoneElsesSession()
    {
        // The container list's exec passes a command, and the command is typed
        // at the prompt as soon as the shell is ready. Landing that in a
        // session the user is working in would run it into what they were
        // halfway through typing.
        var open = new[] { (HostA, SessionKind.Terminal) };
        Assert.Equal(-1, SessionDock.IndexToReuse(open, HostA, SessionKind.Terminal, hasCommand: true));
    }

    [Fact]
    public void TheMatchIsTheFirstOneAndNotJustAnyOne()
    {
        // Two shells on one host is legal — the dock's "+" makes them — so the
        // rule has to name which of them a plain request goes to.
        var open = new[]
        {
            (HostB, SessionKind.Terminal),
            (HostA, SessionKind.Sftp),
            (HostA, SessionKind.Terminal),
            (HostA, SessionKind.Terminal),
        };
        Assert.Equal(2, SessionDock.IndexToReuse(open, HostA, SessionKind.Terminal, hasCommand: false));
    }

    [Fact]
    public void ATabSaysWhatItIsAndWhichHost()
    {
        Assert.Equal(
            $"{Strings.Get("nav.terminal")} · web-01",
            ISessionPane.LabelFor(SessionKind.Terminal, "web-01"));
        Assert.Equal(
            $"{Strings.Get("nav.sftp")} · web-01",
            ISessionPane.LabelFor(SessionKind.Sftp, "web-01"));
    }

    [Fact]
    public void AContainerShellIsLabelledWithTheContainer()
    {
        // The Docker page passes its own title, because "Terminal · web-01" on
        // three container shells at once names none of them.
        Assert.Equal(
            "redis · web-01",
            ISessionPane.LabelFor(SessionKind.Terminal, "web-01", "redis · web-01"));
    }

    [Fact]
    public void AnEmptyTitleIsNotATitle()
    {
        // Windows.Terminal's title parameter is optional and its callers pass
        // whatever they have; an empty one must not produce a nameless tab.
        Assert.Equal(
            $"{Strings.Get("nav.terminal")} · web-01",
            ISessionPane.LabelFor(SessionKind.Terminal, "web-01", ""));
    }

    [Fact]
    public void TheTabAndThePaneAgree()
    {
        // The label the dock shows and the label the pane reports are the same
        // call, so they cannot drift. Asserted through the interface, which is
        // all the dock ever sees.
        var server = new Server { Name = "web-01" };
        var pane = UiThread.Run(() => (ISessionPane)new SftpPane(server));
        Assert.Equal(ISessionPane.LabelFor(SessionKind.Sftp, "web-01"), pane.Label);
        Assert.Equal(server.Id, pane.ServerId);
    }
}
