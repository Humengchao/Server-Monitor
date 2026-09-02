import SwiftUI

/// The cards on the machine screen: the compact ones in a balanced column
/// grid, then the two tall, list-shaped ones — Docker and processes — each
/// across the full width, stacked.
///
/// The tall pair used to sit in the grid too. Placed side by side in different
/// columns, whichever was shorter left a hole beneath it the height of the
/// difference; a list is also simply better read at full width.
struct MachineCardsLayout: View {
    let server: Server
    let snapshot: MetricSnapshot
    let samples: [MetricSample]
    let isWindows: Bool
    /// The pane's content width, from a GeometryReader outside the scroll view.
    let width: CGFloat
    /// Render-check only: containers for the Docker card without a host.
    var previewContainers: [(DockerContainer, DockerContainerStats?)]? = nil

    private var compact: [StaticGrid<AnyView>.Item] {
        // Weights are rough relative heights, only to steer column balance.
        var items: [StaticGrid<AnyView>.Item] = [
            .init(id: "cpu", weight: 3, view: AnyView(StatusCPUCard(snapshot: snapshot))),
            .init(id: "memory", weight: 2.5, view: AnyView(StatusMemoryCard(snapshot: snapshot))),
            .init(id: "load", weight: 2, view: AnyView(StatusLoadCard(snapshot: snapshot, samples: samples, isWindows: isWindows))),
            .init(id: "storage", weight: 1.5, view: AnyView(StatusStorageCard(snapshot: snapshot))),
            .init(id: "network", weight: 1.5, view: AnyView(StatusNetworkCard(snapshot: snapshot))),
            .init(id: "machine", weight: 2, view: AnyView(StatusMachineCard(server: server, snapshot: snapshot))),
            .init(id: "ip", weight: 1, view: AnyView(IPLocationCard(server: server, snapshot: snapshot))),
        ]
        // vnStat is a Linux/BSD tool, and the probe is a POSIX shell line that
        // Windows PowerShell 5.1 cannot even parse — the card would show a red
        // syntax error on every Windows host.
        if !isWindows {
            items.append(.init(id: "traffic", weight: 3.5, view: AnyView(StatusTrafficCard(server: server))))
        }
        if snapshot.gpu.isPresent {
            items.append(.init(id: "gpu", weight: 2.5, view: AnyView(StatusGPUCard(status: snapshot.gpu))))
        }
        return items
    }

    var body: some View {
        VStack(spacing: 14) {
            StaticGrid(items: compact, availableWidth: width)
            // Full width, with the same width hint the grid gives its columns,
            // so these cards can lay their rows out with fixed widths too.
            Group {
                if server.hasDocker {
                    StatusDockerCard(server: server, snapshot: snapshot, preloaded: previewContainers)
                }
                StatusProcessCard(snapshot: snapshot, isWindows: isWindows)
            }
            .environment(\.cardWidth, width)
        }
    }
}
