import SwiftUI

/// Saved commands, with a one-click run against any host.
struct SnippetsView: View {
    @Binding var search: String
    /// Driven by the window toolbar, which owns this destination's actions.
    @Binding var creating: Bool

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization

    @State private var snippets: [Snippet] = []
    @State private var selection: Snippet.ID?
    @State private var editing: Snippet?
    @State private var runTarget: Snippet?
    @State private var failure: String?

    private var rows: [Snippet] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return snippets }
        return snippets.filter {
            $0.name.lowercased().contains(query)
                || $0.command.lowercased().contains(query)
                || $0.category.lowercased().contains(query)
        }
    }

    var body: some View {
        Group {
            if snippets.isEmpty {
                ContentUnavailableView {
                    Label(loc.t("nav.snippets"), systemImage: "curlybraces")
                } description: {
                    Text(loc.t("snippet.empty"))
                } actions: {
                    Button(loc.t("snippet.new")) { creating = true }
                }
            } else {
                List(selection: $selection) {
                    ForEach(rows) { snippet in
                        row(snippet).tag(snippet.id)
                    }
                }
            }
        }
        .navigationTitle(loc.t("nav.snippets"))
        .onAppear(perform: reload)
        .sheet(isPresented: $creating) {
            SnippetEditor(snippet: nil, onSave: reload)
        }
        .sheet(item: $editing) { snippet in
            SnippetEditor(snippet: snippet, onSave: reload)
        }
        .sheet(item: $runTarget) { snippet in
            SnippetRunner(snippet: snippet)
        }
        .actionFailureAlert($failure)
    }

    private func row(_ snippet: Snippet) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(snippet.name).font(.body.weight(.medium))
                    if !snippet.category.isEmpty {
                        Text(snippet.category)
                            .font(.caption2)
                            .padding(.horizontal, 6).padding(.vertical, 1)
                            .background(.quaternary, in: Capsule())
                    }
                    if snippet.useCount > 0 {
                        Text(loc.t("snippet.runCount", "\(snippet.useCount)"))
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
                Text(snippet.summary)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            Button { runTarget = snippet } label: { Image(systemName: "play.fill") }
                .buttonStyle(.borderless)
                .help(loc.t("snippet.run"))
            Button { editing = snippet } label: { Image(systemName: "pencil") }
                .buttonStyle(.borderless)
            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(snippet.command, forType: .string)
            } label: {
                Image(systemName: "doc.on.doc")
            }
            .buttonStyle(.borderless)
            .help(loc.t("snippet.copy"))
            Button(role: .destructive) {
                failure = failureMessage { try monitor.deleteSnippet(id: snippet.id) }
                reload()
            } label: {
                Image(systemName: "trash")
            }
            .buttonStyle(.borderless)
        }
        .padding(.vertical, 3)
    }

    private func reload() {
        snippets = monitor.snippets()
    }
}

/// Create or edit one snippet.
private struct SnippetEditor: View {
    let snippet: Snippet?
    let onSave: () -> Void

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var category = ""
    @State private var command = ""
    @State private var notes = ""
    @State private var failure: String?

    var body: some View {
        VStack(spacing: 0) {
            Form {
                TextField(loc.t("snippet.name"), text: $name)
                TextField(loc.t("snippet.category"), text: $category)
                VStack(alignment: .leading, spacing: 4) {
                    Text(loc.t("snippet.command")).font(.caption).foregroundStyle(.secondary)
                    TextEditor(text: $command)
                        .font(.system(.body, design: .monospaced))
                        .frame(height: 140)
                        .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(.separator))
                }
                TextField(loc.t("server.notes"), text: $notes, axis: .vertical)
                    .lineLimit(2...4)
                if let failure {
                    Label(failure, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.red).font(.callout)
                }
            }
            .formStyle(.grouped)

            Divider()
            HStack {
                Spacer()
                Button(loc.t("common.cancel")) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(loc.t("common.save")) { save() }
                    .buttonStyle(.borderedProminent)
            }
            .padding(12)
        }
        .frame(width: 560)
        .onAppear {
            guard let snippet else { return }
            name = snippet.name
            category = snippet.category
            command = snippet.command
            notes = snippet.notes
        }
    }

    private func save() {
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let trimmedCommand = command.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedName.isEmpty, !trimmedCommand.isEmpty else {
            failure = loc.t("snippet.required")
            return
        }
        var value = snippet ?? Snippet(name: trimmedName, command: trimmedCommand)
        value.name = trimmedName
        value.command = trimmedCommand
        value.category = category.trimmingCharacters(in: .whitespaces)
        value.notes = notes
        do {
            try monitor.save(value)
            onSave()
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }
}

/// Picks a host, runs the snippet, shows the output.
private struct SnippetRunner: View {
    let snippet: Snippet

    @EnvironmentObject private var monitor: MonitorService
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var selected: UUID?
    @State private var output = ""
    @State private var running = false
    @State private var failure: String?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(snippet.name).font(.headline)
                Spacer()
                Picker("", selection: $selected) {
                    ForEach(monitor.servers) { server in
                        Text(server.name).tag(Optional(server.id))
                    }
                }
                .labelsHidden()
                .frame(width: 180)
                Button(loc.t("snippet.run")) { Task { await run() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(running || selected == nil)
                if running { ProgressView().controlSize(.small) }
            }
            .padding(12)
            Divider()

            Text(snippet.command)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12).padding(.vertical, 8)
            Divider()

            ScrollView {
                Text(output.isEmpty ? (failure ?? loc.t("snippet.noOutput")) : output)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(failure == nil ? Color.primary : Color.red)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
            }

            Divider()
            HStack {
                Spacer()
                Button(loc.t("common.close")) { dismiss() }
                    .keyboardShortcut(.cancelAction)
            }
            .padding(12)
        }
        .frame(width: 760, height: 520)
        .onAppear { selected = monitor.servers.first?.id }
    }

    private func run() async {
        guard let id = selected, let server = monitor.servers.first(where: { $0.id == id }) else { return }
        running = true
        failure = nil
        output = ""
        defer { running = false }
        do {
            output = try await monitor.run(snippet: snippet, on: server)
        } catch {
            failure = error.localizedDescription
        }
    }
}
