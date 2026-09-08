using ServerMonitor.App.Views;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// What closing the main window does.
/// </summary>
/// <remarks>
/// Three lines, wrong in a way nothing could see. The app runs with
/// ShutdownMode="OnExplicitShutdown", because a tray app has to survive its
/// own window being closed — so letting the close proceed destroys the window
/// and leaves the process alive. With "keep running in the notification area"
/// switched off, that gave an app with no window at all: still polling,
/// reachable only through the tray, whose "show window" then threw because a
/// closed WPF Window cannot be shown again.
///
/// It had never been reachable: settings did not persist, CloseToTray defaults
/// to true, so the false branch only ran inside a session where someone
/// unticked the box by hand. Fixing persistence is what exposed it.
/// </remarks>
public class CloseBehaviourTests
{
    [Fact]
    public void WithTheSettingOnAClosePutsTheAppInTheTray()
    {
        Assert.Equal(CloseAction.HideToTray, CloseBehaviour.For(closeToTray: true));
    }

    [Fact]
    public void WithTheSettingOffACloseQuitsTheApp()
    {
        // The bug. This used to fall through to a plain base.OnClosing, which
        // under OnExplicitShutdown means the process outlives its own UI.
        Assert.Equal(CloseAction.QuitApp, CloseBehaviour.For(closeToTray: false));
    }

    [Fact]
    public void ClosingNeverJustLeavesTheProcessWithoutAWindow()
    {
        // The property that was violated, stated as a property: whatever the
        // setting says, the outcome is either "stay, hidden" or "go away
        // entirely" — never "window gone, process running".
        foreach (var closeToTray in new[] { true, false })
        {
            var action = CloseBehaviour.For(closeToTray);
            Assert.True(
                action is CloseAction.HideToTray or CloseAction.QuitApp,
                $"closeToTray={closeToTray} gave {action}");
        }
    }

    [Fact]
    public void TheTwoSettingsDoNotAgree()
    {
        // Guards the obvious regression of wiring both sides to the same
        // branch, which is what "it stays in the tray either way" looked like
        // from the outside.
        Assert.NotEqual(CloseBehaviour.For(true), CloseBehaviour.For(false));
    }
}
