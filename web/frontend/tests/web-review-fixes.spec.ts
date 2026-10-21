import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { host, observedAt } from './fixtures/fleet';
import { apiBaseURL, websocketURL } from '../src/utils/apiURL';
import { apiError } from '../src/utils/apiError';
import { networkChartRows } from '../src/utils/networkChart';
import type { NetworkHistory } from '../src/api/network';

async function setup(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en'); localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture'); localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Review' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
}

async function choose(page: Page, label: string, value: string) {
  const input = page.getByLabel(label, { exact: true });
  await input.click();
  await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: value }).click();
}

test('a failed tag save retries the created SSH host without another create', async ({ page }) => {
  await setup(page);
  const server = host('created-host');
  let creates = 0;
  const tagTargets: string[] = [];
  await page.route('**/api/**', route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (path === '/api/servers') {
      if (request.method() === 'POST') { creates++; return route.fulfill({ json: server }); }
      return route.fulfill({ json: [server] });
    }
    if (path === '/api/tags') return route.fulfill({ json: [{ id: 'production', name: 'Production', color: '#123456' }] });
    if (path.endsWith('/tags')) {
      tagTargets.push(path);
      return tagTargets.length === 1 ? route.fulfill({ status: 500, json: { error: 'tag write failed' } }) : route.fulfill({ json: {} });
    }
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/dashboard');
  await page.getByRole('button', { name: 'Add Server' }).first().click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Server Name', { exact: true }).fill('created-host');
  await dialog.getByLabel('Host / IP', { exact: true }).fill('192.0.2.2');
  await dialog.getByLabel('SSH Username', { exact: true }).fill('root');
  await dialog.getByLabel('SSH Password', { exact: true }).fill('secret');
  const tags = dialog.getByRole('combobox').last();
  await tags.click(); await tags.press('ArrowDown'); await tags.press('Enter'); await tags.press('Escape');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => tagTargets.length).toBe(1);
  await expect(dialog).toBeHidden();
  const notice = page.locator('.ant-notification-notice').filter({ hasText: 'tags could not be saved' });
  await notice.getByRole('button', { name: 'Refresh' }).click();
  await expect.poll(() => tagTargets.length).toBe(2);
  expect(tagTargets).toEqual(['/api/servers/created-host/tags', '/api/servers/created-host/tags']);
  expect(creates).toBe(1);
});

for (const view of ['dashboard', 'detail'] as const) {
  test(`${view}: configuration changes preserve notes saved elsewhere and explicitly switch SSH authentication`, async ({ page }) => {
    await setup(page);
    const server = host('editable-host', { ssh_auth_method: 'password', notes: 'original note' });
    const writes: Record<string, unknown>[] = [];
    await page.route('**/api/**', route => {
      const request = route.request(), path = new URL(request.url()).pathname;
      const headers = { Date: new Date(observedAt).toUTCString() };
      if (path === '/api/servers') return route.fulfill({ headers, json: [server] });
      if (path === '/api/servers/editable-host') {
        if (request.method() === 'PUT') { const input = request.postDataJSON(); writes.push(input); Object.assign(server, input); }
        return route.fulfill({ headers, json: server });
      }
      if (path.endsWith('/metrics/latest')) return route.fulfill({ headers, json: server.latest_metrics });
      if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
      if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
      return route.fulfill({ json: [] });
    });
    await page.goto(view === 'dashboard' ? '/dashboard' : '/servers/editable-host');
    if (view === 'dashboard') {
      await page.getByRole('radio', { name: 'List' }).locator('..').click();
      await page.getByRole('row').filter({ hasText: 'editable-host' }).getByRole('button', { name: 'Edit', exact: true }).click();
    } else await page.locator('.detail-actions').getByRole('button', { name: 'Edit' }).click();
    const dialog = page.getByRole('dialog');
    server.notes = 'new note saved in another tab';
    await choose(page, 'Authentication method', 'Private key');
    await dialog.getByLabel('SSH Private Key', { exact: true }).fill('new-private-key');
    await dialog.getByRole('button', { name: 'OK', exact: true }).click();
    await expect.poll(() => writes.length).toBe(1);
    expect(writes[0]).toMatchObject({ ssh_auth_method: 'key', ssh_key: 'new-private-key' });
    expect(writes[0]).not.toHaveProperty('ssh_password');
    expect(writes[0]).not.toHaveProperty('notes');
    expect(server.notes).toBe('new note saved in another tab');
  });
}

