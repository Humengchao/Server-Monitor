import Foundation

/// Parsers for the `/proc` and `df` output gathered by one collection round.
///
/// Ported from the Go collector so both implementations read the same fields in
/// the same way. Every function is pure and total: malformed or truncated input
/// yields zeros rather than throwing, because a partially readable host should
/// still report the metrics it did return.
public enum ProcParsers {

    /// Marker printed between batched commands.
    public static let sectionSeparator = "---SM-SECTION---"

    /// The sections of `linuxMetricsCommand`, in order.
    ///
    /// The clock sections bracket the run so the host can report how long it
    /// spent, which is what makes the latency figure meaningful — see
    /// `networkLatency`.
    public enum Section: Int, CaseIterable {
        case startClock
        case statFirst, statSecond, memInfo, loadAvg, netDev, diskStats, uptime, nproc, diskUsage
        case processes, hostInfo, gpu, docker
        case endClock
    }

    /// Gathers every sample in a single SSH round trip instead of one session
    /// per file, which matters on high-latency links. The second `/proc/stat`
    /// read is delayed *remotely* so CPU usage still comes from two samples
    /// half a second apart without a second round trip.
    public static let linuxMetricsCommand: String = {
        let commands = [
            "date +%s%N",
            "cat /proc/stat",
            "sleep 0.5 || sleep 1; cat /proc/stat",
            "cat /proc/meminfo",
            "cat /proc/loadavg",
            "cat /proc/net/dev",
            "cat /proc/diskstats",
            "cat /proc/uptime",
            "nproc",
            // Every local filesystem, not just `/`: the storage card lists
            // mounts, and the headline figure still comes from `/`.
            "df -P -B1 \(pseudoFilesystemExclusions) 2>/dev/null || df -P -B1",
            // Sorted by CPU share, trimmed here rather than locally so the
            // round trip stays small on a busy host.
            "ps -eo pid=,user=,pcpu=,pmem=,rss=,args= --sort=-pcpu 2>/dev/null | head -n 30",
            // key=value rather than one fact per line: `lines()` drops empty lines,
            // so a host where any of these produced nothing shifted every later
            // field up — the CPU model would arrive as the IP address list.
            "echo host=$(hostname); echo kern=$(uname -r); echo arch=$(uname -m); echo os=$(. /etc/os-release 2>/dev/null && echo $PRETTY_NAME); echo ips=$(hostname -I 2>/dev/null); echo cpu=$(grep -m1 '^model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2-)",
            // Short-circuits to nothing on the ~all hosts with no NVIDIA card,
            // so this costs one `command -v` there. Emitted as key=value for
            // the same reason the host-info section is.
            "command -v nvidia-smi >/dev/null 2>&1 && { nvidia-smi | grep -m1 'Driver Version' | sed -E 's/.*Driver Version: *([^ ]+).*CUDA Version: *([^ ]+).*/driver=\\1\\ncuda=\\2/'; nvidia-smi --query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu,fan.speed,power.draw,power.limit --format=csv,noheader,nounits | sed 's/^/gpu=/'; nvidia-smi --query-compute-apps=pid,process_name,used_memory --format=csv,noheader,nounits | sed 's/^/proc=/'; } || true",
            // The engine counts cost nothing extra here — this call was already
            // being made for the version alone, and asking for all four fields
            // saves the machine screen and the Docker page a round trip each.
            "D=\"{{.ServerVersion}}|{{.Images}}|{{.ContainersRunning}}|{{.ContainersStopped}}|{{.ContainersPaused}}\"; docker info --format \"$D\" || sudo -n docker info --format \"$D\" || true",
            "date +%s%N",
        ]
        return commands.joined(separator: "; echo \(sectionSeparator); ")
    }()

