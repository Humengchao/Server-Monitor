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
    fileprivate weak var view: LocalProcessTerminalView?

    public init() {}

    /// Types text into the shell exactly as if the user had typed it.
    public func send(text: String) {
        view?.send(txt: text)
    }

    fileprivate func began() { state = .running }
    fileprivate func ended(code: Int32?) { state = .ended(code: code) }
    fileprivate func retitled(_ value: String) { title = value }
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
        Coordinator(session: session)
    }

    public func makeNSView(context: Context) -> LocalProcessTerminalView {
        let view = LocalProcessTerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 480))
        view.processDelegate = context.coordinator
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
        // Closing the view must not leave an orphaned ssh process behind.
        nsView.terminate()
    }

    public final class Coordinator: NSObject, LocalProcessTerminalViewDelegate {
        private let session: TerminalSession

        init(session: TerminalSession) {
            self.session = session
        }

        public func sizeChanged(source: LocalProcessTerminalView, newCols: Int, newRows: Int) {}

        public func setTerminalTitle(source: LocalProcessTerminalView, title: String) {
            MainActor.assumeIsolated { session.retitled(title) }
        }

        public func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}

        public func processTerminated(source: TerminalView, exitCode: Int32?) {
            MainActor.assumeIsolated { session.ended(code: exitCode) }
        }
    }
}
