import SwiftUI

/// The main window. The sidebar groups destinations the way a server console
/// is actually used: an overview, the resources you configure, tools, and the
/// live sessions you have open.
public struct RootView: View {
    // Deliberately not `monitor`. This view is the frame around everything —
    // split view, sidebar, toolbar, sheets — and MonitorService publishes on
    // every poll. With the service observed here, each poll re-evaluated the
    // whole frame and diffed the NavigationSplitView for rows and buttons that
    // had not changed. The parts that show live data are their own views
    // further down, so a poll re-evaluates a row, not the window.
    @EnvironmentObject private var sessions: SessionManager
    @EnvironmentObject private var loc: Localization

    @State private var selection: Selection? = .dashboard
    @State private var addingServer = false
    @State private var importing = false
    // Owned here rather than in the child views so the toolbar, which belongs
    // to the window, can drive each destination's primary action.
    @State private var creatingSnippet = false
    @State private var creatingIdentity = false
    @State private var creatingGroup = false
    @State private var generatingKey = false
    @State private var clearingHistory = false
    @State private var editingServer: Server?
    /// Bumped to make the key list rescan ~/.ssh.
    @State private var keyScanToken = 0
    @State private var search = ""

    enum Selection: Hashable {
        case dashboard
        case machines
        case identities
        case sshKeys
        case snippets
        case docker
        case server(UUID)
        case history
        case session(UUID)
    }

    public init() {}

    public var body: some View {
        NavigationSplitView {
            sidebar
        } detail: {
            detail
        }
        .searchable(text: $search, placement: .toolbar, prompt: loc.t("common.search"))
        .toolbar { toolbarContent }
        .modifier(WindowSheets(
            addingServer: $addingServer,
            importing: $importing,
            editingServer: $editingServer
        ))
        .onChange(of: sessions.lastOpened) { _, opened in
            if let opened { selection = .session(opened) }
        }
    }

