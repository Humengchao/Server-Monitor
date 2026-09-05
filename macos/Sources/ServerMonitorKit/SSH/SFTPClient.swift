import Foundation

/// One entry in a remote directory.
public struct RemoteFile: Identifiable, Hashable, Sendable {
    public var id: String { path }
    public let name: String
    public let path: String
    public let size: Int64
    public let modified: Date?
    public let isDirectory: Bool
    public let isSymlink: Bool
    /// Raw permission field, e.g. "drwxr-xr-x".
    public let mode: String
    public let owner: String

    public var icon: String {
        if isDirectory { return "folder" }
        if isSymlink { return "arrow.turn.up.right" }
        return "doc"
    }
}

/// Remote file browsing and transfer over the system ssh/scp clients.
///
/// Listing goes through `ls` rather than the sftp subsystem because it reuses
/// the same multiplexed connection the metric polls already hold open, so
/// opening a directory costs no new handshake.
public struct SFTPClient: Sendable {
    private let runner: SSHRunner

    public init(runner: SSHRunner = SSHRunner()) {
        self.runner = runner
    }

    public enum Failure: LocalizedError {
        case notADirectory(String)
        case transferFailed(String)

        public var errorDescription: String? {
            switch self {
            case .notADirectory(let path): return "Not a directory: \(path)"
            case .transferFailed(let message): return message
            }
        }
    }

