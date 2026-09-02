import Foundation
import GRDB
import SwiftUI

/// A named grouping of machines, e.g. "生产" or a customer's fleet.
public struct MachineGroup: Identifiable, Codable, Hashable, Sendable, FetchableRecord, PersistableRecord {
    public static let databaseTableName = "machineGroup"

    public var id: UUID
    public var name: String
    /// Stored as a name from `MachineGroup.palette` rather than a hex string,
    /// so the colour keeps meaning across light and dark appearance.
    public var colorName: String
    public var sortIndex: Int
    public var createdAt: Date

    public init(
        id: UUID = UUID(),
        name: String,
        colorName: String = "blue",
        sortIndex: Int = 0,
        createdAt: Date = Date()
    ) {
        self.id = id
        self.name = name
        self.colorName = colorName
        self.sortIndex = sortIndex
        self.createdAt = createdAt
    }

    public static let palette: [String] = ["blue", "green", "orange", "purple", "pink", "teal", "red", "gray"]

    public var color: Color {
        switch colorName {
        case "green": return .green
        case "orange": return .orange
        case "purple": return .purple
        case "pink": return .pink
        case "teal": return .teal
        case "red": return .red
        case "gray": return .gray
        default: return .blue
        }
    }

    public static func color(named name: String) -> Color {
        MachineGroup(name: "", colorName: name).color
    }
}
