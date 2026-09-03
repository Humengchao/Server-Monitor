import AppKit
import SwiftUI
import UniformTypeIdentifiers

/// Remote file browser for one host.
struct SFTPView: View {
    let server: Server

    @Environment(\.monitorService) private var monitor
    @EnvironmentObject private var loc: Localization

    @State private var target: SSHTarget?
    @State private var path = ""
    @State private var files: [RemoteFile] = []
    @State private var selection: Set<RemoteFile.ID> = []
    @State private var loading = false
    @State private var failure: String?
    @State private var busy: String?
    /// 0–1 while a transfer is running.
    @State private var progress: Double?
    /// Directories visited, so Back can walk out again.
    @State private var backStack: [String] = []
    @State private var renaming: RemoteFile?
    @State private var pendingDelete: RemoteFile?
    @State private var pendingBulkDelete = false

    var body: some View {
        VStack(spacing: 0) {
            pathBar
            Divider()
            content
        }
        .task { await start() }
        .sheet(item: $renaming) { file in
            RenameSheet(file: file) { newName in
                Task { await rename(file, to: newName) }
            }
        }
        .confirmationDialog(
            pendingDelete.map { loc.t("sftp.deleteConfirm", $0.name) } ?? "",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) {
                if let file = pendingDelete { Task { await remove(file) } }
                pendingDelete = nil
            }
            Button(loc.t("common.cancel"), role: .cancel) { pendingDelete = nil }
        }
        .confirmationDialog(
            loc.t("sftp.deleteSelectedConfirm", "\(selection.count)"),
            isPresented: $pendingBulkDelete,
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) {
                Task { await deleteSelected() }
            }
            Button(loc.t("common.cancel"), role: .cancel) {}
        }
    }

    private var pathBar: some View {
        HStack(spacing: 8) {
            Button {
                Task { await goBack() }
            } label: {
                Image(systemName: "chevron.left")
            }
            .disabled(backStack.isEmpty)
            .hint(loc.t("sftp.back"))

            Button {
                Task { await open(parentPath) }
            } label: {
                Image(systemName: "arrow.up")
            }
            .disabled(path == "/" || path.isEmpty)
            .hint(loc.t("sftp.up"))

            Text(path.isEmpty ? "…" : path)
                .font(.system(.caption, design: .monospaced))
                .lineLimit(1)
                .truncationMode(.head)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)

            if let progress {
                ProgressView(value: progress)
                    .progressViewStyle(.linear)
                    .frame(width: 110)
            } else if loading || busy != nil {
                ProgressView().controlSize(.small)
            }
            if let busy {
                Text(busy).font(.caption2).foregroundStyle(.secondary)
            }
            if !selection.isEmpty {
                Text(verbatim: "\(selection.count)").font(.caption2).foregroundStyle(.secondary)
                Button {
                    Task { await downloadSelected() }
                } label: {
                    Image(systemName: "square.and.arrow.down.on.square")
                }
                .hint(loc.t("sftp.downloadSelected"))
                Button(role: .destructive) {
                    pendingBulkDelete = true
                } label: {
                    Image(systemName: "trash")
                }
                .hint(loc.t("common.delete"))
            }

            Button { Task { await reload() } } label: { Image(systemName: "arrow.clockwise") }
                .hint(loc.t("common.refresh"))
            Button { Task { await newFolder() } } label: { Image(systemName: "folder.badge.plus") }
                .hint(loc.t("sftp.newFolder"))
            Button { Task { await upload() } } label: { Image(systemName: "square.and.arrow.up") }
                .hint(loc.t("sftp.upload"))
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private var content: some View {
        if let failure {
            ContentUnavailableView(
                loc.t("common.error"),
                systemImage: "exclamationmark.triangle",
                description: Text(failure)
            )
        } else if files.isEmpty && !loading {
            ContentUnavailableView(loc.t("sftp.emptyDir"), systemImage: "folder")
        } else {
            Table(files, selection: $selection) {
                TableColumn(loc.t("sftp.name")) { file in
                    HStack(spacing: 6) {
                        Image(systemName: file.icon)
                            .foregroundStyle(file.isDirectory ? Color.accentColor : .secondary)
                        Text(file.name).lineLimit(1)
                    }
                    .contentShape(Rectangle())
                    .onTapGesture(count: 2) {
                        if file.isDirectory { Task { await open(file.path) } }
                    }
                }
                TableColumn(loc.t("sftp.size")) { file in
                    Text(file.isDirectory ? "—" : Format.bytes(file.size))
                        .monospacedDigit()
                        .foregroundStyle(.secondary)
                }
                .width(90)
                TableColumn(loc.t("sftp.modified")) { file in
                    Text(file.modified.map { Self.dateFormatter.string(from: $0) } ?? "—")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .width(140)
                TableColumn(loc.t("sftp.mode")) { file in
                    Text(file.mode)
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(.tertiary)
                }
                .width(96)
                TableColumn("") { file in
                    HStack(spacing: 4) {
                        if !file.isDirectory {
                            Button { Task { await download(file) } } label: {
                                Image(systemName: "square.and.arrow.down")
                            }
                            .buttonStyle(.borderless)
                            .hint(loc.t("sftp.download"))
                        }
                        Button { renaming = file } label: { Image(systemName: "pencil") }
                            .hint(loc.t("sftp.rename"))
                            .buttonStyle(.borderless)
                        Button(role: .destructive) { pendingDelete = file } label: {
                            Image(systemName: "trash")
                        }
                        .hint(loc.t("common.delete"))
                        .buttonStyle(.borderless)
                    }
                }
                .width(96)
            }
        }
    }

    private static let dateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .short
        formatter.timeStyle = .short
        return formatter
    }()

    private var parentPath: String {
        let parent = (path as NSString).deletingLastPathComponent
        return parent.isEmpty ? "/" : parent
    }

    // MARK: - Actions

    private func start() async {
        do {
            let monitor = try monitor.required
            let resolved = try monitor.target(for: server)
            target = resolved
            let home = try await monitor.sftp.home(on: resolved)
            await open(home, pushHistory: false)
        } catch {
            failure = error.localizedDescription
        }
    }

    private func open(_ newPath: String, pushHistory: Bool = true) async {
        guard let target else { return }
        loading = true
        defer { loading = false }
        do {
            let listing = try await monitor.required.sftp.list(newPath, on: target)
            if pushHistory, !path.isEmpty, path != newPath {
                backStack.append(path)
            }
            path = newPath
            files = listing
            failure = nil
        } catch {
            failure = error.localizedDescription
        }
    }

    private func goBack() async {
        guard let previous = backStack.popLast() else { return }
        await open(previous, pushHistory: false)
    }

    private func reload() async {
        await open(path, pushHistory: false)
    }

    private func newFolder() async {
        guard let target else { return }
        let alert = NSAlert()
        alert.messageText = loc.t("sftp.newFolder")
        alert.addButton(withTitle: loc.t("common.save"))
        alert.addButton(withTitle: loc.t("common.cancel"))
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        alert.accessoryView = field
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        let name = field.stringValue.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return }
        busy = loc.t("sftp.newFolder")
        defer { busy = nil }
        do {
            try await monitor.required.sftp.makeDirectory("\(path)/\(name)", on: target)
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func rename(_ file: RemoteFile, to newName: String) async {
        guard let target, !newName.isEmpty else { return }
        busy = loc.t("sftp.rename")
        defer { busy = nil }
        do {
            try await monitor.required.sftp.rename(file, to: newName, on: target)
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func remove(_ file: RemoteFile) async {
        guard let target else { return }
        busy = loc.t("common.delete")
        defer { busy = nil }
        do {
            try await monitor.required.sftp.remove(file, on: target)
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func download(_ file: RemoteFile) async {
        guard let target else { return }
        let panel = NSSavePanel()
        panel.nameFieldStringValue = file.name
        guard panel.runModal() == .OK, let url = panel.url else { return }
        busy = loc.t("sftp.downloading")
        progress = 0
        defer { busy = nil; progress = nil }
        do {
            try await monitor.required.sftp.download(file, to: url, on: target) { value in
                Task { @MainActor in progress = value }
            }
        } catch {
            failure = error.localizedDescription
        }
    }

    /// Downloads every selected file into one chosen directory.
    private func downloadSelected() async {
        guard let target else { return }
        let chosen = files.filter { selection.contains($0.id) && !$0.isDirectory }
        guard !chosen.isEmpty else { return }

        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, let directory = panel.url else { return }

        defer { busy = nil; progress = nil }
        for (index, file) in chosen.enumerated() {
            busy = "\(loc.t("sftp.downloading")) \(index + 1)/\(chosen.count)"
            progress = 0
            do {
                try await monitor.required.sftp.download(
                    file,
                    to: directory.appendingPathComponent(file.name),
                    on: target
                ) { value in
                    Task { @MainActor in progress = value }
                }
            } catch {
                failure = error.localizedDescription
                return
            }
        }
        selection.removeAll()
    }

    private func deleteSelected() async {
        guard let target else { return }
        let chosen = files.filter { selection.contains($0.id) }
        busy = loc.t("common.delete")
        defer { busy = nil }
        for file in chosen {
            do {
                try await monitor.required.sftp.remove(file, on: target)
            } catch {
                failure = error.localizedDescription
                break
            }
        }
        selection.removeAll()
        await reload()
    }

    private func upload() async {
        guard let target else { return }
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let url = panel.url else { return }
        busy = loc.t("sftp.uploading")
        defer { busy = nil; progress = nil }
        do {
            progress = 0
            try await monitor.required.sftp.upload(url, toDirectory: path, on: target) { value in
                Task { @MainActor in progress = value }
            }
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }
}

private struct RenameSheet: View {
    let file: RemoteFile
    let onRename: (String) -> Void

    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(loc.t("sftp.rename")).font(.headline)
            TextField("", text: $name)
                .textFieldStyle(.roundedBorder)
            HStack {
                Spacer()
                Button(loc.t("common.cancel")) { dismiss() }.keyboardShortcut(.cancelAction)
                Button(loc.t("common.save")) {
                    onRename(name.trimmingCharacters(in: .whitespaces))
                    dismiss()
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(16)
        .frame(width: 380)
        .onAppear { name = file.name }
    }
}
