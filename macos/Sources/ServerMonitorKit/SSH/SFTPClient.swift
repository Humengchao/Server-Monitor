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
/// Listing uses NUL-separated records over the pooled SSH connection. Display
/// text from `ls` cannot identify files safely: whitespace and link arrows are
/// legal parts of a filename.
public struct SFTPClient: Sendable {
    private let runner: SSHRunner

    public init(runner: SSHRunner = SSHRunner()) {
        self.runner = runner
    }

    public enum Failure: LocalizedError {
        case notADirectory(String)
        case transferFailed(String)
        case invalidListing
        case invalidPath

        public var errorDescription: String? {
            switch self {
            case .notADirectory(let path): return "Not a directory: \(path)"
            case .transferFailed(let message): return message
            case .invalidListing: return "The host returned an unsupported or incomplete directory listing."
            case .invalidPath: return "The file path cannot be represented safely."
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
        guard output.hasPrefix("/"), !output.contains("\u{FFFD}"), !output.contains("\0") else {
            throw Failure.invalidPath
        }
        return output
    }

    public func list(_ path: String, on target: SSHTarget) async throws -> [RemoteFile] {
        guard Self.isAbsolutePath(path) else { throw Failure.invalidPath }
        let output = try await runner.run(Self.listCommand(path), on: target)
        return try Self.parseListing(output, parent: path)
    }

    static func listCommand(_ path: String) -> String {
        // GNU find is present on the supported Linux hosts. Unsupported hosts
        // fail closed instead of falling back to a lossy human-readable list.
        // Markers also reject a partially successful command: SSHRunner may
        // return stdout when the remote exit status was nonzero.
        "LC_ALL=C; export LC_ALL; cd -- \(quote(path)) && "
            + "printf 'SM-FILES-V1\\0' && "
            + "find . -mindepth 1 -maxdepth 1 -printf '%f\\0%y\\0%s\\0%T@\\0%M\\0%u\\0' && "
            + "printf 'SM-FILES-END\\0'"
    }

    static func parseListing(_ output: String, parent: String) throws -> [RemoteFile] {
        guard isAbsolutePath(parent), !output.contains("\u{FFFD}") else { throw Failure.invalidListing }
        var fields = output.components(separatedBy: "\0")
        guard fields.count >= 3, fields.first == "SM-FILES-V1",
              fields.last == "", fields[fields.count - 2] == "SM-FILES-END"
        else { throw Failure.invalidListing }
        fields.removeLast(2)
        fields.removeFirst()
        guard fields.count.isMultiple(of: 6) else { throw Failure.invalidListing }
        var files: [RemoteFile] = []
        var names: Set<String> = []
        for index in stride(from: 0, to: fields.count, by: 6) {
            let name = fields[index]
            guard isFileName(name), names.insert(name).inserted,
                  let size = Int64(fields[index + 2]), size >= 0,
                  let epoch = Double(fields[index + 3]), epoch.isFinite,
                  fields[index + 1].count == 1, fields[index + 4].count == 10
            else { throw Failure.invalidListing }
            let type = fields[index + 1]
            files.append(RemoteFile(
                name: name,
                path: parent + (parent.hasSuffix("/") ? "" : "/") + name,
                size: size,
                modified: Date(timeIntervalSince1970: epoch),
                isDirectory: type == "d",
                isSymlink: type == "l",
                mode: fields[index + 4],
                owner: fields[index + 5]
            ))
        }
        // Directories first, then case-insensitive by name.
        return files.sorted {
            $0.isDirectory != $1.isDirectory
                ? $0.isDirectory
                : $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending
        }
    }

    static func isFileName(_ name: String) -> Bool {
        !name.isEmpty && name != "." && name != ".."
            && !name.contains("/") && !name.contains("\0") && !name.contains("\u{FFFD}")
    }

    private static func isAbsolutePath(_ path: String) -> Bool {
        path.hasPrefix("/") && !path.contains("\0") && !path.contains("\u{FFFD}")
            && !path.split(separator: "/").contains(where: { $0 == "." || $0 == ".." })
    }

    static func removeCommand(_ file: RemoteFile) throws -> String {
        guard isFileName(file.name), isAbsolutePath(file.path), file.path.hasSuffix("/" + file.name) else {
            throw Failure.invalidPath
        }
        return (file.isDirectory ? "rm -rf -- " : "rm -f -- ") + quote(file.path)
    }

    // MARK: - Mutations

    public func makeDirectory(_ path: String, on target: SSHTarget) async throws {
        _ = try await runner.run("mkdir -p -- \(Self.quote(path))", on: target)
    }

    public func remove(_ file: RemoteFile, on target: SSHTarget) async throws {
        // Recursive only for directories, and only ever on an explicit path.
        let command = try Self.removeCommand(file)
        _ = try await runner.run(command, on: target)
    }

    public func rename(_ file: RemoteFile, to newName: String, on target: SSHTarget) async throws {
        guard Self.isFileName(newName) else { throw Failure.invalidPath }
        _ = try Self.removeCommand(file) // Validate the source without touching it.
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
        let askpass = try SSHRunner.makeAskpass(for: target)
        defer { askpass?.cleanUp() }
        _ = try await SSHRunner.execute(
            executable: "/usr/bin/scp",
            arguments: arguments,
            timeout: 600,
            environment: askpass?.environment
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
        let askpass = try SSHRunner.makeAskpass(for: target)
        defer { askpass?.cleanUp() }
        _ = try await SSHRunner.execute(
            executable: "/usr/bin/scp",
            arguments: arguments,
            timeout: 600,
            environment: askpass?.environment
        )
    }
}
