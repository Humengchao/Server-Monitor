import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet, host } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const zh: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/zh.json', import.meta.url), 'utf8'));

// The detail page judges liveness against the API's Date header, so every
// response is stamped with the fixture's clock rather than the wall clock.
async function fixture(page: Page, options: { statsFail?: boolean } = {}) {
  const hosts = sampleFleet();
  hosts[0].has_docker = true;
  hosts[0].docker_version = '24.0.7';
  hosts.push(host('win-host', { server_type: 'windows' }));
  const state = { hosts, errors: [] as string[] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    const headers = { Date: new Date(observedAt).toUTCString() };
    const match = path.match(/^\/api\/servers\/([^/]+)(\/.*)?$/);
    if (path === '/api/servers') return route.fulfill({ headers, json: state.hosts });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (match) {
      const server = state.hosts.find(item => item.id === match[1]);
      if (!server) return route.fulfill({ status: 404, json: { error: 'not found' } });
      const rest = match[2] || '';
      if (rest === '') return route.fulfill({ headers, json: server });
      if (rest === '/metrics/latest') return route.fulfill({ headers, json: server.latest_metrics });
      if (rest === '/metrics') return route.fulfill({ headers, json: server.latest_metrics ? [server.latest_metrics] : [] });
      if (rest === '/docker/containers') return route.fulfill({ json: [{ id: 'c'.repeat(64), name: 'web', image: 'nginx:1.27', state: 'running', status: 'Up 2 days', ports: '', created: '2 days ago' }] });
      if (rest === '/docker/stats') return options.statsFail
        ? route.fulfill({ status: 502, json: { error: 'docker stats failed' } })
        : route.fulfill({ json: [] });
    }
    return route.fulfill({ json: [] });
  });
  return state;
}

function tile(page: Page, label: string) {
  return page.locator('.stat-tile').filter({ has: page.locator('small', { hasText: label }) });
}

test('a never-sampled host is awaiting data, not idle at zero', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/servers/pending-host');
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.pending']);
  await expect(page.locator('.detail-hero .detail-sample-state.pending')).toHaveText(en['dashboard.pending']);
  const tiles = page.locator('.stat-tile-grid');
  await expect(tile(page, en['metrics.cpu']).locator('strong')).toHaveText('—');
  await expect(tile(page, en['metrics.uptime']).locator('strong')).toHaveText('—');
  await expect(tiles).not.toContainText('0%');
  await expect(tiles).not.toContainText('0d');
  await expect(tiles).not.toContainText('0 B');
  await expect(tiles).not.toContainText(en['detail.cores'].replace('{{count}}', '').trim());
  expect(state.errors).toEqual([]);
});

test('a stale host keeps its last capacity readings but not live rates', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/servers/offline-host');
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.offline']);
  await expect(page.locator('.detail-hero .detail-sample-state.offline')).toContainText(en['dashboard.lastSample'].split('·')[0].trim());
  await expect(page.locator('.stat-tile-grid')).toHaveClass(/is-stale/);
  await expect(tile(page, en['metrics.cpu']).locator('strong')).toHaveText('99%');
  await expect(tile(page, en['metrics.uptime']).locator('strong')).toHaveText('—');
  await expect(tile(page, en['metrics.latency']).locator('strong')).toHaveText('—');
  await expect(tile(page, en['metrics.totalDownload']).locator('.stat-tile-hint')).toHaveCount(0);
  await expect(tile(page, en['detail.sampledAt']).locator('strong')).toContainText('2030');
  expect(state.errors).toEqual([]);
});

test('a live host shows rates, and a Windows host shows no load average', async ({ page }) => {
  await fixture(page);
  await page.goto('/servers/idle-host');
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.online']);
  await expect(page.locator('.detail-hero .detail-sample-state.online')).toContainText(en['detail.liveSample'].split('·')[0].trim());
  await expect(tile(page, en['metrics.totalDownload']).locator('.stat-tile-hint')).toContainText('/s');
  await expect(tile(page, en['metrics.latency']).locator('.stat-tile-hint')).toContainText(en['detail.load'].split('{{')[0].trim());
  await page.goto('/servers/win-host');
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.online']);
  await expect(tile(page, en['metrics.latency']).locator('.stat-tile-hint')).toHaveCount(0);
});