    /// Splits batched output on separator lines. A truncated run leaves the
    /// trailing sections empty rather than misaligning the ones that arrived.
    public static func splitSections(_ output: String, want: Int) -> [String] {
        var sections: [String] = []
        var current = ""
        for line in output.split(separator: "\n", omittingEmptySubsequences: false) {
            if line.trimmingCharacters(in: .whitespaces) == sectionSeparator {
                sections.append(current)
                current = ""
                continue
            }
            current += line
            current += "\n"
        }
        sections.append(current)
        while sections.count < want { sections.append("") }
        return sections
    }

    // MARK: - CPU

    /// CPU usage from two `/proc/stat` snapshots taken half a second apart.
    public static func cpuPercent(first: String, second: String) -> Double {
        let (idle1, total1) = procStatCPU(first)
        let (idle2, total2) = procStatCPU(second)
        let deltaIdle = idle2 - idle1
        let deltaTotal = total2 - total1
        guard deltaTotal > 0 else { return 0 }
        return (1.0 - Double(deltaIdle) / Double(deltaTotal)) * 100
    }

    /// Returns (idle, total) jiffies from the aggregate `cpu ` line.
    /// idle counts idle+iowait, matching the Go collector.
    static func procStatCPU(_ output: String) -> (idle: Int64, total: Int64) {
        for line in output.lines() where line.hasPrefix("cpu ") {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 8 else { return (0, 0) }
            var values = [Int64](repeating: 0, count: 8)
            for index in 0..<8 where index + 1 < fields.count {
                values[index] = Int64(fields[index + 1]) ?? 0
            }
            return (values[3] + values[4], values.reduce(0, +))
        }
        return (0, 0)
    }

    /// Per-core utilisation from the same two `/proc/stat` reads.
    ///
    /// The aggregate `cpu` line is skipped; only the numbered `cpuN` lines are
    /// cores. A core missing from the second read is dropped rather than
    /// reported as 0%, which would look like an idle core instead of a gap.
    public static func coreLoads(first: String, second: String) -> [CoreLoad] {
        let before = perCoreTimes(first)
        let after = perCoreTimes(second)
        return after.keys.sorted().compactMap { index in
            guard let start = before[index], let end = after[index] else { return nil }
            let deltaTotal = end.total - start.total
            guard deltaTotal > 0 else { return CoreLoad(index: index, percent: 0) }
            let deltaIdle = end.idle - start.idle
            let percent = (1.0 - Double(deltaIdle) / Double(deltaTotal)) * 100
            return CoreLoad(index: index, percent: min(100, max(0, percent)))
        }
    }

    private static func perCoreTimes(_ output: String) -> [Int: (idle: Int64, total: Int64)] {
        var result: [Int: (idle: Int64, total: Int64)] = [:]
        for line in output.lines() where line.hasPrefix("cpu") {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 8 else { continue }
            let label = fields[0].dropFirst(3)          // "cpu12" -> "12"
            guard !label.isEmpty, let index = Int(label) else { continue }
            var values = [Int64](repeating: 0, count: 8)
            for slot in 0..<8 where slot + 1 < fields.count {
                values[slot] = Int64(fields[slot + 1]) ?? 0
            }
            result[index] = (values[3] + values[4], values.reduce(0, +))
        }
        return result
    }

    /// How the sampling window was spent, from the same two `/proc/stat` reads.
    ///
    /// Percentages are of the whole window, so they sum to the busy figure
    /// `cpuPercent` reports (plus idle), rather than each being a share of the
    /// busy time.
    public static func cpuBreakdown(first: String, second: String) -> CPUBreakdown {
        let before = aggregateFields(first)
        let after = aggregateFields(second)
        guard before.count >= 8, after.count >= 8 else { return CPUBreakdown() }
        let deltas = zip(before, after).map { max(0, $1 - $0) }
        let total = deltas.reduce(0, +)
        guard total > 0 else { return CPUBreakdown() }

        func share(_ value: Int64) -> Double { Double(value) / Double(total) * 100 }
        var breakdown = CPUBreakdown()
        breakdown.user = share(deltas[0])
        breakdown.nice = share(deltas[1])
        // irq + softirq belong with system; `top` shows them folded in and
        // splitting them out would leave a row nobody reads.
        breakdown.system = share(deltas[2] + deltas[5] + deltas[6])
        breakdown.iowait = share(deltas[4])
        breakdown.steal = share(deltas[7])
        return breakdown
    }

