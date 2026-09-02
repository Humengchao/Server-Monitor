import Foundation
import GRDB

/// One terminal or SFTP session, kept so the sidebar can show history.
public struct SessionRecord: Identifiable, Codable, Hashable, Sendable, FetchableRecord, PersistableRecord {
    public static let databaseTableName = "sessionRecord"

    public enum Kind: String, Codable, Sendable {
        case terminal
        case sftp
    }

    public var id: UUID
    public var serverID: UUID?
    /// Denormalised so history survives the server being deleted.
    public var serverName: String
    public var kind: Kind
    public var startedAt: Date
    public var endedAt: Date?

    public init(
        id: UUID = UUID(),
        serverID: UUID?,
        serverName: String,
        kind: Kind,
        startedAt: Date = Date(),
        endedAt: Date? = nil
    ) {
        self.id = id
        self.serverID = serverID
        self.serverName = serverName
        self.kind = kind
        self.startedAt = startedAt
        self.endedAt = endedAt
    }

    public var duration: TimeInterval? {
        endedAt.map { $0.timeIntervalSince(startedAt) }
    }

    public var isOpen: Bool { endedAt == nil }
}
