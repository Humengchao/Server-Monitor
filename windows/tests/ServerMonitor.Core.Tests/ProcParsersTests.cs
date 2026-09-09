using ServerMonitor.Core;
using ServerMonitor.Core.Collect;
using ServerMonitor.Core.Ssh;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// The parsers are the part of the port most likely to drift from the Go and
/// Swift collectors, so they are pinned against realistic <c>/proc</c> and
/// <c>df</c> output.
/// </summary>
public class ProcParsersTests
{
    [Fact]
    public void CpuPercentFromTwoSamples()
    {
        // user/nice/system/idle/iowait/irq/softirq/steal.
        // first totals 1200 with 1000 idle; second totals 1600 with 1100 idle,
        // so 100 of the 400 elapsed jiffies were idle -> 75% busy.
        const string First = "cpu  100 0 100 1000 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8";
        const string Second = "cpu  200 0 300 1100 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8";
        Assert.Equal(75, ProcParsers.CpuPercent(First, Second), 3);
    }

    [Fact]
    public void CpuPercentIsZeroWhenCountersDoNotMove()
    {
        const string Sample = "cpu  100 0 100 1000 0 0 0 0";
        Assert.Equal(0, ProcParsers.CpuPercent(Sample, Sample));
    }

    [Fact]
    public void MemInfoConvertsKilobytesAndExcludesCache()
    {
        var (used, total) = ProcParsers.MemInfo(Fixture.Read("linux/proc-meminfo.txt"));
        Assert.Equal(32_900_000L * 1024, total);
        Assert.Equal((32_900_000L - 1_200_000 - 1_300_000 - 18_800_000) * 1024, used);
    }

    [Fact]
    public void MemInfoNeverReportsNegativeUsage()
    {
        const string Output = "MemTotal: 1000 kB\nMemFree: 900 kB\nBuffers: 200 kB\nCached: 300 kB";
        Assert.Equal(0, ProcParsers.MemInfo(Output).Used);
    }

    [Fact]
    public void LoadAverageParsesThreeFigures()
    {
        var (one, five, fifteen) = ProcParsers.LoadAverage("0.52 0.31 0.14 1/523 12345");
        Assert.Equal(0.52, one, 4);
        Assert.Equal(0.31, five, 4);
        Assert.Equal(0.14, fifteen, 4);
    }

    [Fact]
    public void LoadAverageUsesInvariantCulture()
    {
        // A Chinese or German Windows would read "0.52" as 52 under the current
        // culture, which is the whole reason Text.ToDouble pins the invariant.
        var original = System.Globalization.CultureInfo.CurrentCulture;
        try
        {
            System.Globalization.CultureInfo.CurrentCulture = new System.Globalization.CultureInfo("de-DE");
            Assert.Equal(0.52, ProcParsers.LoadAverage("0.52 0.31 0.14 1/1 1").One, 4);
        }
        finally
        {
            System.Globalization.CultureInfo.CurrentCulture = original;
        }
    }

    [Fact]
    public void NetDevCountsTheHostsOwnTrafficOnce()
    {
        var (rx, tx) = ProcParsers.NetDev(Fixture.Read("linux/proc-net-dev.txt"));
        // eth0 alone. docker0 and the veth carry the same packets a second and
        // third time — a container's traffic crosses the NIC, the bridge, and
        // the veth at its end — so adding them made the headline read two to
        // three times the host's real throughput. Measured on a live host: a
        // NIC doing 1.3 MB/s reported as 3.9 MB/s, 149.6 GB cumulative shown
        // as 372.7 GB.
        Assert.Equal(918_273_645L, rx);
        Assert.Equal(421_098_234L, tx);
    }

    [Fact]
    public void AHostReachedOnlyThroughATunnelStillReportsTraffic()
    {
        // wg0 and tun0 are in the virtual list because on an ordinary host
        // they carry a copy of traffic counted elsewhere. On a host whose only
        // interface is the tunnel there is nothing else, and reporting zero
        // would be a worse wrong than counting twice — so the exclusion
        // applies only while something real remains.
        var output = string.Join("\n",
            "Inter-|   Receive                    |  Transmit",
            " face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed",
            "    lo:    5000      50    0    0    0     0          0         0     5000      50    0    0    0     0       0          0",
            "  tun0: 8000000    7000    0    0    0     0          0         0  4000000    3000    0    0    0     0       0          0");

        var (rx, tx) = ProcParsers.NetDev(output);
        Assert.Equal(8_000_000L, rx);
        Assert.Equal(4_000_000L, tx);
    }

