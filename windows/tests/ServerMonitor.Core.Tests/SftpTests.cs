using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// Remote path arithmetic, and the Windows naming rules a download runs into.
/// </summary>
public class SftpPathTests
{
    [Theory]
    [InlineData("/var/log", "syslog", "/var/log/syslog")]
    [InlineData("/var/log/", "syslog", "/var/log/syslog")]
    [InlineData("/", "etc", "/etc")]
    [InlineData("", "etc", "/etc")]
    [InlineData("/home/ops", "/absolute", "/absolute")]
    public void CombineJoinsWithForwardSlashes(string directory, string name, string expected) =>
        Assert.Equal(expected, SftpPath.Combine(directory, name));

    [Theory]
    [InlineData("/var/log/syslog", "/var/log")]
    [InlineData("/var/log/", "/var")]
    [InlineData("/var", "/")]
    [InlineData("/", "/")]
    public void ParentWalksUpAndStopsAtTheRoot(string path, string expected) =>
        Assert.Equal(expected, SftpPath.Parent(path));

    [Theory]
    [InlineData("/var/log/syslog", "syslog")]
    [InlineData("/var/log/", "log")]
    [InlineData("syslog", "syslog")]
    public void NameIsTheLastComponent(string path, string expected) =>
        Assert.Equal(expected, SftpPath.Name(path));

    [Theory]
    [InlineData("/var/log/../lib", "/var/lib")]
    [InlineData("/var//log///", "/var/log")]
    [InlineData("/var/./log", "/var/log")]
    [InlineData("/../..", "/")]
    [InlineData("/", "/")]
    public void NormaliseCollapsesDotsAndCannotEscapeTheRoot(string path, string expected) =>
        Assert.Equal(expected, SftpPath.Normalise(path));

    [Theory]
    [InlineData("syslog", true)]
    [InlineData(".hidden", true)]
    [InlineData("", false)]
    [InlineData(".", false)]
    [InlineData("..", false)]
    [InlineData("a/b", false)]
    public void OnlyAUsableNameIsValid(string name, bool expected) =>
        Assert.Equal(expected, SftpPath.IsValidName(name));

    [Theory]
    // Ordinary names survive untouched.
    [InlineData("syslog", "syslog")]
    [InlineData("backup.tar.gz", "backup.tar.gz")]
    // The nine characters Windows refuses.
    [InlineData("report:2026.txt", "report_2026.txt")]
    [InlineData("a<b>c|d?e*f\"g", "a_b_c_d_e_f_g")]
    // A trailing dot or space is legal on ext4 and unrepresentable here.
    [InlineData("notes.", "notes")]
    [InlineData("notes ", "notes")]
    // DOS device names, with and without an extension.
    [InlineData("aux", "_aux")]
    [InlineData("CON.log", "_CON.log")]
    [InlineData("com9", "_com9")]
    // A dotfile is not a device name with an empty stem.
    [InlineData(".bashrc", ".bashrc")]
    // Nothing left to keep.
    [InlineData("...", "download")]
    public void ALocalNameIsAlwaysSomethingWindowsAccepts(string remote, string expected) =>
        Assert.Equal(expected, SftpPath.LocalNameFor(remote));

    [Fact]
    public void ALocalNameIsTakenFromTheLastComponent() =>
        Assert.Equal("syslog", SftpPath.LocalNameFor("/var/log/syslog"));

    [Fact]
    public void ALocalNameFitsInAPathComponent()
    {
        // ext4 allows 255 bytes per component and NTFS 255 UTF-16 units, so a
        // legal remote name can still be one Windows refuses — anything past
        // 255 has to be cut rather than passed through.
        var tooLong = new string('a', 300);
        Assert.Equal(255, SftpPath.LocalNameFor(tooLong).Length);
    }
}

/// <summary>Transfer progress arithmetic.</summary>
public class TransferProgressTests
{
    [Fact]
    public void FractionIsBounded()
    {
        Assert.Equal(0.5, new TransferProgress("f", 50, 100).Fraction);
        // A host that reported a stale size, so more arrived than expected.
        Assert.Equal(1, new TransferProgress("f", 150, 100).Fraction);
        // An empty file is finished, not divided by zero.
        Assert.Equal(0, new TransferProgress("f", 0, 0).Fraction);
    }
}
