import Foundation

/// Thins a history series to a bounded number of points before it is charted.
///
/// A day of 5-second polls is ~17,000 samples. Swift Charts lays every mark
/// out again on every frame of a window resize, and four charts of 17,000
/// area-plus-line points cannot do that in the 16 ms a frame allows — the
/// window visibly stutters. A chart 700 points wide cannot show more than a
/// few hundred distinct x positions anyway, so nothing is lost by averaging
/// each bucket down to one sample first.
public enum HistoryReducer {

    /// Averages `samples` into at most `maxPoints` equal time buckets. Series
    /// already at or under the limit come back untouched, so short ranges keep
    /// every real reading.
    public static func reduce(_ samples: [MetricSample], maxPoints: Int) -> [MetricSample] {
        guard maxPoints > 0, samples.count > maxPoints,
              let first = samples.first, let last = samples.last
        else { return samples }
        let span = last.timestamp.timeIntervalSince(first.timestamp)
        guard span > 0 else { return [samples[samples.count / 2]] }
        let bucketLength = span / Double(maxPoints)

        var reduced: [MetricSample] = []
        reduced.reserveCapacity(maxPoints + 1)
        var bucket: [MetricSample] = []
        var bucketIndex = 0
        for sample in samples {
            let index = min(maxPoints - 1, Int(sample.timestamp.timeIntervalSince(first.timestamp) / bucketLength))
            if index != bucketIndex, !bucket.isEmpty {
                reduced.append(average(bucket))
                bucket.removeAll(keepingCapacity: true)
                bucketIndex = index
            }
            bucket.append(sample)
        }
        if !bucket.isEmpty { reduced.append(average(bucket)) }
        return reduced
    }

    /// One sample standing for a bucket: mean of every rate and level, stamped
    /// at the bucket's midpoint, carrying the last sample's cumulative totals
    /// (a mean of running totals would be a number that never happened).
    static func average(_ bucket: [MetricSample]) -> MetricSample {
        guard bucket.count > 1, let first = bucket.first, let last = bucket.last else {
            return bucket[0]
        }
        let count = Double(bucket.count)
        func mean(_ value: (MetricSample) -> Double) -> Double {
            bucket.reduce(0) { $0 + value($1) } / count
        }
        func meanInt(_ value: (MetricSample) -> Int64) -> Int64 {
            Int64(bucket.reduce(0.0) { $0 + Double(value($1)) } / count)
        }

        var snapshot = MetricSnapshot()
        snapshot.cpuPercent = mean(\.cpuPercent)
        snapshot.load1 = mean(\.load1)
        snapshot.load5 = mean(\.load5)
        snapshot.load15 = mean(\.load15)
        snapshot.memoryUsed = meanInt(\.memoryUsed)
        snapshot.memoryTotal = last.memoryTotal
        snapshot.diskUsed = meanInt(\.diskUsed)
        snapshot.diskTotal = last.diskTotal
        snapshot.netRxRate = mean(\.netRxRate)
        snapshot.netTxRate = mean(\.netTxRate)
        snapshot.diskReadRate = mean(\.diskReadRate)
        snapshot.diskWriteRate = mean(\.diskWriteRate)
        snapshot.netRxTotal = last.netRxTotal
        snapshot.netTxTotal = last.netTxTotal
        snapshot.uptimeSeconds = last.uptimeSeconds
        snapshot.latencyMs = mean(\.latencyMs)

        let midpoint = first.timestamp.addingTimeInterval(
            last.timestamp.timeIntervalSince(first.timestamp) / 2
        )
        return MetricSample(serverID: first.serverID, timestamp: midpoint, snapshot: snapshot)
    }
}
