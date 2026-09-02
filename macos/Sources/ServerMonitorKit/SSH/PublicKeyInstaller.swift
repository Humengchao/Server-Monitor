import Foundation

/// Appends a public key to a host's `authorized_keys`, the way `ssh-copy-id`
/// does — but over the connection the app already has, so it works for hosts
/// reached through a `ProxyJump` or an alias.
public struct PublicKeyInstaller: Sendable {
    private let runner: SSHRunner

    public init(runner: SSHRunner = SSHRunner()) {
        self.runner = runner
    }

    public enum Outcome: Equatable, Sendable {
        case added
        /// The key body was already there — possibly under a different comment.
        case alreadyPresent
    }

    public enum Failure: LocalizedError {
        case notAPublicKey
        case unreadableResult(String)

        public var errorDescription: String? {
            switch self {
            case .notAPublicKey:
                return "That does not look like an OpenSSH public key"
            case .unreadableResult(let output):
                return "The host did not confirm the change: \(output)"
            }
        }
    }

    public func install(publicKey: String, on target: SSHTarget) async throws -> Outcome {
        let key = publicKey.trimmingCharacters(in: .whitespacesAndNewlines)
        guard Self.isPublicKey(key) else { throw Failure.notAPublicKey }
        let output = try await runner.run(Self.command(for: key), on: target, timeout: 20)
        let text = output.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.contains("SM_ADDED") { return .added }
        if text.contains("SM_ALREADY") { return .alreadyPresent }
        throw Failure.unreadableResult(text.isEmpty ? "(no output)" : String(text.prefix(200)))
    }

    /// One line of shell, idempotent, and careful about permissions: sshd
    /// silently ignores an `authorized_keys` that is group- or world-writable,
    /// which is a maddening way for this to "work" and change nothing.
    ///
    /// The presence check is on the key *body* rather than the whole line, so
    /// re-exporting the same key under a different comment does not append a
    /// duplicate.
    static func command(for key: String) -> String {
        let quoted = shellQuoted(key)
        return """
        K=\(quoted); \
        B=$(printf '%s' "$K" | awk '{print $2}'); \
        mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && \
        chmod 600 ~/.ssh/authorized_keys && \
        if [ -n "$B" ] && grep -qF "$B" ~/.ssh/authorized_keys; then echo SM_ALREADY; \
        else printf '%s\\n' "$K" >> ~/.ssh/authorized_keys && echo SM_ADDED; fi
        """
    }

    /// Rejects a private key outright: pasting one here would copy it to a
    /// remote host, which is the opposite of what this feature is for.
    public static func isPublicKey(_ text: String) -> Bool {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.contains("PRIVATE KEY") else { return false }
        let fields = trimmed.split(separator: " ", omittingEmptySubsequences: true)
        guard fields.count >= 2 else { return false }
        let algorithms = [
            "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256",
            "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com",
            "sk-ecdsa-sha2-nistp256@openssh.com", "ssh-dss",
        ]
        guard algorithms.contains(String(fields[0])) else { return false }
        // The body is base64; a line that is one word plus junk is not a key.
        return fields[1].count > 32 && fields[1].allSatisfy {
            $0.isLetter || $0.isNumber || $0 == "+" || $0 == "/" || $0 == "="
        }
    }

    static func shellQuoted(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