test('key-only credentials require a password when switched to Windows', async ({ page }) => {
  await setup(page);
  const credential = { id: 'key-only', name: 'Linux key', ssh_username: 'root', has_password: false, has_key: true, ssh_auth_method: 'key', credential_type: 'linux' };
  const writes: Record<string, unknown>[] = [];
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/credentials') return route.fulfill({ json: [credential] });
    if (path === '/api/credentials/key-only') { writes.push(route.request().postDataJSON()); return route.fulfill({ json: {} }); }
    return route.fulfill({ json: [] });
  });
  await page.goto('/credentials');
  await page.getByRole('row').filter({ hasText: 'Linux key' }).getByRole('button', { name: 'Edit', exact: true }).click();
  await choose(page, 'Type', 'Windows');
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect(dialog.locator('.ant-form-item-explain-error')).toBeVisible();
  expect(writes).toHaveLength(0);
  await dialog.getByLabel('Password', { exact: true }).fill('windows-password');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]).toMatchObject({ credential_type: 'windows', ssh_auth_method: 'password', ssh_password: 'windows-password' });
  expect(writes[0]).not.toHaveProperty('ssh_key');
});

test('collapsed Docker hosts load only inventory and acquire statistics when expanded', async ({ page }) => {
  await setup(page);
  const hosts = Array.from({ length: 6 }, (_, index) => host(`docker-${index}`, { has_docker: true }));
  const stats: string[] = [], inventories: string[] = [];
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/servers') return route.fulfill({ json: hosts });
    if (path.endsWith('/docker/containers')) inventories.push(path);
    if (path.endsWith('/docker/stats')) stats.push(path);
    return route.fulfill({ json: [] });
  });
  await page.goto('/docker');
  await expect.poll(() => inventories.length).toBe(6);
  expect(stats).toEqual([]);
  await page.locator('.ant-collapse-header').filter({ hasText: 'docker-0' }).click();
  await expect.poll(() => stats.length).toBe(1);
  expect(stats[0]).toBe('/api/servers/docker-0/docker/stats');
});

test('minute comparisons retain the newest value, including an unknown result, in either API order', () => {
  const at = '2026-10-20T00:00:';
  for (const newestValue of [900, null]) for (const reversed of [false, true]) {
    const samples = [{ recorded_at: at + '50Z', avg_ms: newestValue }, { recorded_at: at + '10Z', avg_ms: 10 }];
    if (reversed) samples.reverse();
    const rows = networkChartRows({ probe: { samples } as NetworkHistory });
    expect(rows).toEqual([{ time: Date.parse(at + '00Z'), probe: newestValue }]);
  }
});

test('HTTP and both terminal endpoints retain configured origins and path prefixes', () => {
  for (const [configured, expected] of [
    [undefined, 'https://panel.example/api/'],
    ['/monitor/api/', 'https://panel.example/monitor/api/'],
    ['https://api.example/prefix/api', 'https://api.example/prefix/api/'],
    ['http://127.0.0.1:8099/custom', 'http://127.0.0.1:8099/custom/'],
  ]) {
    const base = apiBaseURL(configured, 'https://panel.example');
    expect(base).toBe(expected);
    for (const path of ['ssh/host', 'ws/servers/host/docker/containers/container/exec']) {
      expect(websocketURL(path, base)).toBe(expected!.replace(/^http/, 'ws') + path);
    }
  }
  expect(apiError({ response: { data: { error: { unexpected: 'object' } } } }, 'fallback')).toBe('fallback');
});