    [Fact]
    public void DiskStatsCountsWholeDisksOnly()
    {
        var (read, written) = ProcParsers.DiskStats(Fixture.Read("linux/proc-diskstats.txt"));
        // nvme0n1 and sda; not their partitions, and not loop0.
        Assert.Equal((98_123_456L + 40_219_384) * 512, read);
        Assert.Equal((204_819_234L + 20_481_920) * 512, written);
    }

    [Fact]
    public void UptimeTakesWholeSeconds()
    {
        Assert.Equal(123_456, ProcParsers.Uptime("123456.78 987654.32"));
        Assert.Equal(0, ProcParsers.Uptime(""));
    }

    [Fact]
    public void DiskUsageReadsFieldsFromEndOfLine()
    {
        var (used, total) = ProcParsers.DiskUsage(Fixture.Read("linux/df-multi-mount.txt"));
        Assert.Equal(84_421_599_232, total);
        Assert.Equal(30_799_843_328, used);
    }

    [Fact]
    public void DiskUsageHandlesWrappedDeviceName()
    {
        // df without -P wraps a long device onto its own line; anchoring the
        // fields from the end still finds the numbers.
        var (used, total) = ProcParsers.DiskUsage(Fixture.Read("linux/df-wrapped-device.txt"));
        Assert.Equal(84_421_599_232, total);
        Assert.Equal(30_799_843_328, used);
    }

    [Fact]
    public void DiskUsageReportsRootNotTheLastLine()
    {
        // df is asked for every mount now. Taking the last line made a host
        // with a /data volume report that volume as its disk usage.
        var (used, total) = ProcParsers.DiskUsage(Fixture.Read("linux/df-multi-mount.txt"));
        Assert.Equal(84_421_599_232, total);
        Assert.Equal(30_799_843_328, used);
    }

    [Fact]
    public void CoreCount()
    {
        Assert.Equal(8, ProcParsers.Cores(" 8 \n"));
        Assert.Equal(0, ProcParsers.Cores("nproc: command not found"));
    }

    [Fact]
    public void DockerVersionRejectsNoise()
    {
        Assert.Equal("24.0.7", ProcParsers.DockerVersion(" 24.0.7 \n"));
        Assert.Equal("", ProcParsers.DockerVersion(""));
        Assert.Equal("", ProcParsers.DockerVersion("Cannot connect to the Docker daemon"));
        Assert.Equal("", ProcParsers.DockerVersion(new string('9', 40)));
    }

    [Fact]
    public void DockerVersionKeepsAnEmptyFirstFieldEmpty()
    {
        // A daemon that answered with an empty version ("|14|5|3") must not
        // make the image count the version.
        Assert.Equal("", ProcParsers.DockerVersion("|14|5|3|0"));
    }

    /// <summary>
    /// The probe's sudo fallback, as a real host answers it.
    /// </summary>
    /// <remarks>
    /// On a host where the login user is outside the docker group, the plain
    /// `docker info` does not fail quietly: the CLI prints a complete row of
    /// zeroes to stdout and *then* exits non-zero, so `|| sudo -n docker info`
    /// runs and the section holds two rows — the dud first, the answer second.
    /// Reading up to the section's first pipe therefore reported "no Docker"
    /// about a host running Docker, and the app hid its Docker page for it.
    /// Two of eleven real hosts were in this state.
    /// </remarks>
    [Fact]
    public void DockerVersionTakesTheAnswerAndNotTheFailedFirstAttempt()
    {
        Assert.Equal("29.3.1", ProcParsers.DockerVersion("|0|0|0|0\n29.3.1|5|1|0|0\n"));
    }

