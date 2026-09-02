import Foundation
import SwiftUI

/// A live terminal or SFTP session.
public struct ActiveSession: Identifiable, Hashable, Sendable {
    public let id: UUID
    public let serverID: UUID
    public let serverName: String
    public let kind: SessionRecord.Kind
    public let startedAt: Date
    /// Set for a session opened into a container rather than the host shell.
    public let label: String?

    public var title: String { label ?? serverName }
}

/// Tracks open sessions and writes them to history when they close.
///
/// Sessions live here rather than inside their views so the sidebar can list
/// them, and so closing the window still records an end time.
@MainActor
public final class SessionManager: ObservableObject {
    @Published public private(set) var active: [ActiveSession] = []
    /// The session just opened, so the window can navigate to it. Opening a
    /// session and then having to find it in the sidebar is busywork.
    @Published public private(set) var lastOpened: UUID?

    private let database: Database

    public init(database: Database) {
        self.database = database
        // A crash leaves rows with no end time; close them so history is clean.
        try? database.closeDanglingSessions()
    }

    @discardableResult
    public func open(
        server: Server,
        kind: SessionRecord.Kind,
        label: String? = nil
    ) -> ActiveSession {
        let session = ActiveSession(
            id: UUID(),
            serverID: server.id,
            serverName: server.name,
            kind: kind,
            startedAt: Date(),
            label: label
        )
        active.append(session)
        lastOpened = session.id
        try? database.save(SessionRecord(
            id: session.id,
            serverID: server.id,
            serverName: server.name,
            kind: kind,
            startedAt: session.startedAt
        ))
        return session
    }

    public func close(_ id: UUID) {
        guard let index = active.firstIndex(where: { $0.id == id }) else { return }
        let session = active.remove(at: index)
        try? database.save(SessionRecord(
            id: session.id,
            serverID: session.serverID,
            serverName: session.serverName,
            kind: session.kind,
            startedAt: session.startedAt,
            endedAt: Date()
        ))
    }

    public func sessions(kind: SessionRecord.Kind) -> [ActiveSession] {
        active.filter { $0.kind == kind }
    }

    public func history(limit: Int = 200) -> [SessionRecord] {
        (try? database.recentSessions(limit: limit)) ?? []
    }

    public func clearHistory() {
        try? database.clearSessionHistory()
    }
}
