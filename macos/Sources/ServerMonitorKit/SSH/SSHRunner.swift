import Foundation

/// Runs commands on a remote host through the system OpenSSH client.
///
/// Chosen over an in-process Swift SSH library for a concrete reason: modern
/// OpenSSH servers disable the SHA-1 `ssh-rsa` signature algorithm and require
/// `rsa-sha2-256/512`, which the pure-Swift options do not implement — so an
/// RSA key that works in Terminal fails in-process. Shelling out also inherits
/// everything the user has already configured: ~/.ssh/config aliases, the
/// agent, known_hosts, ProxyJump and per-host options.
///
/// Connection reuse comes from OpenSSH's own ControlMaster multiplexing rather
/// than a hand-rolled pool: the first command opens a master socket and later
/// commands ride on it, so a poll costs one round trip, not a full handshake.
public struct SSHRunner: Sendable {
    public enum Failure: LocalizedError {
        case launchFailed(String)
        case timedOut(seconds: Int)
        /// Non-zero exit with no usable stdout. Partial output is not an error:
        /// a host where one `cat` fails should still report the rest.
        case commandFailed(status: Int32, stderr: String)

        public var errorDescription: String? {
            switch self {
            case .launchFailed(let message):
                return "Could not start ssh: \(message)"
            case .timedOut(let seconds):
                return "ssh timed out after \(seconds)s"
            case .commandFailed(let status, let stderr):
                let trimmed = Failure.essence(of: stderr)
                // A bare "ssh failed" is what the server list used to show, and
                // it is untraceable. ssh reserves 255 for its own errors; any
                // other code came from the remote command.
                guard trimmed.isEmpty else { return trimmed }
                return status == 255
                    ? "ssh exited 255 with no message"
                    : "remote command exited \(status)"
            }
        }

        /// The line worth showing. sshd prints the host's login banner to
        /// stderr before anything else, so on a host with one every failure
        /// began "Authorized users only…"; the actual reason is the last line.
        /// A changed host key is the exception: its warning is a paragraph and
        /// the last line alone ("Host key verification failed.") loses the
        /// part that matters.
        static func essence(of stderr: String) -> String {
            let lines = stderr.lines().map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            guard let last = lines.last else { return "" }
            if stderr.contains("REMOTE HOST IDENTIFICATION HAS CHANGED") {
                return "Host key changed — remove the old entry from ~/.ssh/known_hosts if this is expected"
            }
            return last
        }
    }

    public init() {}

    /// Directory holding ControlMaster sockets. Kept in the sandboxed caches
    /// directory and deliberately short: a unix socket path is capped near 104
    /// bytes, which a long home directory plus a UUID would blow past.
    static func controlDirectory() throws -> URL {
        let base = try FileManager.default.url(
            for: .cachesDirectory, in: .userDomainMask, appropriateFor: nil, create: true
        ).appendingPathComponent("ServerMonitor/cm", isDirectory: true)
        try FileManager.default.createDirectory(at: base, withIntermediateDirectories: true)
        return base
    }

    /// Short, stable per-server socket name derived from the destination.
    ///
    /// The server id is intentional. Two rows may point at the same endpoint
    /// with different identities, passwords, ProxyJump settings, or host-key
    /// policies. Sharing a ControlMaster between them lets the first row's
    /// authenticated connection silently satisfy the second row, which is
    /// both surprising and wrong. A socket belongs to one configured server
    /// row; terminal and SFTP for that row still reuse its poll connection.
    static func controlPath(for target: SSHTarget) throws -> String {
        // %C in ControlPath would be simpler, but computing it ourselves keeps
        // the name stable across option changes and lets us close it by path.
        let credential: String
        switch target.credential {
        case .sshConfigAlias: credential = "alias"
        case .identityFile(let path): credential = "identity:\(path)"
        case .agent: credential = "agent"
        case .password: credential = "password"
        }
        let seed = "\(target.serverID.uuidString)|\(target.username)@\(target.host):\(target.port)|\(credential)"
        var hash: UInt64 = 0xcbf29ce484222325
        for byte in seed.utf8 {
            hash = (hash ^ UInt64(byte)) &* 0x100000001b3
        }
        return try controlDirectory()
            .appendingPathComponent(String(format: "%016llx", hash))
            .path
    }

    /// Removes an `-o name=value` pair — both elements.
    ///
    /// Dropping only the value leaves a dangling `-o`, and ssh exits with
    /// "no argument after keyword" before it ever opens a socket.
    static func removeOption(_ value: String, from arguments: inout [String]) {
        guard let index = arguments.firstIndex(of: value),
              index > 0,
              arguments[index - 1] == "-o"
        else { return }
        arguments.removeSubrange((index - 1)...index)
    }

