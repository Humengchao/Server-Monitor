import Foundation

/// Everything needed to reach one host with the system ssh client.
public struct SSHTarget: Sendable, Hashable {
    public let serverID: UUID
    /// Address, or the ~/.ssh/config alias when `credential` is `.sshConfigAlias`.
    public let host: String
    public let port: Int
    public let username: String
    public let credential: SSHCredential

    public init(serverID: UUID, host: String, port: Int, username: String, credential: SSHCredential) {
        self.serverID = serverID
        self.host = host
        self.port = port
        self.username = username
        self.credential = credential
    }

    /// What to hand ssh as its destination argument.
    ///
    /// For an alias this is the alias alone, so OpenSSH applies the whole
    /// matching Host block — HostName, User, Port, IdentityFile, ProxyJump and
    /// anything else the user configured.
    public var sshDestination: String {
        if case .sshConfigAlias = credential { return host }
        return "\(username)@\(host)"
    }
}

/// How the connection authenticates. All three defer to OpenSSH rather than
/// holding key material: nothing here copies a private key out of ~/.ssh.
public enum SSHCredential: Sendable, Hashable {
    /// Use a Host block from ~/.ssh/config verbatim.
    case sshConfigAlias
    /// An explicit private key file, passed as `ssh -i`.
    case identityFile(path: String)
    /// Whatever the ssh agent offers.
    case agent
    /// A password, fetched from the keychain at connect time.
    ///
    /// Carries no payload on purpose: the secret then cannot end up in a log
    /// line, a crash report, or anything that prints an `SSHTarget`.
    case password
}

public enum SSHError: LocalizedError {
    case missingCredential
    case commandFailed(String)

    public var errorDescription: String? {
        switch self {
        case .missingCredential:
            return "This server has no usable SSH credential."
        case .commandFailed(let message):
            return message
        }
    }
}