    /// Quotes a path for the remote shell. Remote names are attacker-adjacent
    /// data — they come from the filesystem, not from us — so never interpolate
    /// them raw.
    static func quote(_ path: String) -> String {
        "'" + path.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    public func home(on target: SSHTarget) async throws -> String {
        let output = try await runner.run("printf %s \"$HOME\"", on: target)
        let path = output.trimmingCharacters(in: .whitespacesAndNewlines)
        return path.isEmpty ? "/" : path
    }

    public func list(_ path: String, on target: SSHTarget) async throws -> [RemoteFile] {
        // -A hides . and .. but keeps dotfiles; long-iso makes the timestamp
        // parseable regardless of the host's locale.
        let command = "ls -lA --time-style=long-iso -- \(Self.quote(path)) 2>/dev/null || ls -lA -- \(Self.quote(path))"
        let output = try await runner.run(command, on: target)
        return Self.parseListing(output, parent: path)
    }

    static func parseListing(_ output: String, parent: String) -> [RemoteFile] {
        var files: [RemoteFile] = []
        for rawLine in output.lines() {
            let line = String(rawLine)
            if line.hasPrefix("total ") { continue }
            let fields = line.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
            // mode links owner group size date time name…
            guard fields.count >= 8, fields[0].count >= 10 else { continue }

            let mode = fields[0]
            let isDirectory = mode.hasPrefix("d")
            let isSymlink = mode.hasPrefix("l")
            let size = Int64(fields[4]) ?? 0
            let modified = parseDate(fields[5], fields[6])

            // The name is everything after the timestamp, so spaces survive.
            var name = fields[7...].joined(separator: " ")
            if isSymlink, let arrow = name.range(of: " -> ") {
                name = String(name[..<arrow.lowerBound])
            }
            guard name != ".", name != ".." else { continue }

            let base = parent.hasSuffix("/") ? String(parent.dropLast()) : parent
            files.append(RemoteFile(
                name: name,
                path: "\(base)/\(name)",
                size: size,
                modified: modified,
                isDirectory: isDirectory,
                isSymlink: isSymlink,
                mode: mode,
                owner: fields[2]
            ))
        }
        // Directories first, then case-insensitive by name.
        return files.sorted {
            $0.isDirectory != $1.isDirectory
                ? $0.isDirectory
                : $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending
        }
    }

    static func parseDate(_ day: String, _ time: String) -> Date? {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd HH:mm"
        return formatter.date(from: "\(day) \(time)")
    }

    // MARK: - Mutations

    public func makeDirectory(_ path: String, on target: SSHTarget) async throws {
        _ = try await runner.run("mkdir -p -- \(Self.quote(path))", on: target)
    }

    public func remove(_ file: RemoteFile, on target: SSHTarget) async throws {
        // Recursive only for directories, and only ever on an explicit path.
        let command = file.isDirectory
            ? "rm -rf -- \(Self.quote(file.path))"
            : "rm -f -- \(Self.quote(file.path))"
        _ = try await runner.run(command, on: target)
    }

    public func rename(_ file: RemoteFile, to newName: String, on target: SSHTarget) async throws {
        let parent = (file.path as NSString).deletingLastPathComponent
        let destination = "\(parent)/\(newName)"
        _ = try await runner.run(
            "mv -- \(Self.quote(file.path)) \(Self.quote(destination))",
            on: target
        )
    }

    // MARK: - Transfer

    /// Progress of one transfer, 0–1, or nil while the size is unknown.
    public typealias ProgressHandler = @Sendable (Double) -> Void

    /// Watches a growing file and reports progress.
    ///
    /// scp only draws its progress bar when attached to a TTY, and parsing that
    /// bar would be fragile. Sampling the destination size is both simpler and
    /// accurate enough for a UI that updates a few times a second.
    private func trackLocalProgress(
        url: URL,
        total: Int64,
        onProgress: @escaping ProgressHandler
    ) -> Task<Void, Never> {
        Task {
            guard total > 0 else { return }
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: .milliseconds(250))
                } catch {
                    return
                }
                guard !Task.isCancelled else { return }
                let attributes = try? FileManager.default.attributesOfItem(atPath: url.path)
                let written = (attributes?[.size] as? Int64) ?? 0
                onProgress(min(1, Double(written) / Double(total)))
            }
        }
    }

    /// Copies a remote file to the Mac with scp, reusing the master connection.
    public func download(
        _ file: RemoteFile,
        to localURL: URL,
        on target: SSHTarget,
        onProgress: ProgressHandler? = nil
    ) async throws {
        let tracker = onProgress.map {
            trackLocalProgress(url: localURL, total: file.size, onProgress: $0)
        }
        defer { tracker?.cancel() }
        try await downloadFile(file, to: localURL, on: target)
        onProgress?(1)
    }

    private func downloadFile(_ file: RemoteFile, to localURL: URL, on target: SSHTarget) async throws {
        var arguments = SSHRunner.baseArguments(
            for: target,
            controlPath: (try? SSHRunner.controlPath(for: target)) ?? ""
        )
        arguments.append("-r")
        arguments.append("\(target.sshDestination):\(file.path)")
        arguments.append(localURL.path)
        _ = try await SSHRunner.execute(
            executable: "/usr/bin/scp",
            arguments: arguments,
            timeout: 600
        )
    }

    public func upload(
        _ localURL: URL,
        toDirectory remoteDirectory: String,
        on target: SSHTarget,
        onProgress: ProgressHandler? = nil
    ) async throws {
        // Uploads are tracked by asking the host how much has landed, since the
        // growing file is on the far side.
        var tracker: Task<Void, Never>?
        if let onProgress {
            let attributes = try? FileManager.default.attributesOfItem(atPath: localURL.path)
            let total = (attributes?[.size] as? Int64) ?? 0
            let destination = "\(remoteDirectory)/\(localURL.lastPathComponent)"
            if total > 0 {
                tracker = Task {
                    while !Task.isCancelled {
                        do {
                            try await Task.sleep(for: .milliseconds(400))
                        } catch {
                            return
                        }
                        guard !Task.isCancelled else { return }
                        let output = try? await runner.run(
                            "stat -c %s -- \(Self.quote(destination)) 2>/dev/null || echo 0",
                            on: target
                        )
                        guard !Task.isCancelled else { return }
                        let written = Int64(output?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "") ?? 0
                        onProgress(min(1, Double(written) / Double(total)))
                    }
                }
            }
        }
        defer { tracker?.cancel() }
        try await uploadFile(localURL, toDirectory: remoteDirectory, on: target)
        onProgress?(1)
    }

    private func uploadFile(_ localURL: URL, toDirectory remoteDirectory: String, on target: SSHTarget) async throws {
        var arguments = SSHRunner.baseArguments(
            for: target,
            controlPath: (try? SSHRunner.controlPath(for: target)) ?? ""
        )
        arguments.append("-r")
        arguments.append(localURL.path)
        arguments.append("\(target.sshDestination):\(remoteDirectory)")
        _ = try await SSHRunner.execute(
            executable: "/usr/bin/scp",
            arguments: arguments,
            timeout: 600
        )
    }
}
