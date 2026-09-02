import Foundation
import GRDB

/// A saved command, runnable against any host or pasted into a terminal.
public struct Snippet: Identifiable, Codable, Hashable, Sendable, FetchableRecord, PersistableRecord {
    public static let databaseTableName = "snippet"

    public var id: UUID
    public var name: String
    public var command: String
    public var notes: String
    /// Free-form grouping label, e.g. "nginx" or "诊断".
    public var category: String
    public var createdAt: Date
    /// Bumped on each run so the list can surface what actually gets used.
    public var useCount: Int
    public var lastUsedAt: Date?

    public init(
        id: UUID = UUID(),
        name: String,
        command: String,
        notes: String = "",
        category: String = "",
        createdAt: Date = Date(),
        useCount: Int = 0,
        lastUsedAt: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.command = command
        self.notes = notes
        self.category = category
        self.createdAt = createdAt
        self.useCount = useCount
        self.lastUsedAt = lastUsedAt
    }

    /// First line, for the collapsed row in the list.
    public var summary: String {
        command.lines().first.map(String.init) ?? command
    }
}
