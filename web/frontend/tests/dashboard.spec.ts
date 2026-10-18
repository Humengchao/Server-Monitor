import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { GB, observedAt, sampleFleet, host } from './fixtures/fleet';

async function fixture(page: Page, options: { lang?: string; dark?: boolean; fail?: boolean; alertError?: boolean } = {}) {
  const state = { hosts: sampleFleet(), fail: options.fail ?? false, errors: [] as string[] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.addInitScript(({ lang, dark }) => {
    localStorage.setItem('lang', lang);
    localStorage.setItem('theme', dark ? 'dark' : 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  }, { lang: options.lang || 'en', dark: options.dark || false });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/servers') return state.fail ? route.fulfill({ status: 503, json: { error: 'unavailable' } })
      : route.fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: state.hosts });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/alerts/summary' && new URL(route.request().url()).searchParams.get('by_server') === '1') {
      return options.alertError ? route.fulfill({ status: 503, json: {} }) : route.fulfill({ json: {
        active: 2, servers: [{ server_id: 'busy-host', active: 2, rule_names: ['CPU high', 'Memory high'] }],
      } });
    }
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 2 } });
    return route.fulfill({ json: [] });
  });
  return state;
}

test('overview distinguishes collection states and exposes capacity-weighted resource totals', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  const stats = page.locator('.overview-grid');
  for (const [label, count] of [['Total servers', '4'], ['Online', '2'], ['Offline', '1'], ['Awaiting data', '1']]) {
    await expect(stats.locator('.overview-card').filter({ hasText: label }).locator('strong')).toHaveText(count);
  }
  const resources = page.getByRole('region', { name: 'Online resources' });
  await expect(resources).toContainText('30.0 GB / 40.0 GB');
  await expect(resources).toContainText('75%');
  await expect(resources).toContainText('2.00 KB/s');
  await expect(resources).not.toContainText('50.0 GB/s');
  await expect(page.getByRole('button', { name: 'Firing alerts 1', exact: true })).toBeEnabled();
  expect(state.errors).toEqual([]);
});

test('cards and table preserve unknown values and label historical readings', async ({ page }) => {
  await fixture(page);
  await page.goto('/dashboard');
  const pending = page.locator('.server-card').filter({ hasText: 'pending-host' });
  await expect(pending).toContainText('Awaiting data');
  await expect(pending).not.toContainText('0 GB');
  await expect(pending).not.toContainText('0d');
  const offline = page.locator('.server-card').filter({ hasText: 'offline-host' });
  await expect(offline).toContainText('Last sample');
  await expect(offline.locator('.throughput-grid')).not.toContainText('/s');
  await page.getByRole('radio', { name: 'List view' }).locator('..').click();
  const row = page.getByRole('row').filter({ hasText: 'pending-host' });
  await expect(row).toContainText('Awaiting data');
  await expect(row).not.toContainText('0d');
  await expect(page.getByRole('row').filter({ hasText: 'offline-host' })).toContainText('Historical readings');
});

test('attention filters compose with search and reset from an empty result', async ({ page }) => {
  await fixture(page);
  await page.goto('/dashboard');
  await page.getByRole('button', { name: 'Resources ≥ 90% 1', exact: true }).click();
  await expect(page.locator('.server-card')).toHaveCount(1);
  await expect(page.locator('.server-card')).toContainText('busy-host');
  await page.getByRole('textbox', { name: 'Search name, host, location or tag' }).fill('pending-host');
  await expect(page.getByText('No servers match the current filters.')).toBeVisible();
  await page.locator('.empty-state').getByRole('button', { name: 'Clear filters' }).click();
  await expect(page.locator('.server-card')).toHaveCount(4);
  await page.getByRole('button', { name: 'Due within 30 days 2', exact: true }).click();
  await expect(page.locator('.server-card')).toHaveCount(2);
  await expect(page.locator('.server-card').filter({ hasText: 'busy-host' })).toContainText('5d left');
  await page.getByRole('button', { name: 'Firing alerts 1', exact: true }).click();
  await expect(page.locator('.server-card')).toHaveCount(1);
  await page.getByRole('button', { name: 'Clear filters' }).click();
  await page.getByRole('radio', { name: 'Awaiting data 1', exact: true }).locator('..').click();
  await expect(page.locator('.server-card')).toHaveCount(1);
  await expect(page.locator('.server-card')).toContainText('pending-host');
});

test('polling upgrades a pending host and excludes unknown capacity from the average', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  const pending = page.locator('.server-card').filter({ hasText: 'pending-host' });
  await expect(pending).toContainText('Awaiting data');
  const recovered = host('pending-host', { memory_total: 0, disk_total: 0 });
  recovered.latest_metrics!.memory_total = 0;
  recovered.latest_metrics!.disk_used = 0;
  state.hosts = [...state.hosts.slice(0, 3), recovered];
  await expect(pending.locator('.status-pill')).toHaveText('Online', { timeout: 10000 });
  await expect(page.locator('.overview-card').filter({ hasText: 'Avg memory' }).locator('strong')).toHaveText('56%');
  await expect(pending.locator('.metric-progress').nth(1).locator('strong')).toHaveText('—');
  await expect(page.getByRole('region', { name: 'Online resources' })).toContainText('Valid samples from 2 / 3 online hosts');
});

