import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import type { NetworkBucket, NetworkCheck, NetworkSample, NetworkSummary, NetworkTrend } from '../src/api/network';
import { host, observedAt } from './fixtures/fleet';
import { networkChartRows } from '../src/utils/networkChart';

const sample: NetworkSample = { recorded_at: new Date(observedAt).toISOString(), status: 'ok', sent: 5, received: 5, loss_percent: 0, avg_ms: 12, min_ms: 10, max_ms: 15, mdev_ms: 2, threshold_exceeded: false };
const check: NetworkCheck = { id: 'check-one', name: 'Local to target', source_server_id: null, target_server_id: 'target', packets: 5, enabled: true, loss_threshold: 10, latency_threshold_ms: null, created_at: sample.recorded_at, latest: sample };
const summary: NetworkSummary = { sent: 10000, received: 9990, loss_percent: 0.1, avg_ms: 12, min_ms: 10, max_ms: 1000, known_samples: 2000, unknown_samples: 2, loss_samples: 1, down_samples: 1, threshold_exceeded_samples: 2, sample_count: 2002 };

function trend(range: URLSearchParams): NetworkTrend {
  const since = range.get('since')!, until = range.get('until')!;
  const start = Date.parse(since), span = Date.parse(until) - start;
  const size = span / 500;
  const buckets: NetworkBucket[] = Array.from({ length: 500 }, (_, index) => ({ ...summary, recorded_at: new Date(start + index * size).toISOString(), until: new Date(start + (index + 1) * size).toISOString(), status: 'ok', known_samples: 4, unknown_samples: 0, loss_samples: 0, down_samples: 0, threshold_exceeded_samples: 0, sample_count: 4, max_ms: 15 }));
  buckets[3] = { ...buckets[3], max_ms: 1000, status: 'down', loss_samples: 1, down_samples: 1, unknown_samples: 2, threshold_exceeded_samples: 2, sample_count: 6 };
  buckets[4] = { ...buckets[4], status: 'error', sent: 0, received: 0, loss_percent: null, avg_ms: null, min_ms: null, max_ms: null, known_samples: 0, unknown_samples: 1, sample_count: 1 };
  buckets[5] = { ...buckets[4], recorded_at: buckets[5].recorded_at, until: buckets[5].until, status: 'none', unknown_samples: 0, sample_count: 0 };
  return { buckets, summary, since, until, bucket_seconds: size / 1000 };
}

async function setup(page: Page) {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => { localStorage.setItem('token', 'fixture'); localStorage.setItem('lang', 'en'); localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' })); });
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/servers') return route.fulfill({ json: [host('target'), host('source')] });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
    return route.fulfill({ json: [] });
  });
  await page.route('**/api/network/checks', route => route.fulfill({ json: [check] }));
  await page.route('**/api/network/checks/*/trend?**', route => route.fulfill({ json: trend(new URL(route.request().url()).searchParams) }));
  await page.route('**/api/network/checks/*/history?**', route => route.fulfill({ json: { samples: [sample], summary, next_cursor: null, truncated: false } }));
  return errors;
}

test('seven-day network comparison covers the full range with peaks, outages and unknowns in 500 buckets', async ({ page }) => {
  const errors = await setup(page);
  const ranges: URLSearchParams[] = [];
  await page.route('**/api/network/checks/*/trend?**', route => {
    const range = new URL(route.request().url()).searchParams;
    ranges.push(range);
    return route.fulfill({ json: trend(range) });
  });
  await page.goto('/network');
  await expect(page.getByText('1000.00 ms', { exact: true })).toBeVisible();
  await page.getByRole('combobox', { name: 'Time range', exact: true }).click();
  await page.getByText('Last 7 days', { exact: true }).click();
  await expect.poll(() => ranges.length).toBe(2);
  const range = ranges[1];
  expect(Date.parse(range.get('until')!) - Date.parse(range.get('since')!)).toBe(7 * 86400000);
  const data = trend(range), rows = networkChartRows({ probe: data });
  expect(rows).toHaveLength(500);
  expect(rows[0].time).toBe(Date.parse(data.since));
  expect(data.buckets.at(-1)!.until).toBe(data.until);
  expect(rows[3].values.probe.peak).toBe(1000);
  expect(rows[4].values.probe).toEqual({ avg: null, peak: null, event: 0 });
  expect(rows[5].values.probe).toEqual({ avg: null, peak: null, event: null });
  await expect(page.locator('.network-event-dot')).toHaveCount(2);
  await page.locator('.network-event-dot').first().hover();
  const tooltip = page.locator('.network-trend-tooltip');
  await expect(tooltip).toContainText('Peak RTT: 1000.00 ms');
  await expect(tooltip).toContainText('No-reply attempts: 1');
  await expect(tooltip).toContainText('Unknown / failed attempts: 2');
  expect(errors).toEqual([]);
});

