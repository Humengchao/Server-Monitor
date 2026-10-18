import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("Shared probe contract")
struct SharedProbeTests {
    @Test func sectionsKeepTheirWireOrderWithOptionalWorkDisabled() {
        #expect(SharedProbes.linuxSections.count == ProcParsers.Section.allCases.count)
        let sections = ProcParsers.linuxMetricsCommand(processes: false, docker: false)
            .components(separatedBy: "; echo \(ProcParsers.sectionSeparator); ")
        #expect(sections.count == ProcParsers.Section.allCases.count)
        #expect(sections[ProcParsers.Section.processes.rawValue] == "true")
        #expect(sections[ProcParsers.Section.docker.rawValue] == "true")
        #expect(sections[ProcParsers.Section.statFirst.rawValue] == "cat /proc/stat")
        #expect(!sections.joined().contains("%DF_EXCLUDE%"))
        #expect(WindowsMetrics.script == SharedProbes.windowsMetricsScript)
    }
}
