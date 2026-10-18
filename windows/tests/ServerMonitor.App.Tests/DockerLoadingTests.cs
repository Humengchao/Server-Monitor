using System.Reflection;
using System.IO;
using ServerMonitor.App.Views;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.App.Tests;

public class DockerLoadingTests
{
    [Theory]
    [InlineData(false)]
    [InlineData(true)]
    public async Task LateResultsAndFailuresFromThePreviousHostCannotReplaceTheCurrentHost(bool failOld)
    {
        using var fixture = new DockerFixture();
        var oldReply = new TaskCompletionSource<string>(TaskCreationOptions.RunContinuationsAsynchronously);
        CancellationToken oldToken = default;
        fixture.Transport.Handler = (command, target, token) =>
        {
            if (target.ServerId == fixture.A.Id && command.Contains("ps -a", StringComparison.Ordinal))
            {
                oldToken = token;
                return oldReply.Task; // Deliberately ignores cancellation: generation must protect publication too.
            }
            return Task.FromResult(Answer(command, target.ServerId == fixture.A.Id ? "A" : "B"));
        };
        var page = UiThread.Run(() => new DockerPage(fixture.Monitor, _ => { }));
        var initial = UiThread.Run(() => page.PendingLoad);
        await UiThread.Run(() => page.SelectHostAsync(fixture.B)).WaitAsync(TimeSpan.FromSeconds(5));
        Assert.True(oldToken.IsCancellationRequested);
        if (failOld) oldReply.SetException(new IOException("old host failed"));
        else oldReply.SetResult(Answer("ps -a", "A"));
        await initial.WaitAsync(TimeSpan.FromSeconds(5));
        UiThread.Run(() =>
        {
            Assert.Equal("B", Assert.Single(page.LoadedContainers).Name);
            Assert.Null(typeof(DockerPage).GetField("_error", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(page));
            page.Deactivate();
        });
        Assert.Single(fixture.Transport.Calls, c => c.Target.ServerId == fixture.A.Id);
    }

    [Fact]
    public async Task AnActionCompletingAfterNavigationDoesNotStartAnotherInventoryRead()
    {
        using var fixture = new DockerFixture();
        var actionReply = new TaskCompletionSource<string>(TaskCreationOptions.RunContinuationsAsynchronously);
        fixture.Transport.Handler = (command, _, _) => command.Contains("docker stop", StringComparison.Ordinal)
            ? actionReply.Task : Task.FromResult(Answer(command, "A"));
        var page = UiThread.Run(() => new DockerPage(fixture.Monitor, _ => { }));
        await UiThread.Run(() => page.PendingLoad).WaitAsync(TimeSpan.FromSeconds(5));
        var action = UiThread.Run(() =>
        {
            var generation = (int)typeof(DockerPage).GetField("_generation", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(page)!;
            return page.PerformAsync(ContainerAction.Stop, page.LoadedContainers[0], fixture.A,
                fixture.Monitor.Target(fixture.A), generation);
        });
        var count = fixture.Transport.Calls.Count;
        UiThread.Run(page.Deactivate);
        actionReply.SetResult("");
        await action.WaitAsync(TimeSpan.FromSeconds(5));
        Assert.Equal(count, fixture.Transport.Calls.Count);
        Assert.Equal(fixture.A.Id, fixture.Transport.Calls[^1].Target.ServerId);
    }

    [Fact]
    public async Task AMenuFromAnOldHostCannotSendCommandsToTheNewHost()
    {
        using var fixture = new DockerFixture();
        fixture.Transport.Handler = (command, target, _) => Task.FromResult(Answer(command, target.ServerId == fixture.A.Id ? "A" : "B"));
        var page = UiThread.Run(() => new DockerPage(fixture.Monitor, _ => { }));
        await UiThread.Run(() => page.PendingLoad).WaitAsync(TimeSpan.FromSeconds(5));
        var oldMenu = UiThread.Run(() => (System.Windows.Controls.ContextMenu)typeof(DockerPage)
            .GetMethod("ContainerMenu", BindingFlags.Instance | BindingFlags.NonPublic)!.Invoke(page, [0])!);
        await UiThread.Run(() => page.SelectHostAsync(fixture.B)).WaitAsync(TimeSpan.FromSeconds(5));
        var count = fixture.Transport.Calls.Count;
        UiThread.Run(() =>
        {
            ((System.Windows.Controls.MenuItem)oldMenu.Items[0]).RaiseEvent(
                new System.Windows.RoutedEventArgs(System.Windows.Controls.MenuItem.ClickEvent));
            page.Deactivate();
        });
        Assert.Equal(count, fixture.Transport.Calls.Count);
    }

    private static string Answer(string command, string name) => command.Contains("ps -a", StringComparison.Ordinal)
        ? string.Join('\u001f', name + "-id", name, "image", "running", "up")
        : command.Contains("docker info", StringComparison.Ordinal) ? "24.0|0|1|0" : "";

    private sealed class DockerFixture : IDisposable
    {
        private readonly Database _database = Database.InMemory();
        public TestTransport Transport { get; } = new();
        public MonitorService Monitor { get; }
        public Server A { get; }
        public Server B { get; }

        public DockerFixture()
        {
            A = new Server { Name = "A", Host = "a.invalid", Username = "test", DockerVersion = "24.0", SortIndex = 0 };
            B = new Server { Name = "B", Host = "b.invalid", Username = "test", DockerVersion = "24.0", SortIndex = 1 };
            _database.Save(A);
            _database.Save(B);
            Monitor = UiThread.Run(() => new MonitorService(_database, new AppSettings(),
                new InMemoryCredentialStore(), _ => Transport, action => UiThread.Run(action), latency: new NoLatencyProbe()));
        }
        public void Dispose() => _database.Dispose();
    }

    private sealed class TestTransport : ISshTransport
    {
        public string Name => "offline test";
        public Func<string, SshTarget, CancellationToken, Task<string>> Handler { get; set; } = (_, _, _) => Task.FromResult("");
        public List<(string Command, SshTarget Target)> Calls { get; } = [];
        public Task<string> RunAsync(string command, SshTarget target, int timeoutSeconds = 30, CancellationToken cancellationToken = default)
        {
            Calls.Add((command, target));
            return Handler(command, target, cancellationToken);
        }
        public Task DisconnectAsync(SshTarget target) => Task.CompletedTask;
        public ValueTask DisposeAsync() => ValueTask.CompletedTask;
    }
}
