import SwiftUI

/// Circular percentage gauge used for CPU and memory on the dashboard cards.
///
/// A ring rather than a bar because the cards put two of them side by side at a
/// glance-able size, where a bar's fill is much harder to read quickly.
public struct RingGauge: View {
    private let value: Double        // 0...100
    private let diameter: CGFloat
    private let lineWidth: CGFloat

    public init(value: Double, diameter: CGFloat = 62, lineWidth: CGFloat = 7) {
        self.value = max(0, min(value, 100))
        self.diameter = diameter
        self.lineWidth = lineWidth
    }

    public var body: some View {
        ZStack {
            Circle()
                .stroke(Color.primary.opacity(0.10), lineWidth: lineWidth)
            Circle()
                .trim(from: 0, to: value / 100)
                .stroke(
                    AngularGradient(
                        colors: Self.arcColors(for: value),
                        center: .center,
                        startAngle: .degrees(0),
                        endAngle: .degrees(360 * value / 100)
                    ),
                    style: StrokeStyle(lineWidth: lineWidth, lineCap: .round)
                )
                // Start the arc at 12 o'clock instead of 3.
                .rotationEffect(.degrees(-90))
                // No animation on value changes, deliberately. Every poll moves
                // every gauge, and an eased arc meant 0.35 s of display-rate
                // frames per gauge per tick: on a machine screen with a dozen
                // gauges that was 71% of the main thread's work — ~400 ms per
                // tick — for a sweep nobody watches. The gauge snaps, the way
                // Activity Monitor's do.

            Text(verbatim: "\(Int(value.rounded()))%")
                .font(.system(size: diameter * 0.26, weight: .semibold, design: .rounded))
                .monospacedDigit()
                .foregroundStyle(.primary)
        }
        .frame(width: diameter, height: diameter)
    }

    /// Two stops so the arc deepens toward its leading edge, which reads as
    /// "how far into trouble" rather than a flat colour swap at a threshold.
    static func arcColors(for value: Double) -> [Color] {
        switch value {
        case ..<50: return [.green, .green]
        case ..<70: return [.yellow, .yellow]
        case ..<85: return [.yellow, .orange]
        default: return [.orange, .red]
        }
    }
}

/// A labelled read/write or up/down pair, as shown beside the gauges.
public struct RatePair: View {
    private let title: String
    private let firstSymbol: String
    private let firstValue: Double
    private let secondSymbol: String
    private let secondValue: Double

    public init(
        title: String,
        firstSymbol: String,
        firstValue: Double,
        secondSymbol: String,
        secondValue: Double
    ) {
        self.title = title
        self.firstSymbol = firstSymbol
        self.firstValue = firstValue
        self.secondSymbol = secondSymbol
        self.secondValue = secondValue
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.caption)
                .foregroundStyle(.secondary)
            row(symbol: firstSymbol, value: firstValue)
            row(symbol: secondSymbol, value: secondValue)
        }
    }

    private func row(symbol: String, value: Double) -> some View {
        let split = Format.rateParts(value)
        return HStack(spacing: 6) {
            Image(systemName: symbol)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 14)
            Text(split.amount)
                .font(.system(.callout, design: .rounded, weight: .semibold))
                .monospacedDigit()
            Text(split.unit)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
    }
}

/// Small icon + value pair for the card's host-facts row.
public struct FactChip: View {
    private let systemImage: String
    private let text: String

    public init(systemImage: String, text: String) {
        self.systemImage = systemImage
        self.text = text
    }

    public var body: some View {
        HStack(spacing: 5) {
            Image(systemName: systemImage)
                .font(.caption2)
                .foregroundStyle(.secondary)
            Text(text)
                .font(.caption)
                .foregroundStyle(.secondary)
                .monospacedDigit()
        }
    }
}
