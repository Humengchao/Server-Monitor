import type { NetworkHistory } from '../api/network';

/** Align comparisons by minute and retain each check's newest observation. */
export function networkChartRows(history: Record<string, NetworkHistory>) {
  const rows = new Map<number, Record<string, number | null>>();
  const newest = new Map<string, number>();
  for (const [id, result] of Object.entries(history)) for (const sample of result.samples) {
    const recorded = Date.parse(sample.recorded_at);
    if (!Number.isFinite(recorded)) continue;
    const time = Math.floor(recorded / 60000) * 60000;
    const key = `${id}:${time}`;
    if ((newest.get(key) ?? -Infinity) >= recorded) continue;
    newest.set(key, recorded);
    const row = rows.get(time) || { time };
    row[id] = sample.avg_ms;
    rows.set(time, row);
  }
  return [...rows.values()].sort((a, b) => a.time! - b.time!);
}