    /// The ssh argument list for a destination, without the remote command.
    static func baseArguments(for target: SSHTarget, controlPath: String) -> [String] {
        var arguments = [
            // Never prompt: a GUI app has nowhere to show a password prompt,
            // and a hung ssh would stall the poll loop.
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=10",
            "-o", "ServerAliveInterval=15",
            "-o", "ServerAliveCountMax=2",
            // Trust on first use. The default `ask` policy has nowhere to ask
            // from here: with `SSH_ASKPASS_REQUIRE=force` ssh routes the host
            // key confirmation to the askpass helper too, which answers with
            // the password rather than "yes", so a first-time host hangs until
            // the watchdog kills it. `accept-new` records an unknown key and
            // still refuses a *changed* one, which is the protection that
            // matters after first contact.
            "-o", "StrictHostKeyChecking=accept-new",
            // Multiplexing: reuse one connection across polls, keep it warm for
            // five minutes of idleness, then let it close on its own.
            "-o", "ControlMaster=auto",
            "-o", "ControlPath=\(controlPath)",
            "-o", "ControlPersist=300",
        ]

        switch target.credential {
        case .sshConfigAlias:
            // Everything else comes from ~/.ssh/config for this alias.
            break
        case .identityFile(let path):
            arguments += ["-i", path, "-o", "IdentitiesOnly=yes"]
        case .agent:
            arguments += ["-o", "PreferredAuthentications=publickey"]
        case .password:
            // BatchMode suppresses every prompt including the askpass helper,
            // so password auth has to switch it off and lean on the helper plus
            // a hard timeout instead.
            Self.removeOption("BatchMode=yes", from: &arguments)
            arguments += [
                "-o", "BatchMode=no",
                "-o", "PreferredAuthentications=password,keyboard-interactive",
                "-o", "NumberOfPasswordPrompts=1",
            ]
        }

        if target.port != 22, case .sshConfigAlias = target.credential {} else if target.port != 22 {
            arguments += ["-p", String(target.port)]
        }
        return arguments
    }

    /// Runs `command` on the target and returns stdout.
    public func run(
        _ command: String,
        on target: SSHTarget,
        timeout: Int = 30
    ) async throws -> String {
        let controlPath = try Self.controlPath(for: target)
        var arguments = Self.baseArguments(for: target, controlPath: controlPath)
        arguments.append(target.sshDestination)
        arguments.append(command)

        let askpass = try Self.makeAskpass(for: target)
        defer { askpass?.cleanUp() }

        do {
            return try await Self.execute(
                executable: "/usr/bin/ssh",
                arguments: arguments,
                timeout: timeout,
                environment: askpass?.environment
            )
        } catch let failure as Failure where Self.isDeadMultiplexSocket(failure) {
            // The master went away underneath this server row. Its socket is
            // stale; a host that is genuinely unreachable says so on stderr
            // and does not land here. Retry once after removing only this row's
            // socket.
            try? FileManager.default.removeItem(atPath: controlPath)
            return try await Self.execute(
                executable: "/usr/bin/ssh",
                arguments: arguments,
                timeout: timeout,
                environment: askpass?.environment
            )
        }
    }

    /// ssh exiting 255 without a word is how a multiplex client reports that
    /// its master vanished. Every real connection error carries a message.
    static func isDeadMultiplexSocket(_ failure: Failure) -> Bool {
        guard case .commandFailed(let status, let stderr) = failure else { return false }
        return status == 255 && stderr.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// A throwaway `SSH_ASKPASS` helper that feeds ssh a stored password.
    ///
    /// ssh will not read a password from a pipe, and putting one on the command
    /// line would expose it in `ps`. The helper writes it to a file only this
    /// user can read, inside a directory removed as soon as the connection is
    /// made — and because ControlMaster keeps the connection open, that is once
    /// per host rather than once per poll.
    public struct Askpass {
        public let environment: [String: String]
        let directory: URL

        public func cleanUp() {
            try? FileManager.default.removeItem(at: directory)
        }
    }

    static func makeAskpass(for target: SSHTarget) throws -> Askpass? {
        guard case .password = target.credential,
              let password = Keychain.password(serverID: target.serverID)
        else { return nil }

        let directory = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("sm-askpass-\(UUID().uuidString)")
        try FileManager.default.createDirectory(
            at: directory,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )

        let secretURL = directory.appendingPathComponent("secret")
        try Data(password.utf8).write(to: secretURL, options: [.atomic])
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o600], ofItemAtPath: secretURL.path
        )

