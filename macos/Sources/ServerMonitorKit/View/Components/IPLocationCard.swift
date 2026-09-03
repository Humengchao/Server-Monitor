import SwiftUI

/// Where this machine is, the way SwiftServer's IP card presents it: flag,
/// country, the address, then city and organisation.
///
/// The lookup is a button rather than something the poll does. Asking a public
/// service where a server is tells that service which servers this user runs,
/// and that is a disclosure to make deliberately — the button says so before
/// it is pressed.
struct IPLocationCard: View {
    let server: Server
    let snapshot: MetricSnapshot?

    @Environment(\.monitorService) private var monitor
    @EnvironmentObject private var loc: Localization

    @State private var info: GeoInfo?
    @State private var looking = false
    @State private var failure: String?

    /// What the app actually connects to, which is the address whose location
    /// is meaningful — not the host's own LAN addresses.
    private var endpoint: String { server.host.isEmpty ? server.sshAlias : server.host }

    private var isPrivate: Bool { GeoLookup.isPrivate(endpoint) }

    /// A looked-up country wins; otherwise the one typed in the editor.
    private var countryCode: String {
        info?.countryCode.isEmpty == false ? info!.countryCode : server.countryCode
    }

    var body: some View {
        StatusCard(title: loc.t("card.ipLocation"), systemImage: "mappin.and.ellipse", tint: .red) {
            if looking {
                ProgressView().controlSize(.mini)
            } else if !isPrivate {
                Button(loc.t("card.lookUp")) { Task { await lookUp() } }
                    .buttonStyle(.borderless)
                    .font(.caption)
                    .help(loc.t("card.lookupNotice"))
            }
        } content: {
            VStack(alignment: .leading, spacing: 8) {
                headline
                Divider()
                StatusFactRow(label: loc.t("card.endpoint"), value: endpoint)
                if let info, !info.place.isEmpty {
                    StatusFactRow(label: loc.t("card.city"), value: info.place)
                }
                if let info, !info.organisation.isEmpty {
                    StatusFactRow(label: loc.t("card.org"), value: info.organisation)
                }
                if isPrivate {
                    Text(loc.t("card.privateAddress"))
                        .font(.system(size: 9))
                        .foregroundStyle(.tertiary)
                } else if let failure {
                    Text(failure)
                        .font(.system(size: 9))
                        .foregroundStyle(.red)
                        .lineLimit(2)
                }
            }
        }
    }

    private var headline: some View {
        HStack(spacing: 10) {
            Text(Format.flag(countryCode).isEmpty ? "🌐" : Format.flag(countryCode))
                .font(.system(size: 30))
            VStack(alignment: .leading, spacing: 1) {
                Text(countryName)
                    .font(.callout.weight(.medium))
                    .lineLimit(1)
                Text(verbatim: info?.ip.isEmpty == false ? info!.ip : endpoint)
                    .font(.caption)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
        }
    }

    private var countryName: String {
        if let info, !info.country.isEmpty { return info.country }
        // A code with no lookup still names a place through the system's own
        // localised region names, so a hand-typed "JP" reads as 日本.
        if !countryCode.isEmpty,
           let name = Locale.current.localizedString(forRegionCode: countryCode) {
            return name
        }
        return loc.t("card.locationUnknown")
    }

    private func lookUp() async {
        looking = true
        failure = nil
        defer { looking = false }
        do {
            let result = try await monitor.required.geo.lookup(endpoint)
            info = result
            // Persist just the country: the flag is what the dashboard and the
            // sidebar show, and re-asking on every visit would be rude to a
            // free service.
            if !result.countryCode.isEmpty, result.countryCode != server.countryCode {
                var updated = server
                updated.countryCode = result.countryCode
                try? monitor?.updateServer(updated)
            }
        } catch {
            failure = error.localizedDescription
        }
    }
}
