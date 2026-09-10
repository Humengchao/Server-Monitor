using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class DataDirectoryTests : IDisposable
{
    private readonly string _scratch = Path.Combine(
        Path.GetTempPath(), $"sm-datadir-{Guid.NewGuid():N}");

    public void Dispose()
    {
        if (Directory.Exists(_scratch)) Directory.Delete(_scratch, recursive: true);
    }

    private string ExeDir()
    {
        var dir = Path.Combine(_scratch, "app");
        Directory.CreateDirectory(dir);
        return dir;
    }

    private string Profile() => Path.Combine(_scratch, "profile");

    [Fact]
    public void NoMarkerMeansTheProfile()
    {
        var resolved = DataDirectory.Resolve(Path.Combine(ExeDir(), "ServerMonitor.exe"), Profile());
        Assert.Equal(Path.Combine(Profile(), "ServerMonitor"), resolved);
    }

    [Fact]
    public void AMarkerMovesEverythingBesideTheExe()
    {
        var exeDir = ExeDir();
        File.WriteAllText(Path.Combine(exeDir, DataDirectory.MarkerName), "portable");

        var resolved = DataDirectory.Resolve(Path.Combine(exeDir, "ServerMonitor.exe"), Profile());

        Assert.Equal(Path.Combine(exeDir, DataDirectory.PortableDirectoryName), resolved);
        // The directory exists already: the database open must not be the
        // first to learn the place is unusable.
        Assert.True(Directory.Exists(resolved));
    }

    [Fact]
    public void AMarkerSomewhereUnwritableFallsBackRatherThanFailingToStart()
    {
        var exeDir = ExeDir();
        File.WriteAllText(Path.Combine(exeDir, DataDirectory.MarkerName), "portable");
        // Data exists as a file, so creating it as a directory throws
        // IOException — the stand-in for a Program Files install that got a
        // marker anyway.
        File.WriteAllText(Path.Combine(exeDir, DataDirectory.PortableDirectoryName), "not a directory");

        var resolved = DataDirectory.Resolve(Path.Combine(exeDir, "ServerMonitor.exe"), Profile());

        Assert.Equal(Path.Combine(Profile(), "ServerMonitor"), resolved);
    }

    [Fact]
    public void NoProcessPathStillResolves()
    {
        Assert.Equal(
            Path.Combine(Profile(), "ServerMonitor"),
            DataDirectory.Resolve(null, Profile()));
    }
}
