using System.Reflection;
using Renci.SshNet;
using Renci.SshNet.Sftp;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class SftpTransferTests : IDisposable
{
    private readonly string _directory = Path.Combine(Path.GetTempPath(), $"sm-transfer-test-{Guid.NewGuid():N}");
    private readonly SshTarget _target = new(Guid.NewGuid(), "offline.invalid", 22, "test", SshCredential.Agent);
    public SftpTransferTests() => Directory.CreateDirectory(_directory);
    public void Dispose() => Directory.Delete(_directory, recursive: true);

    [Fact]
    public void BatchNamesAvoidSanitizationCaseAndExistingFileCollisions()
    {
        File.WriteAllText(Path.Combine(_directory, "a_b.txt"), "existing");
        var paths = SftpPath.LocalDownloadPaths(_directory, ["a:b.txt", "a?b.txt", "A_B.TXT", "a_b (2).txt"]);
        Assert.Equal(4, paths.Distinct(StringComparer.OrdinalIgnoreCase).Count());
        Assert.DoesNotContain(Path.Combine(_directory, "a_b.txt"), paths, StringComparer.OrdinalIgnoreCase);
        Assert.All(paths, p => Assert.False(File.Exists(p)));
    }

    [Fact]
    public async Task CancelledUploadPreservesDestinationCleansPartialAndReleasesLease()
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "new content");
        var fake = FakeSftp.Create();
        fake.Files["/destination"] = [7, 8, 9];
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fake.Upload = async (_, path, token) =>
        {
            fake.Files[path] = [1];
            entered.SetResult();
            await Task.Delay(Timeout.Infinite, token);
        };
        using var stop = new CancellationTokenSource();
        var activity = new SftpActivity();
        var browser = new SftpBrowser((_, _) => Task.FromResult(activity.Acquire(fake.Client)));
        var transfer = browser.UploadAsync(_target, local, "/destination", cancellationToken: stop.Token);
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        Assert.False(activity.CanSweep(DateTime.UtcNow.AddHours(1), TimeSpan.Zero, true));
        stop.Cancel();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => transfer);
        Assert.Equal(new byte[] { 7, 8, 9 }, fake.Files["/destination"]);
        Assert.Single(fake.Files);
        Assert.Empty(fake.Renames);
        Assert.True(activity.CanSweep(DateTime.UtcNow, TimeSpan.Zero, true));
    }

    [Fact]
    public async Task ACompletedUploadReplacesOnlyAtTheFinalAtomicRename()
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "new content");
        var fake = FakeSftp.Create();
        fake.Files["/destination"] = [7, 8, 9];
        var activity = new SftpActivity();
        fake.Upload = async (input, path, token) =>
        {
            Assert.NotEqual("/destination", path);
            Assert.Equal(new byte[] { 7, 8, 9 }, fake.Files["/destination"]);
            Assert.False(activity.CanSweep(DateTime.UtcNow.AddHours(1), TimeSpan.Zero, true));
            using var output = new MemoryStream();
            await input.CopyToAsync(output, token);
            fake.Files[path] = output.ToArray();
        };
        var browser = new SftpBrowser((_, _) => Task.FromResult(activity.Acquire(fake.Client)));
        await browser.UploadAsync(_target, local, "/destination");
        Assert.Equal(File.ReadAllBytes(local), fake.Files["/destination"]);
        Assert.Single(fake.Renames);
        Assert.True(fake.Renames[0].Posix);
        Assert.Single(fake.Files);
    }

    [Theory]
    [InlineData(600)]
    [InlineData(640)]
    [InlineData(4750)]
    public async Task UploadPreservesDestinationModeOwnerAndGroupWithoutCopyingItsSize(short mode)
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "new content is longer than the original");
        var fake = FakeSftp.Create();
        fake.Files["/destination"] = [7];
        fake.Attributes["/destination"] = FakeSftp.FileAttributes(1, mode, 1234, 5678);
        fake.BeforeWrite = path => Assert.Equal(600, FakeSftp.Mode(fake.Attributes[path]));
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        await browser.UploadAsync(_target, local, "/destination");
        Assert.Equal(File.ReadAllBytes(local), fake.Files["/destination"]);
        Assert.Equal(mode, FakeSftp.Mode(fake.Attributes["/destination"]));
        Assert.Equal(1234, fake.Attributes["/destination"].UserId);
        Assert.Equal(5678, fake.Attributes["/destination"].GroupId);
        Assert.All(fake.Opens, opened =>
        {
            Assert.Equal(FileMode.CreateNew, opened.Mode);
            Assert.Equal(FileAccess.Write, opened.Access);
        });
        Assert.Single(fake.Files);
    }

    [Fact]
    public async Task NewUploadBecomesPrivateBeforeTheFirstContentWriteAndStaysPrivate()
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "private key material");
        var fake = FakeSftp.Create();
        var restricted = false;
        fake.BeforeSetAttributes = (path, attributes) =>
        {
            Assert.Empty(fake.Files[path]);
            Assert.Equal(600, FakeSftp.Mode(attributes));
            restricted = true;
        };
        fake.BeforeWrite = path =>
        {
            Assert.True(restricted);
            Assert.Equal(600, FakeSftp.Mode(fake.Attributes[path]));
        };
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        await browser.UploadAsync(_target, local, "/new-file");
        Assert.Equal(File.ReadAllBytes(local), fake.Files["/new-file"]);
        Assert.Equal(600, FakeSftp.Mode(fake.Attributes["/new-file"]));
    }

    [Theory]
    [InlineData(true)]
    [InlineData(false)]
    public async Task FailedPermissionChangeNeverReplacesDestination(bool initialRestriction)
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "new secret");
        var fake = FakeSftp.Create();
        fake.Files["/destination"] = [7];
        fake.Attributes["/destination"] = FakeSftp.FileAttributes(1, 640, 1234, 5678);
        var changes = 0;
        var writes = 0;
        fake.BeforeWrite = _ => writes++;
        fake.BeforeSetAttributes = (_, _) =>
        {
            if (++changes == (initialRestriction ? 1 : 2)) throw new UnauthorizedAccessException("chmod/chown denied");
        };
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        await Assert.ThrowsAsync<UnauthorizedAccessException>(() => browser.UploadAsync(_target, local, "/destination"));
        Assert.Equal(new byte[] { 7 }, fake.Files["/destination"]);
        Assert.Equal(640, FakeSftp.Mode(fake.Attributes["/destination"]));
        Assert.Equal(1234, fake.Attributes["/destination"].UserId);
        Assert.Empty(fake.Renames);
        Assert.Single(fake.Files);
        if (initialRestriction) Assert.Equal(0, writes);
        else Assert.True(writes > 0);
    }

    [Fact]
    public async Task UnsupportedAtomicReplaceLeavesTheExistingDestinationIntact()
    {
        var local = Path.Combine(_directory, "source");
        File.WriteAllText(local, "new content");
        var fake = FakeSftp.Create();
        fake.Files["/destination"] = [7];
        fake.SupportsAtomicRename = false;
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        await Assert.ThrowsAsync<IOException>(() => browser.UploadAsync(_target, local, "/destination"));
        Assert.Equal(new byte[] { 7 }, fake.Files["/destination"]);
        Assert.Single(fake.Files);
    }

    [Fact]
    public async Task CancelledDownloadPreservesExistingFileAndUnrelatedPartFile()
    {
        var local = Path.Combine(_directory, "destination");
        File.WriteAllText(local, "original");
        File.WriteAllText(local + ".part", "unrelated");
        var fake = FakeSftp.Create();
        var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        fake.Download = async (output, token) =>
        {
            await output.WriteAsync(new byte[] { 1 }, token);
            entered.SetResult();
            await Task.Delay(Timeout.Infinite, token);
        };
        using var stop = new CancellationTokenSource();
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        var transfer = browser.DownloadAsync(_target, "/remote", local, cancellationToken: stop.Token);
        await entered.Task.WaitAsync(TimeSpan.FromSeconds(3));
        stop.Cancel();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => transfer);
        Assert.Equal("original", File.ReadAllText(local));
        Assert.Equal("unrelated", File.ReadAllText(local + ".part"));
        Assert.Equal(2, Directory.GetFiles(_directory).Length);
    }

    [Fact]
    public async Task AFileAppearingAfterBatchPlanningIsNotOverwritten()
    {
        var local = SftpPath.LocalDownloadPaths(_directory, ["report"])[0];
        File.WriteAllText(local, "created by another process");
        var fake = FakeSftp.Create();
        var browser = new SftpBrowser((_, _) => Task.FromResult(new SftpLease(fake.Client, () => { })));
        await Assert.ThrowsAsync<IOException>(() => browser.DownloadAsync(_target, "/remote", local, overwrite: false));
        Assert.Equal("created by another process", File.ReadAllText(local));
        Assert.Single(Directory.GetFiles(_directory));
    }
}

