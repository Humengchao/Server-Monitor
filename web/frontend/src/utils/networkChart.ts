import type { NetworkBucket, NetworkTrend } from '../api/network';

export interface NetworkChartRow {
  time: number;
  values: Record<string, { avg: number | null; peak: number | null; event: number | null }>;
  buckets: Record<string, NetworkBucket>;
}

/** Trends share one fixed range, so their bucket boundaries align exactly. */
export function networkChartRows(history: Record<string, NetworkTrend>): NetworkChartRow[] {
  const rows = new Map<number, NetworkChartRow>();
  for (const [id, result] of Object.entries(history)) for (const bucket of result.buckets) {
    const time = Date.parse(bucket.recorded_at);
    if (!Number.isFinite(time)) continue;
    const row = rows.get(time) || { time, values: {}, buckets: {} };
    row.buckets[id] = bucket;
    row.values[id] = {
      avg: bucket.avg_ms,
      peak: bucket.max_ms,
      // Failure markers are separate from RTT lines: no synthetic zero averages.
      event: bucket.down_samples + bucket.loss_samples + bucket.unknown_samples + bucket.threshold_exceeded_samples > 0
        ? bucket.avg_ms ?? bucket.max_ms ?? 0 : null,
    };
    rows.set(time, row);
  }
  return [...rows.values()].sort((a, b) => a.time - b.time);
}
