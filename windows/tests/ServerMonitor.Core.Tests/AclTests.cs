using System.Diagnostics;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// Whether a key file's ACL is one ssh will accept.
/// </summary>
/// <remarks>
/// Against real files with real ACLs, because the bug this replaces could not
/// have been caught any other way: the check parsed <c>icacls</c> output and
/// split each line at the first colon, which in
/// <c>C:\Users\me\.ssh\id_rsa NT AUTHORITY\SYSTEM:(I)(F)</c> is the drive
/// letter. Every key on every ordinary machine came back "loose", and the app
/// offered to strip inheritance from keys that were already correct.
///
/// Windows-only, and skipped elsewhere: an ACL is not a concept on the other
/// platforms, where OpenSSH checks a permission bitmask instead.
/// </remarks>
public class AclTests
{
    [Fact]
    public void AFileOnlyTheOwnerCanReachIsTight()
    {
        if (!OperatingSystem.IsWindows()) return;

        var path = Temp();
        try
        {
            File.WriteAllText(path, "not a real key");
            // What SshKeyManager itself does to a key it creates.
            Assert.True(SshKeyManager.RestrictPermissions(path), "icacls refused to tighten the file");
            Assert.True(SshKeyManager.AclIsTight(path), "a file only the owner can read reads as loose");
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AFileInheritingTheProfileDefaultsIsTight()
    {
        if (!OperatingSystem.IsWindows()) return;

        // The ordinary case, and the one the old parser got wrong: a file under
        // the user's profile inherits SYSTEM, Administrators and the user, all
        // three of which OpenSSH accepts.
        var path = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.UserProfile),
            $"sm-acl-{Guid.NewGuid():N}.tmp");
        try
        {
            File.WriteAllText(path, "not a real key");
            Assert.True(
                SshKeyManager.AclIsTight(path),
                "a file with the profile's inherited entries reads as loose");
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AFileEveryoneCanReadIsNotTight()
    {
        if (!OperatingSystem.IsWindows()) return;

        var path = Temp();
        try
        {
            File.WriteAllText(path, "not a real key");
            SshKeyManager.RestrictPermissions(path);
            // *S-1-1-0 is Everyone, written as a SID so the test does not
            // depend on the account name the machine's language uses.
            Assert.True(Icacls(path, "/grant", "*S-1-1-0:(R)"), "could not widen the ACL");
            Assert.False(
                SshKeyManager.AclIsTight(path),
                "a file Everyone can read reads as tight");
        }
        finally
        {
            Delete(path);
        }
    }

    [Fact]
    public void AFileThatCannotBeExaminedIsNotReportedAsLoose()
    {
        if (!OperatingSystem.IsWindows()) return;

        // A missing file throws inside the check. Reporting "loose" there
        // would put a warning banner on a key that simply is not there.
        Assert.True(SshKeyManager.AclIsTight(Path.Combine(Path.GetTempPath(), "sm-no-such-file")));
    }

    private static string Temp() =>
        Path.Combine(Path.GetTempPath(), $"sm-acl-{Guid.NewGuid():N}.tmp");

    private static void Delete(string path)
    {
        try
        {
            File.Delete(path);
        }
        catch (Exception)
        {
            // A test that cannot clean up should still report its result.
        }
    }

    private static bool Icacls(string path, params string[] arguments)
    {
        var info = new ProcessStartInfo("icacls")
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };
        info.ArgumentList.Add(path);
        foreach (var argument in arguments) info.ArgumentList.Add(argument);

        using var process = Process.Start(info);
        if (process is null) return false;
        process.StandardOutput.ReadToEnd();
        process.StandardError.ReadToEnd();
        return process.WaitForExit(5000) && process.ExitCode == 0;
    }
}
