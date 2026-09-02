import AppKit
import SwiftUI

/// Inventory of the private keys in ~/.ssh.
///
/// Read-only by design: this app has no business rewriting the directory
/// OpenSSH owns. It reports type, fingerprint and whether a passphrase is set,
/// and can copy a public key out for pasting into authorized_keys.
struct SSHKeysView: View {
    @Binding var search: String
    /// Incremented by the toolbar's refresh to trigger a rescan.
    @Binding var reloadToken: Int
    @Binding var generating: Bool

    @EnvironmentObject private var loc: Localization
    @State private var keys: [SSHKeyFile] = []
    @State private var loading = true
    @State private var copied: String?
    @State private var exporting: String?
    /// (server, message, isError) from the last export, shown as a banner.
    @State private var exportResult: (UUID, String, Bool)?
    @EnvironmentObject private var monitor: MonitorService
    @State private var failure: String?
    @State private var notice: String?
    @State private var pendingDelete: SSHKeyFile?

    private var rows: [SSHKeyFile] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return keys }
        return keys.filter {
            $0.name.lowercased().contains(query) || $0.comment.lowercased().contains(query)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            if let result = exportResult {
                HStack(spacing: 8) {
                    Image(systemName: result.2 ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                        .foregroundStyle(result.2 ? .red : .green)
                    Text(result.1).font(.caption).lineLimit(2).textSelection(.enabled)
                    Spacer(minLength: 8)
                    Button(loc.t("common.close")) { exportResult = nil }
                        .buttonStyle(.borderless)
                        .font(.caption)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(.background.secondary)
                Divider()
            }
            content
        }
    }

    private var content: some View {
        Group {
            if loading {
                ProgressView().controlSize(.small)
            } else if keys.isEmpty {
                VStack(spacing: 14) {
                    Image(systemName: "key")
                        .font(.system(size: 34))
                        .foregroundStyle(.tertiary)
                    Text(loc.t("keys.empty"))
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    VStack(spacing: 8) {
                        Button(loc.t("keys.generate")) { generating = true }
                            .frame(maxWidth: 320)
                        Button(loc.t("keys.importClipboard")) { importFromClipboard() }
                            .frame(maxWidth: 320)
                        Button(loc.t("keys.importFile")) { importFromFile() }
                            .frame(maxWidth: 320)
                    }
                }
                .padding(40)
            } else {
                List {
                    Section {
                        ForEach(rows) { key in row(key) }
                    } footer: {
                        VStack(alignment: .leading, spacing: 6) {
                            if let notice {
                                Label(notice, systemImage: "checkmark.circle.fill")
                                    .font(.caption).foregroundStyle(.green)
                            }
                            if let failure {
                                Label(failure, systemImage: "exclamationmark.triangle.fill")
                                    .font(.caption).foregroundStyle(.red)
                            }
                            HStack(spacing: 10) {
                                Button(loc.t("keys.importClipboard")) { importFromClipboard() }
                                Button(loc.t("keys.importFile")) { importFromFile() }
                            }
                            .controlSize(.small)
                            .padding(.top, 4)
                            Text(loc.t("keys.writeNote"))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
        .navigationTitle(loc.t("nav.sshKeys"))
        .task { await load() }
        .onChange(of: reloadToken) { _, _ in
            Task { await load() }
        }
        .sheet(isPresented: $generating) {
            KeyGenerator { message in
                notice = message
                Task { await load() }
            }
        }
        .confirmationDialog(
            pendingDelete.map { loc.t("keys.deleteConfirm", $0.name) } ?? "",
            isPresented: Binding(
                get: { pendingDelete != nil },
                set: { if !$0 { pendingDelete = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) {
                if let key = pendingDelete {
                    do {
                        try SSHKeyManager.delete(key)
                        Task { await load() }
                    } catch {
                        failure = error.localizedDescription
                    }
                }
                pendingDelete = nil
            }
            Button(loc.t("common.cancel"), role: .cancel) { pendingDelete = nil }
        }
    }

    private func row(_ key: SSHKeyFile) -> some View {
        HStack(spacing: 10) {
            Image(systemName: key.isEncrypted ? "lock.fill" : "key.fill")
                .foregroundStyle(key.isEncrypted ? .green : .secondary)
                .frame(width: 18)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(key.name).font(.body.weight(.medium))
                    // A key length is a spec, not a quantity: "4,096" is wrong.
                    Text(verbatim: "\(key.type) " + String(key.bits))
                        .font(.caption2)
                        .padding(.horizontal, 6).padding(.vertical, 1)
                        .background(.quaternary, in: Capsule())
                    if key.isEncrypted {
                        Text(loc.t("keys.protected")).font(.caption2).foregroundStyle(.green)
                    }
                    if key.isLegacyAlgorithm {
                        Label(loc.t("keys.legacy"), systemImage: "exclamationmark.triangle")
                            .font(.caption2).foregroundStyle(.orange)
                    }
                }
                Text(key.fingerprint)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                if !key.comment.isEmpty {
                    Text(key.comment).font(.caption2).foregroundStyle(.tertiary).lineLimit(1)
                }
            }

            Spacer()

            if copied == key.path {
                Label(loc.t("keys.copied"), systemImage: "checkmark")
                    .font(.caption).foregroundStyle(.green)
            }
            if key.hasPublicKey {
                Button(loc.t("keys.copyPublic")) { copyPublicKey(key) }
                    .controlSize(.small)
                // Straight to a host rather than copy-paste-into-a-terminal,
                // and it works for hosts behind a ProxyJump because it rides
                // the connection the app already has.
                Menu {
                    if monitor.servers.isEmpty {
                        Text(loc.t("nav.noServers"))
                    } else {
                        ForEach(monitor.servers) { server in
                            Button(server.name) { Task { await export(key, to: server) } }
                        }
                    }
                } label: {
                    Label(loc.t("keys.export"), systemImage: "square.and.arrow.up")
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .controlSize(.small)
                .help(loc.t("keys.exportHelp"))
                .disabled(exporting != nil)
                if exporting == key.path {
                    ProgressView().controlSize(.small)
                }
            }
            Button {
                NSWorkspace.shared.selectFile(key.path, inFileViewerRootedAtPath: "")
            } label: {
                Image(systemName: "folder")
            }
            .buttonStyle(.borderless)
            .help(loc.t("keys.reveal"))

            Button(role: .destructive) { pendingDelete = key } label: {
                Image(systemName: "trash")
            }
            .buttonStyle(.borderless)
        }
        .padding(.vertical, 3)
    }

    private func export(_ key: SSHKeyFile, to server: Server) async {
        guard let text = SSHKeyScanner.publicKey(for: key) else {
            exportResult = (server.id, loc.t("keys.exportNoPublic"), true)
            return
        }
        exporting = key.path
        defer { exporting = nil }
        do {
            let target = try monitor.target(for: server)
            let outcome = try await PublicKeyInstaller().install(publicKey: text, on: target)
            exportResult = (
                server.id,
                loc.t(outcome == .added ? "keys.exported" : "keys.exportAlready") + " · " + server.name,
                false
            )
        } catch {
            exportResult = (server.id, error.localizedDescription, true)
        }
    }

    private func copyPublicKey(_ key: SSHKeyFile) {
        guard let text = SSHKeyScanner.publicKey(for: key) else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        copied = key.path
        Task {
            try? await Task.sleep(for: .seconds(2))
            if copied == key.path { copied = nil }
        }
    }

    /// Imports whatever key text is on the pasteboard, naming the file after
    /// the key's own comment when it has one.
    private func importFromClipboard() {
        failure = nil
        notice = nil
        guard let text = NSPasteboard.general.string(forType: .string), !text.isEmpty else {
            failure = loc.t("keys.clipboardEmpty")
            return
        }
        do {
            let path = try SSHKeyManager.importKey(text: text, name: suggestedName())
            notice = loc.t("keys.imported", (path as NSString).lastPathComponent)
            Task { await load() }
        } catch {
            failure = error.localizedDescription
        }
    }

    private func importFromFile() {
        failure = nil
        notice = nil
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.showsHiddenFiles = true
        panel.directoryURL = SSHKeyScanner.sshDirectory
        guard panel.runModal() == .OK, let url = panel.url else { return }
        do {
            let path = try SSHKeyManager.importFile(at: url)
            notice = loc.t("keys.imported", (path as NSString).lastPathComponent)
            Task { await load() }
        } catch {
            failure = error.localizedDescription
        }
    }

    /// A free file name, since ~/.ssh already holds the user's own keys.
    private func suggestedName() -> String {
        let existing = Set(keys.map(\.name))
        var index = 1
        while existing.contains("imported_key_\(index)") { index += 1 }
        return "imported_key_\(index)"
    }

    private func load() async {
        loading = true
        keys = await SSHKeyScanner.scan()
        loading = false
    }
}

/// Generates a key pair with ssh-keygen.
private struct KeyGenerator: View {
    let onCreated: (String) -> Void

    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var name = "id_ed25519_new"
    @State private var type: SSHKeyManager.KeyType = .ed25519
    @State private var comment = ""
    @State private var passphrase = ""
    @State private var working = false
    @State private var failure: String?

    var body: some View {
        VStack(spacing: 0) {
            Form {
                TextField(loc.t("keys.keyName"), text: $name)
                Picker(loc.t("keys.keyType"), selection: $type) {
                    ForEach(SSHKeyManager.KeyType.allCases) { value in
                        Text(value.label).tag(value)
                    }
                }
                .pickerStyle(.segmented)
                TextField(loc.t("keys.comment"), text: $comment)
                SecureField(loc.t("keys.passphrase"), text: $passphrase)
                Label(loc.t("keys.writeNote"), systemImage: "lock.shield")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if let failure {
                    Label(failure, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red).font(.callout)
                }
            }
            .formStyle(.grouped)
            Divider()
            HStack {
                if working { ProgressView().controlSize(.small) }
                Spacer()
                Button(loc.t("common.cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(loc.t("keys.generate")) { Task { await generate() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(working)
            }
            .padding(12)
        }
        .frame(width: 460)
        .onAppear {
            if comment.isEmpty {
                comment = "\(NSUserName())@\(Host.current().localizedName ?? "mac")"
            }
        }
    }

    private func generate() async {
        failure = nil
        working = true
        defer { working = false }
        do {
            let path = try await SSHKeyManager.generate(
                name: name,
                type: type,
                comment: comment,
                passphrase: passphrase
            )
            onCreated(loc.t("keys.generated", (path as NSString).lastPathComponent))
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }
}
