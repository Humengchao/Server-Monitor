using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

public class SshConfigTests
{
    private static SshConfig Config() => SshConfig.Parse(Fixture.Read("ssh/config"));

    [Fact]
    public void WildcardBlocksAreNotImportableHosts()
    {
        // `Host *` describes defaults, not a machine.
        var hosts = Config().Discover();
        Assert.DoesNotContain(hosts, h => h.Alias.Contains('*'));
        Assert.Contains(hosts, h => h.Alias == "web-1");
    }

    [Fact]
    public void AHostBlockIsReadIntoItsFields()
    {
        var web = Config().Discover().First(h => h.Alias == "web-1");
        Assert.Equal("203.0.113.10", web.HostName);
        Assert.Equal("deploy", web.User);
        Assert.Equal(2222, web.Port);
        Assert.NotNull(web.IdentityFile);
        Assert.EndsWith("id_ed25519", web.IdentityFile, StringComparison.Ordinal);
        // The tilde is expanded to this machine's profile, since the config
        // most likely came from the Mac.
        Assert.DoesNotContain('~', web.IdentityFile);
    }

    [Fact]
    public void OneHostLineCanNameSeveralAliases()
    {
        var hosts = Config().Discover();
        Assert.Contains(hosts, h => h.Alias == "db-1");
        Assert.Contains(hosts, h => h.Alias == "db-primary");
        // Both carry the block's settings.
        Assert.Equal("203.0.113.11", hosts.First(h => h.Alias == "db-primary").HostName);
    }

    [Fact]
    public void AQuotedPathWithASpaceSurvives()
    {
        var db = Config().Discover().First(h => h.Alias == "db-1");
        Assert.NotNull(db.IdentityFile);
        Assert.EndsWith("db key", db.IdentityFile, StringComparison.Ordinal);
        Assert.DoesNotContain('"', db.IdentityFile);
    }

    [Fact]
    public void WithoutAHostNameTheAliasIsTheAddress()
    {
        var bastion = Config().Discover().First(h => h.Alias == "bastion.example.com");
        Assert.Equal("bastion.example.com", bastion.HostName);
        Assert.Equal("jump", bastion.User);
    }

    [Fact]
    public void EqualsSeparatedOptionsParseToo()
    {
        // Legal, and what generated configs write.
        var host = Config().Discover().First(h => h.Alias == "equals-style");
        Assert.Equal("198.51.100.7", host.HostName);
        Assert.Equal("admin", host.User);
    }

    [Fact]
    public void ADefaultUserIsAssumedWhenTheBlockIsSilent()
    {
        // ssh would use the local username; root is the useful default for a
        // monitored server, and it is what the macOS importer picks.
        var host = SshConfig.Parse("Host bare\n  HostName 10.0.0.1\n").Discover()[0];
        Assert.Equal("root", host.User);
        Assert.Equal(22, host.Port);
    }

    [Fact]
    public void ProxyJumpIsCarriedThrough()
    {
        var host = Config().Discover().First(h => h.Alias == "behind-jump");
        Assert.Equal("bastion.example.com", host.ProxyJump);
    }

    [Fact]
    public void ResolvingAnAliasAppliesTheWholeBlock()
    {
        // What the library transport needs, since it has no OpenSSH to defer
        // to.
        var resolved = Config().Resolve(new SshTarget(
            Guid.NewGuid(), "web-1", 22, "ignored", SshCredential.ConfigAlias));
        Assert.Equal("203.0.113.10", resolved.HostName);
        Assert.Equal(2222, resolved.Port);
        Assert.Equal("deploy", resolved.User);
        Assert.Single(resolved.IdentityFiles);
    }

    [Fact]
    public void ResolvingAJumpChainKeepsItsOrder()
    {
        var resolved = Config().Resolve("behind-jump");
        Assert.Equal(["bastion.example.com"], resolved.ProxyJump);
        Assert.Equal("10.10.0.5", resolved.HostName);
    }

    [Fact]
    public void ANonAliasTargetIgnoresTheConfigEntirely()
    {
        // For every auth kind but "ssh config alias" the stored row wins; the
        // config is not consulted at all.
        var resolved = Config().Resolve(new SshTarget(
            Guid.NewGuid(), "web-1", 22, "root", SshCredential.Key(@"C:\k\id")));
        Assert.Equal("web-1", resolved.HostName);      // not 203.0.113.10
        Assert.Equal(22, resolved.Port);
        Assert.Equal("root", resolved.User);
    }