    /// The toolbar follows the selection, the way a document app changes its
    /// controls per view: offering "add server" while looking at identities is
    /// just an invitation to misclick.
    ///
    /// One `ToolbarItemGroup` holding a view-level switch, rather than a switch
    /// inside `@ToolbarContentBuilder`: the latter has to unify a distinct
    /// `some ToolbarContent` per branch, which pushed the type checker past its
    /// budget.
    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItemGroup { selectionActions }
    }

    @ViewBuilder
    private var selectionActions: some View {
        switch selection {
        case .dashboard, nil:
            RefreshButton()
            importButton
            addServerButton

        case .machines:
            Button { creatingGroup = true } label: {
                Label(loc.t("group.new"), systemImage: "square.stack.3d.up.badge.a")
            }
            .help(loc.t("group.new"))
            importButton
            addServerButton

        case .server(let id):
            ServerToolbarActions(id: id, editingServer: $editingServer)

        case .identities:
            newItemButton(loc.t("identity.new")) { creatingIdentity = true }

        case .snippets:
            newItemButton(loc.t("snippet.new")) { creatingSnippet = true }

        case .sshKeys:
            Button { generatingKey = true } label: {
                Label(loc.t("keys.generate"), systemImage: "plus")
            }
            .help(loc.t("keys.generate"))
            Button { keyScanToken += 1 } label: {
                Label(loc.t("common.refresh"), systemImage: "arrow.clockwise")
            }
            .help(loc.t("common.refresh"))

        case .docker:
            RefreshButton()

        case .history:
            Button(role: .destructive) { clearingHistory = true } label: {
                Label(loc.t("history.clear"), systemImage: "trash")
            }
            .help(loc.t("history.clear"))

        case .session(let id):
            Button(role: .destructive) {
                sessions.close(id)
                selection = .dashboard
            } label: {
                Label(loc.t("common.close"), systemImage: "xmark.circle")
            }
            .help(loc.t("common.close"))
        }
    }

    private func newItemButton(_ label: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Label(label, systemImage: "plus")
        }
        .help(label)
    }

    private var importButton: some View {
        Button { importing = true } label: {
            Label(loc.t("import.title"), systemImage: "square.and.arrow.down")
        }
        .help(loc.t("import.title"))
    }

    private var addServerButton: some View {
        Button { addingServer = true } label: {
            Label(loc.t("server.add"), systemImage: "plus")
        }
        .help(loc.t("server.add"))
    }

    // MARK: - Sidebar

    private var sidebar: some View {
        List(selection: $selection) {
            Label(loc.t("nav.dashboard"), systemImage: "gauge.with.dots.needle.50percent")
                .tag(Selection.dashboard)

            Section(loc.t("nav.resources")) {
                Label(loc.t("nav.machines"), systemImage: "server.rack")
                    .tag(Selection.machines)
                Label(loc.t("nav.identities"), systemImage: "person.badge.key")
                    .tag(Selection.identities)
                Label(loc.t("nav.sshKeys"), systemImage: "key")
                    .tag(Selection.sshKeys)
            }

            Section(loc.t("nav.toolbox")) {
                Label(loc.t("nav.snippets"), systemImage: "curlybraces")
                    .tag(Selection.snippets)
                Label(loc.t("nav.docker"), systemImage: "shippingbox")
                    .tag(Selection.docker)
            }

            Section(loc.t("nav.servers")) {
                SidebarServerRows()
            }

            Section(loc.t("nav.terminal")) {
                sessionRows(kind: .terminal, systemImage: "terminal")
            }

            Section(loc.t("nav.sftp")) {
                sessionRows(kind: .sftp, systemImage: "folder")
            }

            Section {
                Label(loc.t("nav.history"), systemImage: "clock.arrow.circlepath")
                    .tag(Selection.history)
            }
        }
        .listStyle(.sidebar)
        .navigationSplitViewColumnWidth(min: 220, ideal: 250)
    }

    /// Open sessions of one kind, or a muted placeholder when there are none.
    @ViewBuilder
    private func sessionRows(kind: SessionRecord.Kind, systemImage: String) -> some View {
        let open = sessions.sessions(kind: kind)
        if open.isEmpty {
            Text(loc.t("nav.noSessions"))
                .font(.caption)
                .foregroundStyle(.tertiary)
        } else {
            ForEach(open) { session in
                HStack(spacing: 8) {
                    Image(systemName: systemImage)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Text(session.title).lineLimit(1)
                    Spacer(minLength: 0)
                    Button {
                        sessions.close(session.id)
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.tertiary)
                    }
                    .buttonStyle(.borderless)
                    .help(loc.t("common.close"))
                }
                .tag(Selection.session(session.id))
            }
        }
    }

    // MARK: - Detail

    @ViewBuilder
    private var detail: some View {
        switch selection {
        case .dashboard, nil:
            DashboardView(search: $search, onSelect: { selection = .server($0) })
        case .machines:
            MachinesView(search: $search, creatingGroup: $creatingGroup, onOpen: { selection = .server($0) })
        case .identities:
            IdentitiesView(search: $search, creating: $creatingIdentity)
        case .sshKeys:
            SSHKeysView(search: $search, reloadToken: $keyScanToken, generating: $generatingKey)
        case .snippets:
            SnippetsView(search: $search, creating: $creatingSnippet)
        case .docker:
            DockerOverviewView(search: $search, onOpen: { selection = .server($0) })
        case .server(let id):
            ServerDetailHost(id: id)
        case .history:
            SessionHistoryView(search: $search, clearing: $clearingHistory)
        case .session(let id):
            SessionHost(id: id)
        }
    }
}

// MARK: - Live parts

// Each of these observes MonitorService so that RootView does not have to.

/// The window-level refresh, wherever the toolbar offers one.
private struct RefreshButton: View {
    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        Button {
            Task { await monitor.pollAll() }
        } label: {
            Label(loc.t("common.refresh"), systemImage: "arrow.clockwise")
        }
        .help(loc.t("common.refresh"))
    }
}

/// Terminal, SFTP, edit and refresh for the selected machine.
private struct ServerToolbarActions: View {
    let id: UUID
    @Binding var editingServer: Server?

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var sessions: SessionManager
    @EnvironmentObject private var loc: Localization

