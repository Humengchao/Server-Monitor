import SwiftUI

/// A non-lazy adaptive grid, laid out as balanced columns.
///
/// `LazyVGrid` cannot be used for the machine screen's cards. It materialises
/// only what is visible and estimates the rest, so scrolling up re-creates
/// cards at their real height, the scroll offset is corrected to compensate,
/// and the view jumps back down — with a card that loads asynchronously and
/// changes height when it does, that becomes a loop the user cannot scroll out
/// of. Discarding a card also throws away its `@State` and re-runs its `.task`.
///
/// Ten cards do not need laziness. Columns rather than rows so cards of very
/// different heights do not stretch each other.
struct StaticGrid<Content: View>: View {
    /// One card. `weight` is a rough relative height (1 = a short card, 4 = a
    /// tall one); it only steers which column a card lands in.
    struct Item {
        let id: String
        var weight: Double = 1
        let view: Content
    }

    /// Items, each with a stable id so `@State` inside a card survives relayout.
    let items: [Item]
    let availableWidth: CGFloat
    var minimumWidth: CGFloat = 340
    var spacing: CGFloat = 14
    /// Column widths are rounded down to a multiple of this. During a live
    /// resize the pane width changes by a pixel or two per frame; with the
    /// column width quantised, most of those frames leave every card's
    /// proposed size unchanged and SwiftUI serves the whole card tree from its
    /// layout cache. The slack goes into the gaps between columns.
    var widthStep: CGFloat = GridLayout.step

    /// Exposed for tests: the column maths is the part that can be wrong in a
    /// way nobody notices until a window is resized.
    var columnCountForTesting: Int { columnCount }

    private var columnCount: Int {
        guard availableWidth > 0 else { return 1 }
        return max(1, min(items.count, Int((availableWidth + spacing) / (minimumWidth + spacing))))
    }

    /// Each column's exact width. Handed to the columns as a fixed frame and to
    /// the cards through the environment: a flexible column
    /// (`.frame(maxWidth: .infinity)`) makes the HStack measure every card
    /// two or three times per layout pass, and a window resize is a layout
    /// pass per frame. Measured at 2.4× the cost for identical content.
    var columnWidth: CGFloat {
        let count = columnCount
        guard availableWidth > 0 else { return minimumWidth }
        let exact = (availableWidth - spacing * CGFloat(count - 1)) / CGFloat(count)
        guard widthStep > 1 else { return exact }
        return max(minimumWidth, floor(exact / widthStep) * widthStep)
    }

    /// What the rounding left over, spread over every slot — both outer margins
    /// and each gap — so the grid still fills the pane and no single gap has to
    /// absorb it all. With two columns the slack can reach 30pt; in one gap
    /// that is a 14→44pt gap breathing during a drag, over three slots it is
    /// ≤10pt everywhere.
    var slackPerSlot: CGFloat {
        let count = columnCount
        guard availableWidth > 0 else { return 0 }
        let slack = availableWidth - columnWidth * CGFloat(count) - spacing * CGFloat(count - 1)
        return max(0, slack / CGFloat(count + 1))
    }

    var effectiveSpacing: CGFloat { spacing + slackPerSlot }

    /// Which item indices go in which column: each card, in order, joins the
    /// column that is currently shortest by weight. Round-robin put a tall card
    /// beside a short one and left the short column with a hole the height of
    /// the difference; this keeps the columns' bottoms close together.
    static func assign(weights: [Double], columns: Int) -> [[Int]] {
        guard columns > 0 else { return [Array(weights.indices)] }
        var result = Array(repeating: [Int](), count: columns)
        var heights = Array(repeating: 0.0, count: columns)
        for (index, weight) in weights.enumerated() {
            // Ties go to the leftmost, so a fresh row still fills left to right.
            let column = heights.firstIndex(of: heights.min() ?? 0) ?? 0
            result[column].append(index)
            heights[column] += weight
        }
        return result
    }

    var body: some View {
        let count = columnCount
        let width = columnWidth
        let columns = Self.assign(weights: items.map(\.weight), columns: count)
        HStack(alignment: .top, spacing: effectiveSpacing) {
            ForEach(0..<count, id: \.self) { column in
                VStack(spacing: spacing) {
                    ForEach(columns[column], id: \.self) { index in
                        items[index].view
                            .id(items[index].id)
                    }
                }
                .frame(width: width, alignment: .top)
            }
        }
        .padding(.horizontal, slackPerSlot)
        .environment(\.cardWidth, width)
    }

}

/// The width step the machine screen lays out on.
enum GridLayout {
    /// 16pt: at a typical 2pt-per-frame drag, seven frames in eight leave every
    /// width unchanged and the whole tree is served from SwiftUI's layout
    /// cache. Measured on the full machine screen: median 21 ms per frame
    /// unquantised, 6 ms quantised.
    static let step: CGFloat = 16

    /// Rounds a width down to the step, for anything that should relayout only
    /// when the grid does — the history charts under the cards.
    static func quantise(_ width: CGFloat, step: CGFloat = GridLayout.step) -> CGFloat {
        guard width > 0, step > 1 else { return width }
        return floor(width / step) * step
    }
}

/// The width a card has been given by its container, when known.
///
/// Cards that lay content out in rows use it to give their cells fixed widths
/// instead of flexible ones, for the same reason the grid does. Nil outside a
/// `StaticGrid` (previews, the render tests), where they fall back to
/// flexible cells.
struct CardWidthKey: EnvironmentKey {
    static let defaultValue: CGFloat? = nil
}

extension EnvironmentValues {
    var cardWidth: CGFloat? {
        get { self[CardWidthKey.self] }
        set { self[CardWidthKey.self] = newValue }
    }
}

extension Array {
    /// Fixed-size chunks, for laying a small collection out in rows without a
    /// lazy grid.
    func chunked(into size: Int) -> [[Element]] {
        guard size > 0 else { return [self] }
        return stride(from: 0, to: count, by: size).map {
            Array(self[$0..<Swift.min($0 + size, count)])
        }
    }
}
