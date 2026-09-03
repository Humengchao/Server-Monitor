import SwiftUI

/// Adopts hosts from ~/.ssh/config in one pass.
struct ImportSSHConfigView: View {
    @Environment(MonitorService.self) private var monitor
    @EnvironmentObject private var loc: Localization
    @Environment(\.dismiss) private var dismiss

    @State private var hosts: [SSHConfigHost] = []
    @State private var chosen: Set<String> = []
    @State private var failure: String?
    @State private var importedCount: Int?

    /// Aliases already present, matched on address so re-importing is a no-op.
    private var existingTargets: Set<String> {
        Set(monitor.servers.map { "\($0.username)@\($0.host):\($0.port)" })
    }

    private func isAlreadyAdded(_ host: SSHConfigHost) -> Bool {
        existingTargets.contains("\(host.user)@\(host.hostName):\(host.port)")
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            if hosts.isEmpty {
                ContentUnavailableView(
                    loc.t("import.none"),
                    systemImage: "doc.text.magnifyingglass",
                    description: Text(loc.t("import.noneHelp"))
                )
                .frame(height: 240)
            } else {
                list
            }
            Divider()
            footer
        }
        .frame(width: 620, height: 480)
        .onAppear { hosts = SSHConfigImporter.discover() }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(loc.t("import.title")).font(.headline)
            Text(loc.t("import.subtitle"))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
    }

    private var list: some View {
        List {
            ForEach(hosts) { host in
                let added = isAlreadyAdded(host)
                HStack(spacing: 10) {
                    Toggle("", isOn: Binding(
                        get: { chosen.contains(host.alias) },
                        set: { on in
                            if on { chosen.insert(host.alias) } else { chosen.remove(host.alias) }
                        }
                    ))
                    .labelsHidden()
                    .disabled(added)

                    VStack(alignment: .leading, spacing: 2) {
                        Text(host.alias).font(.body.weight(.medium))
                        // Built as a String so the port is not digit-grouped.
                        Text(verbatim: "\(host.user)@\(host.hostName):" + String(host.port))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    if added {
                        Text(loc.t("import.alreadyAdded"))
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                .padding(.vertical, 2)
            }
        }
    }

    private var footer: some View {
        HStack {
            Button(loc.t("import.selectAll")) {
                chosen = Set(hosts.filter { !isAlreadyAdded($0) }.map(\.alias))
            }
            if let failure {
                Text(failure).font(.caption).foregroundStyle(.red).lineLimit(1)
            }
            if let importedCount {
                Label(loc.t("import.done", "\(importedCount)"), systemImage: "checkmark.circle.fill")
                    .font(.caption).foregroundStyle(.green)
            }
            Spacer()
            Button(loc.t("common.close")) { dismiss() }
                .keyboardShortcut(.cancelAction)
            Button(loc.t("import.action")) { performImport() }
                .keyboardShortcut(.defaultAction)
                .buttonStyle(.borderedProminent)
                .disabled(chosen.isEmpty)
        }
        .padding(12)
    }

    private func performImport() {
        failure = nil
        var imported = 0
        var skipped: [String] = []

        for host in hosts where chosen.contains(host.alias) {
            // Stored as an alias rather than a copied key: ssh then applies the
            // entire Host block, so ProxyJump and per-host options keep working
            // and no private key leaves ~/.ssh.
            let server = Server(
                name: host.alias,
                host: host.hostName,
                port: host.port,
                username: host.user,
                authKind: .sshConfigAlias,
                sshAlias: host.alias,
                identityFile: host.identityFile ?? "",
                sortIndex: (try? monitor.database.nextSortIndex()) ?? 0
            )
            do {
                try monitor.addServer(server)
                imported += 1
            } catch {
                skipped.append(host.alias)
            }
        }

        importedCount = imported
        chosen.removeAll()
        if !skipped.isEmpty {
            failure = loc.t("import.skipped", skipped.joined(separator: ", "))
        }
        // Poll straight away so the new cards fill in rather than sitting empty.
        Task { await monitor.pollAll() }
    }
}
