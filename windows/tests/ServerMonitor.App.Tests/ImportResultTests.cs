using ServerMonitor.App.Views;
using ServerMonitor.Core.L10n;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// What the ssh-config import says when it finishes.
/// </summary>
/// <remarks>
/// The loop had no try around the store, so one host that would not save took
/// the whole import with it: the hosts before it were already written, the
/// ones after were not, the window never closed, and the user got a stack
/// trace instead of a count. macOS collects the failures and names them
/// (import.skipped) — Windows had the string in its table and no caller.
///
/// Reporting only the count, which is what it did, left someone who asked for
/// five and got three with no idea which two were missing.
/// </remarks>
public class ImportResultTests
{
    [Fact]
    public void NothingImportedAndNothingSkippedSaysNothing()
    {
        // Clicking Import with no host ticked. A dialog reading "Imported 0"
        // is worse than no dialog.
        Assert.Null(ImportSshConfigWindow.ResultMessage(0, []));
    }

    [Fact]
    public void ACleanImportReportsTheCount()
    {
        var message = ImportSshConfigWindow.ResultMessage(3, []);
        Assert.NotNull(message);
        Assert.Contains("3", message, StringComparison.Ordinal);
        Assert.DoesNotContain(Strings.Get("import.skipped", ""), message, StringComparison.Ordinal);
    }

    [Fact]
    public void SkippedHostsAreNamed()
    {
        // Named, not counted: "2 skipped" does not tell you which two to go
        // and look at.
        var message = ImportSshConfigWindow.ResultMessage(1, ["build-arm", "nas"]);
        Assert.NotNull(message);
        Assert.Contains("build-arm", message, StringComparison.Ordinal);
        Assert.Contains("nas", message, StringComparison.Ordinal);
    }

    [Fact]
    public void AnImportThatSavedNothingStillExplainsItself()
    {
        // The case the missing try/catch turned into a crash: every host
        // failed, so there is no count to report and only the names matter.
        var message = ImportSshConfigWindow.ResultMessage(0, ["web-01"]);
        Assert.NotNull(message);
        Assert.Contains("web-01", message, StringComparison.Ordinal);
    }

    [Fact]
    public void BothHalvesAppearWhenBothHappened()
    {
        var message = ImportSshConfigWindow.ResultMessage(2, ["mail"]);
        Assert.NotNull(message);
        Assert.Contains("2", message, StringComparison.Ordinal);
        Assert.Contains("mail", message, StringComparison.Ordinal);
        // On separate lines, because they are separate facts.
        Assert.Contains("\n", message, StringComparison.Ordinal);
    }
}
