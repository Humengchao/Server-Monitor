using ServerMonitor.Core.Model;

namespace ServerMonitor.Core.Collect;

/// <summary>
/// Thins a history series to a bounded number of points before it is charted.
/// </summary>
/// <remarks>
/// A day of 5-second polls is ~17,000 samples. The charts are drawn by hand
/// into a <c>DrawingVisual</c> (D6) rather than by a charting library, so the
/// cost is one line segment per point per redraw — and a chart 700 pixels wide
/// cannot show more than a few hundred distinct x positions anyway, so nothing
/// is lost by averaging each bucket down to one sample first.
///
/// <see cref="Store.Database.ReducedSamples"/> does the same thing inside
/// SQLite and is what the app actually calls; this is the in-memory
/// equivalent, used where the samples are already in hand (a chart re-scaled
/// without a re-query) and as the reference the two are tested against.
/// </remarks>
public static class HistoryReducer
{
    /// <summary>
    /// Averages <paramref name="samples"/> into at most
    /// <paramref name="maxPoints"/> equal time buckets. A series already at or
    /// under the limit comes back untouched, so short ranges keep every real
    /// reading.
    /// </summary>
    public static List<MetricSample> Reduce(IReadOnlyList<MetricSample> samples, int maxPoints)
    {
        if (maxPoints <= 0 || samples.Count <= maxPoints) return [.. samples];
        var first = samples[0];
        var last = samples[^1];
        var span = (last.Timestamp - first.Timestamp).TotalSeconds;
        if (span <= 0) return [samples[samples.Count / 2]];
        var bucketLength = span / maxPoints;

        var reduced = new List<MetricSample>(maxPoints + 1);
        var bucket = new List<MetricSample>();
        var bucketIndex = 0;
        foreach (var sample in samples)
        {
            var index = Math.Min(
                maxPoints - 1,
                (int)((sample.Timestamp - first.Timestamp).TotalSeconds / bucketLength));
            if (index != bucketIndex && bucket.Count > 0)
            {
                reduced.Add(Average(bucket));
                bucket.Clear();
                bucketIndex = index;
            }
            bucket.Add(sample);
        }
        if (bucket.Count > 0) reduced.Add(Average(bucket));
        return reduced;
    }

    /// <summary>
    /// One sample standing for a bucket: the mean of every rate and level,
    /// stamped at the bucket's midpoint, carrying the last sample's cumulative
    /// totals — a mean of running totals would be a number that never
    /// happened.
    /// </summary>
    internal static MetricSample Average(List<MetricSample> bucket)
    {
        if (bucket.Count <= 1) return bucket[0];
        var first = bucket[0];
        var last = bucket[^1];
        var count = bucket.Count;

        double Mean(Func<MetricSample, double> select) => bucket.Sum(select) / count;
        long MeanLong(Func<MetricSample, long> select) => (long)(bucket.Sum(s => (double)select(s)) / count);

        var snapshot = new MetricSnapshot
        {
            CpuPercent = Mean(s => s.CpuPercent),
            Load1 = Mean(s => s.Load1),
            Load5 = Mean(s => s.Load5),
            Load15 = Mean(s => s.Load15),
            MemoryUsed = MeanLong(s => s.MemoryUsed),
            MemoryTotal = last.MemoryTotal,
            DiskUsed = MeanLong(s => s.DiskUsed),
            DiskTotal = last.DiskTotal,
            NetRxRate = Mean(s => s.NetRxRate),
            NetTxRate = Mean(s => s.NetTxRate),
            DiskReadRate = Mean(s => s.DiskReadRate),
            DiskWriteRate = Mean(s => s.DiskWriteRate),
            NetRxTotal = last.NetRxTotal,
            NetTxTotal = last.NetTxTotal,
            UptimeSeconds = last.UptimeSeconds,
            LatencyMs = Mean(s => s.LatencyMs),
        };
        var midpoint = first.Timestamp + (last.Timestamp - first.Timestamp) / 2;
        return new MetricSample(first.ServerId, snapshot, midpoint);
    }
}
