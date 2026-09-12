import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

const serverID = '11111111-1111-4111-8111-111111111111';
const containerID = 'a'.repeat(64);
const metrics = { recorded_at: new Date().toISOString(), cpu_percent: 12, memory_used: 1024, memory_total: 4096, disk_used: 1000, network_rx_bytes: 0, network_tx_bytes: 0, disk_rx_bytes: 0, disk_tx_bytes: 0, load_1: 0.1, load_5: 0.1, load_15: 0.1, uptime_seconds: 1000, latency_ms: 10 };
const host = { id: serverID, name: 'Smoke Host', host: '192.0.2.1', server_type: 'linux', has_docker: true, docker_version: 'test', cpu_cores: 2, memory_total: 4096, disk_total: 8192, tags: [], notes: '', latest_metrics: metrics };
const container = { id: containerID, name: 'smoke-container', image: 'test:latest', state: 'running', status: 'Up', ports: '', created: 'now' };
function entry(name: string) { return { name, path: '/tmp/' + name, size: 28, modified_at: '2026-09-12T00:00:00Z', mode: '-rw-r--r--', is_file: true, is_dir: false, is_symlink: false }; }
async function fixture(page: Page) {
  let content = 'enabled: true\r\nport: 8080\r\n';
  let uploads = 0;
  const errors: string[] = [];
  const requests: string[] = [];
  const changes: { action: string; kind?: string; name?: string; version?: string; path: string }[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => { localStorage.setItem('lang', 'en'); localStorage.setItem('token', 'smoke-test-not-a-real-token'); localStorage.setItem('user', JSON.stringify({ id: 'smoke-user', username: 'smoke' })); });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', async route => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname.replace(/^.*\/api/, '');
    requests.push(pathname);
    const send = (json: unknown, status = 200) => route.fulfill({ status, json });
    if (pathname === '/servers') return send([host]);
    if (pathname === '/servers/uptime') return send({ servers: [{ server_id: serverID, windows: [] }], basis: 'observed', generated_at: new Date().toISOString() });
    if (pathname === '/servers/' + serverID) return send(host);
    if (pathname.endsWith('/metrics/latest')) return send(metrics);
    if (pathname.endsWith('/metrics')) return send([metrics]);
    if (pathname.endsWith('/uptime')) return send({ server_id: serverID, days: [], outages: [], basis: 'observed' });
    if (pathname.endsWith('/docker/containers')) return send([container]);
    if (pathname.endsWith('/docker/stats')) { await new Promise(resolve => setTimeout(resolve, 1200)); return send([{ ...container, cpu_percent: 15, memory_percent: 25, stats_available: true }]); }
    if (pathname.endsWith('/files')) return send({ path: '/tmp', parent: '/', entries: [entry(url.searchParams.get('cursor') ? 'page-two.txt' : 'config.yaml')], truncated: false, next_cursor: url.searchParams.get('cursor') ? undefined : '100' });
    if (pathname.endsWith('/files/text')) {
      if (request.method() === 'PUT') { content = request.postDataJSON().content; return send({ revision: createHash('sha256').update(content).digest('hex'), backup_path: '/tmp/.server-monitor-backups/config.yaml.test' }); }
      return send({ content, revision: createHash('sha256').update(content).digest('hex') });
    }
    if (pathname.endsWith('/files/metadata')) return send({ exists: true, version: 'metadata-version', entry: entry('config.yaml') });
    if (pathname.endsWith('/files/upload')) { uploads++; await new Promise(resolve => setTimeout(resolve, 700)); return send({ size: 6 }); }
    if (pathname.endsWith('/files/change')) { changes.push({ ...request.postDataJSON(), path: url.searchParams.get('path') }); return send({ path: url.searchParams.get('path') }); }
    if (pathname.endsWith('/files/audit')) return send([]);
    if (pathname.endsWith('/auth/me')) return send({ id: 'smoke-user', username: 'smoke' });
    return send([]);
  });
  return { errors, requests, changes, content: () => content, uploads: () => uploads };
}
async function openEditor(page: Page) { await page.getByRole('button', { name: 'config.yaml', exact: true }).click(); await expect(page.locator('.cm-content')).toBeVisible(); }