    /// The eight counters on the aggregate `cpu ` line.
    private static func aggregateFields(_ output: String) -> [Int64] {
        for line in output.lines() where line.hasPrefix("cpu ") {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 9 else { return [] }
            return (1...8).map { Int64(fields[$0]) ?? 0 }
        }
        return []
    }

    // MARK: - Memory

    /// Used and total memory in bytes. `/proc/meminfo` reports kB.
    public static func memInfo(_ output: String) -> (used: Int64, total: Int64) {
        var total: Int64 = 0, free: Int64 = 0, buffers: Int64 = 0, cached: Int64 = 0
        for line in output.lines() {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 2, let value = Int64(fields[1]) else { continue }
            switch fields[0] {
            case "MemTotal:": total = value
            case "MemFree:": free = value
            case "Buffers:": buffers = value
            case "Cached:": cached = value
            default: break
            }
        }
        let used = max(0, total - free - buffers - cached)
        return (used * 1024, total * 1024)
    }

    /// The same file, broken out for the memory card. `/proc/meminfo` is in kB.
    public static func memoryBreakdown(_ output: String) -> MemoryBreakdown {
        var fields: [String: Int64] = [:]
        for line in output.lines() {
            let parts = line.split(separator: " ", omittingEmptySubsequences: true)
            guard parts.count >= 2, let value = Int64(parts[1]) else { continue }
            fields[String(parts[0].dropLast())] = value * 1024   // drop the ":"
        }
        var memory = MemoryBreakdown()
        memory.total = fields["MemTotal"] ?? 0
        memory.free = fields["MemFree"] ?? 0
        memory.buffers = fields["Buffers"] ?? 0
        memory.cached = fields["Cached"] ?? 0
        memory.swapTotal = fields["SwapTotal"] ?? 0
        memory.swapUsed = max(0, memory.swapTotal - (fields["SwapFree"] ?? 0))
        return memory
    }

    // MARK: - Load

    public static func loadAverage(_ output: String) -> (Double, Double, Double) {
        let fields = output.split(separator: " ", omittingEmptySubsequences: true)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
        guard fields.count >= 3 else { return (0, 0, 0) }
        return (Double(fields[0]) ?? 0, Double(fields[1]) ?? 0, Double(fields[2]) ?? 0)
    }

    // MARK: - Network

    /// Cumulative rx/tx bytes across every interface except loopback.
    public static func netDev(_ output: String) -> (rx: Int64, tx: Int64) {
        var rxTotal: Int64 = 0, txTotal: Int64 = 0
        for line in output.lines() where line.contains(":") {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 10 else { continue }
            let name = fields[0].hasSuffix(":") ? String(fields[0].dropLast()) : String(fields[0])
            if name == "lo" { continue }
            rxTotal += Int64(fields[1]) ?? 0
            txTotal += Int64(fields[9]) ?? 0
        }
        return (rxTotal, txTotal)
    }

    /// Each interface separately, for the network card. Loopback is skipped for
    /// the same reason it is skipped in the totals: it is not real traffic.
    public static func netInterfaces(_ output: String) -> [NetInterface] {
        var interfaces: [NetInterface] = []
        for line in output.lines() where line.contains(":") {
            // The name and its colon may be joined to the first count on a busy
            // interface ("eth0:1234"), so split on the colon first.
            guard let colon = line.firstIndex(of: ":") else { continue }
            let name = line[line.startIndex..<colon].trimmingCharacters(in: .whitespaces)
            guard !name.isEmpty, name != "lo", !name.contains("|") else { continue }
            let counts = line[line.index(after: colon)...]
                .split(separator: " ", omittingEmptySubsequences: true)
            guard counts.count >= 9 else { continue }
            interfaces.append(NetInterface(
                name: name,
                rxTotal: Int64(counts[0]) ?? 0,
                txTotal: Int64(counts[8]) ?? 0
            ))
        }
        return interfaces.sorted { $0.rxTotal + $0.txTotal > $1.rxTotal + $1.txTotal }
    }

