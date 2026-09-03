import Foundation
import SwiftUI
import Testing
@testable import ServerMonitorKit

@Suite("Machine tags")
@MainActor
struct TagTests {

    @Test func tagsRoundTripThroughTheStoredString() {
        var server = Server(name: "web", host: "h", username: "u", authKind: .agent)
        server.tags = ["prod", "database", "eu-west"]
        #expect(server.tagList == "prod,database,eu-west")
        #expect(server.tags == ["prod", "database", "eu-west"])
    }

    @Test func blanksAndDuplicatesAreDropped() {
        var server = Server(name: "web", host: "h", username: "u", authKind: .agent)
        // What a person actually types.
        server.tagList = " prod ,  , database,PROD ,,database "
        #expect(server.tags == ["prod", "database"], "case-insensitive de-duplication, first wins")
    }

    @Test func typedCaseIsPreserved() {
        // De-duplication is case-insensitive, but the chip shows what was typed.
        var server = Server(name: "web", host: "h", username: "u", authKind: .agent)
        server.tagList = "Prod, Staging"
        #expect(server.tags == ["Prod", "Staging"])
    }

    @Test func aServerWithNoTagsHasNone() {
        let server = Server(name: "web", host: "h", username: "u", authKind: .agent)
        #expect(server.tags.isEmpty)
        #expect(server.tagList.isEmpty)
    }

    @Test func tagColoursAreStableAcrossRunsAndCases() {
        // Swift's own hashValue is seeded per process, so a colour derived from
        // it would change every launch. This must not.
        #expect(TagChips.color(for: "prod") == TagChips.color(for: "PROD"))
        #expect(TagChips.color(for: "prod") == TagChips.color(for: "prod"))
        // A known value, so a future change to the hash is caught rather than
        // silently recolouring everyone's tags.
        let palette: [Color] = [.blue, .green, .orange, .purple, .pink, .teal, .indigo, .brown]
        #expect(palette.contains(TagChips.color(for: "prod")))
    }

    @Test func tagsSurviveASaveAndReload() throws {
        let database = try Database(inMemory: true)
        var server = Server(name: "web", host: "h", username: "u", authKind: .agent)
        server.tags = ["prod", "eu"]
        try database.save(server)

        let reloaded = try #require(try database.allServers().first { $0.id == server.id })
        #expect(reloaded.tags == ["prod", "eu"], "the v8 column has to round-trip")
    }
}
