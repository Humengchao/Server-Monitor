import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet, host } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

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