    [Fact]
    public void AMissingConfigIsNotAnError()
    {
        // The normal case on a fresh machine.
        var config = SshConfig.FromFile(Path.Combine(Path.GetTempPath(), $"missing-{Guid.NewGuid():N}"));
        Assert.Empty(config.Discover());
    }

    [Theory]
    [InlineData("host", null, "host", null)]
    [InlineData("user@host", "user", "host", null)]
    [InlineData("user@host:2222", "user", "host", 2222)]
    [InlineData("host:2222", null, "host", 2222)]
    // An IPv6 literal has several colons and none of them mean a port.
    [InlineData("2001:db8::1", null, "2001:db8::1", null)]
    public void AJumpSpecMayCarryAUserAndPort(
        string spec, string? user, string host, int? port)
    {
        var parsed = SshConfig.SplitHopSpec(spec);
        Assert.Equal(user, parsed.User);
        Assert.Equal(host, parsed.Host);
        Assert.Equal(port, parsed.Port);
    }

    [Fact]
    public void TheFingerprintChangesWithAnythingAConnectionWasBuiltFor()
    {
        // Compared on every lease, so editing a host cannot leave a poll
        // quietly running on the old session.
        var baseline = new ResolvedHost
        {
            HostName = "h", Port = 22, User = "u", Method = AuthMethod.Agent,
        };
        Assert.Equal(baseline.Fingerprint, (baseline with { }).Fingerprint);
        Assert.NotEqual(baseline.Fingerprint, (baseline with { Port = 2222 }).Fingerprint);
        Assert.NotEqual(baseline.Fingerprint, (baseline with { User = "other" }).Fingerprint);
        Assert.NotEqual(baseline.Fingerprint, (baseline with { HostName = "other" }).Fingerprint);
        Assert.NotEqual(
            baseline.Fingerprint,
            (baseline with { Method = AuthMethod.Password }).Fingerprint);
        Assert.NotEqual(
            baseline.Fingerprint,
            (baseline with { ProxyJump = ["bastion"] }).Fingerprint);
    }
}

public class OpenSshExeTransportTests
{
    private static SshTarget Target(SshCredential credential, int port = 22) =>
        new(Guid.NewGuid(), "10.0.0.1", port, "root", credential);

    [Fact]
    public void ControlMasterIsAlwaysOverriddenExplicitly()
    {
        // F1: not a preference. Windows' OpenSSH does not implement
        // ControlMaster, and a user config carrying it under `Host *` makes
        // ssh.exe fail with "getsockname failed: Not a socket" before it opens
        // a socket at all.
        var arguments = OpenSshExeTransport.BaseArguments(Target(SshCredential.Agent));
        Assert.Contains("ControlMaster=no", arguments);
        Assert.Contains("ControlPath=none", arguments);
    }

    [Fact]
    public void APollNeverPrompts()
    {
        var arguments = OpenSshExeTransport.BaseArguments(Target(SshCredential.Agent));
        Assert.Contains("BatchMode=yes", arguments);
        Assert.Contains("ConnectTimeout=10", arguments);
        // Trust on first use, refuse a change.
        Assert.Contains("StrictHostKeyChecking=accept-new", arguments);
    }

    [Fact]
    public void PasswordAuthTurnsBatchModeOffCleanly()
    {
        // BatchMode suppresses every prompt including the askpass helper, so
        // password auth has to switch it off — and dropping only the value
        // would leave a dangling -o, which makes ssh exit with "no argument
        // after keyword".
        var arguments = OpenSshExeTransport.BaseArguments(Target(SshCredential.Password));
        Assert.DoesNotContain("BatchMode=yes", arguments);
        Assert.Contains("BatchMode=no", arguments);
        Assert.Contains("NumberOfPasswordPrompts=1", arguments);

        // No dangling flags: every -o is followed by a value.
        for (var i = 0; i < arguments.Count; i++)
        {
            if (arguments[i] != "-o") continue;
            Assert.True(i + 1 < arguments.Count, "-o at the end of the list");
            Assert.NotEqual("-o", arguments[i + 1]);
        }
    }

