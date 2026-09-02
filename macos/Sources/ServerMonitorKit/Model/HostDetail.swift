import Foundation

/// One logical CPU's utilisation for the current poll.
public struct CoreLoad: Sendable, Equatable, Identifiable {
    public var index: Int
    public var percent: Double
    public var id: Int { index }

    public init(index: Int, percent: Double) {
        self.index = index
        self.percent = percent
    }
}

/// One NVIDIA GPU, as `nvidia-smi --query-gpu` reports it.
///
/// The optional fields really are optional: nvidia-smi prints `[N/A]` for a
/// figure the card does not expose — datacentre parts have no fan, and some
/// report no power limit — and a card that shows 0 °C or 0 W for those is
/// worse than one that shows nothing.
public struct GPUInfo: Sendable, Equatable, Identifiable {
    public var index: Int
    public var name: String
    public var utilizationPercent: Double
    public var memoryTotal: Int64
    public var memoryUsed: Int64
    public var temperatureC: Double?
    public var fanPercent: Double?
    public var powerDrawW: Double?
    public var powerLimitW: Double?
    public var id: Int { index }

    public init(
        index: Int, name: String, utilizationPercent: Double,
        memoryTotal: Int64, memoryUsed: Int64,
        temperatureC: Double? = nil, fanPercent: Double? = nil,
        powerDrawW: Double? = nil, powerLimitW: Double? = nil
    ) {
        self.index = index
        self.name = name
        self.utilizationPercent = utilizationPercent
        self.memoryTotal = memoryTotal
        self.memoryUsed = memoryUsed
        self.temperatureC = temperatureC
        self.fanPercent = fanPercent
        self.powerDrawW = powerDrawW
        self.powerLimitW = powerLimitW
    }

    public var memoryPercent: Double {
        memoryTotal > 0 ? Double(memoryUsed) / Double(memoryTotal) * 100 : 0
    }
}

/// A process holding GPU memory.
public struct GPUProcess: Sendable, Equatable, Identifiable {
    public var pid: Int
    public var name: String
    public var memoryUsed: Int64
    public var id: Int { pid }

    public init(pid: Int, name: String, memoryUsed: Int64) {
        self.pid = pid
        self.name = name
        self.memoryUsed = memoryUsed
    }
}

/// Everything the GPU card shows. Empty on the overwhelming majority of hosts,
/// which is why the card is left out entirely rather than shown empty.
public struct GPUStatus: Sendable, Equatable {
    public var driverVersion: String = ""
    public var cudaVersion: String = ""
    public var gpus: [GPUInfo] = []
    public var processes: [GPUProcess] = []

    public init() {}

    public var isPresent: Bool { !gpus.isEmpty }
}

/// Where the CPU's time actually went, as percentages of the sampling window.
///
/// The single "busy" number hides the difference between a box doing work and
/// one stuck on disk or robbed by its hypervisor — `iowait` and `steal` are the
/// two that change what you do about it.
public struct CPUBreakdown: Sendable, Equatable {
    public var user: Double = 0
    /// Includes irq and softirq, the way `top` folds them in.
    public var system: Double = 0
    public var nice: Double = 0
    public var iowait: Double = 0
    public var steal: Double = 0

    public init() {}

    /// False on hosts that do not report the split (Windows), so the card can
    /// leave the row out instead of drawing five zeroes.
    public var isReported: Bool {
        user > 0 || system > 0 || nice > 0 || iowait > 0 || steal > 0
    }
}

/// `/proc/meminfo` broken out the way `free` presents it, so the memory card
/// can show where the RAM actually went rather than one "used" number.
public struct MemoryBreakdown: Sendable, Equatable {
    public var total: Int64 = 0
    public var free: Int64 = 0
    public var buffers: Int64 = 0
    public var cached: Int64 = 0
    public var swapTotal: Int64 = 0
    public var swapUsed: Int64 = 0

    public init() {}

    /// Everything the kernel has not classified as free, buffer or cache.
    public var used: Int64 { max(0, total - free - buffers - cached) }

    public var usedPercent: Double {
        total > 0 ? Double(used) / Double(total) * 100 : 0
    }

    public var swapPercent: Double {
        swapTotal > 0 ? Double(swapUsed) / Double(swapTotal) * 100 : 0
    }

    public var hasSwap: Bool { swapTotal > 0 }
}

/// A network interface and its traffic. Rates are filled in by the collector
/// from the previous poll's counters; the totals come straight from the host.
public struct NetInterface: Sendable, Equatable, Identifiable {
    public var name: String
    public var rxTotal: Int64
    public var txTotal: Int64
    public var rxRate: Double = 0
    public var txRate: Double = 0
    public var id: String { name }

    public init(name: String, rxTotal: Int64, txTotal: Int64, rxRate: Double = 0, txRate: Double = 0) {
        self.name = name
        self.rxTotal = rxTotal
        self.txTotal = txTotal
        self.rxRate = rxRate
        self.txRate = txRate
    }
}

/// One mounted filesystem.
public struct FilesystemUsage: Sendable, Equatable, Identifiable {
    public var mount: String
    public var device: String
    public var used: Int64
    public var total: Int64
    public var id: String { mount }

    public init(mount: String, device: String, used: Int64, total: Int64) {
        self.mount = mount
        self.device = device
        self.used = used
        self.total = total
    }

    public var percent: Double {
        total > 0 ? Double(used) / Double(total) * 100 : 0
    }
}

/// A row of `ps`. Named to avoid colliding with `Foundation.ProcessInfo`.
public struct HostProcess: Sendable, Equatable, Identifiable {
    public var pid: Int
    public var user: String
    public var cpuPercent: Double
    public var memPercent: Double
    public var residentBytes: Int64
    public var command: String
    public var id: Int { pid }

    public init(
        pid: Int,
        user: String,
        cpuPercent: Double,
        memPercent: Double,
        residentBytes: Int64,
        command: String
    ) {
        self.pid = pid
        self.user = user
        self.cpuPercent = cpuPercent
        self.memPercent = memPercent
        self.residentBytes = residentBytes
        self.command = command
    }
}

/// Slow-moving facts about the machine itself, refreshed with every poll
/// because they cost nothing extra once the connection is open.
public struct HostIdentity: Sendable, Equatable {
    public var hostname: String = ""
    public var osName: String = ""
    public var kernel: String = ""
    public var architecture: String = ""
    /// e.g. "Intel(R) Xeon(R) Platinum 8269CY CPU @ 2.50GHz".
    public var cpuModel: String = ""
    public var addresses: [String] = []

    public init() {}

    public var isEmpty: Bool {
        hostname.isEmpty && osName.isEmpty && kernel.isEmpty && addresses.isEmpty
    }
}
