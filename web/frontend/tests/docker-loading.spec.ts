import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const container = { id: 'a'.repeat(64), name: 'slow-stats-container', image: 'nginx:1.27', state: 'running', status: 'Up', ports: '', created: 'today' };

for (const view of ['fleet', 'detail'] as const) {
  test(`${view} keeps slow Docker statistics running across inventory refreshes`, async ({ page }) => {
    await page.clock.install();
    const host = sampleFleet()[0];
    host.has_docker = true;
    host.docker_version = '24.0.7';
    let inventories = 0;
    let statistics = 0;
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.addInitScript(() => {
      localStorage.setItem('lang', 'en');
      localStorage.setItem('token', 'fixture-token');
      localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
    });
    await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
    await page.route(/^https?:\/\/[^/]+\/api\//, async route => {
      const path = new URL(route.request().url()).pathname;
      const headers = { Date: new Date(observedAt).toUTCString() };
      if (path === '/api/servers') return route.fulfill({ headers, json: [host] });
      if (path === `/api/servers/${host.id}`) return route.fulfill({ headers, json: host });
      if (path.endsWith('/metrics/latest')) return route.fulfill({ headers, json: host.latest_metrics });
      if (path.endsWith('/metrics')) return route.fulfill({ headers, json: [host.latest_metrics] });
      if (path.endsWith('/docker/containers')) {
        inventories++;
        return route.fulfill({ json: [{ ...container, status: `Inventory ${inventories}` }] });
      }
      if (path.endsWith('/docker/stats')) {
        statistics++;
        await held;
        return route.fulfill({ json: [{ ...container, cpu_percent: 37.5, stats_available: true }] });
      }
      return route.fulfill({ json: [] });
    });

    await page.goto(view === 'fleet' ? `/docker?server=${host.id}` : `/servers/${host.id}`);
    if (view === 'detail') await page.getByRole('tab', { name: en['docker.title'], exact: true }).click();
    const row = page.getByRole('row').filter({ hasText: container.name });
    await expect(row).toBeVisible();
    await expect.poll(() => statistics).toBe(1);
    await page.clock.fastForward(16000);
    await expect.poll(() => inventories).toBe(2);
    await expect(row).toContainText('Inventory 2');
    // The inventory refresh should neither abort nor duplicate the slow query.
    expect(statistics).toBe(1);
    release();
    await expect(row.locator('.docker-resource-bar')).toContainText('37.5%');
    await expect(row).toContainText('Inventory 2');
    expect(statistics).toBe(1);
    expect(errors).toEqual([]);
  });
}
