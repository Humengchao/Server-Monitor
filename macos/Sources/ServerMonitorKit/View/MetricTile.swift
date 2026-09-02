import SwiftUI

/// A labelled value with an optional 0–100 bar, used across the dashboard and
/// the server overview so both read the same way.
public struct MetricTile: View {
    private let title: String
    private let value: String
    private let detail: String?
    private let fraction: Double?
    private let systemImage: String

    public init(
        title: String,
        value: String,
        detail: String? = nil,
        fraction: Double? = nil,
        systemImage: String
    ) {
        self.title = title
        self.value = value
        self.detail = detail
        self.fraction = fraction
        self.systemImage = systemImage
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: systemImage)
                    .foregroundStyle(.secondary)
                    .font(.caption)
                Text(title)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Text(value)
                .font(.system(.title3, design: .rounded, weight: .semibold))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.7)
            if let fraction {
                ProgressView(value: max(0, min(fraction, 1)))
                    .progressViewStyle(.linear)
                    .tint(Self.tint(for: fraction))
            }
            if let detail {
                Text(detail)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 10))
    }

    /// Green below 70%, amber to 90%, red above — the usual "should I care?"
    /// thresholds for a host metric.
    static func tint(for fraction: Double) -> Color {
        switch fraction {
        case ..<0.7: return .green
        case ..<0.9: return .orange
        default: return .red
        }
    }
}

/// Status dot for the sidebar and dashboard rows.
public struct StatusDot: View {
    private let status: ServerStatus

    public init(status: ServerStatus) {
        self.status = status
    }

    public var body: some View {
        Circle()
            .fill(color)
            .frame(width: 8, height: 8)
            .overlay(Circle().strokeBorder(.black.opacity(0.08)))
    }

    private var color: Color {
        switch status {
        case .online: return .green
        case .offline: return .red
        case .polling: return .yellow
        case .unknown: return .secondary
        }
    }
}
