import AppKit
import SwiftUI

/// Shared logins that servers can point at instead of each carrying its own.
struct IdentitiesView: View {
    @Binding var search: String
    @Binding var creating: Bool

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    @State private var editing: Identity?
    @State private var pendingDelete: Identity?
    @State private var failure: String?

    private var rows: [Identity] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return monitor.identities }
        return monitor.identities.filter {
            $0.name.lowercased().contains(query) || $0.username.lowercased().contains(query)
        }
    }

    var body: some View {
        Group {
            if monitor.identities.isEmpty {
                ContentUnavailableView {
                    Label(loc.t("nav.identities"), systemImage: "person.badge.key")
                } description: {
                    Text(loc.t("identity.empty"))
                } actions: {
                    Button(loc.t("identity.new")) { creating = true }
                }
            } else {
                List {
                    ForEach(rows) { identity in row(identity) }
                }
            }
        }
        .navigationTitle(loc.t("nav.identities"))
        .sheet(isPresented: $creating) { IdentityEditor(identity: nil) }
        .sheet(item: $editing) { identity in IdentityEditor(identity: identity) }
        .confirmationDialog(
            pendingDelete.map { loc.t("identity.deleteConfirm", $0.name) } ?? "",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) {
                if let identity = pendingDelete {
                    failure = failureMessage { try monitor.deleteIdentity(id: identity.id) }
                }
                pendingDelete = nil
            }
            Button(loc.t("common.cancel"), role: .cancel) { pendingDelete = nil }
        } message: {
            if let identity = pendingDelete {
                let count = monitor.serverCount(usingIdentity: identity.id)
                if count > 0 {
                    Text(loc.t("identity.inUse", "\(count)"))
                }
            }
        }
        .actionFailureAlert($failure)
    }

    private func row(_ identity: Identity) -> some View {
        let usage = monitor.serverCount(usingIdentity: identity.id)
        return HStack(spacing: 10) {
            Image(systemName: "person.badge.key")
                .foregroundStyle(.secondary)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(identity.name).font(.body.weight(.medium))
                Text(identity.summary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            if usage > 0 {
                Text(loc.t("identity.usedBy", "\(usage)"))
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            }
            Button { editing = identity } label: { Image(systemName: "pencil") }
                .buttonStyle(.borderless)
            Button(role: .destructive) { pendingDelete = identity } label: { Image(systemName: "trash") }
                .buttonStyle(.borderless)
        }
        .padding(.vertical, 3)
    }
}

private struct IdentityEditor: View {
    let identity: Identity?

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var username = "root"
    @State private var authKind: AuthKind = .identityFile
    @State private var identityFile = ""
    @State private var failure: String?

    var body: some View {
        VStack(spacing: 0) {
            Form {
                TextField(loc.t("identity.name"), text: $name)
                TextField(loc.t("server.username"), text: $username)
                Picker(loc.t("server.authMethod"), selection: $authKind) {
                    Text(loc.t("auth.identityFile")).tag(AuthKind.identityFile)
                    Text(loc.t("auth.agent")).tag(AuthKind.agent)
                }
                .pickerStyle(.segmented)
                if authKind == .identityFile {
                    HStack {
                        TextField(loc.t("server.identityPath"), text: $identityFile)
                        Button(loc.t("common.browse")) { choose() }
                    }
                }
                if let failure {
                    Label(failure, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red).font(.callout)
                }
            }
            .formStyle(.grouped)
            Divider()
            HStack {
                Spacer()
                Button(loc.t("common.cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(loc.t("common.save")) { save() }.buttonStyle(.borderedProminent)
            }
            .padding(12)
        }
        .frame(width: 520)
        .onAppear {
            guard let identity else { return }
            name = identity.name
            username = identity.username
            authKind = identity.authKind == .sshConfigAlias ? .identityFile : identity.authKind
            identityFile = identity.identityFile
        }
    }

    private func choose() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.showsHiddenFiles = true
        panel.directoryURL = SSHKeyScanner.sshDirectory
        if panel.runModal() == .OK, let url = panel.url {
            identityFile = url.path
        }
    }

    private func save() {
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let trimmedUser = username.trimmingCharacters(in: .whitespaces)
        guard !trimmedName.isEmpty, !trimmedUser.isEmpty else {
            failure = loc.t("identity.required")
            return
        }
        if authKind == .identityFile, identityFile.trimmingCharacters(in: .whitespaces).isEmpty {
            failure = loc.t("server.keyRequired")
            return
        }
        var value = identity ?? Identity(name: trimmedName, username: trimmedUser)
        value.name = trimmedName
        value.username = trimmedUser
        value.authKind = authKind
        value.identityFile = identityFile.trimmingCharacters(in: .whitespaces)
        do {
            try monitor.save(value)
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }
}
