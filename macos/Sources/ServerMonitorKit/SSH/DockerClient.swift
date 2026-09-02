import Foundation

/// Engine-wide counts for one host, as shown on the Docker overview cards.
public struct DockerSummary: Sendable, Hashable {
    public let engineVersion: String
    public let images: Int
    public let running: Int
    public let stopped: Int
    /// Paused containers are neither running nor stopped; without this they are
    /// simply missing from the card, and the parts stop summing to the total.
    public var paused: Int = 0

    public init(engineVersion: String, images: Int, running: Int, stopped: Int, paused: Int = 0) {
        self.engineVersion = engineVersion
        self.images = images
        self.running = running
        self.stopped = stopped
        self.paused = paused
    }

    public var total: Int { running + stopped + paused }
}

/// One container as reported by `docker ps`.
public struct DockerContainer: Identifiable, Sendable, Hashable {
    public let id: String
    public let name: String
    public let image: String
    public let state: String
    public let status: String

    public var isRunning: Bool { state == "running" }

    /// `docker ps --no-trunc` gives a 64-character id, but `docker stats`
    /// prints the 12-character form, so the two only join on this.
    public var shortID: String { String(id.prefix(12)) }
}

/// One image as reported by `docker images`.
public struct DockerImage: Identifiable, Sendable, Hashable {
    public let id: String
    public let repository: String
    public let tag: String
    /// Human-readable, as the engine prints it ("1.24GB"): the format verb has
    /// no byte-count equivalent, and re-deriving one would cost another call.
    public let size: String
    public let created: String

    /// `<none>:<none>` is a dangling layer left by a rebuild.
    public var isDangling: Bool { repository == "<none>" }

    public var displayName: String {
        isDangling ? String(id.prefix(12)) : "\(repository):\(tag)"
    }
}

/// One volume as reported by `docker volume ls`.
public struct DockerVolume: Identifiable, Sendable, Hashable {
    public let name: String
    public let driver: String
    public let mountpoint: String
    public var id: String { name }
}

/// One network as reported by `docker network ls`.
public struct DockerNetwork: Identifiable, Sendable, Hashable {
    public let id: String
    public let name: String
    public let driver: String
    public let scope: String

    /// The three networks the engine creates itself and that cannot be removed.
    public var isBuiltIn: Bool { ["bridge", "host", "none"].contains(name) }
}

/// One Compose project as `docker compose ls` reports it.
public struct DockerComposeProject: Identifiable, Sendable, Hashable {
    public let name: String
    /// The engine's own summary, e.g. "running(2)" or "exited(2), running(1)".
    public let status: String
    public let configFiles: String
    public var id: String { name }

    public init(name: String, status: String, configFiles: String) {
        self.name = name
        self.status = status
        self.configFiles = configFiles
    }

    /// Counts pulled out of `status`, so the row can show them separately.
    public var counts: [(state: String, count: Int)] {
        status.components(separatedBy: ",").compactMap { part in
            let text = part.trimmingCharacters(in: .whitespaces)
            guard let open = text.firstIndex(of: "("), text.hasSuffix(")") else { return nil }
            let state = String(text[text.startIndex..<open])
            let number = text[text.index(after: open)..<text.index(before: text.endIndex)]
            guard let count = Int(number), !state.isEmpty else { return nil }
            return (state, count)
        }
    }

    public var runningCount: Int {
        counts.first { $0.state == "running" }?.count ?? 0
    }

    public var isRunning: Bool { runningCount > 0 }

    /// The directory the compose file lives in — more useful in a list than the
    /// full path to a file that is always called docker-compose.yml.
    public var directory: String {
        let first = configFiles.components(separatedBy: ",").first ?? configFiles
        return (first as NSString).deletingLastPathComponent
    }
}

/// Live resource use for one container, from `docker stats --no-stream`.
public struct DockerContainerStats: Sendable, Hashable {
    public let cpuPercent: Double
    public let memoryPercent: Double
    /// As printed by the engine ("128MiB / 2GiB").
    public let memoryUsage: String
    /// Cumulative traffic since the container started, engine-formatted.
    public let netRx: String
    public let netTx: String
    /// Cumulative block device I/O since the container started.
    public let blockRead: String
    public let blockWrite: String

    public init(
        cpuPercent: Double,
        memoryPercent: Double,
        memoryUsage: String,
        netRx: String = "",
        netTx: String = "",
        blockRead: String = "",
        blockWrite: String = ""
    ) {
        self.cpuPercent = cpuPercent
        self.memoryPercent = memoryPercent
        self.memoryUsage = memoryUsage
        self.netRx = netRx
        self.netTx = netTx
        self.blockRead = blockRead
        self.blockWrite = blockWrite
    }
}