    // MARK: - Disk I/O

    /// Cumulative bytes read/written across whole disks, skipping partitions.
    /// `/proc/diskstats` counts 512-byte sectors.
    public static func diskStats(_ output: String) -> (read: Int64, written: Int64) {
        var readSectors: Int64 = 0, writeSectors: Int64 = 0
        for line in output.lines() {
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            guard fields.count >= 14 else { continue }
            let name = String(fields[2])
            guard name.hasPrefix("sd") || name.hasPrefix("vd") || name.hasPrefix("nvme") else { continue }
            if name.contains("nvme") {
                // nvme partitions look like nvme0n1p1.
                if name.contains("p") && name.dropFirst(4).contains("p") { continue }
            } else if let last = name.last, last.isNumber {
                // sd/vd ending in a digit is a partition.
                continue
            }
            readSectors += Int64(fields[5]) ?? 0
            writeSectors += Int64(fields[9]) ?? 0
        }
        return (readSectors * 512, writeSectors * 512)
    }

    // MARK: - Latency

    /// Nanosecond epoch from `date +%s%N`, or nil where the shell lacks %N.
    public static func clock(_ output: String) -> Int64? {
        let text = output.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, text.allSatisfy(\.isNumber), let value = Int64(text) else { return nil }
        return value
    }

    /// Network round trip, in milliseconds, for one collection round.
    ///
    /// `elapsed` is measured locally around the whole `ssh` invocation, and the
    /// host's own two clock readings say how much of that it spent working —
    /// including the deliberate half-second sleep between CPU samples.
    /// Subtracting leaves the time on the wire.
    ///
    /// This is the only latency probe that survives a VPN or proxy tunnel: ICMP
    /// and even a TCP handshake are answered locally by the tunnel, whereas
    /// this requires bytes to reach the host and come back. It does include the
    /// local `ssh` process launch (~15-20 ms), which is the price of not
    /// speaking SSH in-process.
    public static func networkLatency(
        elapsed: TimeInterval,
        startClock: String,
        endClock: String
    ) -> Double {
        guard let start = clock(startClock), let end = clock(endClock), end >= start else {
            return 0
        }
        let remoteSeconds = Double(end - start) / 1_000_000_000
        return max(0, (elapsed - remoteSeconds) * 1000)
    }

    /// The `nvidia-smi` section. Returns an empty status on the ~all hosts
    /// without a card, which is what makes the GPU card conditional.
    public static func gpuStatus(_ output: String) -> GPUStatus {
        var status = GPUStatus()
        for line in output.lines() {
            guard let separator = line.firstIndex(of: "=") else { continue }
            let key = String(line[line.startIndex..<separator])
            let value = String(line[line.index(after: separator)...])
                .trimmingCharacters(in: .whitespaces)
            switch key {
            case "driver": status.driverVersion = value
            case "cuda": status.cudaVersion = value
            case "gpu":
                if let gpu = parseGPU(value) { status.gpus.append(gpu) }
            case "proc":
                if let process = parseGPUProcess(value) { status.processes.append(process) }
            default: break
            }
        }
        status.gpus.sort { $0.index < $1.index }
        return status
    }

    /// index, name, util%, memTotal, memUsed, temp, fan, powerDraw, powerLimit
    /// — with `nounits`, so every value is a bare number or `[N/A]`.
    static func parseGPU(_ row: String) -> GPUInfo? {
        let fields = row.components(separatedBy: ",").map {
            $0.trimmingCharacters(in: .whitespaces)
        }
        guard fields.count >= 5, let index = Int(fields[0]) else { return nil }
        func optional(_ position: Int) -> Double? {
            guard position < fields.count else { return nil }
            return Double(fields[position])          // "[N/A]" parses to nil
        }
        // nvidia-smi reports memory in MiB under `nounits`.
        let mib: Int64 = 1024 * 1024
        return GPUInfo(
            index: index,
            name: fields[1],
            utilizationPercent: Double(fields[2]) ?? 0,
            memoryTotal: (Int64(fields[3]) ?? 0) * mib,
            memoryUsed: (Int64(fields[4]) ?? 0) * mib,
            temperatureC: optional(5),
            fanPercent: optional(6),
            powerDrawW: optional(7),
            powerLimitW: optional(8)
        )
    }

