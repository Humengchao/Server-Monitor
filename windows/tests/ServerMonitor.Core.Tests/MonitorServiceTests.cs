using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Model;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class PollBackoffTests
{
    [Fact]
    public void TheFirstFailureRetriesAtTheNormalCadence()
    {
        // One missed poll is not a pattern; backing off immediately would make
        // a single dropped packet look like an outage.
        using var harness = new Harness();
        Assert.Equal(TimeSpan.Zero, harness.Service.BackoffDelay(1));
    }

    [Fact]
    public void TheDelayGrowsGeometricallyFromThePollInterval()
    {
        using var harness = new Harness();
        harness.Settings.PollInterval = 5;
        Assert.Equal(TimeSpan.FromSeconds(10), harness.Service.BackoffDelay(2));
        Assert.Equal(TimeSpan.FromSeconds(20), harness.Service.BackoffDelay(3));
        Assert.Equal(TimeSpan.FromSeconds(40), harness.Service.BackoffDelay(4));
    }

    [Fact]
    public void TheDelayIsCappedSoARecoveryIsStillNoticed()
    {
        using var harness = new Harness();
        harness.Settings.PollInterval = 60;
        // A host down all day must still be retried often enough that coming
        // back is seen within a few minutes.
        Assert.Equal(MonitorService.MaxBackoff, harness.Service.BackoffDelay(20));
    }

    [Fact]
    public void AFailureSetsARetryTimeAndASuccessClearsIt()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");

        harness.Service.NoteFailure(server.Id);
        harness.Service.NoteFailure(server.Id);
        Assert.Equal(2, harness.Service.FailureStreak[server.Id]);
        Assert.True(harness.Service.RetryAfter.ContainsKey(server.Id));

        harness.Service.NoteSuccess(server.Id);
        Assert.False(harness.Service.FailureStreak.ContainsKey(server.Id));
        Assert.False(harness.Service.RetryAfter.ContainsKey(server.Id));
    }

    [Fact]
    public void AClockThatMovedBackwardsDoesNotStrandAHost()
    {
        // The window is an absolute time, so an NTP correction on a machine
        // with a dead RTC — or a restored VM snapshot — could otherwise leave
        // a host waiting far longer than any backoff can legitimately produce,
        // with no way out but relaunching.
        var now = DateTime.UtcNow;
        Assert.True(MonitorService.IsStillWaiting(now.AddSeconds(30), now));
        Assert.False(MonitorService.IsStillWaiting(now.AddSeconds(-30), now));
        // A remaining wait longer than the cap is not a backoff; it is a moved
        // clock.
        Assert.False(MonitorService.IsStillWaiting(now.AddDays(1), now));
    }

    [Fact]
    public async Task AHostInsideItsBackoffWindowIsSkipped()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        harness.Service.NoteFailure(server.Id);
        harness.Service.NoteFailure(server.Id);          // 10 s window

        var launched = harness.Service.PollDue();
        Assert.Empty(launched);
        await Task.WhenAll(launched);
    }

    [Fact]
    public async Task AManualRefreshOverridesTheBackoff()
    {
        // A user-driven refresh is an explicit "try now", so it must not
        // silently skip the hosts that need it most.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();
        harness.Service.NoteFailure(server.Id);
        harness.Service.NoteFailure(server.Id);

        var task = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => task.IsCompleted);
        await task;

        Assert.Equal(StatusKind.Online, harness.Service.Status[server.Id].Kind);
    }

    [Fact]
    public async Task WakingUpClearsEveryBackoffAndPollsAtOnce()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();
        harness.Service.NoteFailure(server.Id);
        harness.Service.NoteFailure(server.Id);

        harness.Service.RetryEverythingNow("woke from sleep");
        Assert.Empty(harness.Service.RetryAfter);

        await harness.Pump.PumpUntilAsync(
            () => harness.Service.Status.TryGetValue(server.Id, out var s) && s.IsOnline);
        Assert.Equal(StatusKind.Online, harness.Service.Status[server.Id].Kind);
    }

    [Fact]
    public void EnergySaverSlowsTheCadenceToAThird()
    {
        // The user has asked the whole machine to do less; nine SSH round
        // trips every five seconds is exactly the kind of background work that
        // request is about.
        Assert.Equal(5, MonitorService.EffectiveInterval(5, energySaver: false));
        Assert.Equal(15, MonitorService.EffectiveInterval(5, energySaver: true));
    }
}

