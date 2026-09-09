using System.Reflection;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// What assistive technology hears when it reaches a row of a table.
/// </summary>
/// <remarks>
/// A ListViewItem holding a plain object takes its automation name from that
/// object's ToString, and every one of these tables binds rows that are C#
/// records — whose generated ToString prints every property, nested records
/// included. Read back from the live automation tree, one SFTP row announced
/// itself as:
///
///   Row { Entry = SftpEntry { Name = photos, Path = /root/…,
///   IsDirectory = True, IsSymlink = False, Size = 0, Modified = …,
///   Permissions = drwxr-xr-x, IsHidden = False }, Name = photos/,
///   Size = , Modified = …, Mode = drwxr-xr-x }
///
/// — the file named four times over. The machines table was worse: its row
/// carries an id, a brush and the whole Server, so it read out a GUID, a
/// colour and the host's address and login before saying the name.
///
/// These reach the records through reflection because all three are private
/// to their view. That is the right visibility for them; a row projection is
/// nobody else's business, and it does not stop this from being checkable.
/// </remarks>
public class TableRowNameTests
{
    private static object Row(string view, string row, params object?[] arguments)
    {
        var type = typeof(App).Assembly.GetType($"ServerMonitor.App.Views.{view}+{row}", true)!;
        return Activator.CreateInstance(
            type, BindingFlags.Instance | BindingFlags.NonPublic | BindingFlags.Public,
            null, arguments, null)!;
    }

    [Fact]
    public void AMachineRowSaysItsNameAndNotItsGuid()
    {
        var server = new Core.Model.Server { Name = "web-01", Host = "203.0.113.10" };
        var row = Row(
            "MachinesPage", "Row",
            server.Id, "\U0001F1E9\U0001F1EA", "web-01", "root@203.0.113.10", "Linux",
            "prod", "4", "8 GB", "100 GB", "在线",
            System.Windows.Media.Brushes.Green, "web", server);

        var spoken = row.ToString()!;
        Assert.Equal("web-01 root@203.0.113.10 在线", spoken);
        Assert.Equal(1, spoken.Split("web-01").Length - 1);
        Assert.DoesNotContain(server.Id.ToString(), spoken, StringComparison.Ordinal);
        Assert.DoesNotContain("Server {", spoken, StringComparison.Ordinal);
    }

    [Fact]
    public void AnAliasHostDoesNotSayItsNameTwice()
    {
        // DisplayTarget opens with the alias, and after an ssh-config import
        // the name is that same word — so name plus target read as
        // "web-01 web-01 · root@…".
        var server = new Core.Model.Server { Name = "web-01" };
        var row = Row(
            "MachinesPage", "Row",
            server.Id, "", "web-01", "web-01 · root@203.0.113.10", "Linux",
            "", "2", "4 GB", "40 GB", "在线",
            System.Windows.Media.Brushes.Green, "", server);

        Assert.Equal("web-01 · root@203.0.113.10 在线", row.ToString());
    }

    [Fact]
    public void ASessionRowReadsAsASentence()
    {
        var row = Row("SessionsPage", "Row", "web-01", "终端", "2026-09-09 15:04", "3 分钟");
        Assert.Equal("web-01 终端 2026-09-09 15:04 3 分钟", row.ToString());
    }

    [Fact]
    public void AnSftpRowNamesTheFileOnce()
    {
        var file = new Core.Ssh.SftpEntry(
            "notes.txt", "/root/notes.txt", false, false, 2048, DateTime.UnixEpoch, "-rw-r--r--");
        var spoken = Row("SftpWindow", "Row", file).ToString()!;

        Assert.StartsWith("notes.txt", spoken, StringComparison.Ordinal);
        Assert.DoesNotContain("IsDirectory", spoken, StringComparison.Ordinal);
        Assert.DoesNotContain("/root/notes.txt", spoken, StringComparison.Ordinal);
    }

    [Fact]
    public void AnSftpDirectoryIsNotGivenASize()
    {
        // A directory's size column is blank, so appending it would leave the
        // row announcing a trailing nothing.
        var folder = new Core.Ssh.SftpEntry(
            "logs", "/root/logs", true, false, 0, DateTime.UnixEpoch, "drwxr-xr-x");
        Assert.Equal("logs/", Row("SftpWindow", "Row", folder).ToString());
    }
}