    [Fact]
    public void DockerVersionStaysEmptyWhenSudoDoesNotHelpEither()
    {
        // No docker group *and* no passwordless sudo: the fallback's stderr is
        // dropped by the probe, so all that reaches the section is the dud row.
        Assert.Equal("", ProcParsers.DockerVersion("|0|0|0|0\n"));
    }

    /// <summary>
    /// The version parser and the summary parser must read the same row.
    /// </summary>
    /// <remarks>
    /// These two read the same bytes and were choosing different lines of it —
    /// DockerVersion the first, ParseSummary the last. The collector asks the
    /// first whether to trust the second, so disagreement is not a difference
    /// of opinion but a switch that turns the whole feature off.
    /// </remarks>
    [Fact]
    public void TheVersionAgreesWithTheSummaryOnTheSameOutput()
    {
        foreach (var output in new[]
        {
            "29.3.1|5|1|0|0\n",
            "|0|0|0|0\n29.3.1|5|1|0|0\n",
            "|0|0|0|0\n",
            "",
        })
        {
            Assert.Equal(
                DockerClient.ParseSummary(output).EngineVersion,
                ProcParsers.DockerVersion(output));
        }
    }

    [Fact]
    public void SplitSectionsPadsTruncatedOutput()
    {
        var output = string.Join("\n", ["first", ProcParsers.SectionSeparator, "second", ProcParsers.SectionSeparator, "third"]);
        var sections = ProcParsers.SplitSections(output, 5);
        Assert.Equal(5, sections.Length);
        Assert.Equal("first", sections[0].Trim());
        Assert.Equal("third", sections[2].Trim());
        Assert.Equal("", sections[3]);
        Assert.Equal("", sections[4]);
    }

    [Fact]
    public void EverySectionHasACommand()
    {
        // The sections are read positionally. Adding a command without adding
        // its enum case (or the reverse) shifts every field after it, and the
        // result is plausible-looking nonsense rather than an error.
        Assert.Equal(ProcParsers.SectionCount, Probes.LinuxSectionCount);

        var parts = Probes.LinuxMetricsCommand()
            .Split($"; echo {ProcParsers.SectionSeparator}; ");
        Assert.Equal(ProcParsers.SectionCount, parts.Length);
    }

    [Fact]
    public void TheClocksBracketTheRun()
    {
        // Adding a clock without a matching enum case would silently shift
        // every parser after it.
        Assert.Equal(0, (int)ProcParsers.Section.StartClock);
        Assert.Equal((int)ProcParsers.Section.EndClock, ProcParsers.SectionCount - 1);
    }

    [Fact]
    public void SkippedSectionsKeepTheirSeparators()
    {
        // Leaving `ps` and `docker info` out must not shift the sections after
        // them.
        var full = Probes.LinuxMetricsCommand(processes: true, docker: true);
        var lean = Probes.LinuxMetricsCommand(processes: false, docker: false);
        int Separators(string script) => script.Split(ProcParsers.SectionSeparator).Length;
        Assert.Equal(Separators(full), Separators(lean));
        Assert.Equal(ProcParsers.SectionCount, Separators(full));
        Assert.Contains("ps -eo", full, StringComparison.Ordinal);
        Assert.Contains("docker info", full, StringComparison.Ordinal);
        Assert.DoesNotContain("ps -eo", lean, StringComparison.Ordinal);
        Assert.DoesNotContain("docker info", lean, StringComparison.Ordinal);
    }

    [Fact]
    public void TheCollectionLineDoesNotRunLscpuEveryPoll()
    {
        // `lscpu` walks every core's sysfs topology; on an 80-core ARM box that
        // is tens of milliseconds per tick for a string that never changes.
        // The collector asks once, with its own command, and caches.
        Assert.DoesNotContain("lscpu", Probes.LinuxMetricsCommand(), StringComparison.Ordinal);
        Assert.Contains("lscpu", Probes.CpuModelFallback, StringComparison.Ordinal);
        Assert.Contains("model name", Probes.CpuModelFallback, StringComparison.Ordinal);
    }