public class TickBarrierTests
{
    [Fact]
    public async Task OneTickPublishesOnce()
    {
        // A plain debounce gave 3.4 publishes per tick on the macOS build,
        // because hosts finish about a second apart. Every publish is a full
        // dashboard pass, so that was three layouts to show one tick's
        // numbers.
        using var harness = new Harness();
        var servers = new[] { harness.AddServer("a"), harness.AddServer("b"), harness.AddServer("c") };
        foreach (var server in servers)
        {
            // Staggered, the way real hosts answer.
            var delay = 20 * (Array.IndexOf(servers, server) + 1);
            harness.Transport.Handlers[server.Id] =
                FakeTransport.LinuxHost(async () => await Task.Delay(delay));
        }

        // First round: each card has nothing on screen yet, so those results
        // go straight through — a spinner turning into numbers is worth its
        // own pass, once.
        var warmup = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => warmup.IsCompleted);
        await warmup;

        harness.CountPublishes();
        var second = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => second.IsCompleted);
        await second;

        Assert.Equal(1, harness.PublishCount);
    }

    [Fact]
    public async Task AResultForACardWithNothingOnScreenIsNotHeld()
    {
        using var harness = new Harness();
        var slow = harness.AddServer("slow");
        var fast = harness.AddServer("fast");
        harness.Transport.Handlers[fast.Id] = FakeTransport.LinuxHost();
        harness.Transport.Handlers[slow.Id] =
            FakeTransport.LinuxHost(async () => await Task.Delay(400));

        harness.Service.PollDue();
        // The fast host's first numbers appear without waiting for the slow
        // one; that is the exception the barrier makes deliberately.
        await harness.Pump.PumpUntilAsync(() => harness.Service.Latest.ContainsKey(fast.Id));
        Assert.True(harness.Service.Latest.ContainsKey(fast.Id));
        Assert.False(harness.Service.Latest.ContainsKey(slow.Id));

        await harness.Pump.PumpUntilAsync(() => harness.Service.Latest.ContainsKey(slow.Id));
    }

    [Fact]
    public async Task TheCapClosesATickAStragglerWouldOtherwiseHold()
    {
        // A host that has gone away takes the full connect timeout, and the
        // cap is what keeps one dead host from delaying eight live ones.
        using var harness = new Harness();
        harness.Service.TickCap = TimeSpan.FromMilliseconds(150);
        var live = harness.AddServer("live");
        var gone = harness.AddServer("gone");
        harness.Transport.Handlers[live.Id] = FakeTransport.LinuxHost();
        harness.Transport.Handlers[gone.Id] = FakeTransport.LinuxHost(
            async () => await Task.Delay(2000));

        // Give both a verdict first, so neither is the "nothing on screen"
        // exception.
        harness.Service.PollDue();
        await harness.Pump.PumpUntilAsync(() => harness.Service.Latest.ContainsKey(live.Id));

        harness.Service.PollDue();
        var closed = await harness.Pump.PumpUntilAsync(() => !harness.Service.TickOpen, 3000);
        Assert.True(closed, "the cap should have closed the tick without the straggler");
    }

    [Fact]
    public async Task AStragglerIsNotCarriedIntoTheNextTick()
    {
        // It stays in flight for its whole timeout, and with it still a member
        // every later tick could only close by the cap — eight live hosts
        // waiting the cap each round for the one that is gone.
        using var harness = new Harness();
        harness.Service.TickCap = TimeSpan.FromMilliseconds(100);
        var gone = harness.AddServer("gone");
        harness.Transport.Handlers[gone.Id] = FakeTransport.LinuxHost(
            async () => await Task.Delay(1500));

        harness.Service.PollDue();
        await harness.Pump.PumpUntilAsync(() => !harness.Service.TickOpen, 2000);
        // Still in flight, but no longer a tick member.
        Assert.Contains(gone.Id, harness.Service.InFlight);
        Assert.False(harness.Service.TickOpen);
    }

    [Fact]
    public async Task AHostAlreadyInFlightIsNotPolledTwice()
    {
        // A stall must not pile up a queue of connections behind it.
        using var harness = new Harness();
        var server = harness.AddServer("slow");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost(
            async () => await Task.Delay(300));

        harness.Service.PollDue();
        var second = harness.Service.PollDue();
        Assert.Empty(second);

        await harness.Pump.PumpUntilAsync(() => harness.Service.Latest.ContainsKey(server.Id));
        // One detect plus one batch, not two of each.
        var batches = harness.Transport.Calls.Count(c => c.Command.Contains("/proc/stat", StringComparison.Ordinal));
        Assert.Equal(1, batches);
    }

    [Fact]
    public async Task NothingIsPublishedWhileTheWindowIsHidden()
    {
        // Collection, the database and alerts are unaffected; only the UI
        // update is held. Turning the window back on applies whatever
        // accumulated, so the first frame shown is current.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        harness.Service.SetUiVisible(false);
        harness.Service.PollDue();
        await harness.Pump.PumpUntilAsync(() => harness.Service.PendingCount > 0);

        Assert.False(harness.Service.Latest.ContainsKey(server.Id));
        // But it did reach the store.
        Assert.NotNull(harness.Database.LatestSample(server.Id));

        harness.Service.SetUiVisible(true);
        Assert.True(harness.Service.Latest.ContainsKey(server.Id));
    }

    [Fact]
    public async Task HeldResultsAreReplacedNotQueued()
    {
        // With the window hidden, an append-only queue grew by one closure
        // holding a full snapshot per host per poll — tens of megabytes an
        // hour on the macOS build — until the window was next shown.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        harness.Service.SetUiVisible(false);
        // Through the tick path, not PollAllAsync: a manual refresh flushes
        // regardless of visibility on purpose (it is an explicit "show me
        // now"), so it would empty the queue this asserts on.
        for (var i = 0; i < 5; i++)
        {
            harness.Service.PollDue(ignoringBackoff: true);
            await harness.Pump.PumpUntilAsync(() => harness.Service.InFlight.Count == 0);
        }

        // One per server, the newest — not one per poll.
        Assert.Equal(1, harness.Service.PendingCount);
    }

    [Fact]
    public async Task AResultForADeletedServerIsDroppedRatherThanResurrectingIt()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        harness.Service.SetUiVisible(false);
        harness.Service.PollDue();
        await harness.Pump.PumpUntilAsync(() => harness.Service.PendingCount > 0);

        harness.Service.DeleteServer(server);
        harness.Service.SetUiVisible(true);

        Assert.Empty(harness.Service.Servers);
        Assert.False(harness.Service.Latest.ContainsKey(server.Id));
    }
}

