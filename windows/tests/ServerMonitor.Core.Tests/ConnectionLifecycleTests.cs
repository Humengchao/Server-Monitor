using System.Collections;
using System.Reflection;
using Renci.SshNet;
using ServerMonitor.Core.Ssh;
using ServerMonitor.Core.Store;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class ConnectionLifecycleTests
{
    [Fact]
    public async Task EvictionAllowsTheCurrentGateOwnerAndWaiterToFinish()
    {
        await using var transport = new SshNetTransport(new InMemoryCredentialStore(), SshConfig.Parse(""));
        var target = new SshTarget(Guid.NewGuid(), "offline.invalid", 22, "test", SshCredential.Password);
        var type = typeof(SshNetTransport).GetNestedType("Pooled", BindingFlags.NonPublic)!;
        var pooled = Activator.CreateInstance(type)!;
        type.GetProperty("Client")!.SetValue(pooled, new SshClient("offline.invalid", "test", "unused"));
        type.GetProperty("Fingerprint")!.SetValue(pooled, "test");
        var gate = (SemaphoreSlim)type.GetProperty("Gate")!.GetValue(pooled)!;
        var pool = (IDictionary)typeof(SshNetTransport).GetField("_pool", BindingFlags.Instance | BindingFlags.NonPublic)!.GetValue(transport)!;
        pool.Add(target.ServerId, pooled);
        await gate.WaitAsync();
        var waiter = gate.WaitAsync();

        // This is the same eviction path RunAsync uses before retrying a dead session.
        await transport.DisconnectAsync(target);
        gate.Release();
        await waiter.WaitAsync(TimeSpan.FromSeconds(2));
        gate.Release();
        Assert.Empty(pool);
    }

    [Fact]
    public void AnActiveSftpOperationSurvivesAnyIdleAgeAndStartsIdleTimeOnRelease()
    {
        var now = DateTime.UtcNow;
        var activity = new SftpActivity(() => now);
        using var client = new SftpClient("offline.invalid", "test", "unused");
        var first = activity.Acquire(client);
        var second = activity.Acquire(client);
        now += TimeSpan.FromHours(2);
        Assert.False(activity.CanSweep(now, TimeSpan.FromMinutes(5), connected: true));
        first.Dispose();
        first.Dispose();
        Assert.False(activity.CanSweep(now, TimeSpan.Zero, connected: false));
        second.Dispose();
        Assert.False(activity.CanSweep(now, TimeSpan.FromMinutes(5), connected: true));
        Assert.True(activity.CanSweep(now.AddMinutes(5), TimeSpan.FromMinutes(5), connected: true));
    }
}

public class HostTrustRegressionTests
{
    [Fact]
    public void PortsAreSeparateTrustIdentitiesIncludingHashedEntries()
    {
        var path = Path.GetTempFileName();
        try
        {
            var known = new KnownHosts(path);
            Assert.True(known.Add("host", "ssh-ed25519", [1, 2, 3], 2222));
            Assert.StartsWith("[host]:2222 ", File.ReadAllText(path), StringComparison.Ordinal);
            Assert.Equal(HostKeyVerdict.Changed, known.Check("host", "ssh-ed25519", [4], 2222));
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", [4], 22));
            var salt = new byte[] { 1, 2, 3 };
            using var hmac = new System.Security.Cryptography.HMACSHA1(salt);
            var digest = hmac.ComputeHash(System.Text.Encoding.UTF8.GetBytes("[host]:2222"));
            File.WriteAllText(path, $"|1|{Convert.ToBase64String(salt)}|{Convert.ToBase64String(digest)} ssh-ed25519 AQID\n");
            Assert.Equal(HostKeyVerdict.Known, known.Check("host", "ssh-ed25519", [1, 2, 3], 2222));
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void AnExternalRevocationOverridesAnAlreadyKnownKey()
    {
        var path = Path.GetTempFileName();
        try
        {
            var known = new KnownHosts(path);
            Assert.True(known.Add("node.example.com", "ssh-ed25519", [1, 2, 3]));
            Assert.Equal(HostKeyVerdict.Known, known.Check("node.example.com", "ssh-ed25519", [1, 2, 3]));
            File.AppendAllText(path, "@revoked *.example.com,!safe.example.com ssh-ed25519 AQID\n");
            Assert.Equal(HostKeyVerdict.Revoked, known.Check("node.example.com", "ssh-ed25519", [1, 2, 3]));
            Assert.False(known.Add("node.example.com", "ssh-ed25519", [1, 2, 3]));
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("safe.example.com", "ssh-ed25519", [1, 2, 3]));
        }
        finally { File.Delete(path); }
    }

    [Fact]
    public void FailureToPersistANewKeyDoesNotAcceptIt()
    {
        var path = Path.GetTempFileName();
        try
        {
            var known = new KnownHosts(path);
            using var reader = File.Open(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", [1]));
            Assert.False(known.Add("host", "ssh-ed25519", [1]));
        }
        finally { File.Delete(path); }
    }
}