    static func parseGPUProcess(_ row: String) -> GPUProcess? {
        let fields = row.components(separatedBy: ",").map {
            $0.trimmingCharacters(in: .whitespaces)
        }
        guard fields.count >= 3, let pid = Int(fields[0]) else { return nil }
        return GPUProcess(
            pid: pid,
            name: fields[1],
            memoryUsed: (Int64(fields[2]) ?? 0) * 1024 * 1024
        )
    }

    // MARK: - Uptime, cores, disk usage, docker

    public static func uptime(_ output: String) -> Int64 {
        guard let first = output.split(separator: " ", omittingEmptySubsequences: true).first,
              let seconds = Double(first.trimmingCharacters(in: .whitespacesAndNewlines))
        else { return 0 }
        return Int64(seconds)
    }

    public static func cores(_ output: String) -> Int {
        Int(output.trimmingCharacters(in: .whitespacesAndNewlines)) ?? 0
    }

    /// Every mount from `df -P -B1`, for the storage card.
    ///
    /// Fields are counted from the end so a long device name that wrapped onto
    /// its own line still parses, and pseudo-filesystems that slipped past the
    /// `-x` flags are dropped: they report a size but hold nothing.
    public static func filesystems(_ output: String) -> [FilesystemUsage] {
        var result: [FilesystemUsage] = []
        for line in output.lines().dropFirst() {          // drop the header
            let fields = line.split(separator: " ", omittingEmptySubsequences: true)
            // Five fields is a row whose long device name wrapped onto the
            // previous line; six is a whole row. Counting from the end handles
            // both, which is why the device is read only when it is present.
            guard fields.count >= 5 else { continue }
            let mount = String(fields[fields.count - 1])
            let total = Int64(fields[fields.count - 5]) ?? 0
            let used = Int64(fields[fields.count - 4]) ?? 0
            guard total > 0 else { continue }
            let device = fields.count >= 6 ? String(fields[0]) : ""
            guard !Self.isPseudoFilesystem(device, mount: mount) else { continue }
            result.append(FilesystemUsage(mount: mount, device: device, used: used, total: total))
        }
        // Biggest first, but `/` always leads: it is the one people look for.
        return result.sorted {
            if ($0.mount == "/") != ($1.mount == "/") { return $0.mount == "/" }
            return $0.total > $1.total
        }
    }

    /// Whether a `df` row is a kernel interface rather than storage.
    ///
    /// Type alone is not enough: `efivarfs` slipped through and put the 128 KB
    /// firmware variable store on the storage card of every UEFI host, next to
    /// its real disks. Any mount under /sys, /proc or /dev is the same kind of
    /// thing whatever its type happens to be called, which also covers the
    /// next one of these rather than waiting to be surprised by it.
    static func isPseudoFilesystem(_ device: String, mount: String = "") -> Bool {
        if pseudoFilesystemTypes.contains(device) || device.hasPrefix("/dev/loop") { return true }
        return mount.hasPrefix("/sys/") || mount.hasPrefix("/proc/") || mount.hasPrefix("/dev/")
    }

    /// Filesystem types that are kernel interfaces, not storage. One list: it
    /// builds the `-x` flags `df` is asked with, and it is the check applied to
    /// whatever comes back — the fallback `df` on a host whose `df` lacks `-x`
    /// returns everything, so the local check has to be complete anyway.
    static let pseudoFilesystemTypes = [
        "tmpfs", "devtmpfs", "overlay", "squashfs", "udev", "none", "efivarfs", "ramfs",
    ]