public class PollResultTests
{
    [Fact]
    public async Task ASuccessfulPollFillsTheSnapshotAndTheHostFacts()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost(cpu: 37);

        var task = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => task.IsCompleted);
        await task;

        var snapshot = harness.Service.Latest[server.Id];
        Assert.Equal(37, snapshot.CpuPercent, 1);
        Assert.Equal(8, snapshot.Cores);
        Assert.Equal(0.52, snapshot.Load1, 2);
        Assert.Equal("web-1", snapshot.Identity.Hostname);
        Assert.Equal(84_421_599_232, snapshot.DiskTotal);
        Assert.Equal("24.0.7", snapshot.DockerVersion);

        // The facts land on the row, so the machine list shows them without a
        // second query.
        var row = harness.Service.Server(server.Id)!;
        Assert.Equal(8, row.Cores);
        Assert.Equal("24.0.7", row.DockerVersion);
        // And the engine counts arrive free with the poll.
        Assert.Equal(5, harness.Service.DockerSummaries[server.Id].Running);
    }

    [Fact]
    public async Task AFailedPollGoesOfflineWithTheReasonAttached()
    {
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] =
            _ => Task.FromException<string>(SshException.CommandFailed(255, "Connection refused"));

        var task = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => task.IsCompleted);
        await task;

        var status = harness.Service.Status[server.Id];
        Assert.Equal(StatusKind.Offline, status.Kind);
        Assert.Contains("Connection refused", status.Reason, StringComparison.Ordinal);
        Assert.Single(harness.Service.OfflineServers);
    }

    [Fact]
    public async Task AnEmptyBatchIsAFailureNotAZeroedOutHost()
    {
        // The transport exits 0 with no usable stdout when a channel is torn
        // down mid-read. Returning that would paint a real host as 0 cores,
        // 0 memory and no filesystems on the dashboard.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = command =>
            Task.FromResult(command == Probes.OsDetect ? "Linux\n" : "");

        var task = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => task.IsCompleted);
        await task;

        Assert.Equal(StatusKind.Offline, harness.Service.Status[server.Id].Kind);
        Assert.Contains("truncated", harness.Service.Status[server.Id].Reason, StringComparison.Ordinal);
    }

    [Fact]
    public async Task AHostWithNoDockerGetsNoEngineCard()
    {
        // The card is driven by the poll rather than its own round trip, so
        // "no Docker" has to mean "no entry" — not a stale entry from a host
        // that does have it.
        using var harness = new Harness();
        var withDocker = harness.AddServer("has-docker");
        var without = harness.AddServer("no-docker");
        harness.Transport.Handlers[withDocker.Id] = FakeTransport.LinuxHost();
        harness.Transport.Handlers[without.Id] = command =>
            Task.FromResult(command == Probes.OsDetect
                ? "Linux\n"
                : FakeTransport.LinuxBatch().Replace("24.0.7|14|5|3|0", ""));

        var poll = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => poll.IsCompleted);
        await poll;

        Assert.True(harness.Service.DockerSummaries.ContainsKey(withDocker.Id));
        Assert.False(harness.Service.DockerSummaries.ContainsKey(without.Id));
        Assert.Equal("", harness.Service.Server(without.Id)!.DockerVersion);
    }

    [Fact]
    public async Task TheCachedEngineAnswerStandsBetweenSamples()
    {
        // `docker info` costs the host ~90 ms and its answer changes when an
        // image is pulled, not every five seconds — so it is sampled every 30 s
        // and carried forward. An empty version in between would read as
        // "Docker was removed" and hide the card.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        var first = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => first.IsCompleted);
        await first;
        Assert.True(harness.Service.DockerSummaries.ContainsKey(server.Id));

        // The batch no longer carries a docker section at all — which is what
        // a skipped sample looks like — and the card must survive it.
        harness.Transport.Handlers[server.Id] = command =>
            Task.FromResult(command == Probes.OsDetect
                ? "Linux\n"
                : FakeTransport.LinuxBatch().Replace("24.0.7|14|5|3|0", ""));

        var second = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => second.IsCompleted);
        await second;

        Assert.Equal("24.0.7", harness.Service.Latest[server.Id].DockerVersion);
        Assert.True(harness.Service.DockerSummaries.ContainsKey(server.Id));
    }

    [Fact]
    public void TheEngineIsSampledOnFirstSightAndThenOnItsOwnCadence()
    {
        var now = DateTime.UtcNow;
        // Always when nothing is cached — a first poll, or a host that has
        // just gained Docker.
        Assert.True(MetricsCollector.ShouldSampleDocker(null, now));
        Assert.False(MetricsCollector.ShouldSampleDocker(now.AddSeconds(-5), now));
        Assert.True(MetricsCollector.ShouldSampleDocker(now.AddSeconds(-31), now));
    }

    [Fact]
    public async Task OnlyTheDetailScreensHostPaysForTheProcessList()
    {
        // `ps` over every process costs the host ~30 ms per poll, and only the
        // machine screen shows it.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Transport.Handlers[server.Id] = FakeTransport.LinuxHost();

        var dashboardOnly = harness.Service.PollAllAsync();
        await harness.Pump.PumpUntilAsync(() => dashboardOnly.IsCompleted);
        await dashboardOnly;
        Assert.DoesNotContain(
            harness.Transport.Calls,
            call => call.Command.Contains("ps -eo", StringComparison.Ordinal));

        // Opening the screen polls the host at once, so the process card fills
        // in about a second rather than waiting out the current interval —
        // which is the very poll this asserts on.
        var immediate = harness.Service.SetDetailVisible(server.Id, true);
        Assert.NotNull(immediate);
        await harness.Pump.PumpUntilAsync(() => immediate.IsCompleted);
        await immediate;
        Assert.Contains(
            harness.Transport.Calls,
            call => call.Command.Contains("ps -eo", StringComparison.Ordinal));
    }

    [Fact]
    public async Task EditingAHostDropsItsConnectionAndClearsItsBackoff()
    {
        // Address or credential may have changed, so the next poll must
        // reconnect; and editing a failing host is usually the fix for it —
        // sitting out a five-minute backoff would look like the edit did
        // nothing.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Service.NoteFailure(server.Id);
        harness.Service.NoteFailure(server.Id);

        var edited = server.Clone();
        edited.Host = "10.9.9.9";
        harness.Service.UpdateServer(edited);

        Assert.Empty(harness.Service.RetryAfter);
        // ForgetAsync runs unawaited by design, so give it a moment.
        await harness.Pump.PumpUntilAsync(() => harness.Transport.Disconnected.Contains(server.Id));
        Assert.Contains(server.Id, harness.Transport.Disconnected);
    }

    [Fact]
    public void ARelaunchSeedsTheTilesFromHistory()
    {
        // Otherwise the cards are empty until the first poll lands, which on a
        // 60-second interval is a minute of blank dashboard.
        using var harness = new Harness();
        var server = harness.AddServer("web-1");
        harness.Database.Insert(new MetricSample(
            server.Id, new MetricSnapshot { CpuPercent = 42, MemoryTotal = 16_000_000_000 }));

        harness.Service.Reload();
        Assert.Equal(42, harness.Service.Latest[server.Id].CpuPercent);
    }

    [Fact]
    public async Task AnIdentityGivesItsUsernameAndAuthToEveryServerUsingIt()
    {
        using var harness = new Harness();
        var identity = new Identity
        {
            Name = "deploy",
            Username = "deploy",
            AuthKind = AuthKind.IdentityFile,
            IdentityFile = @"C:\keys\id_ed25519",
        };
        harness.Service.Save(identity);

        var server = harness.AddServer("web-1");
        var updated = server.Clone();
        updated.IdentityId = identity.Id;
        harness.Service.UpdateServer(updated);
        await Task.Yield();

        var target = harness.Service.Target(harness.Service.Server(server.Id)!);
        Assert.Equal("deploy", target.Username);
        Assert.Equal(AuthMethod.IdentityFile, target.Credential.Method);
        Assert.Equal(@"C:\keys\id_ed25519", target.Credential.KeyPath);
    }

    [Fact]
    public void AnAliasServerDialsTheAliasNotTheAddress()
    {
        // The whole point of choosing "ssh config alias": OpenSSH — or, on the
        // library route, our own config parser — applies the entire Host block.
        using var harness = new Harness();
        var server = new Server
        {
            Name = "web",
            Host = "203.0.113.10",
            SshAlias = "web-1",
            Username = "ignored",
            AuthKind = AuthKind.SshConfigAlias,
        };
        harness.Service.AddServer(server);

        var target = harness.Service.Target(harness.Service.Server(server.Id)!);
        Assert.Equal("web-1", target.Host);
        Assert.Equal("web-1", target.SshDestination);
    }
}

