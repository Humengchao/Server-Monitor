import Testing
@testable import ServerMonitorKit

/// The parsers are the part of the port most likely to drift from the Go
/// collector, so they are pinned against realistic /proc and df output.
@Suite("Proc parsers")
struct ProcParsersTests {

    @Test func cpuPercentFromTwoSamples() {
        // user/nice/system/idle/iowait/irq/softirq/steal.
        // first  totals 1200 with 1000 idle; second totals 1600 with 1100 idle,
        // so 100 of the 400 elapsed jiffies were idle -> 75% busy.
        let first = "cpu  100 0 100 1000 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8"
        let second = "cpu  200 0 300 1100 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8"
        #expect(abs(ProcParsers.cpuPercent(first: first, second: second) - 75) < 0.001)
    }

    @Test func cpuPercentIsZeroWhenCountersDoNotMove() {
        let sample = "cpu  100 0 100 1000 0 0 0 0"
        #expect(ProcParsers.cpuPercent(first: sample, second: sample) == 0)
    }

    @Test func memInfoConvertsKilobytesAndExcludesCache() {
        let output = """
        MemTotal:       16384000 kB
        MemFree:         1024000 kB
        Buffers:          512000 kB
        Cached:          2048000 kB
        SwapTotal:       2097152 kB
        """
        let (used, total) = ProcParsers.memInfo(output)
        #expect(total == 16_384_000 * 1024)
        #expect(used == (16_384_000 - 1_024_000 - 512_000 - 2_048_000) * 1024)
    }

    @Test func memInfoNeverReportsNegativeUsage() {
        let output = "MemTotal: 1000 kB\nMemFree: 900 kB\nBuffers: 200 kB\nCached: 300 kB"
        #expect(ProcParsers.memInfo(output).used == 0)
    }

    @Test func loadAverage() {
        let (one, five, fifteen) = ProcParsers.loadAverage("0.52 0.31 0.14 1/523 12345")
        #expect(abs(one - 0.52) < 0.0001)
        #expect(abs(five - 0.31) < 0.0001)
        #expect(abs(fifteen - 0.14) < 0.0001)
    }

    @Test func netDevSumsInterfacesButSkipsLoopback() {
        let output = """
        Inter-|   Receive                                                |  Transmit
         face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
            lo:  999999    1000    0    0    0     0          0         0   999999    1000    0    0    0     0       0          0
          eth0: 1000000    5000    0    0    0     0          0         0   200000    3000    0    0    0     0       0          0
          eth1:  500000    2500    0    0    0     0          0         0   100000    1500    0    0    0     0       0          0
        """
        let (rx, tx) = ProcParsers.netDev(output)
        #expect(rx == 1_500_000)
        #expect(tx == 300_000)
    }

    @Test func diskStatsCountsWholeDisksOnly() {
        // major minor name r_completed r_merged sectors_read t_read
        // w_completed w_merged sectors_written ... (at least 14 fields)
        let output = """
           8       0 sda 1000 0 2048 100 500 0 1024 50 0 0 0 0 0
           8       1 sda1 900 0 1024 90 400 0 512 40 0 0 0 0 0
         259       0 nvme0n1 2000 0 4096 200 800 0 2048 80 0 0 0 0 0
         259       1 nvme0n1p1 100 0 512 10 50 0 256 5 0 0 0 0 0
        """
        let (read, written) = ProcParsers.diskStats(output)
        #expect(read == (2048 + 4096) * 512, "only sda and nvme0n1 count")
        #expect(written == (1024 + 2048) * 512)
    }

    @Test func uptimeTakesWholeSeconds() {
        #expect(ProcParsers.uptime("123456.78 987654.32") == 123_456)
        #expect(ProcParsers.uptime("") == 0)
    }

    @Test func diskUsageReadsFieldsFromEndOfLine() {
        let output = """
        Filesystem     1B-blocks       Used   Available Capacity Mounted on
        /dev/vda1    42006183936 8804478976 31048998912      23% /
        """
        let (used, total) = ProcParsers.diskUsage(output)
        #expect(total == 42_006_183_936)
        #expect(used == 8_804_478_976)
    }

    @Test func diskUsageHandlesWrappedDeviceName() {
        // df without -P wraps a long device onto its own line; anchoring the
        // fields from the end still finds the numbers.
        let output = """
        Filesystem     1B-blocks       Used   Available Capacity Mounted on
        /dev/mapper/a-very-long-logical-volume-name
                     42006183936 8804478976 31048998912      23% /
        """
        let (used, total) = ProcParsers.diskUsage(output)
        #expect(total == 42_006_183_936)
        #expect(used == 8_804_478_976)
    }

    @Test func coreCount() {
        #expect(ProcParsers.cores(" 8 \n") == 8)
        #expect(ProcParsers.cores("nproc: command not found") == 0)
    }