test('unchanged list polls and unselected probes do not reload history, while a manual probe refreshes selected trends', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  let latest = sample, otherLatest = sample, lists = 0, trends = 0, details = 0;
  await page.route('**/api/network/checks', route => {
    lists++;
    return route.fulfill({ json: [{ ...check, latest }, { ...check, id: 'other', target_server_id: 'source', latest: otherLatest }] });
  });
  await page.route('**/api/network/checks/*/trend?**', route => { trends++; return route.fulfill({ json: trend(new URL(route.request().url()).searchParams) }); });
  await page.route('**/api/network/checks/*/history?**', route => { details++; return route.fulfill({ json: { samples: [latest], summary, next_cursor: null, truncated: false } }); });
  await page.route('**/api/network/checks/check-one/run', route => {
    latest = { ...sample, recorded_at: new Date(observedAt + 60000).toISOString() };
    return route.fulfill({ json: latest });
  });
  await page.goto('/network?server=target');
  await expect.poll(() => [trends, details]).toEqual([1, 1]);
  await page.clock.fastForward(15001);
  await expect.poll(() => lists).toBe(2);
  expect([trends, details]).toEqual([1, 1]);
  otherLatest = { ...sample, recorded_at: new Date(observedAt + 60000).toISOString() };
  await page.clock.fastForward(15001);
  await expect.poll(() => lists).toBe(3);
  expect([trends, details]).toEqual([1, 1]);
  await page.getByRole('row').filter({ hasText: 'Local to target' }).first().getByRole('button', { name: 'Probe now' }).click();
  await expect.poll(() => [trends, details]).toEqual([2, 2]);
  expect(errors).toEqual([]);
});