public class AlertServiceTests
{
    private static MetricSnapshot Cpu(double percent) => new() { CpuPercent = percent };

    [Fact]
    public void AThresholdMustBeBreachedForSeveralPollsBeforeItFires()
    {
        // ~15 seconds of sustained load at the default interval, which filters
        // out the spike from a build or a backup starting.
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 80;
        var server = harness.AddServer("web-1");

        for (var i = 0; i < 2; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        }
        Assert.Empty(harness.Alerts);

        harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        Assert.Single(harness.Alerts);
    }

    [Fact]
    public void APersistentBreachDoesNotRepeatWithinTheCooldown()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 80;
        var server = harness.AddServer("web-1");

        for (var i = 0; i < 20; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(95));
        }
        Assert.Single(harness.Alerts);
    }

    [Fact]
    public void RecoveringResetsTheRunSoTheNextBreachMustBuildUpAgain()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 80;
        var server = harness.AddServer("web-1");
        var alerts = harness.Service.Alerts!;

        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(10));   // dipped
        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(90));
        Assert.Empty(harness.Alerts);
    }

    [Fact]
    public void AServersOwnLimitOverridesTheGlobalOne()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 90;
        var server = harness.AddServer("web-1");
        server.CpuThreshold = 50;

        // 60% is under the global 90 but over this server's own 50.
        for (var i = 0; i < 3; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(60));
        }
        Assert.Single(harness.Alerts);
    }

    [Fact]
    public void AServerCanOptOutWhileTheGlobalLimitIsOn()
    {
        // 0 is "off for this metric", which is a different thing from null,
        // meaning "follow the global setting".
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 50;
        var server = harness.AddServer("noisy");
        server.CpuThreshold = 0;

        for (var i = 0; i < 5; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(99));
        }
        Assert.Empty(harness.Alerts);
    }

    [Fact]
    public void GoingOfflineAndComingBackAreTransitionsNotStates()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = true;
        var server = harness.AddServer("web-1");
        var alerts = harness.Service.Alerts!;

        // Pinned rather than left to follow the machine: this test asserts on
        // the text, and the developer machine here runs a Chinese UI.
        using var english = new LanguageScope(Store.AppLanguage.En);

        // The first poll establishes a baseline and must not alert: the user
        // has not been told anything changed.
        alerts.Evaluate(server, ServerStatus.Offline("refused"), null);
        Assert.Empty(harness.Alerts);

        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(5));
        Assert.Single(harness.Alerts);
        Assert.Contains("Back online", harness.Alerts[0].Body, StringComparison.Ordinal);

        alerts.Evaluate(server, ServerStatus.Offline("refused"), null);
        Assert.Equal(2, harness.Alerts.Count);
        Assert.Contains("refused", harness.Alerts[1].Body, StringComparison.Ordinal);
    }

    [Fact]
    public void TheAlertTextFollowsTheSelectedLanguage()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = true;
        var server = harness.AddServer("web-1");
        var alerts = harness.Service.Alerts!;

        using (var _ = new LanguageScope(Store.AppLanguage.Zh))
        {
            alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(1));
            alerts.Evaluate(server, ServerStatus.Offline("gone"), null);
            Assert.Contains("已离线", harness.Alerts[^1].Body, StringComparison.Ordinal);
        }
    }

    [Fact]
    public void AnOfflineHostIsNotAlsoJudgedAgainstItsThresholds()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = false;
        harness.Settings.CpuThreshold = 1;
        var server = harness.AddServer("web-1");

        for (var i = 0; i < 5; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Offline("gone"), null);
        }
        Assert.Empty(harness.Alerts);
    }

    [Fact]
    public void NothingFiresWhileNotificationsAreOff()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = false;
        harness.Settings.CpuThreshold = 10;
        var server = harness.AddServer("web-1");

        for (var i = 0; i < 5; i++)
        {
            harness.Service.Alerts!.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(99));
        }
        Assert.Empty(harness.Alerts);
    }

    [Fact]
    public void TheAlertCarriesTheServerIdSoAToastCanOpenThatHost()
    {
        using var harness = new Harness(withAlerts: true);
        harness.Settings.NotificationsEnabled = true;
        harness.Settings.NotifyOnOffline = true;
        var server = harness.AddServer("web-1");
        var alerts = harness.Service.Alerts!;

        alerts.Evaluate(server, ServerStatus.Online(DateTime.UtcNow), Cpu(1));
        alerts.Evaluate(server, ServerStatus.Offline("gone"), null);

        Assert.Equal(server.Id, Assert.Single(harness.Alerts).ServerId);
    }
}