    static let pseudoFilesystemExclusions = pseudoFilesystemTypes.map { "-x \($0)" }.joined(separator: " ")

    /// Where `/proc/cpuinfo` has no model name — every aarch64 host — `lscpu`
    /// derives one from the implementer/part tables. It walks every core's
    /// sysfs topology to do it, so it is asked once per host, not per poll.
    static let cpuModelFallbackCommand = "lscpu 2>/dev/null | grep -m1 -i '^model name' | cut -d: -f2-"

    /// Rows of `ps -eo pid=,user=,pcpu=,pmem=,rss=,args=`.
    ///
    /// The command is everything after the five fixed columns, kept whole so a
    /// path with spaces survives; `rss` is in kB.
    public static func processes(_ output: String) -> [HostProcess] {
        var result: [HostProcess] = []
        for line in output.lines() {
            let fields = line.split(
                separator: " ", maxSplits: 5, omittingEmptySubsequences: true
            )
            guard fields.count >= 6,
                  let pid = Int(fields[0]),
                  let cpu = Double(fields[2]),
                  let mem = Double(fields[3]),
                  let rss = Int64(fields[4])
            else { continue }
            result.append(HostProcess(
                pid: pid,
                user: String(fields[1]),
                cpuPercent: cpu,
                memPercent: mem,
                residentBytes: rss * 1024,
                command: String(fields[5]).trimmingCharacters(in: .whitespaces)
            ))
        }
        return result
    }

    /// The `key=value` lines emitted by the host-info section.
    ///
    /// Keyed rather than positional so a fact the host could not answer simply
    /// goes missing instead of shifting every later field.
    public static func hostIdentity(_ output: String) -> HostIdentity {
        var fields: [String: String] = [:]
        for line in output.lines() {
            guard let separator = line.firstIndex(of: "=") else { continue }
            let key = String(line[line.startIndex..<separator]).trimmingCharacters(in: .whitespaces)
            fields[key] = String(line[line.index(after: separator)...])
                .trimmingCharacters(in: .whitespaces)
        }
        var identity = HostIdentity()
        identity.hostname = fields["host"] ?? ""
        identity.kernel = fields["kern"] ?? ""
        identity.architecture = fields["arch"] ?? ""
        identity.osName = fields["os"] ?? ""
        identity.cpuModel = fields["cpu"] ?? ""
        identity.addresses = (fields["ips"] ?? "")
            .split(separator: " ", omittingEmptySubsequences: true)
            .map(String.init)
            // Docker bridges and link-local addresses are noise on this card.
            .filter { !$0.hasPrefix("169.254.") && $0 != "127.0.0.1" }
        return identity
    }

    /// Used and total bytes for the root filesystem — what "disk" means on the
    /// dashboard and in the alert thresholds.
    ///
    /// `df` is now asked for every mount, so picking the last line would report
    /// whichever filesystem happened to sort last: on a host with a `/data`
    /// volume the headline figure silently became that volume's.
    public static func diskUsage(_ output: String) -> (used: Int64, total: Int64) {
        let mounts = filesystems(output)
        guard let root = mounts.first(where: { $0.mount == "/" }) ?? mounts.first else {
            return (0, 0)
        }
        return (root.used, root.total)
    }

    /// A real server version is a short single token like "24.0.7". Anything
    /// else (error text, sudo noise) means docker is not usable on the host.
    public static func dockerVersion(_ output: String) -> String {
        // The section carries "version|images|running|stopped" now; older hosts
        // and the Windows script may still send the bare version.
        // omittingEmptySubsequences must be off: a daemon that answered with an
        // empty version ("|14|5|3") would otherwise drop the empty field and
        // make the image count the version.
        let firstField = output
            .split(separator: "|", maxSplits: 1, omittingEmptySubsequences: false)
            .first
            .map(String.init) ?? output
        let version = firstField.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !version.isEmpty, version.count <= 31,
              version.rangeOfCharacter(from: .whitespacesAndNewlines) == nil
        else { return "" }
        return version
    }
}
