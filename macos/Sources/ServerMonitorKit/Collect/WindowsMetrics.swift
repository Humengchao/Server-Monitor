import Compression
import Foundation

/// Collection for Windows hosts running OpenSSH.
///
/// Ported from the Go collector. Uses CIM classes rather than `Get-Counter`
/// because counter *names* are localised — on a Chinese or German Windows the
/// English names simply do not exist — while CIM class and property names are
/// invariant.
public enum WindowsMetrics {

    /// Gathers everything in one PowerShell invocation as `key=value` lines.
    ///
    /// The `Win32_PerfRawData_*` counters are cumulative despite the
    /// "Persec" suffix, which is what makes them usable the same way the
    /// `/proc` counters are: take a delta between polls.
    ///
    /// `$ProgressPreference` is silenced because PowerShell otherwise
    /// serialises its progress stream into stdout as CLIXML when the output is
    /// redirected — verified on Windows Server 2016, where "preparing modules
    /// for first use" records arrived mixed in with the metrics.
    public static let script = """
    $ErrorActionPreference='SilentlyContinue'
    $ProgressPreference='SilentlyContinue'
    $os = Get-CimInstance Win32_OperatingSystem
    $cores = (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
    $raw1 = @{}; foreach ($r in Get-CimInstance Win32_PerfRawData_PerfOS_Processor) { $raw1[$r.Name] = $r }
    Start-Sleep -Milliseconds 500
    $raw2 = Get-CimInstance Win32_PerfRawData_PerfOS_Processor
    $cpuPct = @{}
    foreach ($b in $raw2) {
      $a = $raw1[$b.Name]
      if ($a) {
        $dt = [double]($b.Timestamp_Sys100NS - $a.Timestamp_Sys100NS)
        $di = [double]($b.PercentProcessorTime - $a.PercentProcessorTime)
        if ($dt -gt 0) { $cpuPct[$b.Name] = [math]::Round((1 - ($di / $dt)) * 100, 1) }
      }
    }
    $cpu = $cpuPct['_Total']
    if ($null -eq $cpu) { $cpu = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average }
    $nics = Get-CimInstance Win32_PerfRawData_Tcpip_NetworkInterface
    $netrx = ($nics | Measure-Object -Property BytesReceivedPersec -Sum).Sum
    $nettx = ($nics | Measure-Object -Property BytesSentPersec -Sum).Sum
    $disk = Get-CimInstance Win32_PerfRawData_PerfDisk_PhysicalDisk -Filter "Name='_Total'"
    $uptime = [int64]((Get-Date) - $os.LastBootUpTime).TotalSeconds
    $disks = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'
    $disktotal = ($disks | Measure-Object -Property Size -Sum).Sum
    $diskfree = ($disks | Measure-Object -Property FreeSpace -Sum).Sum
    $queue = (Get-CimInstance Win32_PerfFormattedData_PerfOS_System).ProcessorQueueLength
    $df = "{{.ServerVersion}}|{{.Images}}|{{.ContainersRunning}}|{{.ContainersStopped}}|{{.ContainersPaused}}"
    $docker = if (Get-Command docker -ErrorAction SilentlyContinue) { docker info --format $df } else { '' }
    Write-Output ("cpu=" + [int64][math]::Round([double]$cpu))
    Write-Output ("memtotal=" + $os.TotalVisibleMemorySize)
    Write-Output ("memfree=" + $os.FreePhysicalMemory)
    Write-Output ("netrx=" + [int64]$netrx)
    Write-Output ("nettx=" + [int64]$nettx)
    Write-Output ("diskread=" + [int64]$disk.DiskReadBytesPersec)
    Write-Output ("diskwrite=" + [int64]$disk.DiskWriteBytesPersec)
    Write-Output ("uptime=" + $uptime)
    Write-Output ("cores=" + $cores)
    Write-Output ("disktotal=" + [int64]$disktotal)
    Write-Output ("diskfree=" + [int64]$diskfree)
    Write-Output ("queue=" + [int64]$queue)
    Write-Output ("docker=" + $docker)
    $cpuName = (Get-CimInstance Win32_Processor | Select-Object -First 1).Name
    $ident = @($env:COMPUTERNAME, $os.Version, (Get-CimInstance Win32_ComputerSystem).SystemType, $os.Caption, $cpuName)
    Write-Output ("ident=" + ($ident -join '|'))
    $ips = (Get-CimInstance Win32_NetworkAdapterConfiguration -Filter 'IPEnabled=True' | ForEach-Object { $_.IPAddress } | Where-Object { $_ -and $_ -notmatch ':' })
    Write-Output ("ips=" + ($ips -join ' '))
    foreach ($n in ($cpuPct.Keys | Where-Object { $_ -ne '_Total' })) {
      Write-Output ("core=" + $n + '|' + $cpuPct[$n])
    }
    foreach ($d in $disks) {
      Write-Output ("fs=" + $d.DeviceID + '|' + [int64]$d.Size + '|' + [int64]($d.Size - $d.FreeSpace))
    }
    foreach ($n in $nics) {
      Write-Output ("if=" + ($n.Name -replace '\\|','_') + '|' + [int64]$n.BytesReceivedPersec + '|' + [int64]$n.BytesSentPersec)
    }
    $totalmem = [double]$os.TotalVisibleMemorySize * 1024
    foreach ($pr in Get-Process | Sort-Object -Property CPU -Descending | Select-Object -First 25) {
      $pct = if ($totalmem -gt 0) { [math]::Round(($pr.WorkingSet64 / $totalmem) * 100, 1) } else { 0 }
      Write-Output ("proc=" + $pr.Id + '|' + $pr.ProcessName + '|' + [math]::Round([double]$pr.CPU, 1) + '|' + $pct + '|' + [int64]$pr.WorkingSet64)
    }
    """

