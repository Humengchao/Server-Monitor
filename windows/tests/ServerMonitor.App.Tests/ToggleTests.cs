using ServerMonitor.App.Controls;
using ServerMonitor.App.Platform;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// A toggle for a setting that lives outside the app.
/// </summary>
/// <remarks>
/// "Start with Windows" writes to HKCU\...\Run, and that write can be
/// refused — by group policy, or by a security product that owns the key.
/// StartupRegistration.Set has always returned false in that case, and its
/// doc comment has always said the UI would "put the toggle back rather than
/// showing a state that is not real". The UI did not: it showed a dialog and
/// left the box ticked, so the app claimed it would start with Windows and
/// then did not.
///
/// The revert is easy to write and easy to write wrongly, because assigning
/// IsChecked from inside the handler raises the opposite event and calls
/// straight back in — which for a setting that is failing means a second
/// error dialog on the way back. Both halves are asserted here.
/// </remarks>
public class ToggleTests
{
    [Fact]
    public void ARejectedChangePutsTheBoxBack()
    {
        var box = UiThread.Run(() => Ui.Toggle("Start with Windows", false, _ => false));

        UiThread.Run(() => box.IsChecked = true);

        Assert.False(UiThread.Run(() => box.IsChecked), "a refused write left the box ticked");
    }

    [Fact]
    public void ARejectedChangeAsksOnlyOnce()
    {
        // The re-entrancy guard. Without it the revert's own Unchecked event
        // calls the handler again, and the user gets two dialogs for one click.
        var asked = 0;
        var box = UiThread.Run(() => Ui.Toggle("Start with Windows", false, _ =>
        {
            asked++;
            return false;
        }));

        UiThread.Run(() => box.IsChecked = true);

        Assert.Equal(1, asked);
    }

    [Fact]
    public void AnAcceptedChangeSticks()
    {
        var box = UiThread.Run(() => Ui.Toggle("Start with Windows", false, _ => true));

        UiThread.Run(() => box.IsChecked = true);

        Assert.True(UiThread.Run(() => box.IsChecked), "an accepted change was reverted");
    }

    [Fact]
    public void TurningItOffCanBeRefusedToo()
    {
        // Deleting the value can fail the same way the write can, and the
        // guard has to work in this direction as well.
        var box = UiThread.Run(() => Ui.Toggle("Start with Windows", true, _ => false));

        UiThread.Run(() => box.IsChecked = false);

        Assert.True(UiThread.Run(() => box.IsChecked), "a refused removal left the box clear");
    }

    [Fact]
    public void TheStartupKeyIsNamedFromTheKeyThatIsActuallyWritten()
    {
        // The refusal message names the key so the user can go and look at
        // it. It is derived from the constant Set() uses, so the two cannot
        // drift; this is what would catch a retyped copy.
        Assert.Equal(
            @"HKCU\Software\Microsoft\Windows\CurrentVersion\Run",
            StartupRegistration.KeyDescription);
    }
}
