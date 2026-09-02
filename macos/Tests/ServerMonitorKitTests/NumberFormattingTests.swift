import Foundation
import Testing
@testable import ServerMonitorKit

/// `Text("\(someInt)")` is not what it looks like.
///
/// That argument is a `LocalizedStringKey`, and interpolating a number into
/// one runs it through the locale's number format — so a PID rendered as
/// 1,234, a port as 2,222, and an RSA key as 4,096 bits. This app does all of
/// its own localization through `loc.t`, so a `LocalizedStringKey` is never
/// what is wanted here; `Text(verbatim:)` is.
///
/// The bug is invisible until a value crosses a thousand, which is why it
/// keeps coming back. This reads the sources instead of waiting for it.
@Suite("Number formatting")
struct NumberFormattingTests {

    @Test func noViewInterpolatesIntoALocalizedStringKey() throws {
        let sources = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()      // ServerMonitorKitTests
            .deletingLastPathComponent()      // Tests
            .deletingLastPathComponent()      // macos
            .appendingPathComponent("Sources/ServerMonitorKit")
        let files = try #require(
            FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil)
        )
        // Text(" …\( or Label("…\( — a string literal containing an
        // interpolation, passed where a LocalizedStringKey is expected.
        let pattern = try NSRegularExpression(pattern: #"\b(?:Text|Label)\(\s*"[^"\n]*\\\("#)

        var offenders: [String] = []
        for case let url as URL in files where url.pathExtension == "swift" {
            let text = try String(contentsOf: url, encoding: .utf8)
            for (number, raw) in text.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
                // A Substring's indices belong to its parent, so scan a copy.
                let line = String(raw)
                // A comment explaining the trap is not an instance of it.
                let code = line.trimmingCharacters(in: .whitespaces)
                if code.hasPrefix("//") { continue }
                let range = NSRange(line.startIndex..., in: line)
                if pattern.firstMatch(in: line, range: range) != nil {
                    offenders.append("\(url.lastPathComponent):\(number + 1) — \(code)")
                }
            }
        }
        #expect(
            offenders.isEmpty,
            "use Text(verbatim:) instead:\n\(offenders.joined(separator: "\n"))"
        )
    }
}