    @Test func dockerVersionRejectsNoise() {
        #expect(ProcParsers.dockerVersion(" 24.0.7 \n") == "24.0.7")
        #expect(ProcParsers.dockerVersion("") == "")
        #expect(ProcParsers.dockerVersion("Cannot connect to the Docker daemon") == "")
        #expect(ProcParsers.dockerVersion(String(repeating: "9", count: 40)) == "")
    }

    @Test func splitSectionsPadsTruncatedOutput() {
        let output = [
            "first", ProcParsers.sectionSeparator,
            "second", ProcParsers.sectionSeparator,
            "third",
        ].joined(separator: "\n")
        let sections = ProcParsers.splitSections(output, want: 5)
        #expect(sections.count == 5)
        #expect(sections[0].trimmingCharacters(in: .whitespacesAndNewlines) == "first")
        #expect(sections[2].trimmingCharacters(in: .whitespacesAndNewlines) == "third")
        #expect(sections[3] == "")
        #expect(sections[4] == "")
    }

    @Test func metricsCommandCoversEverySection() {
        // The command and the Section enum must stay in lockstep, or every
        // parser after a mismatch reads the wrong text.
        let separators = ProcParsers.linuxMetricsCommand
            .components(separatedBy: ProcParsers.sectionSeparator).count - 1
        #expect(separators == ProcParsers.Section.allCases.count - 1)
    }
}

@Suite("Latency from remote clocks")
struct LatencyTests {

    @Test func subtractsRemoteWorkFromLocalElapsed() {
        // Host reports 0.6s of its own work; the call took 0.72s locally, so
        // 120 ms was on the wire.
        let start = "1735000000000000000"
        let end = "1735000000600000000"
        let latency = ProcParsers.networkLatency(elapsed: 0.72, startClock: start, endClock: end)
        #expect(abs(latency - 120) < 0.001)
    }

    @Test func neverReportsNegativeLatency() {
        // Clock skew or an NTP step mid-run must not produce a negative number.
        let start = "1735000000000000000"
        let end = "1735000001000000000"
        #expect(ProcParsers.networkLatency(elapsed: 0.5, startClock: start, endClock: end) == 0)
    }

    @Test func missingClocksYieldZeroRatherThanNonsense() {
        // Shells without %N print the literal "N"; better to show "—".
        #expect(ProcParsers.networkLatency(elapsed: 1.0, startClock: "1735000000N", endClock: "x") == 0)
        #expect(ProcParsers.networkLatency(elapsed: 1.0, startClock: "", endClock: "") == 0)
    }

    @Test func backwardsClockIsRejected() {
        let latency = ProcParsers.networkLatency(
            elapsed: 1.0,
            startClock: "1735000000600000000",
            endClock: "1735000000000000000"
        )
        #expect(latency == 0)
    }

    @Test func clockParsesOnlyDigits() {
        #expect(ProcParsers.clock(" 1735000000000000000 \n") == 1_735_000_000_000_000_000)
        #expect(ProcParsers.clock("1735000000N") == nil)
        #expect(ProcParsers.clock("") == nil)
    }

    @Test func commandAndSectionsStayInLockstepWithClocks() {
        // Adding a clock without a matching enum case would silently shift
        // every parser after it.
        let separators = ProcParsers.linuxMetricsCommand
            .components(separatedBy: ProcParsers.sectionSeparator).count - 1
        #expect(separators == ProcParsers.Section.allCases.count - 1)
        #expect(ProcParsers.Section.startClock.rawValue == 0)
        #expect(ProcParsers.Section.endClock.rawValue == ProcParsers.Section.allCases.count - 1)
    }
}

@Suite("Host detail parsers")
struct HostDetailParserTests {

    static let multiMountDf = """
    Filesystem     1024-blocks       Used  Available Capacity Mounted on
    /dev/vda2      84421599232 30799843328 49318871040      39% /
    /dev/vdb1     210301161472 52343545856 157957615616      25% /data
    tmpfs           4194304000          0  4194304000       0% /dev/shm
    """

    @Test func anEmptyBatchYieldsAZeroSnapshotSoTheCollectorCanRejectIt() {
        // The collector treats memoryTotal==0 && cores==0 as "truncated" and
        // throws rather than returning it. This pins the parser side of that
        // contract: empty sections really do produce those zeroes, not some
        // incidental non-zero that would slip past the guard.
        let empty = ProcParsers.splitSections("", want: ProcParsers.Section.allCases.count)
        func section(_ which: ProcParsers.Section) -> String { empty[which.rawValue] }
        let (_, memoryTotal) = ProcParsers.memInfo(section(.memInfo))
        #expect(memoryTotal == 0)
        #expect(ProcParsers.cores(section(.nproc)) == 0)
    }

