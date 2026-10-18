import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { host, observedAt } from './fixtures/fleet';
import { incidentLogsURL } from '../src/utils/logs';

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
  return errors;
}
const sample = { recorded_at: new Date(observedAt).toISOString(), status: 'loss', sent: 5, received: 4, loss_percent: 20, avg_ms: 12, min_ms: 10, max_ms: 15, mdev_ms: 2, threshold_exceeded: true };
const check = { id: 'check-one', name: 'Local to target', source_server_id: null, target_server_id: 'target', packets: 5, enabled: true, loss_threshold: 10, latency_threshold_ms: null, created_at: new Date(observedAt).toISOString(), latest: sample };

test('network compares measured and unknown probes without turning failed execution into packet loss', async ({ page }) => {
  const errors = await setup(page);
  await page.route('**/api/network/checks', route => route.fulfill({ json: [check, { ...check, id: 'check-two', name: 'Remote probe', source_server_id: 'source', latest: { ...sample, status: 'error', sent: null, received: null, loss_percent: null, avg_ms: null, mdev_ms: null, error_code: 'source_unreachable' } }] }));
  await page.route('**/api/network/checks/*/history?**', route => route.fulfill({ json: { samples: [sample], truncated: false, summary: { sent: 6, received: 5, loss_percent: 100 / 6, avg_ms: 12, known_samples: 2, unknown_samples: 1 } } }));
  await page.goto('/network');
  const local = page.getByRole('row').filter({ hasText: 'Local to target' }).first();
  await expect(local).toContainText('5 / 4');
  await expect(local).toContainText('20.00%');
  const remote = page.getByRole('row').filter({ hasText: 'Remote probe' }).first();
  await expect(remote).toContainText('Probe source unavailable');
  await expect(remote).not.toContainText('100.00%');
  await expect(page.getByText('16.67%').first()).toBeVisible();
  expect(errors).toEqual([]);
});

test('network creation only offers assigned servers and sends bounded packet counts', async ({ page }) => {
  const errors = await setup(page);
  let written: Record<string, unknown> | undefined;
  await page.route('**/api/network/checks', route => {
    if (route.request().method() === 'POST') { written = route.request().postDataJSON(); return route.fulfill({ json: { ...check, ...written } }); }
    return route.fulfill({ json: [] });
  });
  await page.goto('/network');
  await page.getByRole('button', { name: 'Add check', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name', { exact: true }).fill('Probe target');
  await dialog.getByLabel('Target server', { exact: true }).click();
  await page.locator('.ant-select-item-option').filter({ hasText: 'target' }).click();
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => !!written).toBe(true);
  expect(written).toMatchObject({ source_server_id: null, target_server_id: 'target', packets: 5, enabled: true, loss_threshold: 10 });
  expect(written).not.toHaveProperty('host');
  expect(errors).toEqual([]);
});

test('journal follow sends the cursor, preserves literal filters, shows partial access, and cancels on navigation', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  const requests: URL[] = []; let held: Route | undefined;
  await page.route('**/api/servers/target/logs?**', route => {
    const url = new URL(route.request().url()); requests.push(url);
    if (requests.length >= 3) { held = route; return; }
    return route.fulfill({ json: { entries: [{ id: `entry-${requests.length}`, time: new Date(observedAt).toISOString(), message: requests.length === 1 ? 'first line' : 'literal .* line', stream: 'journal', unit: 'app.service', priority: '3' }], next_cursor: 'cursor-' + requests.length, partial: requests.length === 2, reason: requests.length === 2 ? 'access_denied' : undefined, truncated: false, scanned: 200 } });
  });
  await page.goto('/logs?server=target');
  await expect(page.getByText('first line', { exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Literal, case-sensitive text' }).fill('.*');
  await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
  await expect(page.getByText('literal .* line', { exact: true })).toBeVisible();
  expect(requests[1].searchParams.get('keyword')).toBe('.*');
  await expect(page.getByText(/Journal or Docker access is restricted/)).toBeVisible();
  await page.getByRole('button', { name: 'Follow new entries', exact: true }).click();
  await page.clock.fastForward(3100);
  await expect.poll(() => !!held).toBe(true);
  expect(requests[2].searchParams.get('cursor')).toBe('cursor-2');
  const failed = page.waitForEvent('requestfailed', request => request.url().includes('/target/logs'));
  await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
  await failed;
  expect(errors).toEqual([]);
});

test('Docker log inspection displays both streams and reports a truncated tail', async ({ page }) => {
  const errors = await setup(page);
  await page.route('**/api/servers/target/docker/containers/abc123/logs/page?**', route => route.fulfill({ json: {
    entries: [{ id: 'out', time: new Date(observedAt).toISOString(), message: 'stdout log', stream: 'stdout' }, { id: 'err', time: new Date(observedAt).toISOString(), message: 'stderr log', stream: 'stderr' }], partial: true, truncated: true, reason: 'tail_limit', next_cursor: 'next', scanned: 2,
  } }));
  await page.goto('/logs?server=target&container=abc123');
  await expect(page.getByText('stdout log', { exact: true })).toBeVisible();
  await expect(page.getByText('stderr log', { exact: true })).toBeVisible();
  await expect(page.getByText(/Older lines are omitted/)).toBeVisible();
  expect(errors).toEqual([]);
});

test('incident logs link has an exact twenty-minute window and never guesses a service', () => {
  const url = new URL(incidentLogsURL('server-a', new Date(observedAt).toISOString()), 'http://fixture');
  expect(url.pathname).toBe('/logs');
  expect(url.searchParams.get('server')).toBe('server-a');
  expect(Date.parse(url.searchParams.get('since')!)).toBe(observedAt - 600000);
  expect(Date.parse(url.searchParams.get('until')!)).toBe(observedAt + 600000);
  expect(url.searchParams.has('unit')).toBe(false);
});
