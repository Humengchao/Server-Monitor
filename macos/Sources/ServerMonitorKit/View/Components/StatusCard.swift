import SwiftUI

/// Shared chrome for every card on the machine screen: an icon, a title, an
/// optional trailing accessory, then the content.
///
/// SwiftServer's status detail is a grid of these rather than one long form,
/// which is what lets CPU, memory and network sit side by side on a wide
/// window and stack on a narrow one without a second layout.
struct StatusCard<Content: View, Accessory: View>: View {
    let title: String
    let systemImage: String
    var tint: Color = .accentColor
    @ViewBuilder var accessory: () -> Accessory
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 7) {
                Image(systemName: systemImage)
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(tint)
                Text(title)
                    .font(.subheadline.weight(.semibold))
                Spacer(minLength: 8)
                accessory()
            }
            content()
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(cardBackground)
    }
}

extension StatusCard where Accessory == EmptyView {
    init(
        title: String,
        systemImage: String,
        tint: Color = .accentColor,
        @ViewBuilder content: @escaping () -> Content
    ) {
        self.init(title: title, systemImage: systemImage, tint: tint, accessory: { EmptyView() }, content: content)
    }
}

/// A label/value line, the shape the info cards repeat.
struct StatusFactRow: View {
    let label: String
    let value: String
    var monospaced = true

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer(minLength: 8)
            Text(value.isEmpty ? "—" : value)
                .font(.caption)
                .monospacedDigit()
                .textSelection(.enabled)
                .multilineTextAlignment(.trailing)
                .lineLimit(2)
        }
    }
}

/// A thin labelled progress bar, used for mounts, swap and per-core load.
struct StatusBar: View {
    let fraction: Double
    var height: CGFloat = 6
    var tint: Color?

    var body: some View {
        GeometryReader { geometry in
            let clamped = max(0, min(fraction, 1))
            ZStack(alignment: .leading) {
                Capsule().fill(Color.primary.opacity(0.10))
                Capsule()
                    .fill(tint ?? Self.color(for: clamped * 100))
                    .frame(width: max(clamped * geometry.size.width, clamped > 0 ? 2 : 0))
            }
        }
        .frame(height: height)
    }

    /// Same thresholds as the ring gauge, so a bar and a ring never disagree
    /// about whether a number is worrying.
    static func color(for percent: Double) -> Color {
        switch percent {
        case ..<50: return .green
        case ..<70: return .yellow
        case ..<85: return .orange
        default: return .red
        }
    }
}