    [Fact]
    public void DfIsAskedToSkipExactlyWhatTheParserWouldDrop()
    {
        // One list for both ends, so the next pseudo-filesystem cannot be added
        // to one and forgotten in the other.
        foreach (var type in Probes.PseudoFilesystemTypes)
        {
            Assert.Contains($"-x {type}", Probes.LinuxMetricsCommand(), StringComparison.Ordinal);
            Assert.True(ProcParsers.IsPseudoFilesystem(type), type);
        }
    }

    [Fact]
    public void AnEmptyBatchYieldsAZeroSnapshotSoTheCollectorCanRejectIt()
    {
        // The collector treats memoryTotal==0 && cores==0 as "truncated" and
        // throws rather than returning it. This pins the parser side of that
        // contract: empty sections really do produce those zeroes, not some
        // incidental non-zero that would slip past the guard.
        var empty = ProcParsers.SplitSections("", ProcParsers.SectionCount);
        Assert.Equal(0, ProcParsers.MemInfo(empty[(int)ProcParsers.Section.MemInfo]).Total);
        Assert.Equal(0, ProcParsers.Cores(empty[(int)ProcParsers.Section.Nproc]));
    }
}

public class LatencyTests
{
    [Fact]
    public void SubtractsRemoteWorkFromLocalElapsed()
    {
        // Host reports 0.6s of its own work; the call took 0.72s locally, so
        // 120 ms was on the wire.
        var latency = ProcParsers.NetworkLatency(0.72, "1735000000000000000", "1735000000600000000");
        Assert.Equal(120, latency, 3);
    }

    [Fact]
    public void NeverReportsNegativeLatency()
    {
        // Clock skew or an NTP step mid-run must not produce a negative number.
        Assert.Equal(0, ProcParsers.NetworkLatency(0.5, "1735000000000000000", "1735000001000000000"));
    }

    [Fact]
    public void MissingClocksYieldZeroRatherThanNonsense()
    {
        // Shells without %N print the literal "N"; better to show "—".
        Assert.Equal(0, ProcParsers.NetworkLatency(1.0, "1735000000N", "x"));
        Assert.Equal(0, ProcParsers.NetworkLatency(1.0, "", ""));
    }

    [Fact]
    public void BackwardsClockIsRejected()
    {
        Assert.Equal(0, ProcParsers.NetworkLatency(1.0, "1735000000600000000", "1735000000000000000"));
    }

    [Fact]
    public void ClockParsesOnlyDigits()
    {
        Assert.Equal(1_735_000_000_000_000_000, ProcParsers.Clock(" 1735000000000000000 \n"));
        Assert.Null(ProcParsers.Clock("1735000000N"));
        Assert.Null(ProcParsers.Clock(""));
    }
}

public class HostDetailParserTests
{
    [Fact]
    public void FilesystemsListsRealMountsRootFirst()
    {
        var mounts = ProcParsers.Filesystems(Fixture.Read("linux/df-multi-mount.txt"));
        Assert.Equal(["/", "/data"], mounts.Select(m => m.Mount));
        Assert.Equal("/dev/vdb1", mounts[1].Device);
        Assert.Equal(36.48, mounts[0].Percent, 1);
    }

    [Fact]
    public void AWrappedDeviceNameStillParses()
    {
        var mounts = ProcParsers.Filesystems(Fixture.Read("linux/df-wrapped-device.txt"));
        Assert.Single(mounts);
        Assert.Equal("/", mounts[0].Mount);
        Assert.Equal(84_421_599_232, mounts[0].Total);
    }

    [Fact]
    public void AFirmwareVariableStoreIsNotStorage()
    {
        // Verbatim from a UEFI ARM host: efivarfs is 128 KB of firmware
        // variables and was sitting on the storage card beside the real disks.
        var rows = ProcParsers.Filesystems(Fixture.Read("linux/df-uefi-arm.txt"));
        Assert.DoesNotContain(rows, r => r.Mount.StartsWith("/sys/", StringComparison.Ordinal));
        Assert.Contains(rows, r => r.Mount == "/boot/efi");   // a real EFI partition stays
        Assert.Equal(3, rows.Count);
        Assert.Equal("/", rows[0].Mount);                     // root leads
        Assert.Equal(11_703_223_296, rows[0].Used);
    }