/// Docker management over the shared SSH connection.
///
/// Every call falls back to `sudo docker` when the plain command fails, which
/// covers hosts where the login user is not in the `docker` group.
public struct DockerClient: Sendable {
    private let runner: SSHRunner

    public init(runner: SSHRunner = SSHRunner()) {
        self.runner = runner
    }

    /// Field separator unlikely to appear in an image name or status string.
    private static let fieldSeparator = "\u{1F}"

    /// Engine version and container counts in a single `docker info` call,
    /// rather than one round trip per number.
    public func summary(target: SSHTarget) async throws -> DockerSummary {
        let format = "{{.ServerVersion}}|{{.Images}}|{{.ContainersRunning}}|{{.ContainersStopped}}|{{.ContainersPaused}}"
        let output = try await run(target: target, arguments: "info --format '\(format)'")
        return Self.parseSummary(output)
    }

    static func parseSummary(_ output: String) -> DockerSummary {
        let line = output
            .lines()
            .last { $0.contains("|") }
            .map(String.init) ?? ""
        let fields = line.components(separatedBy: "|")
        guard fields.count >= 4 else {
            return DockerSummary(engineVersion: "", images: 0, running: 0, stopped: 0)
        }
        func number(_ index: Int) -> Int {
            guard index < fields.count else { return 0 }
            return Int(fields[index].trimmingCharacters(in: .whitespaces)) ?? 0
        }
        return DockerSummary(
            engineVersion: fields[0].trimmingCharacters(in: .whitespacesAndNewlines),
            images: number(1),
            running: number(2),
            stopped: number(3),
            // Optional: a host still answering the older four-field format —
            // one polled before this build — must keep parsing.
            paused: number(4)
        )
    }

    public func listContainers(target: SSHTarget) async throws -> [DockerContainer] {
        let format = ["{{.ID}}", "{{.Names}}", "{{.Image}}", "{{.State}}", "{{.Status}}"]
            .joined(separator: Self.fieldSeparator)
        let output = try await run(target: target, arguments: "ps -a --no-trunc --format '\(format)'")
        return output.lines().compactMap { line in
            let fields = line.components(separatedBy: Self.fieldSeparator)
            guard fields.count >= 5 else { return nil }
            return DockerContainer(
                id: fields[0],
                name: fields[1],
                image: fields[2],
                state: fields[3],
                status: fields[4]
            )
        }
    }

    public func listImages(target: SSHTarget) async throws -> [DockerImage] {
        let format = ["{{.ID}}", "{{.Repository}}", "{{.Tag}}", "{{.Size}}", "{{.CreatedSince}}"]
            .joined(separator: Self.fieldSeparator)
        let output = try await run(target: target, arguments: "images --format '\(format)'")
        return output.lines().compactMap { line in
            let fields = line.components(separatedBy: Self.fieldSeparator)
            guard fields.count >= 5 else { return nil }
            return DockerImage(
                id: fields[0], repository: fields[1], tag: fields[2],
                size: fields[3], created: fields[4]
            )
        }
    }

    public func listVolumes(target: SSHTarget) async throws -> [DockerVolume] {
        let format = ["{{.Name}}", "{{.Driver}}", "{{.Mountpoint}}"]
            .joined(separator: Self.fieldSeparator)
        let output = try await run(target: target, arguments: "volume ls --format '\(format)'")
        return output.lines().compactMap { line in
            let fields = line.components(separatedBy: Self.fieldSeparator)
            guard fields.count >= 3, !fields[0].isEmpty else { return nil }
            return DockerVolume(name: fields[0], driver: fields[1], mountpoint: fields[2])
        }
    }

    public func listNetworks(target: SSHTarget) async throws -> [DockerNetwork] {
        let format = ["{{.ID}}", "{{.Name}}", "{{.Driver}}", "{{.Scope}}"]
            .joined(separator: Self.fieldSeparator)
        let output = try await run(target: target, arguments: "network ls --format '\(format)'")
        return output.lines().compactMap { line in
            let fields = line.components(separatedBy: Self.fieldSeparator)
            guard fields.count >= 4, !fields[1].isEmpty else { return nil }
            return DockerNetwork(id: fields[0], name: fields[1], driver: fields[2], scope: fields[3])
        }
    }

    /// Compose projects on the host, stopped ones included.
    ///
    /// Returns nothing rather than throwing when the engine has no `compose`
    /// subcommand — Compose v1 was a separate `docker-compose` binary with no
    /// `ls` at all, and a host running it should show an empty tab, not an
    /// error.
    public func listComposeProjects(target: SSHTarget) async throws -> [DockerComposeProject] {
        guard let output = try? await run(target: target, arguments: "compose ls -a --format json")
        else { return [] }
        return Self.parseComposeProjects(output)
    }

