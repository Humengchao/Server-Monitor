import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const zh: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/zh.json', import.meta.url), 'utf8'));

const panels = [
  { endpoint: 'processes', label: 'process.title', interval: 5000 },
  { endpoint: 'services', label: 'service.title', interval: 20000 },
  { endpoint: 'ports', label: 'port.title', interval: 30000 },
] as const;
type Panel = typeof panels[number];

function snapshot(endpoint: Panel['endpoint'], name: string) {
  if (endpoint === 'processes') return { processes: [{
    pid: 321, command: name, user: 'monitor', cpu_percent: 3, mem_percent: 1,
    rss_bytes: 1024, state: 'S', elapsed_seconds: 120,
  }], total: 1, returned: 1 };
  if (endpoint === 'services') return { services: [{
    name, load: 'loaded', active: 'inactive', sub: 'dead', enabled: 'enabled', description: '',
  }], total: 1, returned: 1, supported: true, manager: 'systemd' };
  return { ports: [{
    protocol: 'tcp', address: '127.0.0.1', port: 8080, process: name, pid: 321, exposure: 'loopback',
  }], total: 1, returned: 1 };
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}

async function fixture(page: Page) {
  const hosts = sampleFleet();
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.clock.install();
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route(/^https?:\/\/[^/]+\/api\//, route => {
    const path = new URL(route.request().url()).pathname;
    const headers = { Date: new Date(observedAt).toUTCString() };
    if (path === '/api/servers') return route.fulfill({ headers, json: hosts });
    const match = path.match(/^\/api\/servers\/([^/]+)(\/.*)?$/);
    const server = hosts.find(item => item.id === match?.[1]);
    if (server) {
      if (!match![2]) return route.fulfill({ headers, json: server });
      if (match![2] === '/metrics/latest') return route.fulfill({ headers, json: server.latest_metrics });
      if (match![2] === '/metrics') return route.fulfill({ headers, json: [server.latest_metrics] });
    }
    return route.fulfill({ json: [] });
  });
  return errors;
}

async function openPanel(page: Page, panel: Panel) {
  await page.goto('/servers/idle-host');
  await page.getByRole('tab', { name: en[panel.label], exact: true }).click();
}

