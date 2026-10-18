import { test, expect } from '@playwright/test';
import { ChangeSet, Text } from '@codemirror/state';
import { sampleMetrics } from '../src/utils/chartSampling';
import { updatedUTF8Size } from '../src/utils/textStats';
import { shareFleetSnapshot } from '../src/utils/fleet';
import { nextFleetChange } from '../src/hooks/useFleetClock';
import { host, observedAt } from './fixtures/fleet';

test('sampling preserves independent series extrema and both endpoints within the point budget', () => {
  const point = host('sample').latest_metrics!;
  const data = Array.from({ length: 6000 }, (_, index) => ({ ...point, recorded_at: new Date(observedAt + index * 1000).toISOString() }));
  data[301].cpu_percent = 100;
  data[501].memory_used = data[501].memory_total;
  data[701].network_rx_bytes = 999999;
  data[901].network_tx_bytes = 999999;
  data[1101].disk_rx_bytes = 999999;
  data[1301].disk_tx_bytes = 999999;
  data[1501].load_1 = 99;
  data[1701].load_5 = 99;
  data[1901].latency_ms = 9999;
  const sampled = sampleMetrics(data);
  expect(sampled.length).toBeLessThanOrEqual(300);
  expect(sampled[0]).toBe(data[0]);
  expect(sampled.at(-1)).toBe(data.at(-1));
  for (const index of [301, 501, 701, 901, 1101, 1301, 1501, 1701, 1901]) expect(sampled).toContain(data[index]);
  expect(sampled.map(p => p.recorded_at)).toEqual(sampled.map(p => p.recorded_at).sort());
});

test('incremental UTF-8 sizes match full encoding through unicode edits, undo and multiple selections', () => {
  let doc = Text.of(['a😀中文', 'second line', '\ud800\udc00 x']);
  const size = (value: Text) => new TextEncoder().encode(value.toString()).length;
  let bytes = size(doc);
  let seed = 12345;
  const random = (max: number) => { seed = (seed * 1664525 + 1013904223) >>> 0; return seed % max; };
  const inserts = ['x', '😀', '汉字', '\n', '\ud800', '\udc00', ''];
  for (let i = 0; i < 300; i++) {
    const from = random(doc.length + 1), to = from + random(doc.length - from + 1);
    const changes = ChangeSet.of([{ from, to, insert: inserts[random(inserts.length)] }], doc.length);
    const next = changes.apply(doc);
    const nextSize = updatedUTF8Size(doc, changes, next, bytes);
    expect(nextSize).toBe(size(next));
    expect(updatedUTF8Size(next, changes.invert(doc), doc, nextSize)).toBe(bytes);
    doc = next; bytes = nextSize;
  }
  const original = Text.of(['😀ab😀cd😀']);
  const changes = ChangeSet.of([{ from: 1, to: 2, insert: 'x' }, { from: 5, to: 6, insert: '\udc00' }], original.length);
  expect(updatedUTF8Size(original, changes, changes.apply(original), size(original))).toBe(size(changes.apply(original)));
});

test('fleet snapshots share unchanged rows while freshness schedules exact transitions', () => {
  const original = [host('one'), host('two')];
  expect(shareFleetSnapshot(original, structuredClone(original))).toBe(original);
  const next = structuredClone(original);
  next[1].latest_metrics!.cpu_percent = 99;
  const shared = shareFleetSnapshot(original, next);
  expect(shared[0]).toBe(original[0]);
  expect(shared[1]).toBe(next[1]);
  expect(nextFleetChange(original, observedAt)).toBe(observedAt + 110000);
  expect(nextFleetChange(original, observedAt + 110000)).toBe(Infinity);
  original[0].expires_at = new Date(observedAt + 30 * 86400000 + 1000).toISOString();
  expect(nextFleetChange(original, observedAt)).toBe(observedAt + 1001);
  for (const server of original) {
    server.expires_at = null;
    server.latest_metrics!.recorded_at = new Date(observedAt + 90 * 86400000).toISOString();
  }
  expect(nextFleetChange(original, observedAt)).toBe(observedAt + 86400000);
});