test('raw samples use bounded cursor pages within the fixed trend range and cancel stale pages', async ({ page }) => {
  const errors = await setup(page);
  const requests: URL[] = [];
  let held: Route | undefined;
  await page.route('**/api/network/checks/*/history?**', route => {
    const url = new URL(route.request().url()); requests.push(url);
    if (url.searchParams.get('cursor') === 'older-two') { held = route; return; }
    const older = url.searchParams.get('cursor') === 'older-one';
    return route.fulfill({ json: { samples: [{ ...sample, avg_ms: older ? 88 : 12 }], summary, next_cursor: older ? 'older-two' : 'older-one', truncated: true } });
  });
  await page.goto('/network');
  await expect(page.getByRole('button', { name: 'Older samples' })).toBeEnabled();
  await page.getByRole('button', { name: 'Older samples' }).click();
  await expect(page.getByText('88.00 ms', { exact: true })).toBeVisible();
  expect(requests).toHaveLength(2);
  expect(requests[1].searchParams.get('cursor')).toBe('older-one');
  for (const url of requests) {
    expect(url.searchParams.get('limit')).toBe('20');
    expect(url.searchParams.get('since')).toBe(requests[0].searchParams.get('since'));
    expect(url.searchParams.get('until')).toBe(requests[0].searchParams.get('until'));
  }
  await page.getByRole('button', { name: 'Newer samples' }).click();
  await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
  await expect(page.getByText('88.00 ms', { exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Older samples' }).click();
  await expect(page.getByText('88.00 ms', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Older samples' }).click();
  await expect.poll(() => !!held).toBe(true);
  const aborted = page.waitForEvent('requestfailed', request => request.url().includes('cursor=older-two'));
  await page.getByRole('combobox', { name: 'Time range', exact: true }).click();
  await page.getByText('Last 7 days', { exact: true }).click();
  await aborted;
  await held!.fulfill({ json: { samples: [{ ...sample, avg_ms: 999 }], summary, next_cursor: null } });
  await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
  await expect(page.getByText('999.00 ms', { exact: true })).toHaveCount(0);
  expect(requests.at(-1)!.searchParams.get('cursor')).toBeNull();
  expect(errors).toEqual([]);
});

test('stopped probes advance the visible trend window once a minute without disturbing an older raw page', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  const trendRequests: URL[] = [], rawRequests: URL[] = [];
  await page.route('**/api/network/checks/*/trend?**', route => {
    const url = new URL(route.request().url()); trendRequests.push(url);
    return route.fulfill({ json: trend(url.searchParams) });
  });
  await page.route('**/api/network/checks/*/history?**', route => {
    const url = new URL(route.request().url()); rawRequests.push(url);
    return route.fulfill({ json: { samples: [sample], summary, next_cursor: `cursor-${rawRequests.length}`, truncated: true } });
  });
  await page.goto('/network');
  await expect(page.getByRole('button', { name: 'Older samples' })).toBeEnabled();
  await page.getByRole('button', { name: 'Older samples' }).click();
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  await expect.poll(() => rawRequests.length).toBe(2);
  await page.clock.fastForward(61000);
  await expect.poll(() => trendRequests.length).toBe(2);
  expect(Date.parse(trendRequests[1].searchParams.get('until')!)).toBeGreaterThan(Date.parse(trendRequests[0].searchParams.get('until')!));
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  expect(rawRequests).toHaveLength(2);
  await page.getByRole('button', { name: 'Older samples' }).click();
  await expect.poll(() => rawRequests.length).toBe(3);
  expect(rawRequests[2].searchParams.get('since')).toBe(rawRequests[0].searchParams.get('since'));
  expect(rawRequests[2].searchParams.get('until')).toBe(rawRequests[0].searchParams.get('until'));
  expect(errors).toEqual([]);
});

test('a completed manual probe cancels an older list poll before it can replace the new sample', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  let held: Route | undefined;
  let lists = 0, trends = 0;
  const fresh = { ...sample, recorded_at: new Date(observedAt + 60000).toISOString() };
  await page.route('**/api/network/checks', route => {
    lists++;
    if (lists === 2) { held = route; return; }
    return route.fulfill({ json: [{ ...check, latest: lists > 2 ? fresh : sample }] });
  });
  await page.route('**/api/network/checks/*/trend?**', route => { trends++; return route.fulfill({ json: trend(new URL(route.request().url()).searchParams) }); });
  await page.route('**/api/network/checks/check-one/run', route => route.fulfill({ json: fresh }));
  await page.goto('/network');
  await expect.poll(() => trends).toBe(1);
  await page.clock.fastForward(15001);
  await expect.poll(() => !!held).toBe(true);
  const aborted = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/network/checks');
  await page.getByRole('button', { name: 'Probe now', exact: true }).click();
  await aborted;
  await held!.fulfill({ json: [check] });
  await expect.poll(() => trends).toBe(2);
  await expect.poll(() => lists).toBe(3);
  expect(errors).toEqual([]);
});

test('an empty network interval is distinct from unknown attempts in Chinese', async ({ page }) => {
  const errors = await setup(page);
  await page.addInitScript(() => localStorage.setItem('lang', 'zh'));
  await page.route('**/api/network/checks/*/trend?**', route => {
    const data = trend(new URL(route.request().url()).searchParams);
    const empty = { ...summary, sample_count: 0, known_samples: 0, unknown_samples: 0, loss_samples: 0, down_samples: 0, threshold_exceeded_samples: 0, sent: 0, received: 0, avg_ms: null, min_ms: null, max_ms: null, loss_percent: null };
    return route.fulfill({ json: { ...data, summary: empty, buckets: data.buckets.map(bucket => ({ ...bucket, ...empty, status: 'none' })) } });
  });
  await page.goto('/network');
  await expect(page.getByText('此区间没有采样', { exact: true })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: '未知 / 失败次数', exact: true })).toBeVisible();
  await expect(page.locator('.network-event-dot')).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('an isolated successful measurement remains visible between empty buckets', async ({ page }) => {
  const errors = await setup(page);
  await page.route('**/api/network/checks/*/trend?**', route => {
    const data = trend(new URL(route.request().url()).searchParams);
    const empty = { ...summary, sample_count: 0, known_samples: 0, unknown_samples: 0, loss_samples: 0, down_samples: 0, threshold_exceeded_samples: 0, sent: 0, received: 0, avg_ms: null, min_ms: null, max_ms: null, loss_percent: null };
    const buckets: NetworkBucket[] = data.buckets.map(bucket => ({ ...bucket, ...empty, status: 'none' }));
    buckets[100] = { ...data.buckets[100], avg_ms: 12, max_ms: 15 };
    return route.fulfill({ json: { ...data, buckets } });
  });
  await page.goto('/network');
  await expect(page.locator('.recharts-line-dot')).toHaveCount(1);
  await expect(page.locator('.recharts-line-dot')).toBeVisible();
  await expect(page.locator('.network-event-dot')).toHaveCount(0);
  expect(errors).toEqual([]);
});