    static func parseComposeProjects(_ output: String) -> [DockerComposeProject] {
        // The engine may print a deprecation warning above the array, and that
        // warning contains a bracket of its own ("WARN[0000] …") — so the first
        // `[` in the output is not necessarily where the JSON starts. Try each
        // candidate rather than guessing.
        guard let rows = Self.firstJSONArray(in: output) else { return [] }
        return rows.compactMap { row in
            guard let name = row["Name"] as? String, !name.isEmpty else { return nil }
            return DockerComposeProject(
                name: name,
                status: row["Status"] as? String ?? "",
                configFiles: row["ConfigFiles"] as? String ?? ""
            )
        }
        .sorted { $0.name < $1.name }
    }

    /// The first substring that actually parses as a JSON array of objects.
    static func firstJSONArray(in output: String) -> [[String: Any]]? {
        guard let end = output.lastIndex(of: "]") else { return nil }
        var cursor = output.startIndex
        while let start = output[cursor...].firstIndex(of: "["), start < end {
            let candidate = String(output[start...end])
            if let rows = try? JSONSerialization.jsonObject(with: Data(candidate.utf8))
                as? [[String: Any]] {
                return rows
            }
            cursor = output.index(after: start)
        }
        return nil
    }

    /// Live stats for every running container, keyed by container id.
    ///
    /// `--no-stream` because the streaming form never exits, and this runs over
    /// a one-shot ssh invocation that reads to EOF.
    public func stats(target: SSHTarget) async throws -> [String: DockerContainerStats] {
        let format = [
            "{{.ID}}", "{{.CPUPerc}}", "{{.MemPerc}}", "{{.MemUsage}}",
            "{{.NetIO}}", "{{.BlockIO}}",
        ].joined(separator: Self.fieldSeparator)
        let output = try await run(
            target: target, arguments: "stats --no-stream --format '\(format)'"
        )
        return Self.parseStats(output)
    }

    static func parseStats(_ output: String) -> [String: DockerContainerStats] {
        var result: [String: DockerContainerStats] = [:]
        for line in output.lines() {
            let fields = line.components(separatedBy: Self.fieldSeparator)
            guard fields.count >= 4, !fields[0].isEmpty else { continue }
            let net = Self.pair(fields.count > 4 ? fields[4] : "")
            let block = Self.pair(fields.count > 5 ? fields[5] : "")
            result[fields[0]] = DockerContainerStats(
                cpuPercent: Self.percent(fields[1]),
                memoryPercent: Self.percent(fields[2]),
                memoryUsage: fields[3].trimmingCharacters(in: .whitespaces),
                netRx: net.0, netTx: net.1,
                blockRead: block.0, blockWrite: block.1
            )
        }
        return result
    }

    /// Splits the engine's "18.5GB / 53.3GB" into its two halves. Kept as the
    /// engine's own strings rather than reparsed into bytes: docker prints
    /// SI units here and re-formatting them with a 1024-based formatter would
    /// show numbers that disagree with `docker stats` itself.
    static func pair(_ text: String) -> (String, String) {
        let halves = text.components(separatedBy: "/")
        guard halves.count >= 2 else { return ("", "") }
        return (
            halves[0].trimmingCharacters(in: .whitespaces),
            halves[1].trimmingCharacters(in: .whitespaces)
        )
    }

    /// "12.34%" -> 12.34. The engine prints "--" for a container it could not
    /// sample, which becomes 0 rather than a parse failure.
    static func percent(_ text: String) -> Double {
        Double(text.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: "%", with: "")) ?? 0
    }

    public func perform(_ action: ContainerAction, containerID: String, target: SSHTarget) async throws {
        _ = try await run(target: target, arguments: "\(action.rawValue) \(shellQuoted(containerID))")
    }

    public func logs(containerID: String, tail: Int = 200, target: SSHTarget) async throws -> String {
        // stderr is where most container logs land, so fold it into stdout.
        try await run(target: target, arguments: "logs --tail \(tail) \(shellQuoted(containerID)) 2>&1")
    }

    public enum ContainerAction: String, Sendable {
        case start, stop, restart
    }

    private func run(target: SSHTarget, arguments: String) async throws -> String {
        // -n so sudo fails immediately rather than blocking on a password
        // prompt that nothing is there to answer.
        let command = "docker \(arguments) 2>/dev/null || sudo -n docker \(arguments)"
        return try await runner.run(command, on: target)
    }

    /// Container ids and names come from the host, but they still reach a shell,
    /// so quote them rather than trusting their contents.
    private func shellQuoted(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}