    [Fact]
    public void AKernelMountIsDroppedWhateverItsTypeIsCalled()
    {
        // The type list will always be one behind; the mount point is the part
        // that does not need updating when a new pseudo-filesystem appears.
        Assert.True(ProcParsers.IsPseudoFilesystem("efivarfs"));
        Assert.True(ProcParsers.IsPseudoFilesystem("somethingnew", "/sys/fs/whatever"));
        Assert.True(ProcParsers.IsPseudoFilesystem("tracefs", "/proc/x"));
        Assert.False(ProcParsers.IsPseudoFilesystem("/dev/sda15", "/boot/efi"));
        Assert.False(ProcParsers.IsPseudoFilesystem("/dev/sda1", "/"));
    }

    [Fact]
    public void PerCoreUsageComesFromTheSameTwoReads()
    {
        const string First = """
            cpu  100 0 100 800 0 0 0 0 0 0
            cpu0 50 0 50 400 0 0 0 0 0 0
            cpu1 50 0 50 400 0 0 0 0 0 0
            """;
        const string Second = """
            cpu  200 0 200 1200 0 0 0 0 0 0
            cpu0 150 0 150 400 0 0 0 0 0 0
            cpu1 50 0 50 800 0 0 0 0 0 0
            """;
        var cores = ProcParsers.CoreLoads(First, Second);
        Assert.Equal(2, cores.Count);            // the aggregate cpu line is not a core
        Assert.Equal(0, cores[0].Index);
        Assert.Equal(100, cores[0].Percent, 2);  // idle unchanged: every added jiffy was work
        Assert.Equal(0, cores[1].Percent, 2);    // only idle grew
    }

    [Fact]
    public void EightCoresFromARealHost()
    {
        var cores = ProcParsers.CoreLoads(
            Fixture.Read("linux/proc-stat-first.txt"),
            Fixture.Read("linux/proc-stat-second.txt"));
        Assert.Equal(8, cores.Count);
        Assert.Equal(Enumerable.Range(0, 8), cores.Select(c => c.Index));
        Assert.All(cores, core => Assert.InRange(core.Percent, 0, 100));
    }

    [Fact]
    public void ACoreMissingFromTheSecondReadIsDropped()
    {
        // Reporting it as 0% would look like an idle core rather than a gap.
        const string First = "cpu0 1 0 1 1 0 0 0 0\ncpu1 1 0 1 1 0 0 0 0";
        const string Second = "cpu0 2 0 2 2 0 0 0 0";
        Assert.Equal([0], ProcParsers.CoreLoads(First, Second).Select(c => c.Index));
    }

    [Fact]
    public void MemoryBreakdownSplitsBuffersCacheAndSwap()
    {
        var memory = ProcParsers.MemoryBreakdownOf(Fixture.Read("linux/proc-meminfo.txt"));
        Assert.Equal(32_900_000L * 1024, memory.Total);
        Assert.Equal(1_300_000L * 1024, memory.Buffers);
        Assert.Equal(18_800_000L * 1024, memory.Cached);
        Assert.Equal((32_900_000L - 1_200_000 - 1_300_000 - 18_800_000) * 1024, memory.Used);
        Assert.Equal(500_000L * 1024, memory.SwapUsed);
        Assert.True(memory.HasSwap);
    }

    [Fact]
    public void InterfacesAreListedIndividuallyWithoutLoopback()
    {
        var interfaces = ProcParsers.NetInterfaces(Fixture.Read("linux/proc-net-dev.txt"));
        // Busiest first, no loopback.
        Assert.Equal(["eth0", "docker0", "veth1a2b3c"], interfaces.Select(i => i.Name));
        Assert.Equal(918_273_645, interfaces[0].RxTotal);
        Assert.Equal(421_098_234, interfaces[0].TxTotal);
    }

    [Fact]
    public void VirtualInterfacesAreRecognised()
    {
        var interfaces = ProcParsers.NetInterfaces(Fixture.Read("linux/proc-net-dev.txt"));
        Assert.False(interfaces.First(i => i.Name == "eth0").IsVirtual);
        Assert.True(interfaces.First(i => i.Name == "docker0").IsVirtual);
        Assert.True(interfaces.First(i => i.Name == "veth1a2b3c").IsVirtual);
    }