    [Fact]
    public void RemoveOptionTakesBothElements()
    {
        List<string> arguments = ["-o", "A=1", "-o", "B=2"];
        OpenSshExeTransport.RemoveOption("A=1", arguments);
        Assert.Equal(["-o", "B=2"], arguments);
    }

    [Fact]
    public void RemoveOptionIgnoresAValueThatIsNotAnOption()
    {
        List<string> arguments = ["-i", "A=1"];
        OpenSshExeTransport.RemoveOption("A=1", arguments);
        Assert.Equal(["-i", "A=1"], arguments);
    }

    [Fact]
    public void AnIdentityFileIsOfferedAlone()
    {
        // IdentitiesOnly, or ssh tries every key it can find and a host with
        // MaxAuthTries 2 refuses before reaching the right one.
        var arguments = OpenSshExeTransport.BaseArguments(Target(SshCredential.Key(@"C:\k\id")));
        Assert.Contains(@"C:\k\id", arguments);
        Assert.Contains("IdentitiesOnly=yes", arguments);
    }

    [Fact]
    public void AnAliasKeepsItsOwnPort()
    {
        // The port belongs to the Host block; overriding it here would
        // silently ignore what the user configured.
        var alias = OpenSshExeTransport.BaseArguments(Target(SshCredential.ConfigAlias, 2222));
        Assert.DoesNotContain("-p", alias);

        var explicitPort = OpenSshExeTransport.BaseArguments(Target(SshCredential.Agent, 2222));
        Assert.Contains("-p", explicitPort);
        Assert.Contains("2222", explicitPort);
    }

    [Fact]
    public void TheDefaultPortIsNotPassedAtAll()
    {
        Assert.DoesNotContain("-p", OpenSshExeTransport.BaseArguments(Target(SshCredential.Agent)));
    }

    [Fact]
    public void AnAliasDialsTheAliasAndEverythingElseDialsUserAtHost()
    {
        Assert.Equal("10.0.0.1", Target(SshCredential.ConfigAlias) with { Host = "10.0.0.1" } is var t
            ? t.SshDestination
            : "");
        Assert.Equal("root@10.0.0.1", Target(SshCredential.Agent).SshDestination);
    }
}

public class SshLocatorTests
{
    [Fact]
    public void ProgramFilesIsPreferredOverTheInBoxCopy()
    {
        // The GitHub build is newer and is what a user who installed it
        // expects to be used.
        var candidates = SshLocator.Candidates().ToList();
        var programFiles = candidates.FindIndex(c => c.Contains(@"Program Files\OpenSSH", StringComparison.OrdinalIgnoreCase));
        var system32 = candidates.FindIndex(c => c.Contains(@"System32\OpenSSH", StringComparison.OrdinalIgnoreCase));
        Assert.True(programFiles >= 0, "no Program Files candidate");
        Assert.True(system32 >= 0, "no System32 candidate");
        Assert.True(programFiles < system32);
    }

    [Fact]
    public void GitForWindowsCygwinBuildIsExcluded()
    {
        // That build has its own idea of what a path is (/c/Users/...) and its
        // own home directory, so it reads a different known_hosts and config
        // than everything else on the machine.
        Assert.True(SshLocator.IsCygwinBuild(@"C:\Program Files\Git\usr\bin"));
        Assert.True(SshLocator.IsCygwinBuild(@"C:\Program Files\Git\bin"));
        Assert.True(SshLocator.IsCygwinBuild(@"C:\cygwin64\bin"));
        Assert.True(SshLocator.IsCygwinBuild("C:/Program Files/Git/usr/bin"));
        Assert.False(SshLocator.IsCygwinBuild(@"C:\Windows\System32\OpenSSH"));
    }

    [Fact]
    public void TheSystemHasAUsableSshClient()
    {
        // Not a unit test of ours so much as a check on the environment the
        // fallback transport needs (plan §0 step 2). If this fails, run:
        // Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0
        Assert.NotNull(SshLocator.Find());
    }
}

public class KnownHostsTests
{
    private static string TempPath() =>
        Path.Combine(Path.GetTempPath(), $"sm-known-{Guid.NewGuid():N}");

