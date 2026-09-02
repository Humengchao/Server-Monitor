import SwiftUI

/// Free-text labels on a machine, rendered as chips.
///
/// The colour is derived from the tag's own text rather than stored, so the
/// same tag is the same colour everywhere without anyone picking one — and two
/// machines tagged "prod" always match at a glance.
struct TagChips: View {
    let tags: [String]
    var limit: Int = 4

    var body: some View {
        HStack(spacing: 4) {
            ForEach(tags.prefix(limit), id: \.self) { tag in
                Text(tag)
                    .font(.system(size: 9, weight: .medium))
                    .lineLimit(1)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(
                        Capsule().fill(TagChips.color(for: tag).opacity(0.16))
                    )
                    .foregroundStyle(TagChips.color(for: tag))
            }
            if tags.count > limit {
                Text(verbatim: "+\(tags.count - limit)")
                    .font(.system(size: 9))
                    .foregroundStyle(.tertiary)
            }
        }
    }

    /// Stable across launches: a hash of the lowercased text picks from a fixed
    /// palette, so "prod" is the same colour today and tomorrow. Swift's own
    /// `hashValue` is seeded per process and would not be.
    static func color(for tag: String) -> Color {
        let palette: [Color] = [.blue, .green, .orange, .purple, .pink, .teal, .indigo, .brown]
        var hash: UInt64 = 0xcbf29ce484222325
        for byte in tag.lowercased().utf8 {
            hash = (hash ^ UInt64(byte)) &* 0x100000001b3
        }
        return palette[Int(hash % UInt64(palette.count))]
    }
}
