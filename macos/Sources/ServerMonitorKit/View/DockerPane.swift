import SwiftUI

/// Container list and actions for one server, over the shared SSH connection.
struct DockerPane: View {
    let server: Server

    @Environment(\.monitorService) private var monitor
    @EnvironmentObject private var loc: Localization

    @State private var containers: [DockerContainer] = []
    @State private var compose: [DockerComposeProject] = []
    @State private var images: [DockerImage] = []
    @State private var volumes: [DockerVolume] = []
    @State private var networks: [DockerNetwork] = []
    @State private var stats: [String: DockerContainerStats] = [:]
    @State private var tab: Resource = .containers
    @State private var loading = false
    @State private var failure: String?
    @State private var busy: Set<String> = []
    @State private var logs: LogsSheet?
    @State private var exec: DockerContainer?

    /// The engine resources this pane lists, matching `docker`'s own nouns.
    enum Resource: String, CaseIterable, Identifiable {
        case containers, compose, images, volumes, networks
        var id: String { rawValue }

        var labelKey: String { "docker.\(rawValue)" }

        var emptyKey: String {
            switch self {
            case .containers: return "docker.empty"
            case .compose: return "docker.noCompose"
            case .images: return "docker.noImages"
            case .volumes: return "docker.noVolumes"
            case .networks: return "docker.noNetworks"
            }
        }
    }

    /// Identifiable wrapper so `.sheet(item:)` can carry the text.
    struct LogsSheet: Identifiable {
        let id: String
        let title: String
        let text: String
    }