/// <summary>Only the SFTP wire boundary is replaced; tests exercise the real browser.</summary>
public class FakeSftp : DispatchProxy
{
    public ISftpClient Client { get; private set; } = null!;
    public Dictionary<string, byte[]> Files { get; } = [];
    public Dictionary<string, SftpFileAttributes> Attributes { get; } = [];
    public List<(string Path, FileMode Mode, FileAccess Access)> Opens { get; } = [];
    public List<(string From, string To, bool Posix)> Renames { get; } = [];
    public bool SupportsAtomicRename { get; set; } = true;
    public Func<Stream, string, CancellationToken, Task>? Upload { get; set; }
    public Func<Stream, CancellationToken, Task>? Download { get; set; }
    public Action<string>? BeforeWrite { get; set; }
    public Action<string, SftpFileAttributes>? BeforeSetAttributes { get; set; }

    public static short Mode(SftpFileAttributes attributes) => (short)(
        (attributes.IsUIDBitSet ? 4000 : 0) + (attributes.IsGroupIDBitSet ? 2000 : 0) + (attributes.IsStickyBitSet ? 1000 : 0)
        + (attributes.OwnerCanRead ? 400 : 0) + (attributes.OwnerCanWrite ? 200 : 0) + (attributes.OwnerCanExecute ? 100 : 0)
        + (attributes.GroupCanRead ? 40 : 0) + (attributes.GroupCanWrite ? 20 : 0) + (attributes.GroupCanExecute ? 10 : 0)
        + (attributes.OthersCanRead ? 4 : 0) + (attributes.OthersCanWrite ? 2 : 0) + (attributes.OthersCanExecute ? 1 : 0));

