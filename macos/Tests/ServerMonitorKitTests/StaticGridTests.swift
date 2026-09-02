import Foundation
import SwiftUI
import Testing
@testable import ServerMonitorKit

@Suite("Card layout")
struct StaticGridTests {

    private func grid(width: CGFloat, count: Int) -> StaticGrid<Text> {
        StaticGrid(
            items: (0..<count).map { .init(id: "c\($0)", view: Text(verbatim: "\($0)")) },
            availableWidth: width
        )
    }

    @Test func columnCountFollowsTheAvailableWidth() {
        // Two columns need 2×340 plus one 14pt gap = 694; below that, one.
        #expect(grid(width: 300, count: 9).columnCountForTesting == 1)
        #expect(grid(width: 693, count: 9).columnCountForTesting == 1)
        #expect(grid(width: 694, count: 9).columnCountForTesting == 2)
        // Three need 3×340 plus two gaps = 1048.
        #expect(grid(width: 1047, count: 9).columnCountForTesting == 2)
        #expect(grid(width: 1048, count: 9).columnCountForTesting == 3)
    }

    @Test func everyColumnStaysAtOrAboveTheMinimum() {
        // The point of the maths: a column narrower than the minimum would
        // squash the cards it holds.
        for width in stride(from: 200.0, through: 2000.0, by: 7.0) {
            let columns = grid(width: width, count: 12).columnCountForTesting
            let each = (width - 14 * Double(columns - 1)) / Double(columns)
            #expect(columns == 1 || each >= 340, "\(columns) columns at \(width) gives \(each)pt")
        }
    }

    @Test func neverMoreColumnsThanCards() {
        // Two cards on a very wide window must not produce four empty columns.
        #expect(grid(width: 2000, count: 2).columnCountForTesting == 2)
        #expect(grid(width: 2000, count: 1).columnCountForTesting == 1)
    }

    @Test func aZeroWidthPaneStillLaysOut() {
        // GeometryReader reports 0 on the first pass; one column beats a crash
        // or an empty screen.
        #expect(grid(width: 0, count: 9).columnCountForTesting == 1)
        #expect(grid(width: -50, count: 9).columnCountForTesting == 1)
    }

    @Test func quantisedColumnsStillFillThePaneExactly() {
        // The rounding slack goes into the gaps, so the grid's right edge must
        // land on the pane's right edge at every width — a grid that drifted
        // in and out from the edge during a drag would be worse than jank.
        for width in stride(from: 694.0, through: 2000.0, by: 3.0) {
            let g = grid(width: width, count: 12)
            let columns = g.columnCountForTesting
            guard columns > 1 else { continue }
            let total = g.columnWidth * Double(columns)
                + g.effectiveSpacing * Double(columns - 1)
                + g.slackPerSlot * 2
            #expect(abs(total - width) < 0.001, "at \(width): \(columns) cols of \(g.columnWidth) + gaps \(g.effectiveSpacing) + margins = \(total)")
            #expect(g.effectiveSpacing >= 14)
            // Spread over count+1 slots the breathing stays under one step even
            // at two columns, where the total slack can be nearly two steps.
            #expect(g.slackPerSlot < 16 * Double(columns) / Double(columns + 1) + 0.001, "slack per slot \(g.slackPerSlot)")
            #expect(g.slackPerSlot < 16, "breathing of \(g.slackPerSlot)pt — a full step, visible")
        }
    }

    @Test func columnWidthMovesInStepsNotPixels() {
        // Seven frames in eight of a 2pt drag must produce the same column
        // width, or the layout cache buys nothing.
        var distinct = Set<Double>()
        for width in stride(from: 900.0, through: 1000.0, by: 2.0) {
            distinct.insert(grid(width: width, count: 12).columnWidth)
        }
        // 100pt of drag at 2 columns is 50pt of column change: 3–4 steps of 16.
        #expect(distinct.count <= 5, "saw \(distinct.count) distinct widths over 100pt")
        for width in distinct {
            #expect(width.truncatingRemainder(dividingBy: 16) == 0 || width == 340, "\(width) is neither a step nor the minimum")
        }
    }

    @Test func quantiseRoundsDownAndLeavesNonsenseAlone() {
        #expect(GridLayout.quantise(700) == 688)
        #expect(GridLayout.quantise(688) == 688)
        #expect(GridLayout.quantise(15) == 0)
        #expect(GridLayout.quantise(0) == 0)
        #expect(GridLayout.quantise(-40) == -40)
        #expect(GridLayout.quantise(700, step: 1) == 700, "a step of 1 disables it")
    }

    @Test func chunkingKeepsEveryElementInOrder() {
        let cores = Array(0..<7)
        let rows = cores.chunked(into: 2)
        #expect(rows.count == 4)
        #expect(rows.last == [6], "the odd one out is its own row, not dropped")
        #expect(rows.flatMap { $0 } == cores)
    }

    @Test func chunkingHandlesEmptyAndDegenerateSizes() {
        #expect([Int]().chunked(into: 2).isEmpty)
        #expect([1, 2, 3].chunked(into: 0) == [[1, 2, 3]], "a zero size must not loop forever")
    }

    @Test func cardsJoinTheShortestColumn() {
        // Weights are relative heights. Round-robin would put the two tall
        // cards (3) in opposite columns with a short one under each — leaving
        // a hole; greedy keeps the columns' totals close.
        let weights: [Double] = [3, 1, 1, 3, 1, 1]
        let columns = StaticGrid<Text>.assign(weights: weights, columns: 2)
        let totals = columns.map { $0.reduce(0.0) { $0 + weights[$1] } }
        #expect(abs(totals[0] - totals[1]) <= 1, "column totals \(totals)")
        #expect(columns.flatMap { $0 }.sorted() == Array(0..<6), "every card placed exactly once")
    }

    @Test func aFreshRowStillFillsLeftToRight() {
        // Ties go to the leftmost column, so the first cards read in order.
        let columns = StaticGrid<Text>.assign(weights: [1, 1, 1, 1], columns: 2)
        #expect(columns[0].first == 0)
        #expect(columns[1].first == 1)
    }

    @Test func oneColumnKeepsTheOriginalOrder() {
        #expect(StaticGrid<Text>.assign(weights: [2, 5, 1], columns: 1) == [[0, 1, 2]])
    }

    @Test func dockerTilesPerRowFollowTheCardWidth() {
        // 250pt tiles with 10pt gaps inside 28pt of card padding.
        #expect(StatusDockerCard.tileColumns(cardWidth: 300) == 1)
        #expect(StatusDockerCard.tileColumns(cardWidth: 537) == 1)
        #expect(StatusDockerCard.tileColumns(cardWidth: 538) == 2)     // 28 + 250×2 + 10
        #expect(StatusDockerCard.tileColumns(cardWidth: 900) == 3)
        #expect(StatusDockerCard.tileColumns(cardWidth: 1400) == 5)
    }
}
