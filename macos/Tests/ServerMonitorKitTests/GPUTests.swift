import Foundation
import Testing
@testable import ServerMonitorKit

@Suite("GPU collection")
struct GPUTests {

    /// Two cards on one box: a datacentre part with no fan, and a consumer card
    /// that reports everything. `nounits` means bare numbers and `[N/A]`.
    static let twoCards = """
    driver=550.90.07
    cuda=12.4
    gpu=0, NVIDIA A100-SXM4-40GB, 87, 40960, 32768, 61, [N/A], 245.31, 400.00
    gpu=1, NVIDIA GeForce RTX 4090, 12, 24564, 1024, 44, 31, 55.12, 450.00
    proc=3241, /usr/bin/python3, 30720
    proc=8899, ollama, 900
    """

    @Test func parsesBothCardsInIndexOrder() {
        let status = ProcParsers.gpuStatus(Self.twoCards)
        #expect(status.isPresent)
        #expect(status.driverVersion == "550.90.07")
        #expect(status.cudaVersion == "12.4")
        #expect(status.gpus.map(\.index) == [0, 1])
        #expect(status.gpus[0].name == "NVIDIA A100-SXM4-40GB")
        #expect(status.gpus[0].utilizationPercent == 87)
        #expect(status.gpus[0].memoryTotal == Int64(40960) * 1024 * 1024)
        #expect(abs(status.gpus[0].memoryPercent - 80) < 0.01)
    }

    @Test func notAvailableStaysNilRatherThanBecomingZero() {
        // The A100 has no fan. Reporting 0% would read as "fan stopped".
        let status = ProcParsers.gpuStatus(Self.twoCards)
        #expect(status.gpus[0].fanPercent == nil)
        #expect(status.gpus[0].temperatureC == 61)
        #expect(status.gpus[1].fanPercent == 31)
        #expect(status.gpus[0].powerDrawW == 245.31)
        #expect(status.gpus[0].powerLimitW == 400.00)
    }

    @Test func computeProcessesAreListed() {
        let status = ProcParsers.gpuStatus(Self.twoCards)
        #expect(status.processes.map(\.pid) == [3241, 8899])
        #expect(status.processes[0].name == "/usr/bin/python3")
        #expect(status.processes[0].memoryUsed == Int64(30720) * 1024 * 1024)
    }

    @Test func aHostWithoutNvidiaSmiReportsNothing() {
        // The command short-circuits to empty output; the card is then omitted
        // rather than shown with zeroes.
        for output in ["", "\n", "bash: nvidia-smi: command not found"] {
            let status = ProcParsers.gpuStatus(output)
            #expect(status.isPresent == false)
            #expect(status.gpus.isEmpty)
        }
    }

    @Test func aTruncatedRowIsDroppedNotHalfParsed() {
        // A card that answered only part of the query would otherwise become a
        // GPU with 0 memory and a blank name.
        #expect(ProcParsers.parseGPU("0, NVIDIA A100") == nil)
        #expect(ProcParsers.parseGPU("notanindex, x, 1, 2, 3") == nil)
    }

    @Test func aNameContainingACommaWouldSplitWrong() {
        // Documents a real constraint: the query is comma-separated with no
        // quoting, so a name with a comma shifts every later field. No shipping
        // NVIDIA product name contains one, which is why this is acceptable —
        // but the row must still not produce nonsense numbers.
        let gpu = ProcParsers.parseGPU("0, Weird, Name, 10, 20, 30")
        #expect(gpu?.name == "Weird")
        #expect(gpu?.utilizationPercent == 0, "\"Name\" is not a number, so it reads as 0")
    }
}
