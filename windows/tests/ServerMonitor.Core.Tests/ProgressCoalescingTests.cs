using Xunit;

namespace ServerMonitor.Core.Tests;

public class ProgressCoalescingTests
{
    [Fact]
    public void ABurstPostsOnceAndDeliversTheNewestValueIncludingCompletion()
    {
        var queue = new Queue<Action>();
        var delivered = new List<int>();
        using var progress = new CoalescedProgress<int>(queue.Enqueue, delivered.Add);
        for (var i = 0; i <= 10000; i++) progress.Report(i);
        Assert.Single(queue);
        queue.Dequeue()();
        Assert.Equal([10000], delivered);
        progress.Report(10001);
        Assert.Single(queue);
        queue.Dequeue()();
        Assert.Equal([10000, 10001], delivered);
    }

    [Fact]
    public void ClosingADetachedViewDiscardsQueuedCallbacks()
    {
        var queue = new Queue<Action>();
        var delivered = new List<int>();
        var progress = new CoalescedProgress<int>(queue.Enqueue, delivered.Add);
        progress.Report(1);
        progress.Dispose();
        queue.Dequeue()();
        progress.Report(2);
        Assert.Empty(queue);
        Assert.Empty(delivered);
    }
}