test('a failed container statistics request explains itself in words', async ({ page }) => {
  const state = await fixture(page, { statsFail: true });
  await page.goto('/servers/idle-host');
  await page.getByRole('tab', { name: en['docker.title'], exact: true }).click();
  await expect(page.getByText('web', { exact: true })).toBeVisible();
  await expect(page.getByText(en['docker.statsFailed'])).toBeVisible();
  await expect(page.locator('.server-detail-tabs')).not.toContainText('docker.stats');
  expect(state.errors).toEqual([]);
});

test('offline host keeps a capacity-aware memory figure', async ({ page }) => {
  await fixture(page);
  await page.goto('/servers/offline-host');
  await expect(tile(page, en['metrics.memory']).locator('strong')).toHaveText('2.0 GB / 8.0 GB');
});

for (const source of ['latest', 'embedded'] as const) {
  test(`${source} samples expire during refresh failures despite a skewed browser clock, then recover`, async ({ page }) => {
    await page.clock.install({ time: new Date('2040-01-01T00:00:00Z') });
    const state = await fixture(page);
    let mode: 'ok' | 'failed' | 'recovered' = source === 'latest' ? 'ok' : 'failed';
    let requests = 0;
    await page.route('**/api/servers/idle-host/metrics/latest', route => {
      requests++;
      if (mode === 'failed') return route.fulfill({ status: 503, json: { error: 'unavailable' } });
      const apiNow = mode === 'recovered' ? observedAt + 180000 : observedAt;
      return route.fulfill({
        headers: { Date: new Date(apiNow).toUTCString() },
        json: { ...state.hosts[0].latest_metrics, recorded_at: new Date(apiNow - 10000).toISOString() },
      });
    });
    await page.goto('/servers/idle-host');
    await expect.poll(() => requests).toBeGreaterThan(0);
    await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.online']);
    await expect(tile(page, en['metrics.totalDownload']).locator('.stat-tile-hint')).toContainText('/s');
    mode = 'failed';
    await page.clock.fastForward(3100);
    await expect(page.getByText(en['probe.staleHint'], { exact: true })).toBeVisible();
    await page.clock.fastForward(120000);
    await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.offline']);
    await expect(tile(page, en['metrics.uptime']).locator('strong')).toHaveText('—');
    await expect(tile(page, en['metrics.totalDownload']).locator('.stat-tile-hint')).toHaveCount(0);
    mode = 'recovered';
    await page.clock.fastForward(3100);
    await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.online']);
    await expect(page.getByText(en['probe.staleHint'], { exact: true })).toHaveCount(0);
    expect(state.errors).toEqual([]);
  });
}

test('samples expire while the next request is still waiting for its response', async ({ page }) => {
  await page.clock.install({ time: new Date('2040-01-01T00:00:00Z') });
  const state = await fixture(page);
  state.hosts[0].latest_metrics!.recorded_at = new Date(observedAt - 110000).toISOString();
  let hold = false;
  let waiting = false;
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/servers/idle-host/metrics/latest', async route => {
    if (hold) { waiting = true; await held; }
    await route.fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: { ...state.hosts[0].latest_metrics, cpu_percent: 23 } });
  });
  await page.goto('/servers/idle-host');
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.online']);
  await expect(tile(page, en['metrics.cpu']).locator('strong')).toHaveText('23%');
  hold = true;
  await page.clock.fastForward(3100);
  await expect.poll(() => waiting).toBe(true);
  await page.clock.fastForward(7100);
  await expect(page.locator('.detail-hero .status-pill')).toHaveText(en['dashboard.offline']);
  await expect(page.getByText(en['probe.staleHint'], { exact: true })).toHaveCount(0);
  const releasedResponse = page.waitForResponse(response => response.url().endsWith('/metrics/latest'));
  release();
  await releasedResponse;
  expect(state.errors).toEqual([]);
});