    var body: some View {
        let server = monitor.servers.first { $0.id == id }

        Button {
            if let server { sessions.open(server: server, kind: .terminal) }
        } label: {
            Label(loc.t("nav.terminal"), systemImage: "terminal")
        }
        .help(loc.t("nav.terminal"))
        .disabled(server == nil)

        Button {
            if let server { sessions.open(server: server, kind: .sftp) }
        } label: {
            Label("SFTP", systemImage: "folder")
        }
        .help("SFTP")
        .disabled(server == nil)

        Button { editingServer = server } label: {
            Label(loc.t("common.edit"), systemImage: "pencil")
        }
        .help(loc.t("common.edit"))
        .disabled(server == nil)

        RefreshButton()
    }
}

/// The sidebar's server list. Sits inside the sidebar `List`, so its rows'
/// tags still drive the selection.
private struct SidebarServerRows: View {
    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        if monitor.servers.isEmpty {
            Text(loc.t("nav.noServers"))
                .font(.caption)
                .foregroundStyle(.tertiary)
        } else {
            ForEach(monitor.servers) { server in
                SidebarServerRow(server: server)
                    .tag(RootView.Selection.server(server.id))
            }
        }
    }
}

/// Status dot, flag, name, and one line of live detail.
private struct SidebarServerRow: View {
    let server: Server

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        HStack(spacing: 8) {
            StatusDot(status: monitor.status[server.id] ?? .unknown)
            if !server.flag.isEmpty {
                Text(server.flag).font(.caption)
            }
            VStack(alignment: .leading, spacing: 1) {
                Text(server.name).lineLimit(1)
                if case .offline(let reason) = monitor.status[server.id] ?? .unknown {
                    // Not the last snapshot's CPU: that reads as a live figure.
                    Text(loc.t("common.offline"))
                        .font(.caption2)
                        .foregroundStyle(.red)
                        .help(reason)
                } else if let snapshot = monitor.latest[server.id] {
                    Text(verbatim: "CPU \(Format.percent(snapshot.cpuPercent))")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                } else {
                    Text(server.host)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
        }
    }
}

/// The machine screen for a selected server, or a placeholder once it is gone.
private struct ServerDetailHost: View {
    let id: UUID

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    var body: some View {
        if let server = monitor.servers.first(where: { $0.id == id }) {
            // Keyed on the server so switching hosts starts a fresh screen:
            // without it the same view instance was reused and kept the
            // previous host's history, looked-up location and containers
            // until each of its timers came round.
            ServerDetailView(server: server).id(server.id)
        } else {
            ContentUnavailableView(loc.t("dashboard.emptyTitle"), systemImage: "server.rack")
        }
    }
}

/// A terminal or SFTP session, or a placeholder once it has been closed.
private struct SessionHost: View {
    let id: UUID

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var sessions: SessionManager
    @EnvironmentObject private var loc: Localization

    var body: some View {
        if let session = sessions.active.first(where: { $0.id == id }),
           let server = monitor.servers.first(where: { $0.id == session.serverID }) {
            switch session.kind {
            case .terminal:
                TerminalPane(server: server, sessionID: session.id).id(session.id)
            case .sftp:
                SFTPView(server: server).id(session.id)
            }
        } else {
            ContentUnavailableView(loc.t("nav.noSessions"), systemImage: "terminal")
        }
    }
}

/// The window's sheets, extracted because chaining them onto the split view
/// pushed `body` past what the type checker will solve in reasonable time.
private struct WindowSheets: ViewModifier {
    @Binding var addingServer: Bool
    @Binding var importing: Bool
    @Binding var editingServer: Server?

    func body(content: Content) -> some View {
        content
            .sheet(isPresented: $addingServer) {
                ServerEditorView(mode: .add)
            }
            .sheet(isPresented: $importing) {
                ImportSSHConfigView()
            }
            .sheet(item: $editingServer) { server in
                ServerEditorView(mode: .edit(server))
            }
    }
}
