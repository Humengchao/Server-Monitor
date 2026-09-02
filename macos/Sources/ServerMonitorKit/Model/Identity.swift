import Foundation
import GRDB

/// A reusable login: the username plus how it authenticates.
///
/// Exists so a fleet sharing one key does not restate it per machine — a
/// server can point at an identity instead of carrying its own settings.
public struct Identity: Identifiable, Codable, Hashable, Sendable, FetchableRecord, PersistableRecord {
    public static let databaseTableName = "identity"

    public var id: UUID
    public var name: String
    public var username: String
    public var authKind: AuthKind
    /// Used when `authKind` is `.identityFile`.
    public var identityFile: String
    public var createdAt: Date

    public init(
        id: UUID = UUID(),
        name: String,
        username: String,
        authKind: AuthKind = .identityFile,
        identityFile: String = "",
        createdAt: Date = Date()
    ) {
        self.id = id
        self.name = name
        self.username = username
        self.authKind = authKind
        self.identityFile = identityFile
        self.createdAt = createdAt
    }

    public var summary: String {
        switch authKind {
        case .identityFile:
            return identityFile.isEmpty ? username : "\(username) · \((identityFile as NSString).lastPathComponent)"
        case .agent:
            return "\(username) · agent"
        case .sshConfigAlias, .password:
            return username
        }
    }
}
