import Foundation
import Testing
@testable import ServerMonitorKit

/// Parses captured output from a real Linux host (set SM_REAL_OUTPUT to a file
/// holding one collection round). Skipped when the fixture is absent, so the
/// suite stays runnable without a server.
@Suite("Real host output")
struct RealHostParseTests {

    @Test func parsesCapturedHostOutput() throws {
        guard let path = ProcessInfo.processInfo.environment["SM_REAL_OUTPUT"],
              let raw = try? String(contentsOfFile: path, encoding: .utf8)
        else { return }

        let sections = ProcParsers.splitSections(raw, want: ProcParsers.Section.allCases.count)
        #expect(sections.count == ProcParsers.Section.allCases.count)

        func section(_ which: ProcParsers.Section) -> String { sections[which.rawValue] }

        let cpu = ProcParsers.cpuPercent(first: section(.statFirst), second: section(.statSecond))
        let (memUsed, memTotal) = ProcParsers.memInfo(section(.memInfo))
        let (load1, _, _) = ProcParsers.loadAverage(section(.loadAvg))
        let (rx, tx) = ProcParsers.netDev(section(.netDev))
        let (dRead, dWrite) = ProcParsers.diskStats(section(.diskStats))
        let uptime = ProcParsers.uptime(section(.uptime))
        let cores = ProcParsers.cores(section(.nproc))
        let (diskUsed, diskTotal) = ProcParsers.diskUsage(section(.diskUsage))

        // Sanity ranges: a live host must produce plausible values, not zeros.
        #expect(cpu >= 0 && cpu <= 100, "cpu=\(cpu)")
        #expect(memTotal > 0, "memTotal=\(memTotal)")
        #expect(memUsed > 0 && memUsed <= memTotal, "memUsed=\(memUsed)/\(memTotal)")
        #expect(load1 >= 0, "load1=\(load1)")
        #expect(rx > 0 && tx > 0, "net rx=\(rx) tx=\(tx)")
        #expect(dRead >= 0 && dWrite >= 0, "disk r=\(dRead) w=\(dWrite)")
        #expect(uptime > 0, "uptime=\(uptime)")
        #expect(cores > 0, "cores=\(cores)")
        #expect(diskTotal > 0 && diskUsed <= diskTotal, "disk=\(diskUsed)/\(diskTotal)")

        print("""

        ── parsed from real host ──
        cores      \(cores)
        cpu        \(Format.percent(cpu))
        load1      \(Format.load(load1))
        memory     \(Format.usage(used: memUsed, total: memTotal))
        disk       \(Format.usage(used: diskUsed, total: diskTotal))
        net total  rx \(Format.bytes(rx))  tx \(Format.bytes(tx))
        disk io    r \(Format.bytes(dRead))  w \(Format.bytes(dWrite))
        uptime     \(Format.uptime(uptime, chinese: false))
        docker     \(ProcParsers.dockerVersion(section(.docker)).isEmpty ? "(none)" : ProcParsers.dockerVersion(section(.docker)))
        ───────────────────────────
        """)
    }
}