test('initial failure shows unknown totals and retry recovers without a reload', async ({ page }) => {
  const state = await fixture(page, { fail: true });
  await page.goto('/dashboard');
  await expect(page.locator('.ant-result')).toBeVisible();
  await expect(page.locator('.overview-card').first().locator('strong')).toHaveText('—');
  state.fail = false;
  await page.locator('.ant-result').getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('.server-card')).toHaveCount(4);
});

test('failed background polling marks the retained snapshot and clears on recovery', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  await expect(page.locator('.server-card')).toHaveCount(4);
  state.fail = true;
  await expect(page.getByText('Connection lost — showing last known data')).toBeVisible({ timeout: 15000 });
  await expect(page.getByRole('region', { name: 'Online resources' })).toContainText('30.0 GB / 40.0 GB');
  state.fail = false;
  await expect(page.getByText('Connection lost — showing last known data')).not.toBeVisible({ timeout: 10000 });
});

test('unavailable alert data does not display a zero count', async ({ page }) => {
  await fixture(page, { alertError: true });
  await page.goto('/dashboard');
  await expect(page.getByRole('button', { name: 'Firing alerts —', exact: true })).toBeDisabled();
});

test('Chinese dashboard fits a narrow screen and resource summaries follow the dark theme', async ({ page }, testInfo) => {
  const state = await fixture(page, { lang: 'zh', dark: true });
  state.hosts[0].name = '跨区域生产服务 / A long host name for narrow screens';
  state.hosts[0].latest_metrics!.network_rx_bytes = 7 * GB;
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/dashboard');
  await expect(page.locator('.server-card')).toHaveCount(4);
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('dashboard-mobile-dark.png'), fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('dashboard-desktop-dark.png'), fullPage: true });
  expect(state.errors).toEqual([]);
});

test('fleet readings age during an outage without advancing the last refresh label', async ({ page }) => {
  await page.clock.install({ time: new Date('2040-01-01T00:00:00Z') });
  const state = await fixture(page);
  await page.goto('/dashboard');
  const onlineCount = page.locator('.overview-grid .overview-card').filter({ hasText: 'Online' }).locator('strong');
  await expect(onlineCount).toHaveText('2');
  const refreshLabel = page.locator('.section-heading [title]').first();
  const lastRefresh = await refreshLabel.textContent();
  state.fail = true;
  await page.clock.fastForward(125000);
  await expect(onlineCount).toHaveText('0');
  await expect(page.locator('.overview-card').filter({ hasText: 'Avg CPU' }).locator('strong')).toHaveText('—');
  await expect(page.getByRole('region', { name: 'Online resources' })).not.toContainText('/s');
  const idle = page.locator('.server-card').filter({ hasText: 'idle-host' });
  await expect(idle.locator('.status-pill')).toHaveText('Offline');
  await expect(idle.locator('.throughput-grid')).not.toContainText('/s');
  await expect(refreshLabel).toHaveText(lastRefresh!);
  await expect(page.getByRole('button', { name: 'Resources ≥ 90% 0', exact: true })).toBeVisible();
  await page.getByRole('radio', { name: 'List view' }).locator('..').click();
  await expect(page.getByRole('row').filter({ hasText: 'idle-host' })).toContainText('Historical readings');
  state.fail = false;
  await page.clock.fastForward(3100);
  await expect(onlineCount).toHaveText('2');
  await expect(page.getByRole('region', { name: 'Online resources' })).toContainText('2.00 KB/s');
  expect(state.errors).toEqual([]);
});

test('fleet sample freshness advances while a slow refresh is still pending', async ({ page }) => {
  await page.clock.install({ time: new Date('2040-01-01T00:00:00Z') });
  const state = await fixture(page);
  state.hosts = [host('near-expiry')];
  state.hosts[0].latest_metrics!.recorded_at = new Date(observedAt - 110000).toISOString();
  await page.goto('/dashboard');
  await expect(page.locator('.server-card .status-pill')).toHaveText('Online');
  let started = false;
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/servers', async route => {
    started = true;
    await held;
    return route.fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: state.hosts });
  });
  await page.clock.fastForward(3100);
  await expect.poll(() => started).toBe(true);
  await page.clock.fastForward(9000);
  await expect(page.locator('.server-card .status-pill')).toHaveText('Offline');
  await expect(page.locator('.throughput-grid')).not.toContainText('/s');
  release();
  await expect(page.locator('.server-card .status-pill')).toHaveText('Online');
  expect(state.errors).toEqual([]);
});

test('finishing a server operation after leaving the dashboard does not restart its requests', async ({ page }) => {
  const state = await fixture(page);
  let reads = 0;
  page.on('request', request => {
    if (new URL(request.url()).pathname === '/api/servers') reads++;
  });
  let started = false;
  let release!: () => void;
  const held = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/api/servers/idle-host', async route => {
    started = true;
    await held;
    return route.fulfill({ json: { message: 'deleted' } });
  });
  await page.goto('/dashboard');
  await page.getByRole('radio', { name: 'List view' }).locator('..').click();
  await page.getByRole('row').filter({ hasText: 'idle-host' }).getByRole('button', { name: 'Delete', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click();
  await expect.poll(() => started).toBe(true);
  await page.evaluate(() => {
    window.history.pushState({}, '', '/login');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.locator('.dashboard-page')).toHaveCount(0);
  const readsBefore = reads;
  const finished = page.waitForResponse(response => new URL(response.url()).pathname === '/api/servers/idle-host');
  release();
  await finished;
  // Wait for the mutation continuation, including its attempted refresh.
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(reads).toBe(readsBefore);
  expect(state.errors).toEqual([]);
});
