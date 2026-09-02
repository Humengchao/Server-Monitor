import SwiftUI

// The Docker pane's listings, as views over their data rather than computed
// properties over its private state: each is then renderable on its own, which
// is the only way to look at them without a window server.

/// DockerImage rows from `docker images`.
struct DockerImageTable: View {
    let images: [DockerImage]
    @EnvironmentObject private var loc: Localization

    var body: some View {
    Table(images) {
        TableColumn(loc.t("docker.repository")) { image in
            HStack(spacing: 6) {
                Text(image.isDangling ? String(image.id.prefix(12)) : image.repository)
                    .lineLimit(1)
                    .truncationMode(.middle)
                if image.isDangling {
                    Text(loc.t("docker.dangling"))
                        .font(.caption2)
                        .foregroundStyle(.orange)
                }
            }
        }
        TableColumn(loc.t("docker.tag")) { image in
            Text(image.isDangling ? "—" : image.tag)
                .lineLimit(1)
                .foregroundStyle(.secondary)
        }
        .width(min: 80, max: 180)
        TableColumn(loc.t("docker.size")) { image in
            Text(image.size).monospacedDigit().foregroundStyle(.secondary)
        }
        .width(min: 70, max: 110)
        TableColumn(loc.t("docker.created")) { image in
            Text(image.created).lineLimit(1).foregroundStyle(.secondary)
        }
        .width(min: 90, max: 160)
    }
    }
}

/// DockerVolume rows from `docker volume ls`.
struct DockerVolumeTable: View {
    let volumes: [DockerVolume]
    @EnvironmentObject private var loc: Localization

    var body: some View {
    Table(volumes) {
        TableColumn(loc.t("docker.name")) { volume in
            Text(volume.name).lineLimit(1).truncationMode(.middle)
        }
        TableColumn(loc.t("docker.driver")) { volume in
            Text(volume.driver).foregroundStyle(.secondary)
        }
        .width(min: 60, max: 100)
        TableColumn(loc.t("docker.mountpoint")) { volume in
            Text(volume.mountpoint)
                .lineLimit(1)
                .truncationMode(.middle)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
        }
    }
    }
}

/// DockerNetwork rows from `docker network ls`.
struct DockerNetworkTable: View {
    let networks: [DockerNetwork]
    @EnvironmentObject private var loc: Localization

    var body: some View {
    Table(networks) {
        TableColumn(loc.t("docker.name")) { network in
            HStack(spacing: 6) {
                Text(network.name).lineLimit(1)
                if network.isBuiltIn {
                    Text(loc.t("docker.builtIn"))
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
        }
        TableColumn(loc.t("docker.driver")) { network in
            Text(network.driver).foregroundStyle(.secondary)
        }
        .width(min: 70, max: 120)
        TableColumn(loc.t("docker.scope")) { network in
            Text(network.scope).foregroundStyle(.secondary)
        }
        .width(min: 60, max: 100)
    }
    }
}

/// DockerComposeProject rows from `docker compose ls -a`.
struct DockerComposeTable: View {
    let projects: [DockerComposeProject]
    @EnvironmentObject private var loc: Localization

    var body: some View {
        Table(projects) {
            TableColumn(loc.t("docker.name")) { project in
                HStack(spacing: 6) {
                    StatusDot(status: project.isRunning
                        ? .online(at: Date())
                        : .offline(reason: project.status))
                    Text(project.name).lineLimit(1)
                }
            }
            TableColumn(loc.t("docker.status")) { project in
                // Split out of "exited(2), running(1)" so the states read as
                // separate facts rather than one string to decode.
                HStack(spacing: 8) {
                    ForEach(project.counts, id: \.state) { entry in
                        HStack(spacing: 3) {
                            Text(verbatim: String(entry.count))
                                .font(.caption.weight(.medium))
                                .monospacedDigit()
                            Text(entry.state)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .foregroundStyle(entry.state == "running" ? Color.green : .secondary)
                    }
                    if project.counts.isEmpty {
                        Text(project.status).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            .width(min: 120, max: 220)
            TableColumn(loc.t("docker.configPath")) { project in
                Text(project.directory)
                    .lineLimit(1)
                    .truncationMode(.head)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
    }
}
