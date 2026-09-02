import SwiftUI

/// Past terminal and SFTP sessions.
struct SessionHistoryView: View {
    @Binding var search: String
    @Binding var clearing: Bool

    @EnvironmentObject private var sessions: SessionManager
    @EnvironmentObject private var loc: Localization

    @State private var records: [SessionRecord] = []

    private var rows: [SessionRecord] {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return records }
        return records.filter { $0.serverName.lowercased().contains(query) }
    }

    var body: some View {
        Group {
            if records.isEmpty {
                ContentUnavailableView(
                    loc.t("nav.history"),
                    systemImage: "clock.arrow.circlepath",
                    description: Text(loc.t("history.empty"))
                )
            } else {
                Table(rows) {
                    TableColumn(loc.t("history.server")) { record in
                        HStack(spacing: 6) {
                            Image(systemName: record.kind == .terminal ? "terminal" : "folder")
                                .foregroundStyle(.secondary)
                            Text(record.serverName).lineLimit(1)
                        }
                    }
                    TableColumn(loc.t("history.kind")) { record in
                        Text(record.kind == .terminal ? loc.t("nav.terminal") : "SFTP")
                            .foregroundStyle(.secondary)
                    }
                    .width(90)
                    TableColumn(loc.t("history.started")) { record in
                        Text(Self.formatter.string(from: record.startedAt))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    .width(150)
                    TableColumn(loc.t("history.duration")) { record in
                        Text(record.duration.map(Self.formatDuration) ?? loc.t("history.open"))
                            .font(.caption)
                            .monospacedDigit()
                            .foregroundStyle(record.isOpen ? Color.green : Color.secondary)
                    }
                    .width(100)
                }
            }
        }
        .navigationTitle(loc.t("nav.history"))
        .onAppear { records = sessions.history() }
        .confirmationDialog(
            loc.t("history.clearConfirm"),
            isPresented: $clearing,
            titleVisibility: .visible
        ) {
            Button(loc.t("history.clear"), role: .destructive) {
                sessions.clearHistory()
                records = sessions.history()
            }
            Button(loc.t("common.cancel"), role: .cancel) {}
        }
    }

    private static let formatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .short
        formatter.timeStyle = .short
        return formatter
    }()

    static func formatDuration(_ seconds: TimeInterval) -> String {
        let total = Int(seconds.rounded())
        if total < 60 { return "\(total)s" }
        if total < 3600 { return "\(total / 60)m \(total % 60)s" }
        return "\(total / 3600)h \((total % 3600) / 60)m"
    }
}