    /// The full remote command.
    ///
    /// `-EncodedCommand` takes UTF-16LE base64, which sidesteps quoting
    /// entirely: the default shell on Windows OpenSSH may be cmd.exe or
    /// PowerShell, and they disagree about almost every metacharacter.
    ///
    /// The script is deflated first because UTF-16LE base64 nearly triples
    /// whatever it wraps, and the whole thing still has to fit in the command
    /// line of the host's default shell — cmd.exe, capped near 8191 characters.
    /// The uncompressed script crossed that limit as soon as the machine-screen
    /// detail was added, and the host answered "命令行太长" (command line too
    /// long) instead of running anything. Deflating leaves the payload about a
    /// third of the size, with room for the script to keep growing.
    public static var command: String {
        "powershell -NoProfile -NonInteractive -EncodedCommand \(encode(stub))"
    }

    /// The self-extracting wrapper actually sent to the host.
    ///
    /// `COMPRESSION_ZLIB` is raw DEFLATE (RFC 1951), which is exactly what
    /// .NET's `DeflateStream` reads — no gzip or zlib header is involved on
    /// either side.
    static var stub: String {
        let payload = deflate(script).base64EncodedString()
        return """
        $s=[IO.Compression.DeflateStream]::new([IO.MemoryStream]::new([Convert]::FromBase64String('\(payload)')),[IO.Compression.CompressionMode]::Decompress)
        iex ([IO.StreamReader]::new($s)).ReadToEnd()
        """
    }

    /// Raw DEFLATE, via the system compressor.
    static func deflate(_ text: String) -> Data {
        let source = Array(text.utf8)
        // DEFLATE can expand incompressible input; give the buffer room so a
        // pathological script is never silently truncated.
        var destination = [UInt8](repeating: 0, count: source.count + 4096)
        let written = compression_encode_buffer(
            &destination, destination.count,
            source, source.count,
            nil, COMPRESSION_ZLIB
        )
        guard written > 0 else { return Data(source) }
        return Data(destination.prefix(written))
    }

    static func encode(_ script: String) -> String {
        var bytes: [UInt8] = []
        bytes.reserveCapacity(script.utf16.count * 2)
        for unit in script.utf16 {
            bytes.append(UInt8(unit & 0xFF))
            bytes.append(UInt8(unit >> 8))
        }
        return Data(bytes).base64EncodedString()
    }

