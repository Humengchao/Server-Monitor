import Foundation

/// A host entry discovered in ~/.ssh/config.
public struct SSHConfigHost: Identifiable, Hashable, Sendable {
    public var id: String { alias }
    public let alias: String
    public let hostName: String
    public let user: String
    public let port: Int
    /// Expanded path of the first IdentityFile, if the entry names one.
    public let identityFile: String?

    /// True when the key file exists and can be read.
    public var hasReadableKey: Bool {
        guard let identityFile else { return false }
        return FileManager.default.isReadableFile(atPath: identityFile)
    }
}

/// Reads ~/.ssh/config so existing hosts can be adopted without retyping them.
///
/// A deliberately small subset of the format: `Host` blocks with `HostName`,
/// `User`, `Port` and `IdentityFile`. Patterns (`Host *`, wildcards) are
/// skipped because they describe defaults rather than a specific machine.
public enum SSHConfigImporter {

    public static var defaultConfigURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".ssh/config")
    }

    public static func discover(at url: URL = defaultConfigURL) -> [SSHConfigHost] {
        guard let text = try? String(contentsOf: url, encoding: .utf8) else { return [] }
        return parse(text)
    }

    static func parse(_ text: String) -> [SSHConfigHost] {
        var hosts: [SSHConfigHost] = []
        var alias: String?
        var settings: [String: String] = [:]

        func flush() {
            defer { alias = nil; settings = [:] }
            guard let alias else { return }
            // Without a HostName the alias itself is the address.
            let hostName = settings["hostname"] ?? alias
            hosts.append(SSHConfigHost(
                alias: alias,
                hostName: hostName,
                user: settings["user"] ?? "root",
                port: Int(settings["port"] ?? "") ?? 22,
                identityFile: settings["identityfile"].map(expandTilde)
            ))
        }

        for rawLine in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = rawLine.trimmingCharacters(in: .whitespaces)
            guard !line.isEmpty, !line.hasPrefix("#") else { continue }
            // Keys may be separated by whitespace or '='.
            let parts = line.split(whereSeparator: { $0 == " " || $0 == "\t" || $0 == "=" })
            guard let key = parts.first?.lowercased(), parts.count >= 2 else { continue }
            let value = parts.dropFirst().joined(separator: " ")

            if key == "host" {
                flush()
                // One Host line can list several patterns; take the first plain
                // alias and ignore wildcard-only entries.
                let candidates = value.split(separator: " ").map(String.init)
                alias = candidates.first { !$0.contains("*") && !$0.contains("?") }
            } else if alias != nil {
                settings[key] = value
            }
        }
        flush()
        return hosts
    }

    static func expandTilde(_ path: String) -> String {
        let unquoted = path.trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
        return NSString(string: unquoted).expandingTildeInPath
    }

    /// Reads a private key file. Returns nil when unreadable, so the caller can
    /// report which hosts could not be imported instead of failing the batch.
    public static func readPrivateKey(at path: String) -> String? {
        try? String(contentsOfFile: path, encoding: .utf8)
    }
}