    [Fact]
    public void InterfaceNameJoinedToItsCountStillParses()
    {
        // A busy interface prints as "eth0:12345678" with no space.
        var interfaces = ProcParsers.NetInterfaces("  eth0:900 9 0 0 0 0 0 0 700 7 0 0 0 0 0 0");
        Assert.Single(interfaces);
        Assert.Equal("eth0", interfaces[0].Name);
        Assert.Equal(900, interfaces[0].RxTotal);
    }

    [Fact]
    public void ProcessesKeepCommandsContainingSpaces()
    {
        var processes = ProcParsers.Processes(Fixture.Read("linux/ps.txt"));
        Assert.Equal(5, processes.Count);
        Assert.Equal(1234, processes[0].Pid);
        Assert.Equal("root", processes[0].User);
        Assert.Equal(12.5, processes[0].CpuPercent);
        Assert.Equal(512_000L * 1024, processes[0].ResidentBytes);
        Assert.Equal("/usr/bin/python3 /opt/app/main.py --flag", processes[0].Command);
        Assert.Equal("nginx: worker process", processes[1].Command);
        Assert.Equal("postgres: 16/main: checkpointer", processes[2].Command);
    }

    [Fact]
    public void HostIdentityIsKeyedNotPositional()
    {
        var full = ProcParsers.HostIdentityOf(Fixture.Read("linux/host-info.txt"));
        Assert.Equal("web-1", full.Hostname);
        Assert.Equal("Ubuntu 24.04.4 LTS", full.OsName);
        Assert.Equal("x86_64", full.Architecture);
        Assert.Equal("Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz", full.CpuModel);
        // Link-local and loopback are noise on the card.
        Assert.Equal(["192.168.9.132", "172.17.0.1"], full.Addresses);
    }

    [Fact]
    public void AFactTheHostCouldNotAnswerDoesNotShiftTheOthers()
    {
        // The whole reason this section is keyed: empty lines are dropped when
        // the output is split, so with positional parsing a host with no
        // `hostname -I` reported its CPU model as its IP address.
        var sparse = ProcParsers.HostIdentityOf("""
            host=box
            kern=5.10.0
            arch=
            os=
            ips=
            cpu=AMD EPYC 7B13
            """);
        Assert.Equal("box", sparse.Hostname);
        Assert.Equal("5.10.0", sparse.Kernel);
        Assert.Empty(sparse.OsName);
        Assert.Empty(sparse.Addresses);
        Assert.Equal("AMD EPYC 7B13", sparse.CpuModel);
    }

    [Fact]
    public void AnArmHostStillNamesItsCPU()
    {
        // /proc/cpuinfo has no "model name" on aarch64, so the CPU model came
        // back empty on every ARM host. The collector falls back to `lscpu`,
        // whose column padding leaves the value indented.
        var arm = ProcParsers.HostIdentityOf(Fixture.Read("linux/host-info-arm.txt"));
        Assert.Equal("aarch64", arm.Architecture);
        Assert.Equal("Neoverse-N1", arm.CpuModel);
    }

    [Fact]
    public void CpuBreakdownSharesTheWholeWindow()
    {
        // user 10, nice 0, system 20, idle 60, iowait 10 over a 100-jiffy window.
        const string First = "cpu  0 0 0 0 0 0 0 0 0 0";
        const string Second = "cpu  10 0 20 60 10 0 0 0 0 0";
        var breakdown = ProcParsers.CpuBreakdownOf(First, Second);
        Assert.Equal(10, breakdown.User, 2);
        Assert.Equal(20, breakdown.System, 2);
        Assert.Equal(10, breakdown.Iowait, 2);
        Assert.Equal(0, breakdown.Steal);
        Assert.True(breakdown.IsReported);
        // The parts plus idle account for the window, matching CpuPercent.
        var busy = ProcParsers.CpuPercent(First, Second);
        Assert.Equal(
            busy,
            breakdown.User + breakdown.System + breakdown.Nice + breakdown.Steal,
            2);
    }