    @Test func everySectionHasACommand() {
        // The sections are read positionally. Adding a command without adding
        // its case (or the reverse) shifts every field after it, and the result
        // is plausible-looking nonsense rather than an error.
        let parts = ProcParsers.linuxMetricsCommand
            .components(separatedBy: "; echo \(ProcParsers.sectionSeparator); ")
        #expect(
            parts.count == ProcParsers.Section.allCases.count,
            "\(parts.count) commands for \(ProcParsers.Section.allCases.count) sections"
        )
    }

    @Test func sectionsAreSplitInOrder() {
        let output = ["A", "B", "C"].joined(separator: "\n\(ProcParsers.sectionSeparator)\n")
        let sections = ProcParsers.splitSections(output, want: 5)
        #expect(sections.count == 5, "missing trailing sections are padded, not dropped")
        #expect(sections[0].trimmingCharacters(in: .whitespacesAndNewlines) == "A")
        #expect(sections[2].trimmingCharacters(in: .whitespacesAndNewlines) == "C")
        #expect(sections[4].trimmingCharacters(in: .whitespacesAndNewlines) == "")
    }

    @Test func diskUsageReportsRootNotTheLastLine() {
        // df is asked for every mount now. Taking the last line made a host
        // with a /data volume report that volume as its disk usage.
        let (used, total) = ProcParsers.diskUsage(Self.multiMountDf)
        #expect(total == 84_421_599_232)
        #expect(used == 30_799_843_328)
    }

    @Test func filesystemsListsRealMountsRootFirst() {
        let mounts = ProcParsers.filesystems(Self.multiMountDf)
        #expect(mounts.map(\.mount) == ["/", "/data"], "tmpfs should be dropped, root first")
        #expect(mounts[1].device == "/dev/vdb1")
        #expect(abs(mounts[0].percent - 36.48) < 0.1)
    }

    @Test func aWrappedDeviceNameStillParses() {
        // df wraps a long device onto its own line and puts the numbers on the
        // next, leaving a five-field row.
        let wrapped = """
        Filesystem 1024-blocks Used Available Capacity Mounted on
        /dev/mapper/a-very-long-volume-group-name-here
                   84421599232 30799843328 49318871040 39% /
        """
        let mounts = ProcParsers.filesystems(wrapped)
        #expect(mounts.count == 1)
        #expect(mounts[0].mount == "/")
        #expect(mounts[0].total == 84_421_599_232)
    }

    @Test func perCoreUsageComesFromTheSameTwoReads() {
        let first = """
        cpu  100 0 100 800 0 0 0 0 0 0
        cpu0 50 0 50 400 0 0 0 0 0 0
        cpu1 50 0 50 400 0 0 0 0 0 0
        """
        let second = """
        cpu  200 0 200 1200 0 0 0 0 0 0
        cpu0 150 0 150 400 0 0 0 0 0 0
        cpu1 50 0 50 800 0 0 0 0 0 0
        """
        let cores = ProcParsers.coreLoads(first: first, second: second)
        #expect(cores.count == 2, "the aggregate cpu line is not a core")
        // cpu0: idle unchanged, so every added jiffy was work.
        #expect(cores[0].index == 0)
        #expect(abs(cores[0].percent - 100) < 0.01)
        // cpu1: only idle grew.
        #expect(abs(cores[1].percent - 0) < 0.01)
    }

    @Test func aCoreMissingFromTheSecondReadIsDropped() {
        // Reporting it as 0% would look like an idle core rather than a gap.
        let first = "cpu0 1 0 1 1 0 0 0 0\ncpu1 1 0 1 1 0 0 0 0"
        let second = "cpu0 2 0 2 2 0 0 0 0"
        #expect(ProcParsers.coreLoads(first: first, second: second).map(\.index) == [0])
    }

    @Test func memoryBreakdownSplitsBuffersCacheAndSwap() {
        let meminfo = """
        MemTotal:       32900000 kB
        MemFree:         1200000 kB
        MemAvailable:   24000000 kB
        Buffers:         1300000 kB
        Cached:         18800000 kB
        SwapTotal:       2000000 kB
        SwapFree:        1500000 kB
        """
        let memory = ProcParsers.memoryBreakdown(meminfo)
        #expect(memory.total == Int64(32_900_000) * 1024)
        #expect(memory.buffers == Int64(1_300_000) * 1024)
        #expect(memory.cached == Int64(18_800_000) * 1024)
        #expect(memory.used == Int64(32_900_000 - 1_200_000 - 1_300_000 - 18_800_000) * 1024)
        #expect(memory.swapUsed == Int64(500_000) * 1024)
        #expect(memory.hasSwap)
    }

    @Test func interfacesAreListedIndividuallyWithoutLoopback() {
        let netdev = """
        Inter-|   Receive                                                |  Transmit
         face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets
            lo: 5000 10 0 0 0 0 0 0 5000 10 0 0 0 0 0 0
          eth0: 900 9 0 0 0 0 0 0 700 7 0 0 0 0 0 0
        docker0: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0
        """
        let interfaces = ProcParsers.netInterfaces(netdev)
        #expect(interfaces.map(\.name) == ["eth0", "docker0"], "busiest first, no loopback")
        #expect(interfaces[0].rxTotal == 900)
        #expect(interfaces[0].txTotal == 700)
    }

    @Test func interfaceNameJoinedToItsCountStillParses() {
        // A busy interface prints as "eth0:12345678" with no space.
        let interfaces = ProcParsers.netInterfaces("  eth0:900 9 0 0 0 0 0 0 700 7 0 0 0 0 0 0")
        #expect(interfaces.count == 1)
        #expect(interfaces[0].name == "eth0")
        #expect(interfaces[0].rxTotal == 900)
    }

    @Test func processesKeepCommandsContainingSpaces() {
        let ps = """
          1234 root      12.5  3.2  512000 /usr/bin/python3 /opt/app/main.py --flag
          5678 www-data   0.0  0.1   20480 nginx: worker process
        """
        let processes = ProcParsers.processes(ps)
        #expect(processes.count == 2)
        #expect(processes[0].pid == 1234)
        #expect(processes[0].user == "root")
        #expect(processes[0].cpuPercent == 12.5)
        #expect(processes[0].residentBytes == Int64(512_000) * 1024)
        #expect(processes[0].command == "/usr/bin/python3 /opt/app/main.py --flag")
        #expect(processes[1].command == "nginx: worker process")
    }

    @Test func hostIdentityIsKeyedNotPositional() {
        let full = ProcParsers.hostIdentity("""
        host=web-1
        kern=6.8.0-134-generic
        arch=x86_64
        os=Ubuntu 24.04.4 LTS
        ips=192.168.9.132 172.17.0.1 169.254.1.1 127.0.0.1
        cpu=Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz
        """)
        #expect(full.hostname == "web-1")
        #expect(full.osName == "Ubuntu 24.04.4 LTS")
        #expect(full.architecture == "x86_64")
        #expect(full.cpuModel == "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz")
        // Link-local and loopback are noise on the card.
        #expect(full.addresses == ["192.168.9.132", "172.17.0.1"])
    }

    @Test func aFactTheHostCouldNotAnswerDoesNotShiftTheOthers() {
        // The whole reason this section is keyed: `lines()` drops empty lines,
        // so with positional parsing a host with no `hostname -I` reported its
        // CPU model as its IP address.
        let sparse = ProcParsers.hostIdentity("""
        host=box
        kern=5.10.0
        arch=
        os=
        ips=
        cpu=AMD EPYC 7B13
        """)
        #expect(sparse.hostname == "box")
        #expect(sparse.kernel == "5.10.0")
        #expect(sparse.osName.isEmpty)
        #expect(sparse.addresses.isEmpty)
        #expect(sparse.cpuModel == "AMD EPYC 7B13", "cpu model must not land in another field")
    }

    @Test func cpuBreakdownSharesTheWholeWindow() {
        // user 10, nice 0, system 20, idle 60, iowait 10 over a 100-jiffy window.
        let first = "cpu  0 0 0 0 0 0 0 0 0 0"
        let second = "cpu  10 0 20 60 10 0 0 0 0 0"
        let breakdown = ProcParsers.cpuBreakdown(first: first, second: second)
        #expect(abs(breakdown.user - 10) < 0.01)
        #expect(abs(breakdown.system - 20) < 0.01)
        #expect(abs(breakdown.iowait - 10) < 0.01)
        #expect(breakdown.steal == 0)
        #expect(breakdown.isReported)
        // The parts plus idle account for the window, matching cpuPercent.
        let busy = ProcParsers.cpuPercent(first: first, second: second)
        #expect(abs((breakdown.user + breakdown.system + breakdown.nice + breakdown.steal) - busy) < 0.01)
    }

    @Test func irqAndSoftirqFoldIntoSystem() {
        // `top` shows them inside system; a separate row nobody reads is worse.
        let first = "cpu  0 0 0 0 0 0 0 0 0 0"
        let second = "cpu  0 0 10 70 0 10 10 0 0 0"
        let breakdown = ProcParsers.cpuBreakdown(first: first, second: second)
        #expect(abs(breakdown.system - 30) < 0.01)
    }

    @Test func anIdleWindowReportsNothingRatherThanNaN() {
        let breakdown = ProcParsers.cpuBreakdown(first: "cpu  1 1 1 1 1 1 1 1", second: "cpu  1 1 1 1 1 1 1 1")
        #expect(breakdown.isReported == false)
        #expect(breakdown.user == 0)
    }
}
