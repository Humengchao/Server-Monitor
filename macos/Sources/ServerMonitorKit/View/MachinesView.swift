import SwiftUI

/// Machine inventory: groups on top, then the machines themselves.
struct MachinesView: View {
    @Binding var search: String
    @Binding var creatingGroup: Bool
    let onOpen: (UUID) -> Void

    @Environment(MonitorService.self) private var monitor
    @EnvironmentObject private var loc: Localization

    @State private var editingServer: Server?
    @State private var editingGroup: MachineGroup?
    @State private var pendingGroupDelete: MachineGroup?
    @State private var failure: String?

    private func matches(_ server: Server) -> Bool {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return true }
        return server.name.lowercased().contains(query)
            || server.host.lowercased().contains(query)
            || server.tags.contains { $0.lowercased().contains(query) }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                groupsSection
                machinesSection
            }
            .padding(16)
        }
        .navigationTitle(loc.t("nav.machines"))
        .sheet(isPresented: $creatingGroup) { GroupEditor(group: nil) }
        .sheet(item: $editingGroup) { group in GroupEditor(group: group) }
        .sheet(item: $editingServer) { server in ServerEditorView(mode: .edit(server)) }
        .confirmationDialog(
            pendingGroupDelete.map { loc.t("group.deleteConfirm", $0.name) } ?? "",
            isPresented: Binding(
                get: { pendingGroupDelete != nil },
                set: { if !$0 { pendingGroupDelete = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button(loc.t("common.delete"), role: .destructive) {
                if let group = pendingGroupDelete {
                    failure = failureMessage { try monitor.deleteGroup(id: group.id) }
                }
                pendingGroupDelete = nil
            }
            Button(loc.t("common.cancel"), role: .cancel) { pendingGroupDelete = nil }
        }
        .actionFailureAlert($failure)
    }

    // MARK: - Groups

    private var groupsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(loc.t("group.title")).font(.headline)

            if monitor.groups.isEmpty {
                VStack(spacing: 12) {
                    Image(systemName: "square.stack.3d.up.slash")
                        .font(.system(size: 30))
                        .foregroundStyle(.tertiary)
                    Text(loc.t("group.empty"))
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    Button(loc.t("group.new")) { creatingGroup = true }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 28)
                .background(cardBackground)
            } else {
                LazyVGrid(
                    columns: [GridItem(.adaptive(minimum: 220, maximum: 340), spacing: 12)],
                    spacing: 12
                ) {
                    ForEach(monitor.groups) { group in
                        groupCard(group)
                    }
                }
            }
        }
    }

    private func groupCard(_ group: MachineGroup) -> some View {
        let members = monitor.servers(in: group)
        let online = members.filter { (monitor.status[$0.id] ?? .unknown).isOnline }.count
        return HStack(spacing: 10) {
            RoundedRectangle(cornerRadius: 4)
                .fill(group.color)
                .frame(width: 4, height: 34)
            VStack(alignment: .leading, spacing: 3) {
                Text(group.name).font(.body.weight(.medium)).lineLimit(1)
                Text(verbatim: "\(loc.t("group.machines", "\(members.count)")) · \(online) \(loc.t("common.online"))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 4)
            Button { editingGroup = group } label: { Image(systemName: "pencil") }
                .hint(loc.t("common.edit"))
                .buttonStyle(.borderless)
            Button(role: .destructive) { pendingGroupDelete = group } label: {
                Image(systemName: "trash")
            }
            .hint(loc.t("common.delete"))
            .buttonStyle(.borderless)
        }
        .padding(12)
        .background(cardBackground)
    }

    // MARK: - Machines

    private var machinesSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(loc.t("machines.all")).font(.headline)

            if monitor.servers.isEmpty {
                Text(loc.t("dashboard.empty"))
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 28)
                    .background(cardBackground)
            } else {
                ForEach(monitor.groups) { group in
                    let members = monitor.servers(in: group).filter(matches)
                    if !members.isEmpty {
                        groupedMachines(title: group.name, tint: group.color, servers: members)
                    }
                }
                let loose = monitor.ungroupedServers.filter(matches)
                if !loose.isEmpty {
                    groupedMachines(
                        title: monitor.groups.isEmpty ? "" : loc.t("group.none"),
                        tint: .secondary,
                        servers: loose
                    )
                }
            }
        }
    }

    private func groupedMachines(title: String, tint: Color, servers: [Server]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if !title.isEmpty {
                HStack(spacing: 6) {
                    Circle().fill(tint).frame(width: 7, height: 7)
                    Text(title).font(.subheadline).foregroundStyle(.secondary)
                }
            }
            LazyVGrid(
                columns: [GridItem(.adaptive(minimum: 260, maximum: 400), spacing: 12)],
                spacing: 12
            ) {
                ForEach(servers) { server in
                    machineCard(server)
                }
            }
        }
    }

    private func machineCard(_ server: Server) -> some View {
        HStack(spacing: 10) {
            StatusDot(status: monitor.status[server.id] ?? .unknown)
            Image(systemName: "server.rack")
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 5) {
                    if !server.flag.isEmpty { Text(server.flag).font(.caption) }
                    Text(server.name).font(.body.weight(.medium)).lineLimit(1)
                }
                Text(server.displayTarget)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 4)
            Menu {
                Button(loc.t("group.none")) {
                    failure = failureMessage { try monitor.assign(server, to: nil) }
                }
                Divider()
                ForEach(monitor.groups) { group in
                    Button(group.name) {
                        failure = failureMessage { try monitor.assign(server, to: group) }
                    }
                }
            } label: {
                Image(systemName: "folder.badge.gearshape")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .hint(loc.t("group.assign"))

            Button { editingServer = server } label: { Image(systemName: "pencil") }
                .hint(loc.t("common.edit"))
                .buttonStyle(.borderless)
        }
        .padding(12)
        .background(cardBackground)
        .contentShape(Rectangle())
        .onTapGesture { onOpen(server.id) }
    }
}

/// Create or rename a group.
private struct GroupEditor: View {
    let group: MachineGroup?

    @Environment(MonitorService.self) private var monitor
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var colorName = "blue"
    @State private var failure: String?

    var body: some View {
        VStack(spacing: 0) {
            Form {
                TextField(loc.t("group.name"), text: $name)
                Picker(loc.t("group.color"), selection: $colorName) {
                    ForEach(MachineGroup.palette, id: \.self) { value in
                        HStack {
                            Circle().fill(MachineGroup.color(named: value)).frame(width: 10, height: 10)
                            Text(value.capitalized)
                        }
                        .tag(value)
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
                Button(loc.t("common.save")) { save() }
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
            }
            .padding(12)
        }
        .frame(width: 420)
        .onAppear {
            guard let group else { return }
            name = group.name
            colorName = group.colorName
        }
    }

    private func save() {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else {
            failure = loc.t("group.required")
            return
        }
        var value = group ?? MachineGroup(name: trimmed, sortIndex: monitor.nextGroupSortIndex())
        value.name = trimmed
        value.colorName = colorName
        do {
            try monitor.save(value)
            dismiss()
        } catch {
            failure = error.localizedDescription
        }
    }
}