    /// Parses the `key=value` output. Returns nil when nothing parsed, which is
    /// how a Linux host (or a failed command) is told apart from a real result.
    public static func parse(_ output: String) -> MetricSnapshot? {
        var values: [String: Int64] = [:]
        var dockerVersion = ""
        var dockerSummary: DockerSummary?
        var cores: [CoreLoad] = []
        var filesystems: [FilesystemUsage] = []
        var interfaces: [NetInterface] = []
        var processes: [HostProcess] = []
        var identity = HostIdentity()

        for rawLine in output.lines() {
            let line = rawLine.trimmingCharacters(in: .whitespacesAndNewlines)
            guard let separator = line.firstIndex(of: "=") else { continue }
            let key = String(line[line.startIndex..<separator])
            let value = String(line[line.index(after: separator)...])
                .trimmingCharacters(in: .whitespaces)
            // Repeated keys carry the list-shaped detail, pipe-separated
            // because a Windows path or adapter name may contain anything else.
            let parts = value.split(separator: "|", omittingEmptySubsequences: false).map(String.init)
            switch key {
            case "docker":
                dockerVersion = ProcParsers.dockerVersion(value)
                if !dockerVersion.isEmpty {
                    let parsed = DockerClient.parseSummary(value)
                    if !parsed.engineVersion.isEmpty { dockerSummary = parsed }
                }
            case "ident":
                if parts.count > 0 { identity.hostname = parts[0] }
                if parts.count > 1 { identity.kernel = parts[1] }
                if parts.count > 2 { identity.architecture = parts[2] }
                if parts.count > 3 { identity.osName = parts[3] }
                // Windows has no user/nice/iowait/steal split to report, so
                // the card shows the model and leaves the breakdown row out.
                if parts.count > 4 { identity.cpuModel = parts[4] }
            case "ips":
                identity.addresses = value
                    .split(separator: " ", omittingEmptySubsequences: true)
                    .map(String.init)
                    .filter { !$0.hasPrefix("169.254.") && $0 != "127.0.0.1" }
            case "core":
                // Name is the core index on Windows, but can be "0,1" on
                // multi-group machines; only a plain index is usable here.
                if parts.count >= 2, let index = Int(parts[0]), let percent = Double(parts[1]) {
                    cores.append(CoreLoad(index: index, percent: min(100, max(0, percent))))
                }
            case "fs":
                if parts.count >= 3, let total = Int64(parts[1]), let used = Int64(parts[2]), total > 0 {
                    filesystems.append(
                        FilesystemUsage(mount: parts[0], device: parts[0], used: used, total: total)
                    )
                }
            case "if":
                if parts.count >= 3, let rx = Int64(parts[1]), let tx = Int64(parts[2]) {
                    interfaces.append(NetInterface(name: parts[0], rxTotal: rx, txTotal: tx))
                }
            case "proc":
                if parts.count >= 5, let pid = Int(parts[0]), let rss = Int64(parts[4]) {
                    processes.append(HostProcess(
                        pid: pid,
                        user: "",
                        // Get-Process reports CPU as total seconds used, not a
                        // percentage; it is shown as such rather than faked
                        // into one.
                        cpuPercent: Double(parts[2]) ?? 0,
                        memPercent: Double(parts[3]) ?? 0,
                        residentBytes: rss,
                        command: parts[1]
                    ))
                }
            default:
                if let number = Int64(value) { values[key] = number }
            }
        }
        guard !values.isEmpty else { return nil }

        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = Double(values["cpu"] ?? 0)
        // TotalVisibleMemorySize and FreePhysicalMemory are in kB.
        snapshot.memoryTotal = (values["memtotal"] ?? 0) * 1024
        snapshot.memoryUsed = max(0, ((values["memtotal"] ?? 0) - (values["memfree"] ?? 0)) * 1024)
        snapshot.diskTotal = values["disktotal"] ?? 0
        snapshot.diskUsed = max(0, (values["disktotal"] ?? 0) - (values["diskfree"] ?? 0))
        snapshot.uptimeSeconds = values["uptime"] ?? 0
        snapshot.cores = Int(values["cores"] ?? 0)
        // Windows has no load average; the processor queue length is the
        // closest equivalent and is what the web backend showed too.
        snapshot.load1 = Double(values["queue"] ?? 0)
        snapshot.netRxTotal = values["netrx"] ?? 0
        snapshot.netTxTotal = values["nettx"] ?? 0
        snapshot.dockerVersion = dockerVersion
        snapshot.dockerSummary = dockerSummary
        snapshot.coreLoads = cores.sorted { $0.index < $1.index }
        snapshot.filesystems = filesystems.sorted { $0.total > $1.total }
        snapshot.interfaces = interfaces.sorted { $0.rxTotal + $0.txTotal > $1.rxTotal + $1.txTotal }
        snapshot.processes = processes
        snapshot.identity = identity
        var memory = MemoryBreakdown()
        memory.total = snapshot.memoryTotal
        memory.free = (values["memfree"] ?? 0) * 1024
        // Windows does not expose a buffers/cache split the way /proc does, so
        // the card shows used against free and leaves those bands out.
        snapshot.memory = memory
        return snapshot
    }

    /// Cumulative counters for the rate calculation.
    public static func counters(_ output: String) -> (net: (rx: Int64, tx: Int64), disk: (read: Int64, written: Int64)) {
        var values: [String: Int64] = [:]
        for rawLine in output.lines() {
            let line = rawLine.trimmingCharacters(in: .whitespacesAndNewlines)
            guard let separator = line.firstIndex(of: "="),
                  let number = Int64(line[line.index(after: separator)...].trimmingCharacters(in: .whitespaces))
            else { continue }
            values[String(line[line.startIndex..<separator])] = number
        }
        return (
            (values["netrx"] ?? 0, values["nettx"] ?? 0),
            (values["diskread"] ?? 0, values["diskwrite"] ?? 0)
        )
    }
}