    [Fact]
    public void IrqAndSoftirqFoldIntoSystem()
    {
        // `top` shows them inside system; a separate row nobody reads is worse.
        var breakdown = ProcParsers.CpuBreakdownOf(
            "cpu  0 0 0 0 0 0 0 0 0 0", "cpu  0 0 10 70 0 10 10 0 0 0");
        Assert.Equal(30, breakdown.System, 2);
    }

    [Fact]
    public void AnIdleWindowReportsNothingRatherThanNaN()
    {
        var breakdown = ProcParsers.CpuBreakdownOf("cpu  1 1 1 1 1 1 1 1", "cpu  1 1 1 1 1 1 1 1");
        Assert.False(breakdown.IsReported);
        Assert.Equal(0, breakdown.User);
    }
}

public class GpuTests
{
    [Fact]
    public void ParsesBothCardsInIndexOrder()
    {
        var status = ProcParsers.GpuStatusOf(Fixture.Read("linux/nvidia-smi.txt"));
        Assert.True(status.IsPresent);
        Assert.Equal("550.90.07", status.DriverVersion);
        Assert.Equal("12.4", status.CudaVersion);
        Assert.Equal([0, 1], status.Gpus.Select(g => g.Index));
        Assert.Equal("NVIDIA A100-SXM4-40GB", status.Gpus[0].Name);
        Assert.Equal(87, status.Gpus[0].UtilizationPercent);
        Assert.Equal(40960L * 1024 * 1024, status.Gpus[0].MemoryTotal);
        Assert.Equal(80, status.Gpus[0].MemoryPercent, 2);
    }

    [Fact]
    public void NotAvailableStaysNullRatherThanBecomingZero()
    {
        // The A100 has no fan. Reporting 0% would read as "fan stopped".
        var status = ProcParsers.GpuStatusOf(Fixture.Read("linux/nvidia-smi.txt"));
        Assert.Null(status.Gpus[0].FanPercent);
        Assert.Equal(61, status.Gpus[0].TemperatureC);
        Assert.Equal(31, status.Gpus[1].FanPercent);
        Assert.Equal(245.31, status.Gpus[0].PowerDrawW);
        Assert.Equal(400.00, status.Gpus[0].PowerLimitW);
    }

    [Fact]
    public void ComputeProcessesAreListed()
    {
        var status = ProcParsers.GpuStatusOf(Fixture.Read("linux/nvidia-smi.txt"));
        Assert.Equal([3241, 8899], status.Processes.Select(p => p.Pid));
        Assert.Equal("/usr/bin/python3", status.Processes[0].Name);
        Assert.Equal(30720L * 1024 * 1024, status.Processes[0].MemoryUsed);
    }

    [Theory]
    [InlineData("")]
    [InlineData("\n")]
    [InlineData("bash: nvidia-smi: command not found")]
    public void AHostWithoutNvidiaSmiReportsNothing(string output)
    {
        // The command short-circuits to empty output; the card is then omitted
        // rather than shown with zeroes.
        var status = ProcParsers.GpuStatusOf(output);
        Assert.False(status.IsPresent);
        Assert.Empty(status.Gpus);
    }

    [Fact]
    public void ATruncatedRowIsDroppedNotHalfParsed()
    {
        // A card that answered only part of the query would otherwise become a
        // GPU with 0 memory and a blank name.
        var status = ProcParsers.GpuStatusOf("gpu=0, NVIDIA A100");
        Assert.Empty(status.Gpus);
    }
}

public class TextTests
{
    [Fact]
    public void LinesSplitsCrlfAsOneBreak()
    {
        // Windows hosts answer entirely in CRLF. A plain Split('\n') leaves a
        // stray \r on every line, which then fails to parse as a number or
        // becomes part of a hostname.
        Assert.Equal(["a", "b", "c"], "a\r\nb\r\nc".Lines());
        Assert.Equal(["a", "b"], "a\nb".Lines());
        Assert.Equal(["a", "b"], "a\rb".Lines());
        Assert.Empty("".Lines());
    }

