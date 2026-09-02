import Foundation
import Testing
@testable import ServerMonitorKit

/// Every `loc.t("…")` in the sources must name a key in the table.
///
/// `t()` traps on an unknown key in debug builds and returns the raw key in
/// release ones, so a typo shows up either as a crash or as "card.xyz" on
/// screen — and only when that view is reached. This finds it at test time by
/// reading the source files themselves.
@Suite("Localization keys")
struct LocalizationKeysTests {

    @Test func everyKeyUsedInSourceExistsInTheTable() throws {
        let sources = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()      // ServerMonitorKitTests
            .deletingLastPathComponent()      // Tests
            .deletingLastPathComponent()      // macos
            .appendingPathComponent("Sources/ServerMonitorKit")
        let files = try #require(
            FileManager.default.enumerator(at: sources, includingPropertiesForKeys: nil)
        )
        let pattern = try NSRegularExpression(pattern: #"\bt\(\s*"([a-zA-Z0-9_.]+)""#)
        var used: [String: [String]] = [:]
        for case let url as URL in files where url.pathExtension == "swift" {
            let text = try String(contentsOf: url, encoding: .utf8)
            let range = NSRange(text.startIndex..., in: text)
            for match in pattern.matches(in: text, range: range) {
                let key = String(text[Range(match.range(at: 1), in: text)!])
                used[key, default: []].append(url.lastPathComponent)
            }
        }
        #expect(used.count > 100, "found only \(used.count) keys — the scan is probably looking in the wrong place")

        let known = Set(Localization.knownKeys)
        let missing = used.keys.filter { !known.contains($0) }.sorted()
        #expect(missing.isEmpty, "keys used but not in the table: \(missing.map { "\($0) (\(used[$0]!.first!))" })")
    }
}
