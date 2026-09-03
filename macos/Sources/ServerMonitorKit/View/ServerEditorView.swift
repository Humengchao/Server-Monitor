import AppKit
import SwiftUI

/// Add or edit a server, including the credential that goes to the Keychain.
public struct ServerEditorView: View {
    public enum Mode {
        case add
        case edit(Server)
    }

    private let mode: Mode

    @Environment(MonitorService.self) private var monitor
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var host = ""
    @State private var port = "22"
    @State private var username = ""
    @State private var authKind: AuthKind = .sshConfigAlias
    @State private var sshAlias = ""
    @State private var identityFile = ""
    @State private var password = ""
    @State private var countryCode = ""
    @State private var tagList = ""
    @State private var osKind: OSKind = .auto
    @State private var groupID: UUID?
    // -1 stands for "follow the global limit", which the picker shows as such.
    @State private var cpuThreshold = -1
    @State private var memoryThreshold = -1
    @State private var diskThreshold = -1
    @State private var notes = ""

    @State private var failure: String?
    @State private var info: String?
    @State private var testing = false
    @State private var confirmingDelete = false

    public init(mode: Mode) {
        self.mode = mode
    }

    private var isEditing: Bool {
        if case .edit = mode { return true }
        return false
    }

    private var existing: Server? {
        if case .edit(let server) = mode { return server }
        return nil
    }

