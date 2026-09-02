import AppKit
import Foundation

/// Creates and imports private keys, always into `~/.ssh`.
///
/// Writing to the directory OpenSSH already owns — rather than a private store
/// inside the app — means a key made here works immediately in Terminal, in
/// `~/.ssh/config`, and for every other tool on the machine.
public enum SSHKeyManager {

    public enum Failure: LocalizedError {
        case invalidName
        case alreadyExists(String)
        case notAPrivateKey
        case generationFailed(String)

        public var errorDescription: String? {
            switch self {
            case .invalidName:
                return "The key name may only contain letters, digits, dot, dash and underscore."
            case .alreadyExists(let name):
                return "~/.ssh/\(name) already exists."
            case .notAPrivateKey:
                return "That text is not an OpenSSH private key."
            case .generationFailed(let message):
                return message.isEmpty ? "ssh-keygen failed." : message
            }
        }
    }

    public enum KeyType: String, CaseIterable, Identifiable, Sendable {
        case ed25519
        case rsa4096

        public var id: String { rawValue }

        public var label: String {
            switch self {
            case .ed25519: return "Ed25519"
            case .rsa4096: return "RSA 4096"
            }
        }

        /// Arguments for ssh-keygen.
        var arguments: [String] {
            switch self {
            case .ed25519: return ["-t", "ed25519"]
            case .rsa4096: return ["-t", "rsa", "-b", "4096"]
            }
        }
    }

    /// A file name safe to place in ~/.ssh and to pass to a shell.
    static func isValidName(_ name: String) -> Bool {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty, trimmed.count <= 64 else { return false }
        guard !trimmed.hasPrefix("."), !trimmed.hasSuffix(".pub") else { return false }
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "._-"))
        return trimmed.unicodeScalars.allSatisfy(allowed.contains)
    }

    static func destination(for name: String) throws -> URL {
        guard isValidName(name) else { throw Failure.invalidName }
        let url = SSHKeyScanner.sshDirectory.appendingPathComponent(name)
        if FileManager.default.fileExists(atPath: url.path) {
            throw Failure.alreadyExists(name)
        }
        return url
    }

    /// Generates a key pair with ssh-keygen.
    ///
    /// An empty passphrase is passed explicitly with `-N ""` so ssh-keygen
    /// never drops into its interactive prompt, which a GUI app cannot answer.
    public static func generate(
        name: String,
        type: KeyType,
        comment: String,
        passphrase: String
    ) async throws -> String {
        let url = try destination(for: name)
        try ensureSSHDirectory()

        var arguments = type.arguments
        arguments += ["-f", url.path, "-N", passphrase, "-q"]
        if !comment.trimmingCharacters(in: .whitespaces).isEmpty {
            arguments += ["-C", comment.trimmingCharacters(in: .whitespaces)]
        }

        do {
            _ = try await SSHRunner.execute(
                executable: "/usr/bin/ssh-keygen",
                arguments: arguments,
                timeout: 60
            )
        } catch {
            throw Failure.generationFailed(error.localizedDescription)
        }
        guard FileManager.default.fileExists(atPath: url.path) else {
            throw Failure.generationFailed("")
        }
        try restrictPermissions(at: url)
        return url.path
    }

    /// Saves pasted or loaded key text as `~/.ssh/<name>`.
    public static func importKey(text: String, name: String) throws -> String {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard looksLikePrivateKey(trimmed) else { throw Failure.notAPrivateKey }
        let url = try destination(for: name)
        try ensureSSHDirectory()
        // Written with 0600 from the start rather than fixed afterwards, so the
        // key is never briefly readable by other users.
        let data = Data((trimmed + "\n").utf8)
        try data.write(to: url, options: [.atomic])
        try restrictPermissions(at: url)
        return url.path
    }

    /// Copies an existing key file into ~/.ssh, keeping its public half if present.
    public static func importFile(at source: URL, name: String? = nil) throws -> String {
        let text = try String(contentsOf: source, encoding: .utf8)
        let target = name ?? source.lastPathComponent
        let path = try importKey(text: text, name: target)

        let publicSource = source.appendingPathExtension("pub")
        if FileManager.default.fileExists(atPath: publicSource.path) {
            let publicTarget = URL(fileURLWithPath: path + ".pub")
            try? FileManager.default.removeItem(at: publicTarget)
            try? FileManager.default.copyItem(at: publicSource, to: publicTarget)
        }
        return path
    }

    static func looksLikePrivateKey(_ text: String) -> Bool {
        text.contains("-----BEGIN") && text.contains("PRIVATE KEY-----")
    }

    static func ensureSSHDirectory() throws {
        let directory = SSHKeyScanner.sshDirectory
        if !FileManager.default.fileExists(atPath: directory.path) {
            try FileManager.default.createDirectory(
                at: directory,
                withIntermediateDirectories: true,
                attributes: [.posixPermissions: 0o700]
            )
        }
    }

    /// ssh refuses to use a key others can read, so 0600 is not cosmetic.
    static func restrictPermissions(at url: URL) throws {
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }

    /// Deletes a key and its public half.
    public static func delete(_ key: SSHKeyFile) throws {
        try FileManager.default.removeItem(atPath: key.path)
        let publicPath = key.path + ".pub"
        if FileManager.default.fileExists(atPath: publicPath) {
            try? FileManager.default.removeItem(atPath: publicPath)
        }
    }
}