    var body: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            content
        }
        .task { await reload() }
        .sheet(item: $logs) { sheet in
            logsView(sheet)
        }
        .sheet(item: $exec) { container in
            execView(container)
        }
    }

    private var toolbar: some View {
        HStack {
            Picker("", selection: $tab) {
                ForEach(Resource.allCases) { resource in
                    Text(loc.t(resource.labelKey) + count(for: resource)).tag(resource)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(maxWidth: 500)
            if !server.dockerVersion.isEmpty {
                Text(server.dockerVersion).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if loading { ProgressView().controlSize(.small) }
            Button(loc.t("common.refresh"), systemImage: "arrow.clockwise") {
                Task { await reload() }
            }
            .controlSize(.small)
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
        } else if isEmpty(tab) && !loading {
            ContentUnavailableView(loc.t(tab.emptyKey), systemImage: "shippingbox")
        } else {
            switch tab {
            case .containers: containerTable
            case .compose: DockerComposeTable(projects: compose)
            case .images: DockerImageTable(images: images)
            case .volumes: DockerVolumeTable(volumes: volumes)
            case .networks: DockerNetworkTable(networks: networks)
            }
        }
    }

    private func count(for resource: Resource) -> String {
        let value: Int
        switch resource {
        case .containers: value = containers.count
        case .compose: value = compose.count
        case .images: value = images.count
        case .volumes: value = volumes.count
        case .networks: value = networks.count
        }
        return value > 0 ? " \(value)" : ""
    }

    private func isEmpty(_ resource: Resource) -> Bool {
        switch resource {
        case .containers: return containers.isEmpty
        case .compose: return compose.isEmpty
        case .images: return images.isEmpty
        case .volumes: return volumes.isEmpty
        case .networks: return networks.isEmpty
        }
    }

    private var containerTable: some View {
        Table(containers) {
                TableColumn(loc.t("docker.name")) { container in
                    HStack(spacing: 6) {
                        StatusDot(status: container.isRunning
                            ? .online(at: Date())
                            : .offline(reason: container.status))
                        Text(container.name).lineLimit(1)
                    }
                }
                TableColumn(loc.t("docker.image")) { container in
                    Text(container.image).lineLimit(1).foregroundStyle(.secondary)
                }
                TableColumn(loc.t("docker.status")) { container in
                    Text(container.status).lineLimit(1).foregroundStyle(.secondary)
                }
                TableColumn(loc.t("metric.cpu")) { container in
                    // Only running containers are sampled; a dash is honest
                    // where 0% would read as "running and idle".
                    if let sample = stats[container.shortID] {
                        Text(Format.percent(sample.cpuPercent))
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                    } else {
                        Text("—").foregroundStyle(.tertiary)
                    }
                }
                .width(min: 60, max: 90)
                TableColumn(loc.t("docker.memUsage")) { container in
                    if let sample = stats[container.shortID] {
                        Text(sample.memoryUsage)
                            .lineLimit(1)
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                    } else {
                        Text("—").foregroundStyle(.tertiary)
                    }
                }
                .width(min: 110, max: 170)
                TableColumn("") { container in
                    actions(for: container)
                }
                .width(min: 210)
        }
    }




    private func actions(for container: DockerContainer) -> some View {
        HStack(spacing: 4) {
            if busy.contains(container.id) {
                ProgressView().controlSize(.small)
            } else if container.isRunning {
                iconButton("stop.fill", loc.t("docker.stop")) { await act(.stop, container) }
                iconButton("arrow.clockwise", loc.t("docker.restart")) { await act(.restart, container) }
            } else {
                iconButton("play.fill", loc.t("docker.start")) { await act(.start, container) }
            }
            iconButton("doc.plaintext", loc.t("docker.logs")) { await showLogs(container) }
            if container.isRunning {
                Button {
                    exec = container
                } label: {
                    Image(systemName: "terminal")
                }
                .buttonStyle(.borderless)
                .help(loc.t("nav.terminal"))
                .accessibilityLabel(loc.t("nav.terminal"))
            }
        }
    }

    private func iconButton(
        _ symbol: String,
        _ help: String,
        action: @escaping () async -> Void
    ) -> some View {
        Button {
            Task { await action() }
        } label: {
            Image(systemName: symbol)
        }
        .buttonStyle(.borderless)
        .help(help)
        .accessibilityLabel(help)
    }

    private func logsView(_ sheet: LogsSheet) -> some View {
        VStack(spacing: 0) {
            HStack {
                Text(sheet.title).font(.headline)
                Spacer()
                Button(loc.t("common.close")) { logs = nil }
            }
            .padding(12)
            Divider()
            ScrollView {
                Text(sheet.text)
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
            }
        }
        .frame(width: 760, height: 480)
    }

    private func execView(_ container: DockerContainer) -> some View {
        VStack(spacing: 0) {
            HStack {
                Text(container.name).font(.headline)
                Spacer()
                Button(loc.t("common.close")) { exec = nil }
            }
            .padding(12)
            Divider()
            // A login shell inside the container, with sh as the fallback for
            // images that have no bash.
            TerminalPane(
                server: server,
                remoteCommand: "docker exec -it \(shellQuoted(container.id)) sh -c 'command -v bash >/dev/null && exec bash || exec sh'"
            )
        }
        .frame(width: 860, height: 560)
    }

    private func shellQuoted(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    // MARK: - Actions

    /// Fetches every listing in one pass.
    ///
    /// Sequentially rather than concurrently on purpose: all five calls share
    /// one multiplexed SSH connection, and `docker stats` alone takes a couple
    /// of seconds because the engine has to sample twice. Firing them together
    /// would open five channels to save nothing.
    private func reload() async {
        loading = true
        defer { loading = false }
        do {
            let target = try monitor.required.target(for: server)
            let docker = try monitor.required.docker
            containers = try await docker.listContainers(target: target)
            compose = (try? await docker.listComposeProjects(target: target)) ?? []
            images = try await docker.listImages(target: target)
            volumes = try await docker.listVolumes(target: target)
            networks = try await docker.listNetworks(target: target)
            failure = nil
            // Last, and tolerated failing: it is the slowest call and the only
            // one that is decoration. Losing it must not blank the tables.
            stats = (try? await docker.stats(target: target)) ?? [:]
        } catch {
            if error is CancellationError { return }
            failure = error.localizedDescription
        }
    }

    private func act(_ action: DockerClient.ContainerAction, _ container: DockerContainer) async {
        busy.insert(container.id)
        defer { busy.remove(container.id) }
        do {
            let target = try monitor.required.target(for: server)
            try await monitor.required.docker.perform(action, containerID: container.id, target: target)
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func showLogs(_ container: DockerContainer) async {
        do {
            let target = try monitor.required.target(for: server)
            let text = try await monitor.required.docker.logs(containerID: container.id, target: target)
            logs = LogsSheet(id: container.id, title: container.name, text: text)
        } catch {
            failure = error.localizedDescription
        }
    }
}

/// `sheet(item:)` needs a Binding<Item?>; DockerContainer is already Identifiable.
private extension View {
    func sheet<Item: Identifiable, Content: View>(
        item: Binding<Item?>,
        @ViewBuilder content: @escaping (Item) -> Content
    ) -> some View {
        sheet(isPresented: Binding(
            get: { item.wrappedValue != nil },
            set: { if !$0 { item.wrappedValue = nil } }
        )) {
            if let value = item.wrappedValue {
                content(value)
            }
        }
    }
}