    public var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    TextField(loc.t("server.name"), text: $name, prompt: Text(loc.t("server.namePlaceholder")))
                    TextField(loc.t("server.host"), text: $host, prompt: Text(loc.t("server.hostPlaceholder")))
                    TextField(loc.t("server.port"), text: $port)
                    TextField(loc.t("server.username"), text: $username)
                }

                Section {
                    Picker(loc.t("server.authMethod"), selection: $authKind) {
                        Text(loc.t("auth.sshConfigAlias")).tag(AuthKind.sshConfigAlias)
                        Text(loc.t("auth.identityFile")).tag(AuthKind.identityFile)
                        Text(loc.t("auth.agent")).tag(AuthKind.agent)
                        Text(loc.t("server.password")).tag(AuthKind.password)
                    }
                    .pickerStyle(.segmented)

                    switch authKind {
                    case .sshConfigAlias:
                        TextField(loc.t("server.alias"), text: $sshAlias)
                        Text(loc.t("auth.aliasHelp"))
                            .font(.caption).foregroundStyle(.secondary)
                    case .identityFile:
                        HStack {
                            TextField(loc.t("server.identityPath"), text: $identityFile)
                            Button(loc.t("common.browse")) { chooseKeyFile() }
                        }
                        Text(loc.t("auth.identityHelp"))
                            .font(.caption).foregroundStyle(.secondary)
                    case .agent:
                        Text(loc.t("auth.agent"))
                            .font(.caption).foregroundStyle(.secondary)
                    case .password:
                        SecureField(
                            loc.t("server.password"),
                            text: $password,
                            prompt: Text(isEditing ? loc.t("server.secretUnchanged") : "")
                        )
                        Text(loc.t("auth.passwordHelp"))
                            .font(.caption).foregroundStyle(.secondary)
                    }

                    Label(loc.t("settings.credentialsNoteLocal"), systemImage: "lock.shield")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                Section {
                    Picker(loc.t("server.osKind"), selection: $osKind) {
                        Text(loc.t("server.osAuto")).tag(OSKind.auto)
                        Text("Linux").tag(OSKind.linux)
                        Text("Windows").tag(OSKind.windows)
                    }
                    .pickerStyle(.segmented)
                    Text(loc.t("server.osHelp"))
                        .font(.caption).foregroundStyle(.secondary)

                    Picker(loc.t("group.assign"), selection: $groupID) {
                        Text(loc.t("group.none")).tag(UUID?.none)
                        ForEach(monitor.groups) { group in
                            Text(group.name).tag(Optional(group.id))
                        }
                    }
                }

                Section(loc.t("server.thresholds")) {
                    thresholdPicker(loc.t("settings.cpuThreshold"), selection: $cpuThreshold)
                    thresholdPicker(loc.t("settings.memoryThreshold"), selection: $memoryThreshold)
                    thresholdPicker(loc.t("settings.diskThreshold"), selection: $diskThreshold)
                    Text(loc.t("server.thresholdHelp"))
                        .font(.caption).foregroundStyle(.secondary)
                }

                Section {
                    TextField(loc.t("server.country"), text: $countryCode)
                    TextField(loc.t("server.tags"), text: $tagList)
                        .help(loc.t("server.tagsHelp"))
                    Text(loc.t("server.countryHelp"))
                        .font(.caption).foregroundStyle(.secondary)
                    TextField(loc.t("server.notes"), text: $notes, axis: .vertical)
                        .lineLimit(2...4)
                }

                if let failure {
                    Label(failure, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
                if let info {
                    Label(info, systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .font(.callout)
                }
            }
            .formStyle(.grouped)

            Divider()

            HStack {
                if isEditing {
                    Button(loc.t("common.delete"), role: .destructive) { confirmingDelete = true }
                }
                Button(loc.t("common.testConnection")) { Task { await test() } }
                    .disabled(testing)
                if testing { ProgressView().controlSize(.small) }
                Spacer()
                Button(loc.t("common.cancel")) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(loc.t("common.save")) { save() }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
            }
            .padding(12)
        }
        .frame(width: 520)
        .onAppear(perform: populate)
        .confirmationDialog(
            loc.t("server.deleteConfirm", existing?.name ?? ""),
            isPresented: $confirmingDelete,
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) { delete() }
            Button(loc.t("common.cancel"), role: .cancel) {}
        }
    }

    private func thresholdPicker(_ title: String, selection: Binding<Int>) -> some View {
        Picker(title, selection: selection) {
            Text(loc.t("server.thresholdInherit")).tag(-1)
            ForEach(AppSettings.thresholdChoices, id: \.self) { value in
                Text(value == 0 ? loc.t("settings.thresholdOff") : "\(value)%").tag(value)
            }
        }
    }

    private func populate() {
        guard let server = existing else { return }
        name = server.name
        host = server.host
        port = String(server.port)
        username = server.username
        authKind = server.authKind
        sshAlias = server.sshAlias
        identityFile = server.identityFile
        countryCode = server.countryCode
        tagList = server.tags.joined(separator: ", ")
        osKind = server.osKind
        groupID = server.groupID
        cpuThreshold = server.cpuThreshold ?? -1
        memoryThreshold = server.memoryThreshold ?? -1
        diskThreshold = server.diskThreshold ?? -1
        notes = server.notes
    }

    /// Key files live in ~/.ssh, which the open panel needs to be pointed at
    /// explicitly because it is hidden by default.
    private func chooseKeyFile() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.showsHiddenFiles = true
        panel.directoryURL = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".ssh")
        if panel.runModal() == .OK, let url = panel.url {
            identityFile = url.path
        }
    }

    /// Builds the record from the form, or nil when required fields are missing.
    ///
    /// In alias mode the address and user come from ~/.ssh/config, so only the
    /// name and the alias are required; demanding a host there would make the
    /// user restate what ssh already knows.
    private func compose() -> Server? {
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let trimmedHost = host.trimmingCharacters(in: .whitespaces)
        let trimmedUser = username.trimmingCharacters(in: .whitespaces)
        guard !trimmedName.isEmpty else {
            failure = loc.t("server.nameRequired")
            return nil
        }
        if authKind != .sshConfigAlias, trimmedHost.isEmpty || trimmedUser.isEmpty {
            failure = loc.t("server.hostRequired")
            return nil
        }
        var server = existing ?? Server(
            name: trimmedName,
            host: trimmedHost,
            username: trimmedUser,
            authKind: authKind,
            sortIndex: (try? monitor.database.nextSortIndex()) ?? 0
        )
        server.name = trimmedName
        server.host = trimmedHost
        server.port = Int(port.trimmingCharacters(in: .whitespaces)) ?? 22
        server.username = trimmedUser
        server.authKind = authKind
        server.sshAlias = sshAlias.trimmingCharacters(in: .whitespaces)
        server.identityFile = identityFile.trimmingCharacters(in: .whitespaces)
        server.countryCode = countryCode.trimmingCharacters(in: .whitespaces).uppercased()
        server.tags = Server.parseTags(tagList)
        server.osKind = osKind
        server.groupID = groupID
        server.cpuThreshold = cpuThreshold < 0 ? nil : cpuThreshold
        server.memoryThreshold = memoryThreshold < 0 ? nil : memoryThreshold
        server.diskThreshold = diskThreshold < 0 ? nil : diskThreshold
        server.notes = notes
        return server
    }

    private func save() {
        failure = nil
        info = nil
        guard let server = compose() else { return }
        if authKind == .sshConfigAlias && server.sshAlias.isEmpty {
            failure = loc.t("server.aliasRequired")
            return
        }
        if authKind == .identityFile && server.identityFile.isEmpty {
            failure = loc.t("server.keyRequired")
            return
        }
        if authKind == .password {
            // An empty field on an existing server means "keep what is stored".
            if password.isEmpty, Keychain.password(serverID: server.id) == nil {
                failure = loc.t("server.passwordRequired")
                return
            }
            if !password.isEmpty {
                do {
                    try Keychain.savePassword(password, serverID: server.id)
                } catch {
                    failure = error.localizedDescription
                    return
                }
            }
        }
        do {
            if isEditing {
                try monitor.updateServer(server)
            } else {
                try monitor.addServer(server)
            }
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func delete() {
        guard let server = existing else { return }
        do {
            try monitor.deleteServer(server)
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }

    /// Dials the host with the values currently in the form, without saving.
    private func test() async {
        failure = nil
        info = nil
        guard let server = compose() else { return }
        testing = true
        defer { testing = false }
        do {
            try await monitor.testConnection(osKind: osKind, target: SSHTarget(
                serverID: server.id,
                host: server.authKind == .sshConfigAlias ? server.sshAlias : server.host,
                port: server.port,
                username: server.username,
                credential: server.credential
            ))
            info = loc.t("server.connectionOK")
        } catch {
            failure = error.localizedDescription
        }
    }
}
