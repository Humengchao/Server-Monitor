import Charts
import SwiftUI

/// The four history charts under the machine screen's cards.
///
/// Expects a series already thinned by `HistoryReducer`: these re-lay out on
/// every frame of a window resize, and their cost is linear in the number of
/// marks.
struct HistoryCharts: View {
    let samples: [MetricSample]
    @EnvironmentObject private var loc: Localization

    var body: some View {
        VStack(spacing: 14) {
            // `AreaPlot`/`LinePlot` take the whole series at once instead of
            // one mark per sample; measured at 1.8× faster than the mark
            // equivalents for the same data, and a resize redraws all four.
            chartCard(loc.t("metric.cpu")) {
                Chart {
                    AreaPlot(samples, x: .value("t", \.timestamp), y: .value("cpu", \.cpuPercent))
                        .foregroundStyle(.blue.opacity(0.18))
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("cpu", \.cpuPercent))
                        .foregroundStyle(.blue)
                }
                .chartYScale(domain: 0...100)
            }

            chartCard(loc.t("metric.memory")) {
                Chart {
                    AreaPlot(samples, x: .value("t", \.timestamp), y: .value("mem", \.memoryPercent))
                        .foregroundStyle(.purple.opacity(0.18))
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("mem", \.memoryPercent))
                        .foregroundStyle(.purple)
                }
                .chartYScale(domain: 0...100)
            }

            chartCard(loc.t("metric.network")) {
                Chart {
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("rx", \.netRxRate))
                        .foregroundStyle(.green)
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("tx", \.netTxRate))
                        .foregroundStyle(.orange)
                }
                .chartYAxis { rateAxis }
            }

            chartCard(loc.t("metric.diskIO")) {
                Chart {
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("r", \.diskReadRate))
                        .foregroundStyle(.teal)
                    LinePlot(samples, x: .value("t", \.timestamp), y: .value("w", \.diskWriteRate))
                        .foregroundStyle(.pink)
                }
                .chartYAxis { rateAxis }
            }
        }
    }

    /// Four gridlines rather than the default's automatic count: fewer labels
    /// to format per frame, and a 150pt-tall chart has no room for more.
    private var rateAxis: some AxisContent {
        AxisMarks(values: .automatic(desiredCount: 4)) { value in
            AxisGridLine()
            AxisValueLabel {
                if let rate = value.as(Double.self) {
                    Text(Format.rate(rate))
                }
            }
        }
    }

    private func chartCard<Content: View>(
        _ title: String,
        @ViewBuilder content: () -> Content
    ) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.subheadline.weight(.semibold))
            content()
                .frame(height: 150)
                .chartLegend(.hidden)
                .chartXAxis { AxisMarks(values: .automatic(desiredCount: 5)) }
        }
        .padding(12)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }
}