test('changing language keeps unsaved notes and does not refetch the host', async ({ page }) => {
  const state = await fixture(page);
  state.hosts[0].notes = 'Saved notes';
  let serverReads = 0;
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/api/servers/idle-host' && request.method() === 'GET') serverReads++;
  });
  await page.goto('/servers/idle-host');
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  const notes = page.locator('textarea');
  await expect(notes).toHaveValue('Saved notes');
  await notes.fill('Unsaved draft');
  const readsBefore = serverReads;
  await page.getByRole('button', { name: '切换到中文', exact: true }).click();
  await expect(page.getByRole('tab', { name: zh['server.notes'], exact: true })).toBeVisible();
  await expect(notes).toHaveValue('Unsaved draft');
  await expect(page.getByRole('button', { name: zh['common.save'], exact: true })).toBeEnabled();
  await page.getByRole('button', { name: 'Switch to English', exact: true }).click();
  await expect(notes).toHaveValue('Unsaved draft');
  expect(serverReads).toBe(readsBefore);
  expect(state.errors).toEqual([]);
});

test('notes edited during a save remain dirty until those edits are saved', async ({ page }) => {
  const state = await fixture(page);
  state.hosts[0].notes = 'Original notes';
  const submitted: string[] = [];
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/servers/idle-host/notes', async route => {
    expect(route.request().method()).toBe('PATCH');
    expect(Object.keys(route.request().postDataJSON())).toEqual(['notes']);
    const notes = route.request().postDataJSON().notes as string;
    submitted.push(notes);
    if (submitted.length === 1) await held;
    state.hosts[0].notes = notes;
    await route.fulfill({ json: { message: 'updated' } });
  });
  await page.goto('/servers/idle-host');
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  const notes = page.locator('textarea');
  const save = page.getByRole('button', { name: en['common.save'], exact: true });
  await notes.fill('First draft');
  await save.click();
  await expect.poll(() => submitted).toEqual(['First draft']);
  await notes.fill('Second draft typed during save');
  release();
  await expect(save).not.toHaveClass(/ant-btn-loading/);
  await expect(notes).toHaveValue('Second draft typed during save');
  await expect(save).toBeEnabled();
  await save.click();
  await expect.poll(() => submitted).toEqual(['First draft', 'Second draft typed during save']);
  await expect(save).toBeDisabled();
  expect(state.errors).toEqual([]);
});

test('a changed history window hides old charts and disables export while waiting or failed', async ({ page }) => {
  const state = await fixture(page);
  let fail = true;
  let requests = 0;
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.goto('/servers/idle-host');
  const exportButton = page.getByRole('button', { name: en['metrics.export'], exact: true });
  await expect(exportButton).toBeEnabled();
  await expect(page.locator('.metrics-charts')).toBeVisible();
  await page.route('**/api/servers/idle-host/metrics?*', async route => {
    requests++;
    if (fail) {
      await held;
      return route.fulfill({ status: 503, json: { error: 'unavailable' } });
    }
    return route.fulfill({ json: [state.hosts[0].latest_metrics] });
  });
  await page.getByRole('radio', { name: en['preset.yesterday'], exact: true }).locator('..').click();
  await expect.poll(() => requests).toBe(1);
  await expect(page.locator('.metrics-history')).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator('.metrics-charts')).toHaveCount(0);
  await expect(exportButton).toBeDisabled();
  release();
  await expect(page.locator('.metrics-history .ant-result')).toBeVisible();
  await expect(exportButton).toBeDisabled();
  await expect(page.locator('.metrics-charts')).toHaveCount(0);
  fail = false;
  await page.locator('.metrics-history').getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await expect(exportButton).toBeEnabled();
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(requests).toBe(2);
  expect(state.errors).toEqual([]);
});

