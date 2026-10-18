import AppKit
import SwiftTerm
import SwiftUI

/// Observable state for one terminal session.
@MainActor
public final class TerminalSession: ObservableObject {
    public enum State: Equatable {
        case starting
        case running
        case ended(code: Int32?)
    }

    @Published public private(set) var state: State = .starting
    @Published public private(set) var title: String = ""
    /// The live view, so a snippet can be typed into the running shell.
    fileprivate var view: LocalProcessTerminalView?
    fileprivate var coordinator: SSHTerminalView.Coordinator?
    fileprivate var connectionID = UUID()
    fileprivate let persistent: Bool
    var onEnded: ((Int32?) -> Void)?
    private(set) var isClosed = false

    public init(persistent: Bool = false) { self.persistent = persistent }

    /// Types text into the shell exactly as if the user had typed it.
    public func send(text: String) {
        view?.send(txt: text)
    }

    fileprivate func began() { state = .running }
    fileprivate func ended(code: Int32?) {
        state = .ended(code: code)
        onEnded?(code)
    }
    fileprivate func retitled(_ value: String) { title = value }

    /// Navigation detaches the view, whereas an explicit close owns cleanup.
    public func close() {
        isClosed = true
        onEnded = nil
        discardConnection()
    }

    func reconnect() {
        discardConnection()
        state = .starting
        title = ""
    }

    private func discardConnection() {
        // Ignore a termination callback queued by the connection being replaced.
        connectionID = UUID()
        let previous = view
        view = nil
        coordinator = nil
        previous?.terminate()
    }
}

/// A real SSH shell: SwiftTerm driving `/usr/bin/ssh` over a pseudo-terminal.
///
/// Spawning the system client rather than speaking SSH in-process means job
/// control, `sudo` prompts, `vim`, colours and window resizing behave exactly
/// as they do in Terminal.app — and every auth method the user already has
/// configured keeps working.
public struct SSHTerminalView: NSViewRepresentable {
    private let target: SSHTarget
    /// Optional command to run instead of an interactive login shell, used by
    /// the Docker tab to drop straight into a container.
    private let remoteCommand: String?
    private let session: TerminalSession
    private let fontName: String
    private let fontSize: Double

    public init(
        target: SSHTarget,
        remoteCommand: String? = nil,
        session: TerminalSession,
        fontName: String = "Menlo",
        fontSize: Double = 13
    ) {
        self.target = target
        self.remoteCommand = remoteCommand
        self.session = session
        self.fontName = fontName
        self.fontSize = fontSize
    }

    /// Falls back to the system monospaced face if the named font is missing,
    /// rather than silently landing on a proportional one.
    private func resolvedFont() -> NSFont {
        NSFont(name: fontName, size: fontSize)
            ?? NSFont.monospacedSystemFont(ofSize: fontSize, weight: .regular)
    }

    public func makeCoordinator() -> Coordinator {
        session.coordinator ?? Coordinator(session: session, connectionID: session.connectionID)
    }

    public func makeNSView(context: Context) -> LocalProcessTerminalView {
        if let existing = session.view {
            existing.font = resolvedFont()
            return existing
        }
        let view = LocalProcessTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        view.processDelegate = context.coordinator
        session.coordinator = context.coordinator
        view.getTerminal().silentLog = true
        view.font = resolvedFont()
        session.view = view

        var arguments = SSHRunner.baseArguments(
            for: target,
            controlPath: (try? SSHRunner.controlPath(for: target)) ?? ""
        )
        // An interactive session must be allowed to ask for a passphrase or a
        // host-key confirmation, unlike the silent metric polls.
        SSHRunner.removeOption("BatchMode=yes", from: &arguments)
        arguments += ["-t", target.sshDestination]
        if let remoteCommand {
            arguments.append(remoteCommand)
        }

        // Password hosts need the same askpass helper the polls use, so the
        // shell opens without stopping to ask.
        let askpass = try? SSHRunner.makeAskpass(for: target)
        let environment = askpass.map { helper in
            helper.environment.map { "\($0.key)=\($0.value)" }
        }
        view.startProcess(executable: "/usr/bin/ssh", args: arguments, environment: environment)
        // The helper is only needed until the handshake completes.
        if let askpass {
            DispatchQueue.main.asyncAfter(deadline: .now() + 30) { askpass.cleanUp() }
        }
        session.began()
        return view
    }

    public func updateNSView(_ nsView: LocalProcessTerminalView, context: Context) {
        // The terminal owns its buffer, so nothing here may redraw it — but the
        // font can change under it while a session is open.
        let font = resolvedFont()
        if nsView.font != font { nsView.font = font }
    }

    public static func dismantleNSView(_ nsView: LocalProcessTerminalView, coordinator: Coordinator) {
        // Sidebar sessions outlive their selected page. Standalone terminals
        // (machine detail / Docker sheets) are still scoped to their view.
        guard let session = coordinator.session, !session.persistent,
              session.connectionID == coordinator.connectionID else { return }
        session.close()
    }

    public final class Coordinator: NSObject, LocalProcessTerminalViewDelegate {
        fileprivate weak var session: TerminalSession?
        fileprivate let connectionID: UUID

        init(session: TerminalSession, connectionID: UUID) {
            self.session = session
            self.connectionID = connectionID
        }

        public func sizeChanged(source: LocalProcessTerminalView, newCols: Int, newRows: Int) {}

        // SwiftTerm does not document which thread these arrive on, so hop to
        // the main actor rather than assert we are already there:
        // `assumeIsolated` traps if the assumption is ever wrong, turning a
        // would-be data race into a crash. `session` is main-actor state.
        public func setTerminalTitle(source: LocalProcessTerminalView, title: String) {
            let session = session
            let connectionID = connectionID
            Task { @MainActor in
                guard let session, session.connectionID == connectionID else { return }
                session.retitled(title)
            }
        }

        public func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

        public func processTerminated(source: TerminalView, exitCode: Int32?) {
            let session = session
            let connectionID = connectionID
            Task { @MainActor in
                guard let session, session.connectionID == connectionID else { return }
                session.ended(code: exitCode)
            }
        }
    }
}