    [Fact]
    public void AnUnknownHostIsRecordedAndThenKnown()
    {
        // accept-new: trust on first use, because a background poll has
        // nowhere to ask from.
        var path = TempPath();
        try
        {
            var known = new KnownHosts(path);
            var key = new byte[] { 1, 2, 3, 4 };
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", key));

            known.Add("host", "ssh-ed25519", key);
            Assert.Equal(HostKeyVerdict.Known, known.Check("host", "ssh-ed25519", key));
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void AChangedKeyIsRefusedRatherThanRecorded()
    {
        // The protection that actually matters after first contact.
        var path = TempPath();
        try
        {
            var known = new KnownHosts(path);
            known.Add("host", "ssh-ed25519", [1, 2, 3, 4]);
            Assert.Equal(HostKeyVerdict.Changed, known.Check("host", "ssh-ed25519", [9, 9, 9, 9]));
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void ADifferentKeyTypeForAKnownHostIsUnknownNotChanged()
    {
        // A host offering ed25519 when only its RSA key is recorded has not
        // changed its identity; refusing there would break every host that
        // gains a new host key type.
        var path = TempPath();
        try
        {
            var known = new KnownHosts(path);
            known.Add("host", "ssh-rsa", [1, 2, 3, 4]);
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", [5, 6, 7, 8]));
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void AFileWithoutATrailingNewlineIsNotCorrupted()
    {
        // Appending to it would otherwise glue the new line onto the last
        // entry, breaking both.
        var path = TempPath();
        try
        {
            File.WriteAllText(path, "old.example.com ssh-rsa AAAA");
            var known = new KnownHosts(path);
            known.Add("new.example.com", "ssh-ed25519", [1, 2]);

            Assert.Equal(HostKeyVerdict.Known, known.Check("new.example.com", "ssh-ed25519", [1, 2]));
            var lines = File.ReadAllLines(path).Where(l => l.Length > 0).ToList();
            Assert.Equal(2, lines.Count);
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void AFileThatCannotBeReadIsNotRememberedAsIfItHadBeen()
    {
        // The dangerous shape of a swallowed read error. A read that fails
        // partway leaves the parser holding only the hosts it reached; caching
        // that would make a host recorded further down the file read as
        // Unknown for the rest of the process — and Unknown is accept-new, so
        // a key that had in fact changed would be accepted and appended as a
        // new one. ssh.exe appending while a poll reads is the realistic
        // cause, so it has to be transient rather than sticky.
        var path = TempPath();
        try
        {
            File.WriteAllText(path, "host ssh-ed25519 AQID\n");
            var known = new KnownHosts(path);
            var errors = new List<string>();
            known.OnError = errors.Add;

            // Hold the file open for writing with no sharing, so the read
            // throws the way a concurrent appender would make it throw.
            using (File.Open(path, FileMode.Open, FileAccess.ReadWrite, FileShare.None))
            {
                Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", [1, 2, 3]));
                Assert.NotEmpty(errors);
            }

            // The lock is gone; the very next check must see the real file
            // again rather than a cached empty map.
            Assert.Equal(HostKeyVerdict.Known, known.Check("host", "ssh-ed25519", [1, 2, 3]));
            Assert.Equal(HostKeyVerdict.Changed, known.Check("host", "ssh-ed25519", [9, 9, 9]));
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void AMissingFileIsNotReportedAsAFailure()
    {
        // The first run. It is also the one case where caching the empty set
        // is right, so it must stay distinguishable from a failed read.
        var errors = new List<string>();
        var known = new KnownHosts(TempPath()) { OnError = errors.Add };
        Assert.Equal(HostKeyVerdict.Unknown, known.Check("host", "ssh-ed25519", [1, 2, 3]));
        Assert.Empty(errors);
    }

    [Fact]
    public void CommaSeparatedPatternsAreAllMatched()
    {
        var path = TempPath();
        try
        {
            File.WriteAllText(path, "alpha,beta ssh-ed25519 AQID\n");
            var known = new KnownHosts(path);
            Assert.Equal(HostKeyVerdict.Known, known.Check("alpha", "ssh-ed25519", [1, 2, 3]));
            Assert.Equal(HostKeyVerdict.Known, known.Check("beta", "ssh-ed25519", [1, 2, 3]));
        }
        finally
        {
            File.Delete(path);
        }
    }

    [Fact]
    public void HashedEntriesFromTheUsersOwnFileAreRead()
    {
        // We write plain entries, but the user's existing file may well have
        // hashed ones — HMAC-SHA1 of the hostname keyed by a per-entry salt.
        var salt = new byte[] { 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20 };
        using var hmac = new System.Security.Cryptography.HMACSHA1(salt);
        var digest = hmac.ComputeHash(System.Text.Encoding.UTF8.GetBytes("secret.example.com"));
        var pattern = $"|1|{Convert.ToBase64String(salt)}|{Convert.ToBase64String(digest)}";

        Assert.True(KnownHosts.MatchesHashed(pattern, "secret.example.com"));
        Assert.False(KnownHosts.MatchesHashed(pattern, "other.example.com"));
    }

    [Fact]
    public void AMalformedHashedEntryMatchesNothingRatherThanEverything()
    {
        // Failing safe: the key is then "unknown" and gets recorded, not
        // "known" and trusted.
        Assert.False(KnownHosts.MatchesHashed("|1|not-base64", "host"));
        Assert.False(KnownHosts.MatchesHashed("|1|!!!|!!!", "host"));
    }

    [Fact]
    public void RevocationAndCaMarkersAreNotTreatedAsPlainKeys()
    {
        // Treating an @cert-authority line as a host key would trust the wrong
        // thing entirely.
        var path = TempPath();
        try
        {
            File.WriteAllText(path, "@cert-authority *.example.com ssh-rsa AQID\n");
            var known = new KnownHosts(path);
            Assert.Equal(HostKeyVerdict.Unknown, known.Check("x.example.com", "ssh-rsa", [1, 2, 3]));
        }
        finally
        {
            File.Delete(path);
        }
    }
}

public class PingProbeTests
{
    [Fact]
    public void TheChecksumMatchesRfc1071()
    {
        // A wrong checksum means every host silently reports no latency, which
        // looks like "ICMP is filtered" rather than a bug.
        var packet = PingProbe.EchoRequest(0x1234, 1);
        // A correct packet, checksum included, sums to zero.
        Assert.Equal(0, PingProbe.Checksum(packet));
    }

    [Fact]
    public void AnEchoRequestIsWellFormed()
    {
        var packet = PingProbe.EchoRequest(0xBEEF, 7);
        Assert.Equal(8, packet[0]);                     // echo request
        Assert.Equal(0, packet[1]);
        Assert.Equal(0xBEEF, BitConverter.ToUInt16(packet, 4));
        Assert.Equal(7, BitConverter.ToUInt16(packet, 6));
    }

    [Fact]
    public void AReplyIsMatchedOnItsIdentifierAndSource()
    {
        // A raw ICMP socket sees every echo reply on the machine, including
        // other processes'; without this a busy machine measures somebody
        // else's ping.
        var reply = new byte[28];
        reply[0] = 0x45;                                // IPv4, 20-byte header
        reply[12] = 10; reply[13] = 0; reply[14] = 0; reply[15] = 7;
        reply[20] = 0;                                  // echo reply
        BitConverter.TryWriteBytes(reply.AsSpan(24, 2), (ushort)0x1234);

        var from = System.Net.IPAddress.Parse("10.0.0.7");
        Assert.True(PingProbe.IsOurReply(reply, reply.Length, 0x1234, from));
        Assert.False(PingProbe.IsOurReply(reply, reply.Length, 0x9999, from));
        Assert.False(PingProbe.IsOurReply(
            reply, reply.Length, 0x1234, System.Net.IPAddress.Parse("10.0.0.8")));
    }

    [Fact]
    public void AVariableLengthIpHeaderIsHandled()
    {
        // The ICMP payload does not start at a fixed offset: IP options make
        // the header longer than 20 bytes.
        var reply = new byte[40];
        reply[0] = 0x46;                                // 24-byte header
        reply[12] = 10; reply[13] = 0; reply[14] = 0; reply[15] = 7;
        reply[24] = 0;
        BitConverter.TryWriteBytes(reply.AsSpan(28, 2), (ushort)0x1234);
        Assert.True(PingProbe.IsOurReply(
            reply, reply.Length, 0x1234, System.Net.IPAddress.Parse("10.0.0.7")));
    }

    [Fact]
    public void ATruncatedDatagramIsNotOurReply()
    {
        Assert.False(PingProbe.IsOurReply(new byte[4], 4, 1, System.Net.IPAddress.Loopback));
        Assert.False(PingProbe.IsOurReply(new byte[20], 20, 1, System.Net.IPAddress.Loopback));
    }

    [Fact]
    public void TunnelInterfacesAreExcludedByName()
    {
        // Binding to one would reintroduce exactly the problem the binding
        // exists to avoid (R12).
        Assert.True(PingProbe.LooksLikeTunnel("WireGuard Tunnel"));
        Assert.True(PingProbe.LooksLikeTunnel("TAP-Windows Adapter V9"));
        Assert.True(PingProbe.LooksLikeTunnel("Tailscale"));
        Assert.True(PingProbe.LooksLikeTunnel("Clash"));
        Assert.False(PingProbe.LooksLikeTunnel("Intel(R) Ethernet Connection I219-V"));
        Assert.False(PingProbe.LooksLikeTunnel("Wi-Fi"));
    }

    [Fact]
    public void APrivateAddressIsNotSuspectedOfBeingTunnelled()
    {
        // A LAN host genuinely does answer in under 2 ms.
        Assert.True(GeoLookup.IsPrivate("192.168.1.10"));
        Assert.True(GeoLookup.IsPrivate("10.0.0.1"));
        Assert.True(GeoLookup.IsPrivate("172.16.0.1"));
        Assert.True(GeoLookup.IsPrivate("172.31.255.255"));
        Assert.False(GeoLookup.IsPrivate("172.32.0.1"));
        Assert.False(GeoLookup.IsPrivate("8.8.8.8"));
    }

    [Fact]
    public async Task TheProbeIsBoundedEvenForAHostThatNeverAnswers()
    {
        // Three unanswered echoes must not add six seconds to a poll on a
        // five-second cadence. 192.0.2.0/24 is reserved for documentation and
        // routes nowhere.
        var stopwatch = System.Diagnostics.Stopwatch.StartNew();
        var reading = await new PingProbe().MeasureAsync("192.0.2.1");
        stopwatch.Stop();

        Assert.Null(reading);
        Assert.True(
            stopwatch.Elapsed < PingProbe.TotalBudget + TimeSpan.FromSeconds(1),
            $"took {stopwatch.Elapsed}");
    }
}

public class GeoLookupTests
{
    [Fact]
    public void ASuccessfulLookupIsParsed()
    {
        var info = GeoLookup.Parse("""
            {"ip":"203.0.113.10","success":true,"country":"Japan","country_code":"jp",
             "region":"Tokyo","city":"Shinjuku","connection":{"org":"Example Ltd","isp":"Example"}}
            """);
        Assert.Equal("203.0.113.10", info.Ip);
        Assert.Equal("JP", info.CountryCode);        // uppercased, for the flag
        Assert.Equal("Shinjuku, Tokyo", info.Place);
        Assert.Equal("Example Ltd", info.Organisation);
        Assert.False(info.IsEmpty);
    }

    [Fact]
    public void TheIspStandsInWhenThereIsNoOrg()
    {
        var info = GeoLookup.Parse("""{"success":true,"connection":{"isp":"Example ISP"}}""");
        Assert.Equal("Example ISP", info.Organisation);
    }

    [Fact]
    public void SuccessFalseIsAFailureDespiteTheStatusCode()
    {
        // The service answers 200 with success:false for a private or bogus
        // address, so the status code alone does not say whether this worked.
        var error = Assert.Throws<GeoException>(
            () => GeoLookup.Parse("""{"success":false,"message":"Reserved range"}"""));
        Assert.Contains("Reserved range", error.Message, StringComparison.Ordinal);
    }

    [Fact]
    public void UnreadableJsonIsReported()
    {
        Assert.Throws<GeoException>(() => GeoLookup.Parse("<html>502</html>"));
        Assert.Throws<GeoException>(() => GeoLookup.Parse("[]"));
    }

    [Fact]
    public async Task TheFetchIsInjectableSoNoTestTouchesTheNetwork()
    {
        var lookup = new GeoLookup((url, _) =>
        {
            Assert.Contains("ipwho.is", url.ToString(), StringComparison.Ordinal);
            return Task.FromResult("""{"success":true,"country_code":"us","city":"San Jose"}""");
        });
        var info = await lookup.LookupAsync("8.8.8.8");
        Assert.Equal("US", info.CountryCode);
    }

    [Fact]
    public async Task AnEmptyAddressIsRejectedBeforeAnyRequest()
    {
        var lookup = new GeoLookup((_, _) => throw new InvalidOperationException("should not fetch"));
        await Assert.ThrowsAsync<GeoException>(() => lookup.LookupAsync("  "));
    }
}