        let helperURL = directory.appendingPathComponent("askpass")
        // The path is passed through the environment; the secret itself never
        // appears in an argument list.
        let script = "#!/bin/sh\nexec /bin/cat \"$SM_SECRET\"\n"
        try Data(script.utf8).write(to: helperURL, options: [.atomic])
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o700], ofItemAtPath: helperURL.path
        )

        var environment = ProcessInfo.processInfo.environment
        environment["SSH_ASKPASS"] = helperURL.path
        // Without "force", ssh prefers a terminal prompt when one exists, which
        // in the interactive terminal would mean typing the password by hand.
        environment["SSH_ASKPASS_REQUIRE"] = "force"
        environment["SM_SECRET"] = secretURL.path
        environment["DISPLAY"] = environment["DISPLAY"] ?? ":0"

        return Askpass(environment: environment, directory: directory)
    }

    /// Drops the multiplexed connection for a host, e.g. after its settings
    /// changed or it was deleted.
    public func disconnect(_ target: SSHTarget) async {
        guard let controlPath = try? Self.controlPath(for: target) else { return }
        guard FileManager.default.fileExists(atPath: controlPath) else { return }
        var arguments = Self.baseArguments(for: target, controlPath: controlPath)
        arguments += ["-O", "exit", target.sshDestination]
        _ = try? await Self.execute(executable: "/usr/bin/ssh", arguments: arguments, timeout: 5)
    }

    /// Runs a subprocess, returning stdout, with a hard timeout.
    static func execute(
        executable: String,
        arguments: [String],
        timeout: Int,
        environment: [String: String]? = nil
    ) async throws -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        if let environment { process.environment = environment }
        let outPipe = Pipe()
        let errPipe = Pipe()
        process.standardOutput = outPipe
        process.standardError = errPipe
        // Nothing may inherit the app's stdin; ssh must not try to read from it.
        process.standardInput = FileHandle.nullDevice

        // Armed before run(): a process that exits immediately would otherwise
        // fire its handler before anything was listening.
        let exit = ProcessExitSignal()
        process.terminationHandler = { _ in exit.complete() }

        do {
            try process.run()
        } catch {
            throw Failure.launchFailed(error.localizedDescription)
        }

        let watchdog = Task {
            try await Task.sleep(for: .seconds(timeout))
            if process.isRunning {
                exit.markTimedOut()
                process.terminate()
            }
        }
        defer { watchdog.cancel() }

        // Cancelling the calling task kills the process. Without this a
        // cancelled task merely stopped listening: the ssh kept running to
        // completion, so switching servers left the old screen's `docker
        // stats` and `vnstat` going for seconds, and a failed collection sat
        // waiting for its `async let` ping to finish its three echoes (~2 s)
        // before it could report the host offline.
        let (stdout, stderr) = await withTaskCancellationHandler {
            // Read both pipes concurrently: a full stderr buffer would
            // otherwise deadlock a process that is still writing stdout.
            async let outData = blockingRead(outPipe.fileHandleForReading)
            async let errData = blockingRead(errPipe.fileHandleForReading)
            let out = String(decoding: await outData, as: UTF8.self)
            let err = String(decoding: await errData, as: UTF8.self)
            await exit.wait()
            return (out, err)
        } onCancel: {
            // Closing the process closes its pipes, which is what lets the two
            // reads above return and the wait complete.
            if process.isRunning { process.terminate() }
        }

        // A cancelled process also died by signal; report it as what it was,
        // not as a timeout.
        if Task.isCancelled { throw CancellationError() }

        // Do this before accepting partial stdout. A timed-out metrics command
        // can have printed a plausible-looking prefix; returning it would
        // silently publish a half-read snapshot as if it were current.
        if exit.wasTimedOut { throw Failure.timedOut(seconds: timeout) }

        if process.terminationStatus != 0 {
            // Partial output still parses; only a completely empty result is a
            // real failure.
            if stdout.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                // We only ever signal this process from the watchdog above.
                if process.terminationReason == .uncaughtSignal {
                    throw Failure.timedOut(seconds: timeout)
                }
                throw Failure.commandFailed(status: process.terminationStatus, stderr: stderr)
            }
        }
        return stdout
    }

    /// Reads to EOF on a thread that is allowed to block.
    ///
    /// `Task.detached` looks like the way to do this and is a trap: detached
    /// tasks share the same fixed-width cooperative pool as everything else, so
    /// a blocking read there consumes a thread the pool cannot get back. Polling
    /// a handful of hosts at once exhausted it — and once it was exhausted even
    /// the watchdog `Task` above stopped being scheduled, so a stuck call hung
    /// forever instead of timing out. The Dispatch global queue grows on demand
    /// and is the right place for a blocking wait.
    private static func blockingRead(_ handle: FileHandle) async -> Data {
        await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                continuation.resume(returning: (try? handle.readToEnd()) ?? Data())
            }
        }
    }

    /// One-shot "the process ended" signal, bridging `terminationHandler` to
    /// async.
    ///
    /// `waitUntilExit()` is the obvious call and cannot be used here: with
    /// several of these running at once it blocks forever on a child that has
    /// already exited — observed with no ssh process left alive and no child of
    /// the test process, yet the wait never returning. `terminationHandler`
    /// fires exactly once and costs no thread at all.
    private final class ProcessExitSignal: @unchecked Sendable {
        private let lock = NSLock()
        private var hasExited = false
        private var waiter: CheckedContinuation<Void, Never>?
        private var timedOut = false

        func markTimedOut() {
            lock.lock()
            timedOut = true
            lock.unlock()
        }

        var wasTimedOut: Bool {
            lock.lock()
            defer { lock.unlock() }
            return timedOut
        }

        func complete() {
            lock.lock()
            hasExited = true
            let pending = waiter
            waiter = nil
            lock.unlock()
            pending?.resume()
        }

        func wait() async {
            await withCheckedContinuation { continuation in
                lock.lock()
                if hasExited {
                    lock.unlock()
                    continuation.resume()
                    return
                }
                waiter = continuation
                lock.unlock()
            }
        }
    }
}
