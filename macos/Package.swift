// swift-tools-version:5.9
import PackageDescription

// Language mode 5 on purpose: the app is single-window and drives all UI state
// from the main actor, so Swift 6's strict concurrency checking would only add
// annotation noise around the AppKit and GRDB types that are not Sendable.
let package = Package(
    name: "ServerMonitor",
    // 15.0 rather than 14.0: Citadel's interactive PTY API is macOS 15+.
    // Spelled as a string because `.v15` needs swift-tools-version 6.
    platforms: [.macOS("15.0")],
    dependencies: [
        // Pure-Swift SSH (SwiftNIO). Keeps the app self-contained: no libssh2,
        // no shelling out to /usr/bin/ssh, and password auth works directly.
        .package(url: "https://github.com/orlandos-nl/Citadel.git", from: "0.7.0"),
        // Terminal emulator with an AppKit view, used for the SSH console.
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", from: "1.2.0"),
        // SQLite persistence. SwiftData is not an option here: its @Model
        // macro needs the compiler plugin that ships with full Xcode, and
        // this project builds with the Command Line Tools alone.
        .package(url: "https://github.com/groue/GRDB.swift.git", from: "6.29.0"),
    ],
    targets: [
        .target(
            name: "ServerMonitorKit",
            dependencies: ["Citadel", "SwiftTerm", .product(name: "GRDB", package: "GRDB.swift")],
            path: "Sources/ServerMonitorKit"
        ),
        .executableTarget(
            name: "ServerMonitor",
            dependencies: ["ServerMonitorKit"],
            path: "Sources/ServerMonitor"
        ),
        .testTarget(
            name: "ServerMonitorKitTests",
            dependencies: ["ServerMonitorKit"],
            path: "Tests/ServerMonitorKitTests"
        ),
    ]
)
