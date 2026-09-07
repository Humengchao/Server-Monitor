import SwiftUI

/// Container list and actions for one server, over the shared SSH connection.
struct DockerPane: View {
    let server: Server

    @Environment(MonitorService.self) private var monitor
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
        .task(id: server.id) {
            await reload()
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: .seconds(15))
                } catch {
                    break
                }
                await refreshStats()
            }
        }
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
                .hint(loc.t("nav.terminal"))
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
        .hint(help)
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
    /// Read-only Docker queries are independent, and OpenSSH multiplexing can
    /// carry them on separate channels. Start them together so page-open time
    /// is bounded by the slowest query rather than the sum of five SSH calls.
    private func reload() async {
        loading = true
        defer { loading = false }
        do {
            let target = try monitor.target(for: server)
            let docker = monitor.docker
            async let containersResult = docker.listContainers(target: target)
            async let composeResult = docker.listComposeProjects(target: target)
            async let imagesResult = docker.listImages(target: target)
            async let volumesResult = docker.listVolumes(target: target)
            async let networksResult = docker.listNetworks(target: target)
            async let statsResult = docker.stats(target: target)

            containers = try await containersResult
            compose = (try? await composeResult) ?? []
            images = try await imagesResult
            volumes = try await volumesResult
            networks = try await networksResult
            failure = nil
            // Tolerated failing: it is decoration and can fail while the
            // engine is busy. The resource tables remain useful without it.
            stats = (try? await statsResult) ?? [:]
        } catch {
            if error is CancellationError { return }
            failure = error.localizedDescription
        }
    }

    private func refreshStats() async {
        guard !containers.isEmpty else { return }
        do {
            let target = try monitor.target(for: server)
            stats = try await monitor.docker.stats(target: target)
            failure = nil
        } catch {
            if error is CancellationError { return }
            // Preserve the last successful stats during a transient refresh
            // failure; the resource list is still valid.
        }
    }

    private func act(_ action: DockerClient.ContainerAction, _ container: DockerContainer) async {
        busy.insert(container.id)
        defer { busy.remove(container.id) }
        do {
            let target = try monitor.target(for: server)
            try await monitor.docker.perform(action, containerID: container.id, target: target)
            await reload()
        } catch {
            failure = error.localizedDescription
        }
    }

    private func showLogs(_ container: DockerContainer) async {
        do {
            let target = try monitor.target(for: server)
            let text = try await monitor.docker.logs(containerID: container.id, target: target)
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
