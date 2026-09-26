import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

async function fixture(page: Page) {
  const hosts = sampleFleet();
  hosts[0].credential_id = 'cred-1';
  hosts[0].credential_name = 'Ops key';
  const state = { created: [] as Record<string, unknown>[], updated: [] as Record<string, unknown>[], errors: [] as string[] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const headers = { Date: new Date(observedAt).toUTCString() };
    if (path === '/api/servers' && request.method() === 'POST') {
      state.created.push(request.postDataJSON());
      return route.fulfill({ headers, json: { ...hosts[0], id: 'new-host', name: 'win-01', credential_id: null } });
    }
    if (path === '/api/servers') return route.fulfill({ headers, json: hosts });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/credentials') return route.fulfill({ json: [
      { id: 'cred-1', user_id: 'u', name: 'Ops key', ssh_username: 'ops', has_password: false, has_key: true, credential_type: 'linux', created_at: '2030-01-01T00:00:00Z' },
    ] });
    const match = path.match(/^\/api\/servers\/([^/]+)(\/.*)?$/);
    if (match) {
      const server = hosts.find(item => item.id === match[1]);
      if (!server) return route.fulfill({ status: 404, json: { error: 'not found' } });
      const rest = match[2] || '';
      if (rest === '' && request.method() === 'PUT') { state.updated.push(request.postDataJSON()); return route.fulfill({ headers, json: server }); }
      if (rest === '') return route.fulfill({ headers, json: server });
      if (rest === '/metrics/latest') return route.fulfill({ headers, json: server.latest_metrics });
      if (rest === '/metrics') return route.fulfill({ headers, json: [] });
      if (rest === '/tags') return route.fulfill({ json: {} });
    }
    return route.fulfill({ json: [] });
  });
  return state;
}

test('adding a Windows host keeps the dialog buttons on screen and sends only Windows fields', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  await expect(page.locator('.server-card')).toHaveCount(4);
  await page.getByRole('button', { name: en['server.add'] }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  const ok = dialog.getByRole('button', { name: 'OK', exact: true });
  await expect(ok).toBeVisible();
  const box = (await ok.boundingBox())!;
  expect(box.y + box.height).toBeLessThanOrEqual(page.viewportSize()!.height);

  await dialog.getByLabel(en['server.serverName']).fill('  win-01  ');
  await dialog.getByLabel(en['server.host']).fill('198.51.100.7');
  await expect(dialog.getByLabel(en['server.sshPort'])).toHaveValue('22');
  await dialog.getByLabel(en['server.type']).click();
  await page.locator('.ant-select-item-option').filter({ hasText: 'Windows' }).click();
  await expect(dialog.getByLabel(en['server.sshPort'])).toHaveCount(0);
  await expect(dialog.getByLabel(en['server.sshKey'])).toHaveCount(0);
  await expect(dialog.getByLabel(en['server.sshHostKey'])).toHaveCount(0);
  await dialog.getByLabel(en['server.username']).fill('Administrator');
  await dialog.getByLabel(en['server.password']).fill('secret');
  await ok.click();
  await expect.poll(() => state.created.length).toBe(1);
  expect(state.created[0]).toMatchObject({ name: 'win-01', host: '198.51.100.7', server_type: 'windows', ssh_username: 'Administrator', ssh_password: 'secret', credential_id: null });
  expect(state.created[0].port).toBeUndefined();
  expect(state.created[0].ssh_key).toBeUndefined();
  expect(state.created[0].ssh_host_key).toBeUndefined();
  await expect(dialog).toBeHidden();
  expect(state.errors).toEqual([]);
});

test('editing a host pre-fills the form and hides direct credentials behind a shared one', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/servers/idle-host');
  await expect(page.getByRole('heading', { name: 'idle-host' })).toBeVisible();
  await page.locator('.detail-actions').getByRole('button', { name: en['common.edit'] }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByLabel(en['server.serverName'])).toHaveValue('idle-host');
  await expect(dialog.getByLabel(en['server.sshPort'])).toHaveValue('22');
  await expect(dialog.getByText('Ops key')).toBeVisible();
  await expect(dialog.getByLabel(en['server.sshUsername'])).toHaveCount(0);
  await expect(dialog.getByLabel(en['server.sshPassword'])).toHaveCount(0);
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => state.updated.length).toBe(1);
  expect(state.updated[0]).toMatchObject({ name: 'idle-host', credential_id: 'cred-1', server_type: 'linux', port: 22 });
  expect(state.updated[0].ssh_password).toBeUndefined();
  expect(state.updated[0].ssh_key).toBeUndefined();
  expect(state.errors).toEqual([]);
});