for (const panel of panels) {
  test(`${panel.endpoint}: polling and language changes preserve an in-flight manual refresh`, async ({ page }) => {
    const errors = await fixture(page);
    const held = deferred();
    let reads = 0;
    await page.route(`**/api/servers/idle-host/${panel.endpoint}`, async route => {
      const index = ++reads;
      if (index === 2) await held.promise;
      await route.fulfill({ json: snapshot(panel.endpoint, `snapshot-${index}`) });
    });
    await openPanel(page, panel);
    await expect(page.getByRole('row').filter({ hasText: 'snapshot-1' })).toBeVisible();
    // Start a manual read shortly before the next poll, avoiding the API timeout.
    await page.clock.fastForward(panel.interval - 2500);
    await page.locator('.process-panel').getByRole('button', { name: en['common.refresh'], exact: true }).click();
    await expect.poll(() => reads).toBe(2);
    await page.clock.fastForward(3000);
    await page.getByRole('button', { name: '切换到中文', exact: true }).click();
    await expect(page.getByRole('tab', { name: zh[panel.label], exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Switch to English', exact: true }).click();
    expect(reads).toBe(2);
    held.resolve();
    await expect(page.getByRole('row').filter({ hasText: 'snapshot-2' })).toBeVisible();
    expect(reads).toBe(2);
    expect(errors).toEqual([]);
  });
}

test('processes: a slow initial SSH read survives the first polling tick', async ({ page }) => {
  const errors = await fixture(page);
  const held = deferred();
  let reads = 0;
  await page.route('**/api/servers/idle-host/processes', async route => {
    reads++;
    await held.promise;
    await route.fulfill({ json: snapshot('processes', 'slow-process') });
  });
  await openPanel(page, panels[0]);
  await expect.poll(() => reads).toBe(1);
  await page.clock.fastForward(5500);
  expect(reads).toBe(1);
  held.resolve();
  await expect(page.getByRole('row').filter({ hasText: 'slow-process' })).toBeVisible();
  expect(reads).toBe(1);
  expect(errors).toEqual([]);
});

async function runAction(page: Page, panel: Panel) {
  const row = page.getByRole('row').filter({ hasText: 'action-target' });
  if (panel.endpoint === 'processes') {
    await row.getByRole('button', { name: `${en['process.terminate']} action-target`, exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: en['process.terminate'], exact: true }).click();
  } else {
    await row.getByRole('button', { name: en['service.action.start'], exact: true }).click();
  }
}

for (const panel of panels.slice(0, 2)) {
  for (const completion of ['before', 'after'] as const) {
    test(`${panel.endpoint}: action completed ${completion} leaving the tab cannot restart SSH reads`, async ({ page }) => {
      const errors = await fixture(page);
      const heldAction = deferred();
      let reads = 0;
      let actions = 0;
      await page.route(`**/api/servers/idle-host/${panel.endpoint}`, route => {
        reads++;
        return route.fulfill({ json: snapshot(panel.endpoint, 'action-target') });
      });
      await page.route(`**/api/servers/idle-host/${panel.endpoint}/*`, async route => {
        actions++;
        if (completion === 'after') await heldAction.promise;
        await route.fulfill({ json: { message: 'ok' } });
      });
      await openPanel(page, panel);
      await expect(page.getByRole('row').filter({ hasText: 'action-target' })).toBeVisible();
      await runAction(page, panel);
      await expect.poll(() => actions).toBe(1);
      if (completion === 'before') await expect(page.locator('.ant-message-success')).toBeVisible();
      if (panel.endpoint === 'processes' && completion === 'after') {
        // The confirmation stays open until DELETE finishes; browser navigation
        // can still leave the page while its modal is waiting for the response.
        await page.evaluate(() => {
          window.history.pushState({}, '', '/dashboard');
          window.dispatchEvent(new PopStateEvent('popstate'));
        });
      } else {
        await page.getByRole('tab', { name: en['server.notes'], exact: true }).click();
      }
      await expect(page.locator('.process-panel')).toHaveCount(0);
      if (completion === 'after') {
        const response = page.waitForResponse(res => res.url().includes(`/${panel.endpoint}/`));
        heldAction.resolve();
        await response;
      }
      await page.clock.fastForward(2500);
      expect(reads).toBe(1);
      expect(errors).toEqual([]);
    });
  }
}

test('processes: an action refresh waits for an older read and then fetches a new snapshot', async ({ page }) => {
  const errors = await fixture(page);
  const held = deferred();
  let reads = 0;
  let actions = 0;
  await page.route('**/api/servers/idle-host/processes', async route => {
    const index = ++reads;
    if (index === 2) await held.promise;
    await route.fulfill({ json: index >= 3 ? { processes: [], total: 0, returned: 0 } : snapshot('processes', 'action-target') });
  });
  await page.route('**/api/servers/idle-host/processes/321', route => {
    actions++;
    return route.fulfill({ json: { message: 'ok' } });
  });
  await openPanel(page, panels[0]);
  await expect(page.getByRole('row').filter({ hasText: 'action-target' })).toBeVisible();
  await page.locator('.process-panel').getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await expect.poll(() => reads).toBe(2);
  await runAction(page, panels[0]);
  await expect.poll(() => actions).toBe(1);
  await expect(page.locator('.ant-message-success')).toBeVisible();
  await page.clock.fastForward(1000);
  expect(reads).toBe(2);
  held.resolve();
  await expect.poll(() => reads).toBe(3);
  await expect(page.getByRole('row').filter({ hasText: 'action-target' })).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('ports: switching hosts cancels an old read and never displays its late rows', async ({ page }) => {
  const errors = await fixture(page);
  const held = deferred();
  let oldReads = 0;
  await page.route('**/api/servers/idle-host/ports', async route => {
    oldReads++;
    await held.promise;
    await route.fulfill({ json: snapshot('ports', 'old-host-process') });
  });
  await page.route('**/api/servers/busy-host/ports', route => route.fulfill({ json: snapshot('ports', 'new-host-process') }));
  await openPanel(page, panels[2]);
  await expect.poll(() => oldReads).toBe(1);
  await page.evaluate(() => {
    window.history.pushState({}, '', '/servers/busy-host');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.locator('.detail-identity h3')).toHaveText('busy-host');
  await page.getByRole('tab', { name: en['port.title'], exact: true }).click();
  await expect(page.getByRole('row').filter({ hasText: 'new-host-process' })).toBeVisible();
  held.resolve();
  await page.clock.fastForward(1000);
  await expect(page.getByRole('row').filter({ hasText: 'old-host-process' })).toHaveCount(0);
  expect(oldReads).toBe(1);
  expect(errors).toEqual([]);
});