    public static SftpFileAttributes FileAttributes(long size, short mode = 644, int userId = 1000, int groupId = 1000)
    {
        // Construct the SDK's normal stat result, including its original-value
        // tracking, rather than bypassing initialization with an empty object.
        var constructor = typeof(SftpFileAttributes).GetConstructors(BindingFlags.Instance | BindingFlags.NonPublic).Single();
        return (SftpFileAttributes)constructor.Invoke([DateTime.UtcNow, DateTime.UtcNow, size, userId, groupId,
            (uint)(0x8000 | Convert.ToInt32(mode.ToString(), 8)), new Dictionary<string, string>()]);
    }

    public static FakeSftp Create()
    {
        var client = Create<ISftpClient, FakeSftp>();
        var proxy = (FakeSftp)client;
        proxy.Client = client;
        return proxy;
    }

    protected override object? Invoke(MethodInfo? method, object?[]? args)
    {
        args ??= [];
        switch (method!.Name)
        {
            case "GetAttributesAsync":
                var path = (string)args[0]!;
                if (path == "/remote") return Task.FromResult(FileAttributes(100));
                if (!Files.ContainsKey(path)) throw new Renci.SshNet.Common.SftpPathNotFoundException(path);
                var attributes = Attributes.GetValueOrDefault(path) ?? FileAttributes(Files[path].LongLength);
                return Task.FromResult(FileAttributes(Files[path].LongLength, Mode(attributes), attributes.UserId, attributes.GroupId));
            case "SetAttributes":
                var changedPath = (string)args[0]!;
                var changed = (SftpFileAttributes)args[1]!;
                BeforeSetAttributes?.Invoke(changedPath, changed);
                Attributes[changedPath] = FileAttributes(changed.Size, Mode(changed), changed.UserId, changed.GroupId);
                // Model the dangerous side effect of accidentally restoring an
                // original Size along with metadata: a real SETSTAT truncates.
                var bytes = Files[changedPath];
                Array.Resize(ref bytes, checked((int)changed.Size));
                Files[changedPath] = bytes;
                return null;
            case "OpenAsync":
                var openedPath = (string)args[0]!;
                Assert.Equal(FileMode.CreateNew, args[1]);
                Files.Add(openedPath, []);
                Attributes[openedPath] = FileAttributes(0, 666);
                Opens.Add((openedPath, (FileMode)args[1]!, (FileAccess)args[2]!));
                return Task.FromResult(FakeSftpSession.Open(openedPath));
            case "UploadFileAsync":
                Assert.True((bool)args[2]!);
                Assert.True(Files.ContainsKey((string)args[1]!));
                BeforeWrite?.Invoke((string)args[1]!);
                return Upload?.Invoke((Stream)args[0]!, (string)args[1]!, (CancellationToken)args[^1]!)
                    ?? SaveAsync((Stream)args[0]!, (string)args[1]!, (CancellationToken)args[^1]!);
            case "DownloadFileAsync":
                return Download?.Invoke((Stream)args[1]!, (CancellationToken)args[^1]!)
                    ?? ((Stream)args[1]!).WriteAsync(new byte[] { 1, 2, 3 }, (CancellationToken)args[^1]!).AsTask();
            case "RenameFile":
                if (!SupportsAtomicRename) throw new NotSupportedException("posix rename unavailable");
                Rename((string)args[0]!, (string)args[1]!, (bool)args[2]!);
                return null;
            case "RenameFileAsync":
                Rename((string)args[0]!, (string)args[1]!, false);
                return Task.CompletedTask;
            case "ExistsAsync": return Task.FromResult(Files.ContainsKey((string)args[0]!));
            case "DeleteFileAsync": Files.Remove((string)args[0]!); Attributes.Remove((string)args[0]!); return Task.CompletedTask;
            default: throw new NotSupportedException(method.Name);
        }
    }

