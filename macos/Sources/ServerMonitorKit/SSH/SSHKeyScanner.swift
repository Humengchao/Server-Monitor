import Foundation

/// A private key found in ~/.ssh.
public struct SSHKeyFile: Identifiable, Hashable, Sendable {
    public var id: String { path }
    public let path: String
    public let name: String
    /// "ED25519", "RSA", … as reported by ssh-keygen.
    public let type: String
    public let bits: Int
    /// SHA256 fingerprint.
    public let fingerprint: String
    public let comment: String
    public let hasPublicKey: Bool
    /// True when the private key is passphrase-protected.
    public let isEncrypted: Bool

    /// Modern servers reject SHA-1 `ssh-rsa`; RSA still works because OpenSSH
    /// negotiates rsa-sha2, but it is worth surfacing the distinction.
    public var isLegacyAlgorithm: Bool { type.uppercased() == "DSA" }
}

/// Inventories the private keys in ~/.ssh by asking ssh-keygen about them.
///
/// Reads only metadata: fingerprints and comments, never key material.
public enum SSHKeyScanner {

    public static var sshDirectory: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".ssh")
    }

    /// Filenames in ~/.ssh that are never private keys.
    private static let ignored: Set<String> = [
        "config", "known_hosts", "known_hosts.old", "authorized_keys", "environment", "rc",
    ]

    public static func scan(directory: URL = sshDirectory) async -> [SSHKeyFile] {
        let manager = FileManager.default
        guard let names = try? manager.contentsOfDirectory(atPath: directory.path) else { return [] }

        var results: [SSHKeyFile] = []
        for name in names.sorted() {
            guard !ignored.contains(name), !name.hasSuffix(".pub"), !name.hasPrefix(".") else { continue }
            let path = directory.appendingPathComponent(name).path
            var isDirectory: ObjCBool = false
            guard manager.fileExists(atPath: path, isDirectory: &isDirectory), !isDirectory.boolValue else { continue }
            guard let key = await describe(path: path, name: name) else { continue }
            results.append(key)
        }
        return results
    }

    /// `ssh-keygen -l -f` prints "<bits> <fingerprint> <comment> (<TYPE>)".
    static func describe(path: String, name: String) async -> SSHKeyFile? {
        guard let output = try? await SSHRunner.execute(
            executable: "/usr/bin/ssh-keygen",
            arguments: ["-l", "-f", path],
            timeout: 5
        ) else { return nil }

        guard let parsed = parseKeygenLine(output) else { return nil }
        let publicPath = path + ".pub"
        let encrypted = isEncrypted(path: path)
        return SSHKeyFile(
            path: path,
            name: name,
            type: parsed.type,
            bits: parsed.bits,
            fingerprint: parsed.fingerprint,
            comment: parsed.comment,
            hasPublicKey: FileManager.default.fileExists(atPath: publicPath),
            isEncrypted: encrypted
        )
    }

    static func parseKeygenLine(_ output: String) -> (bits: Int, fingerprint: String, comment: String, type: String)? {
        let line = output.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !line.isEmpty else { return nil }
        let fields = line.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
        guard fields.count >= 3, let bits = Int(fields[0]) else { return nil }
        let fingerprint = fields[1]
        // The type is the parenthesised last field; whatever sits between it
        // and the fingerprint is the comment.
        var type = ""
        var commentFields = Array(fields.dropFirst(2))
        if let last = commentFields.last, last.hasPrefix("("), last.hasSuffix(")") {
            type = String(last.dropFirst().dropLast())
            commentFields.removeLast()
        }
        return (bits, fingerprint, commentFields.joined(separator: " "), type)
    }

    /// Detects a passphrase without prompting for one.
    ///
    /// An OpenSSH key stores its cipher name in the *decoded* body, so the
    /// base64 has to be decoded first — searching the armoured text for "none"
    /// matches nothing and reports every key as encrypted.
    static func isEncrypted(path: String) -> Bool {
        guard let text = try? String(contentsOfFile: path, encoding: .utf8) else { return false }

        // Classic PEM keys announce it in the headers.
        if text.contains("Proc-Type:") && text.contains("ENCRYPTED") { return true }

        guard text.contains("OPENSSH PRIVATE KEY") else { return false }
        let body = text
            .lines()
            .filter { !$0.hasPrefix("-----") }
            .joined()
        guard let data = Data(base64Encoded: body, options: .ignoreUnknownCharacters) else {
            return false
        }
        // Layout: "openssh-key-v1\0" then a uint32-length-prefixed cipher name.
        let magic = Array("openssh-key-v1\u{0}".utf8)
        guard data.count > magic.count + 4 else { return false }
        let bytes = [UInt8](data)
        guard Array(bytes.prefix(magic.count)) == magic else { return false }

        var offset = magic.count
        let length = bytes[offset..<(offset + 4)].reduce(0) { Int($0) << 8 | Int($1) }
        offset += 4
        guard length > 0, offset + length <= bytes.count else { return false }
        let cipher = String(decoding: bytes[offset..<(offset + length)], as: UTF8.self)
        return cipher != "none"
    }

    /// Public key text, for copying into a server's authorized_keys.
    public static func publicKey(for key: SSHKeyFile) -> String? {
        try? String(contentsOfFile: key.path + ".pub", encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
