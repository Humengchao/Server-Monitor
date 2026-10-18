import type { MetricPoint } from '../api/servers';

// Each bucket contributes each visible series' minimum and maximum. A peak in
// latency or memory must not disappear merely because CPU was flat there.
const series = [
  (p: MetricPoint) => p.cpu_percent,
  (p: MetricPoint) => p.memory_total > 0 ? p.memory_used / p.memory_total : 0,
  (p: MetricPoint) => p.network_rx_bytes, (p: MetricPoint) => p.network_tx_bytes,
  (p: MetricPoint) => p.disk_rx_bytes, (p: MetricPoint) => p.disk_tx_bytes,
  (p: MetricPoint) => p.load_1, (p: MetricPoint) => p.load_5,
  (p: MetricPoint) => p.latency_ms,
];

export function sampleMetrics(data: MetricPoint[], maxPoints = 300): MetricPoint[] {
  if (data.length <= maxPoints) return data;
  // Reserve enough room for every series' extrema plus the two endpoints.
  const buckets = Math.max(1, Math.floor((maxPoints - 2) / (series.length * 2)));
  const selected = new Set([0, data.length - 1]);
  for (let bucket = 0; bucket < buckets; bucket++) {
    const start = 1 + Math.floor(bucket * (data.length - 2) / buckets);
    const end = 1 + Math.floor((bucket + 1) * (data.length - 2) / buckets);
    for (const read of series) {
      let min = -1, max = -1, low = Infinity, high = -Infinity;
      for (let index = start; index < end; index++) {
        const value = read(data[index]);
        if (!Number.isFinite(value)) continue;
        if (value < low) { low = value; min = index; }
        if (value > high) { high = value; max = index; }
      }
      if (min >= 0) selected.add(min);
      if (max >= 0) selected.add(max);
    }
  }
  return [...selected].sort((a, b) => a - b).map(index => data[index]);
}
