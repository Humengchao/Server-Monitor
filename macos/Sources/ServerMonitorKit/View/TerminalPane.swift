import SwiftUI

/// An interactive SSH shell for one server.
struct TerminalPane: View {
    let server: Server
    /// Runs instead of a login shell; the Docker tab uses it to enter a container.
    var remoteCommand: String? = nil
    /// Set when the pane is driven by a sidebar session, so ending the shell
    /// also closes that session's history record.
    var sessionID: UUID? = nil

    @Environment(\.monitorService) private var monitor
    @EnvironmentObject private var sessions: SessionManager
    @EnvironmentObject private var settings: AppSettings
    @EnvironmentObject private var loc: Localization
    @StateObject private var session = TerminalSession()
    @State private var target: SSHTarget?
    @State private var failure: String?
    /// Bumped to rebuild the terminal view, which restarts the ssh process.
    @State private var generation = 0

    var body: some View {
        VStack(spacing: 0) {
            statusBar
            Divider()
            if let target {
                SSHTerminalView(
                    target: target,
                    remoteCommand: remoteCommand,
                    session: session,
                    fontName: settings.terminalFontName,
                    fontSize: settings.terminalFontSize
                )
                    .id(generation)
                    .frame(minWidth: 480, minHeight: 280)
            } else {
                ContentUnavailableView(
                    loc.t("common.error"),
                    systemImage: "exclamationmark.triangle",
                    description: Text(failure ?? loc.t("common.error"))
                )
            }
        }
        .onAppear(perform: resolve)
        .onChange(of: session.state) { _, state in
            // Record the end time as soon as the shell exits, rather than
            // waiting for the user to dismiss the pane.
            if case .ended = state, let sessionID {
                sessions.close(sessionID)
            }
        }
    }

    private var statusBar: some View {
        HStack(spacing: 8) {
            switch session.state {
            case .starting:
                ProgressView().controlSize(.small)
                Text(loc.t("common.connecting")).font(.caption)
            case .running:
                StatusDot(status: .online(at: Date()))
                Text(session.title.isEmpty ? loc.t("terminal.connected") : session.title)
                    .font(.caption)
                    .lineLimit(1)
            case .ended(let code):
                StatusDot(status: .unknown)
                Text(code.map { "\(loc.t("terminal.disconnected")) (\($0))" } ?? loc.t("terminal.disconnected"))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Text(server.displayTarget)
                .font(.caption)
                .foregroundStyle(.tertiary)
                .lineLimit(1)

            SnippetMenu(session: session)

            Button(loc.t("terminal.reconnect"), systemImage: "arrow.clockwise") {
                generation += 1
            }
            .controlSize(.small)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }

    private func resolve() {
        do {
            target = try monitor.required.target(for: server)
            failure = nil
        } catch {
            failure = error.localizedDescription
        }
    }
}

/// Types a saved command into the running shell, without submitting it, so the
/// user can edit or confirm before it executes.
///
/// Its own view, observing the service, because the pane itself does not:
/// saving or deleting a snippet publishes, and this menu is the one part of
/// the pane that has to see it. Re-evaluating a menu button per poll is
/// nothing; re-evaluating the terminal was not.
private struct SnippetMenu: View {
    @ObservedObject var session: TerminalSession

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        Menu {
            let snippets = monitor.snippets()
            if snippets.isEmpty {
                Text(loc.t("terminal.noSnippets"))
            } else {
                ForEach(snippets) { snippet in
                    Button(snippet.name) { session.send(text: snippet.command) }
                }
            }
        } label: {
            Image(systemName: "curlybraces")
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .controlSize(.small)
        .hint(loc.t("terminal.snippets"))
        .disabled(session.state != .running)
    }
}