test('switching hosts in the same detail component shows request failures and can retry', async ({ page }) => {
  const state = await fixture(page);
  let fail = true;
  await page.route('**/api/servers/busy-host', route => fail
    ? route.fulfill({ status: 503, json: { error: 'unavailable' } })
    : route.fallback());
  await page.goto('/servers/idle-host');
  await expect(page.locator('.detail-identity h3')).toHaveText('idle-host');
  await page.evaluate(() => {
    window.history.pushState({}, '', '/servers/busy-host');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  const failure = page.locator('.ant-result');
  await expect(failure).toContainText(en['server.loadFailed']);
  await expect(page.locator('.detail-hero')).toHaveCount(0);
  fail = false;
  await failure.getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await expect(page.locator('.detail-identity h3')).toHaveText('busy-host');
  await expect(tile(page, en['metrics.cpu']).locator('strong')).toHaveText('92%');
  expect(state.errors).toEqual([]);
});

test('history pauses outside its tab, resumes live windows immediately and preserves fixed windows', async ({ page }) => {
  await page.clock.install({ time: new Date(observedAt) });
  const state = await fixture(page);
  const windows: string[] = [];
  let latestReads = 0;
  page.on('request', request => {
    const url = new URL(request.url());
    if (url.pathname === '/api/servers/idle-host/metrics') windows.push(url.searchParams.get('until')!);
    if (url.pathname === '/api/servers/idle-host/metrics/latest') latestReads++;
  });
  await page.goto('/servers/idle-host');
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(windows).toHaveLength(1);
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  await expect(page.locator('.metrics-charts')).toHaveCount(0);
  const initialLatest = latestReads;
  await page.clock.fastForward(61000);
  await expect.poll(() => latestReads).toBeGreaterThan(initialLatest);
  expect(windows).toHaveLength(1);
  await page.getByRole('tab', { name: en['metrics.title'], exact: true }).click();
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(windows).toHaveLength(2);
  expect(Date.parse(windows[1])).toBeGreaterThan(Date.parse(windows[0]));

  await page.getByRole('radio', { name: en['preset.yesterday'], exact: true }).locator('..').click();
  await expect(page.locator('.metrics-history')).toHaveAttribute('aria-busy', 'false');
  expect(windows).toHaveLength(3);
  const fixedUntil = windows[2];
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  await page.clock.fastForward(61000);
  await page.getByRole('tab', { name: en['metrics.title'], exact: true }).click();
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(windows).toHaveLength(3);
  expect(windows.at(-1)).toBe(fixedUntil);
  expect(state.errors).toEqual([]);
});

test('backgrounding the browser retains history and refreshes it when visible again', async ({ page }) => {
  await page.clock.install({ time: new Date(observedAt) });
  const state = await fixture(page);
  let reads = 0;
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/api/servers/idle-host/metrics') reads++;
  });
  await page.goto('/servers/idle-host');
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(reads).toBe(1);
  await page.evaluate(() => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await page.clock.fastForward(61000);
  expect(reads).toBe(1);
  await expect(page.locator('.metrics-charts')).toBeVisible();
  await expect(page.getByRole('button', { name: en['metrics.export'], exact: true })).toBeEnabled();
  await page.evaluate(() => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await expect.poll(() => reads).toBe(2);
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(state.errors).toEqual([]);
});

async function trackReadAborts(page: Page) {
  await page.addInitScript(() => {
    const original = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function (method: string, url: string | URL, async = true, username?: string | null, password?: string | null) {
      this.addEventListener('abort', () => {
        const aborted = JSON.parse(document.documentElement.dataset.abortedReads || '[]') as string[];
        aborted.push(new URL(String(url), location.href).pathname);
        document.documentElement.dataset.abortedReads = JSON.stringify(aborted);
      });
      return original.call(this, method, url, async, username, password);
    };
  });
}

test('leaving history aborts a pending chart request and returning fetches the current window', async ({ page }) => {
  await trackReadAborts(page);
  await page.clock.install({ time: new Date(observedAt) });
  await fixture(page);
  let reads = 0;
  await page.route('**/api/servers/idle-host/metrics?*', route => {
    reads++;
    if (reads === 1) return;
    return route.fallback();
  });
  await page.goto('/servers/idle-host');
  await expect.poll(() => reads).toBe(1);
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  await expect.poll(() => page.evaluate(() => JSON.parse(document.documentElement.dataset.abortedReads || '[]')))
    .toContain('/api/servers/idle-host/metrics');
  await page.clock.fastForward(61000);
  expect(reads).toBe(1);
  await page.getByRole('tab', { name: en['metrics.title'], exact: true }).click();
  await expect(page.locator('.metrics-charts')).toBeVisible();
  expect(reads).toBe(2);
});

for (const source of ['fleet', 'detail'] as const) {
  test(`${source} availability requests are cancelled when their view leaves`, async ({ page }) => {
    await trackReadAborts(page);
    await page.clock.install({ time: new Date(observedAt) });
    await fixture(page);
    const reads: string[] = [];
    await page.route(/\/api\/servers(?:\/idle-host)?\/uptime(?:\?|$)/, route => {
      reads.push(new URL(route.request().url()).pathname);
    });
    await page.goto(source === 'fleet' ? '/dashboard' : '/servers/idle-host');
    if (source === 'detail') await page.getByRole('tab', { name: en['uptime.title'], exact: true }).click();
    const expected = source === 'fleet' ? ['/api/servers/uptime'] : ['/api/servers/idle-host/uptime', '/api/servers/uptime'];
    await expect.poll(() => [...reads].sort()).toEqual([...expected].sort());
    if (source === 'detail') await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
    else await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
    await expect.poll(() => page.evaluate(() => JSON.parse(document.documentElement.dataset.abortedReads || '[]')))
      .toEqual(expect.arrayContaining(expected));
    await page.clock.fastForward(310000);
    expect(reads).toHaveLength(expected.length);
  });
}

test('availability failure cancels its remaining request and the next visit can recover', async ({ page }) => {
  await trackReadAborts(page);
  const state = await fixture(page);
  let fail = true;
  let reads = 0;
  await page.route('**/api/servers/idle-host/uptime?*', route => {
    reads++;
    return fail
      ? route.fulfill({ status: 503, json: { error: 'unavailable' } })
      : route.fulfill({ json: { server_id: 'idle-host', days: [], outages: [] } });
  });
  await page.route('**/api/servers/uptime', route => {
    if (fail) return;
    return route.fulfill({ json: { servers: [] } });
  });
  await page.goto('/servers/idle-host');
  await page.getByRole('tab', { name: en['uptime.title'], exact: true }).click();
  await expect(page.getByText(en['uptime.loadFailed'], { exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => JSON.parse(document.documentElement.dataset.abortedReads || '[]')))
    .toContain('/api/servers/uptime');
  fail = false;
  await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
  await page.getByRole('tab', { name: en['uptime.title'], exact: true }).click();
  await expect(page.getByText(en['uptime.noHistory'], { exact: true })).toBeVisible();
  expect(reads).toBe(2);
  expect(state.errors).toEqual([]);
});

test('a slow initial latest read survives polling and still cancels immediately when the host changes', async ({ page }) => {
  await trackReadAborts(page);
  await page.clock.install({ time: new Date(observedAt) });
  const state = await fixture(page);
  const pending: Route[] = [];
  const reads: string[] = [];
  await page.route(/\/api\/servers\/(idle-host|busy-host)\/metrics\/latest$/, route => {
    reads.push(new URL(route.request().url()).pathname);
    pending.push(route);
  });
  await page.goto('/servers/idle-host');
  await expect.poll(() => reads).toEqual(['/api/servers/idle-host/metrics/latest']);
  await page.clock.fastForward(3100);
  await expect(page.locator('.detail-identity h3')).toHaveText('idle-host');
  expect(reads).toHaveLength(1);
  expect(await page.evaluate(() => JSON.parse(document.documentElement.dataset.abortedReads || '[]')))
    .not.toContain('/api/servers/idle-host/metrics/latest');

  await page.evaluate(() => {
    history.pushState({}, '', '/servers/busy-host');
    dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect.poll(() => reads).toEqual(['/api/servers/idle-host/metrics/latest', '/api/servers/busy-host/metrics/latest']);
  await expect.poll(() => page.evaluate(() => JSON.parse(document.documentElement.dataset.abortedReads || '[]')))
    .toContain('/api/servers/idle-host/metrics/latest');
  // Completing the old transport must not clear the new host's in-flight guard.
  await pending[0].fulfill({ json: { ...state.hosts[0].latest_metrics, cpu_percent: 17 } });
  await page.clock.fastForward(3100);
  expect(reads).toHaveLength(2);
  await pending[1].fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: { ...state.hosts[1].latest_metrics, cpu_percent: 68 } });
  await expect(tile(page, en['metrics.cpu']).locator('strong')).toHaveText('68%');
  await page.clock.fastForward(3100);
  await expect.poll(() => reads).toHaveLength(3);
  expect(reads.at(-1)).toBe('/api/servers/busy-host/metrics/latest');
  expect(state.errors).toEqual([]);
});