for (const route of ['/login', '/dashboard', '/servers/' + serverID, '/docker', '/files?server=' + serverID]) {
  test('production bundle renders ' + route, async ({ page }) => {
    const state = await fixture(page);
    await page.goto(route);
    if (route === '/login') await expect(page.getByRole('button', { name: en['login.submit'], exact: true })).toBeVisible();
    else if (route.startsWith('/files')) { await openEditor(page); await expect(page.locator('.cm-line span[class]').first()).toBeVisible(); }
    else await expect(page.getByText('Smoke Host', { exact: true }).first()).toBeVisible();
    expect(state.errors).toEqual([]);
  });
}
test('browser back protects edits and save preserves CRLF', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  await page.getByRole('menuitem').filter({ hasText: en['nav.files'] }).click();
  await openEditor(page);
  await page.locator('.cm-content').fill('enabled: false\nport: 9090\n');
  await page.evaluate(() => history.back());
  const guard = page.getByRole('dialog').filter({ hasText: en['files.discardTitle'] });
  await expect(guard).toBeVisible();
  await guard.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('.cm-content')).toContainText('9090');
  await page.locator('.cm-content').press('Control+s');
  await expect.poll(state.content).toBe('enabled: false\r\nport: 9090\r\n');
  await page.locator('.ant-modal-footer').getByRole('button', { name: en['files.close'], exact: true }).click();
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(/\/dashboard$/);
  expect(state.errors).toEqual([]);
});
test('expired login preserves and exports unsaved contents', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/files?server=' + serverID);
  await openEditor(page);
  await page.locator('.cm-content').fill('private: draft\n');
  await page.route('**/files/text?**', route => route.fulfill({ status: 401, json: { error: 'expired' } }));
  await page.locator('.cm-content').press('Control+s');
  const expired = page.getByRole('dialog').filter({ hasText: en['files.sessionExpired'] });
  await expect(expired).toBeVisible();
  const downloaded = page.waitForEvent('download');
  await expired.getByRole('button', { name: en['files.exportDraft'] }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe('config.yaml');
  expect(await readFile((await download.path())!, 'utf8')).toBe('private: draft\r\n');
  await expired.getByRole('button', { name: en['files.discardAndLogin'] }).click();
  await expect(page).toHaveURL(/\/login$/);
  expect(state.errors).toEqual([]);
});
test('upload confirms before sending one body and shows remote writing phase', async ({ page }) => {
  test.skip(!!process.env.SMOKE_BASE_URL, 'Real transfer uses an isolated local HTTP receiver; production only uses mocked file mutations');
  const state = await fixture(page);
  await page.goto('/files?server=' + serverID);
  await expect(page.getByRole('button', { name: 'config.yaml', exact: true })).toBeVisible();
  let transmitted = 0;
  const receiver = createServer((request, response) => {
    response.setHeader('Access-Control-Allow-Origin', '*');
    response.setHeader('Access-Control-Allow-Headers', '*');
    if (request.method === 'OPTIONS') { response.end(); return; }
    request.resume();
    request.on('end', () => {
      transmitted++;
      setTimeout(() => { response.setHeader('Content-Type', 'application/json'); response.end('{"size":6}'); }, 1500);
    });
  });
  receiver.listen(0, '127.0.0.1');
  await once(receiver, 'listening');
  const address = receiver.address();
  if (!address || typeof address === 'string') throw new Error('Upload test listener unavailable');
  await page.route('**/files/upload?**', route => route.continue({ url: 'http://127.0.0.1:' + address.port + '/upload' }));
  try {
  await page.locator('input[type=file]').setInputFiles({ name: 'config.yaml', mimeType: 'text/plain', buffer: Buffer.from('upload') });
  const overwrite = page.getByRole('dialog').filter({ hasText: en['files.overwriteTitle'] });
  await expect(overwrite).toBeVisible();
  expect(transmitted).toBe(0);
  await overwrite.getByRole('button', { name: en['files.overwrite'], exact: true }).click();
  await expect(page.getByText(en['files.phase.writing'])).toBeVisible();
  await expect.poll(() => transmitted).toBe(1);
  await expect(page.locator('.file-transfer')).toHaveCount(0);
  await page.getByRole('button', { name: en['files.nextPage'] }).click();
  await expect(page.getByRole('button', { name: 'page-two.txt', exact: true })).toBeVisible();
  expect(state.errors).toEqual([]);
  } finally { receiver.closeAllConnections(); await new Promise<void>(resolve => receiver.close(() => resolve())); }
});
test('container names render before slow statistics', async ({ page }) => {
  const state = await fixture(page);
  let release: () => void = () => undefined;
  const waiting = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/docker/stats', async route => { await waiting; await route.fulfill({ json: [{ ...container, stats_available: true, cpu_percent: 30 }] }); });
  try {
    await page.goto('/docker');
    await page.locator('.ant-collapse-header').first().click();
    await expect(page.getByText('smoke-container', { exact: true })).toBeVisible();
    expect(state.requests.some(path => path.endsWith('/docker/containers'))).toBe(true);
  } finally { release(); }
  expect(state.errors).toEqual([]);
});
test('browser forward and refresh protect an unsaved editor', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/files?server=' + serverID);
  await expect(page.getByRole('button', { name: 'config.yaml', exact: true })).toBeVisible();
  await page.getByRole('menuitem').filter({ hasText: en['nav.servers'] }).click();
  await expect(page).toHaveURL(/dashboard$/);
  await expect(page.getByText('Smoke Host', { exact: true }).first()).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/files\?server=/);
  await openEditor(page);
  await page.locator('.cm-content').fill('keep: forward-test\n');
  await page.evaluate(() => history.forward());
  const guard = page.getByRole('dialog').filter({ hasText: en['files.discardTitle'] });
  await expect(guard).toBeVisible();
  await guard.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('.cm-content')).toContainText('forward-test');
  const warning = page.waitForEvent('dialog');
  const reload = page.reload({ timeout: 3000 }).catch(() => undefined);
  const dialog = await warning;
  expect(dialog.type()).toBe('beforeunload');
  await dialog.dismiss();
  await reload;
  await expect(page.locator('.cm-content')).toContainText('forward-test');
  await page.evaluate(() => history.forward());
  await guard.getByRole('button', { name: en['files.discard'], exact: true }).click();
  await expect(page).toHaveURL(/dashboard$/);
  expect(state.errors).toEqual([]);
});
test('logout waits for explicit discard before clearing credentials', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/files?server=' + serverID);
  await openEditor(page);
  await page.locator('.cm-content').fill('keep: logout-test\n');
  await page.getByRole('button', { name: en['nav.logout'] }).evaluate(button => (button as HTMLButtonElement).click());
  const guard = page.getByRole('dialog').filter({ hasText: en['files.discardTitle'] });
  await expect(guard).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeTruthy();
  await guard.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('.cm-content')).toContainText('logout-test');
  await page.getByRole('button', { name: en['nav.logout'] }).evaluate(button => (button as HTMLButtonElement).click());
  await guard.getByRole('button', { name: en['files.discard'], exact: true }).click();
  await expect(page).toHaveURL(/login/);
  await expect.poll(() => page.evaluate(() => localStorage.getItem('token'))).toBeNull();
  expect(state.errors).toEqual([]);
});
test('new files, directories, rename and confirmed delete carry safe requests', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/files?server=' + serverID);
  await expect(page.getByRole('button', { name: 'config.yaml', exact: true })).toBeVisible();
  for (const kind of ['file', 'directory']) {
    const title = en[kind === 'file' ? 'files.newFile' : 'files.newDirectory'];
    await page.getByRole('button', { name: new RegExp(title + '$') }).click();
    const dialog = page.getByRole('dialog').filter({ hasText: title });
    await dialog.getByRole('textbox', { name: en['common.name'] }).fill(kind + '-new');
    await dialog.getByRole('button', { name: 'OK', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(state.changes.at(-1)).toMatchObject({ action: 'create', kind, path: '/tmp/' + kind + '-new' });
  }
  await page.getByRole('button', { name: en['files.rename'], exact: true }).click();
  const rename = page.getByRole('dialog').filter({ hasText: en['files.rename'] });
  await rename.getByRole('textbox', { name: en['common.name'] }).fill('renamed.yaml');
  await rename.getByRole('button', { name: 'OK', exact: true }).click();
  await expect(rename).not.toBeVisible();
  expect(state.changes.at(-1)).toMatchObject({ action: 'rename', version: 'metadata-version', name: 'renamed.yaml' });
  const previous = state.changes.length;
  await page.getByRole('button', { name: new RegExp(en['common.delete'] + '$') }).click();
  const deletion = page.getByRole('dialog').filter({ hasText: en['files.deleteTitle'] });
  await expect(deletion).toBeVisible();
  expect(state.changes.length).toBe(previous);
  await deletion.getByRole('button', { name: en['common.delete'], exact: true }).click();
  await expect(deletion).not.toBeVisible();
  expect(state.changes.at(-1)).toMatchObject({ action: 'delete', version: 'metadata-version', path: '/tmp/config.yaml' });
  expect(state.errors).toEqual([]);
});
test('leaving during overwrite confirmation cancels the pending upload', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  await page.getByRole('menuitem').filter({ hasText: en['nav.files'] }).click();
  await expect(page.getByRole('button', { name: 'config.yaml', exact: true })).toBeVisible();
  await page.locator('input[type=file]').setInputFiles({ name: 'config.yaml', mimeType: 'text/plain', buffer: Buffer.from('upload') });
  const overwrite = page.getByRole('dialog').filter({ hasText: en['files.overwriteTitle'] });
  await expect(overwrite).toBeVisible();
  await page.evaluate(() => history.back());
  const guard = page.getByRole('dialog').filter({ hasText: en['files.discardTitle'] });
  await guard.getByRole('button', { name: en['files.discard'], exact: true }).click();
  await expect(page).toHaveURL(/dashboard$/);
  await expect(overwrite).not.toBeVisible();
  expect(state.uploads()).toBe(0);
  expect(state.errors).toEqual([]);
});
test('production database readiness', async ({ request }) => {
  test.skip(!process.env.SMOKE_BASE_URL, 'Checked against the real production API after deployment');
  const response = await request.get('/api/ready');
  expect(response.ok()).toBe(true);
  expect(await response.json()).toEqual({ status: 'ready' });
});