    private async Task SaveAsync(Stream input, string path, CancellationToken token)
    {
        using var output = new MemoryStream();
        await input.CopyToAsync(output, token);
        Files[path] = output.ToArray();
    }

    private void Rename(string from, string to, bool posix)
    {
        Renames.Add((from, to, posix));
        Files[to] = Files[from];
        Files.Remove(from);
        Attributes[to] = Attributes[from];
        Attributes.Remove(from);
    }
}

/// <summary>Exercise the actual sealed SDK stream used to create the empty temporary.</summary>
public class FakeSftpSession : DispatchProxy
{
    public static SftpFileStream Open(string path)
    {
        var sessionType = typeof(SftpFileStream).Assembly.GetType("Renci.SshNet.Sftp.ISftpSession")!;
        var session = (FakeSftpSession)Create(sessionType, typeof(FakeSftpSession));
        var constructor = typeof(SftpFileStream).GetConstructors(BindingFlags.Instance | BindingFlags.NonPublic).Single();
        return (SftpFileStream)constructor.Invoke([session, path, FileAccess.Write, true, 65536, 65536, new byte[] { 1 }, 0L, null]);
    }

    protected override object? Invoke(MethodInfo? method, object?[]? args)
    {
        args ??= [];
        return method!.Name switch
        {
            "get_OperationTimeout" => 10000,
            "get_IsOpen" => true,
            "RequestWriteAsync" => throw new InvalidOperationException("The creation stream must never buffer content."),
            "RequestCloseAsync" => Task.CompletedTask,
            "RequestClose" or "Dispose" => null,
            _ => throw new NotSupportedException(method.Name),
        };
    }
}
