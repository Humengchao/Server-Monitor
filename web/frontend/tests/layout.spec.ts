import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { GB, observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const MB = 1024 ** 2;

function entry(name: string, size: number) {
  return { name, path: '/home/deploy/' + name, size, modified_at: '2030-06-01T00:00:00Z', mode: '-rw-r--r--', is_file: true, is_dir: false, is_symlink: false };
}

function publicNode(alias: string, status: 'online' | 'offline', expires_at: string | null) {
  return {
    alias, location: 'Tokyo', tags: [], server_type: 'linux', status, cpu_cores: 2, cpu_percent: status === 'online' ? 20 : 0,
    load_1: 0.2, load_5: 0.2, load_15: 0.2, memory_used: 2 * GB, memory_total: 8 * GB, memory_percent: 25,
    disk_used: 20 * GB, disk_total: 100 * GB, disk_percent: 20, network_rx_bytes: 0, network_tx_bytes: 0,
    network_rx_total_bytes: 10 * GB, network_tx_total_bytes: 5 * GB, traffic_limit_bytes: 0, traffic_percent: 0,
    uptime_seconds: status === 'online' ? 90000 : 0, expires_at, remaining_days: 0, billing_price: 0, billing_currency: 'CNY',
    billing_cycle: 'year', remaining_value: 0, latency_ms: status === 'online' ? 12 : 0, packet_loss_percent: status === 'online' ? 0 : 100,
    availability_30d: null,
  };
}

async function fixture(page: Page) {
  const hosts = sampleFleet();
  // Two-digit megabyte rates: the list-view network cell used to wrap these mid-value.
  hosts[1].latest_metrics!.network_rx_bytes = 18.4 * MB;
  hosts[1].latest_metrics!.network_tx_bytes = 22.1 * MB;
  hosts[0].has_docker = true;
  hosts[0].docker_version = '24.0.7';
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
    if (path === '/api/servers') return route.fulfill({ headers, json: state.hosts });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/public/status') return route.fulfill({ json: {
      overall: 'degraded', generated_at: new Date().toISOString(),
      summary: { total: 2, online: 1, degraded: 0, offline: 1, memory_used: 4 * GB, memory_total: 16 * GB, disk_used: 40 * GB, disk_total: 200 * GB, traffic_total_bytes: 30 * GB, network_rx_bytes: 0, network_tx_bytes: 0, remaining_value: 0 },
      nodes: [publicNode('alpha', 'online', '2031-01-01T00:00:00Z'), publicNode('beta', 'offline', '2020-01-01T00:00:00Z')],
      privacy: { anonymized: false, hidden_fields: [] },
    } });
    if (path.endsWith('/docker/containers')) return route.fulfill({ json: [{ id: 'c'.repeat(64), name: 'web', image: 'nginx:1.27', state: 'running', status: 'Up 2 days', ports: '', created: '2 days ago' }] });
    if (path.endsWith('/docker/stats')) return route.fulfill({ json: [] });
    if (path.endsWith('/files')) return route.fulfill({ json: { path: '/home/deploy', parent: '/home', truncated: false, entries: [entry('backup-2030-06-01-full.tar.gz', 118 * MB), entry('nginx.conf', 4096)] } });
    return route.fulfill({ json: [] });
  });
  return state;
}

test('auth pages render the hero headline at display size', async ({ page }) => {
  await fixture(page);
  for (const route of ['/login', '/register']) {
    await page.goto(route);
    const headline = page.locator('.auth-showcase-copy h1');
    await expect(headline).toBeVisible();
    const size = await headline.evaluate(element => parseFloat(getComputedStyle(element).fontSize));
    expect(size, route).toBeGreaterThanOrEqual(38);
  }
});

test('list view keeps each network rate on one line', async ({ page }) => {
  await fixture(page);
  await page.goto('/dashboard');
  await page.getByRole('radio', { name: 'List view' }).locator('..').click();
  const rates = page.getByRole('row').filter({ hasText: 'busy-host' }).locator('.table-throughput .ant-space-item');
  await expect(rates).toHaveCount(2);
  for (const rate of await rates.all()) {
    const box = await rate.boundingBox();
    expect(box!.height).toBeLessThan(24);
  }
});

test('public status page marks offline and expired nodes honestly', async ({ page }) => {
  await fixture(page);
  await page.goto('/status');
  const offline = page.locator('.glass-node-card.offline');
  await expect(offline).toContainText('beta');
  await expect(offline.locator('.node-plan-row strong').first()).toHaveText('—');
  await expect(offline.locator('.node-bottom-grid strong.is-expired')).toContainText(en['probe.expired']);
  const online = page.locator('.glass-node-card.online');
  await expect(online.locator('.node-plan-row strong').first()).toHaveText('1d 1h');
  await expect(online.locator('.node-bottom-grid strong.is-expired')).toHaveCount(0);
});

test('phone layouts keep file names and Docker host headers readable', async ({ page }) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/files?server=idle-host');
  const name = page.getByRole('button', { name: 'backup-2030-06-01-full.tar.gz', exact: true });
  await expect(name).toBeVisible();
  expect((await name.boundingBox())!.height).toBeLessThan(40);

  await page.goto('/docker');
  const header = page.locator('.ant-collapse-header').first();
  await expect(header).toContainText('idle-host');
  for (const count of await header.locator('.docker-count').all()) {
    expect((await count.boundingBox())!.height).toBeLessThan(24);
  }
  const detail = header.getByRole('button', { name: en['docker.serverDetail'] });
  await expect(detail.locator('.anticon')).toBeVisible();
  const box = (await detail.boundingBox())!;
  expect(box.x + box.width).toBeLessThanOrEqual(390);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  expect(state.errors).toEqual([]);
});