    [Fact]
    public void LinesDropsEmptiesButKeepEmptyLinesDoesNot()
    {
        Assert.Equal(["a", "b"], "a\n\n\nb".Lines());
        Assert.Equal(["a", "", "", "b"], "a\n\n\nb".KeepEmptyLines());
    }

    [Fact]
    public void KeyValueSplitsAtTheFirstEqualsOnly()
    {
        // A value may itself contain '=' — a base64 tail, an adapter GUID.
        Assert.True("k=a=b".TryKeyValue(out var key, out var value));
        Assert.Equal("k", key);
        Assert.Equal("a=b", value);
        Assert.False("no-separator".TryKeyValue(out _, out _));
    }

    [Fact]
    public void ShellQuoteSurvivesAnApostrophe()
    {
        // Remote names come from the filesystem, not from us.
        Assert.Equal("'it'\\''s'", "it's".ShellQuote());
    }
}

public class FormatTests
{
    [Fact]
    public void BytesAreBinaryNotDecimal()
    {
        // 1 KB = 1024 B, matching what the shell tools report.
        Assert.Equal("0 B", Format.Bytes(0));
        Assert.Equal("512 B", Format.Bytes(512));
        Assert.Equal("1.0 KB", Format.Bytes(1024));
        Assert.Equal("1.0 GB", Format.Bytes(1024L * 1024 * 1024));
    }

    [Fact]
    public void BytesUseTheInvariantSeparatorWhateverTheCulture()
    {
        var original = System.Globalization.CultureInfo.CurrentCulture;
        try
        {
            System.Globalization.CultureInfo.CurrentCulture = new System.Globalization.CultureInfo("de-DE");
            Assert.Equal("1.5 KB", Format.Bytes(1536));
        }
        finally
        {
            System.Globalization.CultureInfo.CurrentCulture = original;
        }
    }

    [Fact]
    public void RatePartsKeepThreeSignificantFigures()
    {
        Assert.Equal(("0", "B/s"), Format.RateParts(0));
        Assert.Equal(("5.66", "K/s"), Format.RateParts(5.66 * 1024));
        Assert.Equal(("12.0", "M/s"), Format.RateParts(12.0 * 1024 * 1024));
        Assert.Equal(("512", "K/s"), Format.RateParts(512 * 1024));
    }

    [Fact]
    public void FlagIsARegionalIndicatorPair()
    {
        // Two surrogate pairs, so four UTF-16 units.
        Assert.Equal(4, Format.Flag("CN").Length);
        Assert.Equal(Format.Flag("cn"), Format.Flag("CN"));
        Assert.Equal("", Format.Flag(""));
        Assert.Equal("", Format.Flag("C"));
        Assert.Equal("", Format.Flag("C1"));
    }

    [Fact]
    public void UptimeReadsInBothLanguages()
    {
        Assert.Equal("—", Format.Uptime(0, chinese: false));
        Assert.Equal("12d 4h", Format.Uptime(12 * 86_400 + 4 * 3600, chinese: false));
        Assert.Equal("12 天 4 小时", Format.Uptime(12 * 86_400 + 4 * 3600, chinese: true));
        Assert.Equal("3h 20m", Format.Uptime(3 * 3600 + 20 * 60, chinese: false));
        Assert.Equal("45m", Format.Uptime(45 * 60, chinese: false));
    }

    [Fact]
    public void TagColourIsStableAcrossRunsAndClients()
    {
        // .NET randomises string hashing per process, so a tag would change
        // colour on every launch; and the macOS build computes exactly this
        // FNV-1a, so "prod" must be the same colour on both clients.
        var first = Format.TagColorIndex("prod", 8);
        Assert.Equal(first, Format.TagColorIndex("prod", 8));
        Assert.Equal(first, Format.TagColorIndex("PROD", 8));
        Assert.InRange(first, 0, 7);
        // FNV-1a of "prod" is 0x85729c0e3537be14, so index 4 in an 8-colour
        // palette. Pinned so a "tidy-up" of the constants is caught here
        // rather than by the colours silently diverging from the Mac.
        Assert.Equal(4, first);
        Assert.Equal(3, Format.TagColorIndex("db", 8));
    }
}
