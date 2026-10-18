import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { host, observedAt } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
async function setup(page: Page) {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => { localStorage.setItem('lang', 'en'); localStorage.setItem('token', 'fixture'); localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' })); });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  return errors;
}
async function agentFixture(page: Page) {
  const errors = await setup(page);
  const state = { server: host('new-agent', { connection_mode: 'agent', is_public: false, monitoring_enabled: true }),
    created: [] as Record<string, unknown>[], updated: [] as Record<string, unknown>[], enrolled: false, online: false, generated: 0, revoked: 0,
    statusReads: 0, holdStatus: false, held: undefined as Route | undefined, enrollmentError: '' };
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    const method = route.request().method();
    const headers = { Date: new Date(observedAt).toUTCString() };
    if (path === '/api/servers') {
      if (method === 'POST') { const body = route.request().postDataJSON(); state.created.push(body); state.server = { ...state.server, ...body }; return route.fulfill({ json: state.server }); }
      return route.fulfill({ headers, json: [state.server] });
    }
    if (path === '/api/servers/new-agent') {
      if (method === 'PUT') { const body = route.request().postDataJSON(); state.updated.push(body); state.server = { ...state.server, ...body }; }
      return route.fulfill({ headers, json: state.server });
    }
    if (path === '/api/servers/new-agent/agent') {
      if (method === 'DELETE') { state.revoked++; state.enrolled = false; state.online = false; return route.fulfill({ status: 204 }); }
      state.statusReads++;
      if (state.holdStatus) { state.held = route; return; }
      return route.fulfill({ json: { enrolled: state.enrolled, online: state.online, version: state.enrolled ? '1.0' : '', username: state.enrolled ? 'agent-user' : '' } });
    }
    if (path === '/api/servers/new-agent/agent/enroll') {
      if (state.enrollmentError) return route.fulfill({ status: 400, json: { error: state.enrollmentError } });
      state.generated++;
      return route.fulfill({ headers, json: { command: `sudo install-agent --ticket linux-${state.generated}`, windows_command: `Install-Agent -Ticket windows-${state.generated}`, expires_at: new Date(observedAt + 900000).toISOString() } });
    }
    if (path.endsWith('/metrics/latest')) return route.fulfill({ headers, json: state.server.latest_metrics });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
    return route.fulfill({ json: [] });
  });
  return { state, errors };
}
async function choose(page: Page, label: string, option: string) {
  const select = page.getByLabel(label, { exact: true });
  await select.click();
  const controls = await select.getAttribute('aria-controls');
  const popup = page.locator('.ant-select-dropdown').filter({ has: page.locator(`[id="${controls}"]`) });
  // Both choices exercised here are the second option (Agent and Windows).
  // Use the real combobox keyboard path instead of a portaled popup's coordinates.
  await expect(popup.locator('.ant-select-item-option-active')).toBeVisible();
  await select.press('ArrowDown');
  await expect(popup.locator('.ant-select-item-option-active')).toContainText(option);
  await select.press('Enter');
  await expect(select).toHaveAttribute('aria-expanded', 'false');
}

for (const platform of ['linux', 'windows'] as const) {
  test(`${platform} Agent creation needs no SSH fields and opens the installation panel`, async ({ page }) => {
    const { state, errors } = await agentFixture(page);
    await page.goto('/dashboard');
    await page.getByRole('button', { name: en['server.add'] }).first().click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel(en['server.sshPassword'], { exact: true }).fill('do-not-send');
    await dialog.getByLabel(en['server.sshKey'], { exact: true }).fill('do-not-send-key');
    await choose(page, 'Connection method', 'Install Agent');
    if (platform === 'windows') await choose(page, en['server.type'], 'Windows');
    await dialog.getByLabel(en['server.serverName'], { exact: true }).fill(`${platform}-agent`);
    await expect(dialog.getByRole('switch', { name: 'Show on public status page' })).toBeChecked();
    await expect(dialog.getByLabel(en['server.sshPort'], { exact: true })).toHaveCount(0);
    await expect(dialog.getByLabel(en['server.password'], { exact: true })).toHaveCount(0);
    await expect(dialog.getByLabel(en['server.sshPassword'], { exact: true })).toHaveCount(0);
    await dialog.getByRole('button', { name: 'OK', exact: true }).click();
    await expect.poll(() => state.created.length).toBe(1);
    expect(state.created[0]).toMatchObject({ connection_mode: 'agent', server_type: platform, is_public: true, monitoring_enabled: true });
    for (const key of ['ssh_password', 'ssh_key', 'ssh_host_key', 'ssh_username', 'port']) expect(state.created[0]).not.toHaveProperty(key);
    await expect(page).toHaveURL(/servers\/new-agent$/);
    await page.getByRole('button', { name: 'Generate installation command', exact: true }).click();
    await expect(page.locator('.agent-panel pre')).toContainText(platform === 'windows' ? 'Install-Agent -Ticket windows-1' : 'sudo install-agent --ticket linux-1');
    expect(errors).toEqual([]);
  });
}

test('changing server platform clears hidden SSH secrets and paused private servers can be enabled explicitly', async ({ page }) => {
  const { state, errors } = await agentFixture(page);
  state.server = { ...state.server, connection_mode: 'ssh', is_public: false, monitoring_enabled: false, auto_renew_enabled: false };
  await page.goto('/servers/new-agent');
  await page.locator('.detail-actions').getByRole('button', { name: en['common.edit'] }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel(en['server.sshKey'], { exact: true }).fill('stale-linux-key');
  await dialog.getByLabel(en['server.sshPassword'], { exact: true }).fill('stale-linux-password');
  await choose(page, en['server.type'], 'Windows');
  await expect(dialog.getByLabel(en['server.password'], { exact: true })).toHaveValue('');
  await dialog.getByLabel(en['server.username'], { exact: true }).fill('Administrator');
  await dialog.getByLabel(en['server.password'], { exact: true }).fill('new-windows-password');
  await dialog.getByRole('switch', { name: 'Enable monitoring' }).click();
  await dialog.getByRole('switch', { name: 'Show on public status page' }).click();
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => state.updated.length).toBe(1);
  expect(state.updated[0]).toMatchObject({ monitoring_enabled: true, is_public: true, auto_renew_enabled: false, ssh_password: 'new-windows-password', ssh_username: 'Administrator' });
  expect(state.updated[0]).not.toHaveProperty('ssh_key');
  expect(state.updated[0]).not.toHaveProperty('ssh_host_key');
  expect(errors).toEqual([]);
});

test('Agent reenrollment, expiry, server errors and revocation are visible and pending polls cancel on leave', async ({ page }) => {
  const { state, errors } = await agentFixture(page);
  await page.clock.install({ time: new Date(observedAt) });
  state.enrolled = true; state.online = true;
  await page.goto('/servers/new-agent');
  await expect(page.locator('.agent-panel')).toContainText('Connected');
  await page.getByRole('button', { name: 'Generate new installation command', exact: true }).click();
  await expect(page.locator('.agent-panel pre')).toContainText('linux-1');
  await expect(page.getByRole('button', { name: 'Generate new installation command', exact: true })).toBeEnabled();
  await page.clock.fastForward(901000);
  await expect(page.getByText('This installation command has expired. Generate a new command.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Copy command', exact: true })).toBeDisabled();
  state.enrollmentError = 'Enrollment request expired; generate a new ticket';
  await page.getByRole('button', { name: 'Generate new installation command', exact: true }).click();
  await expect(page.locator('.agent-panel .ant-alert').filter({ hasText: state.enrollmentError })).toBeVisible();
  await page.getByRole('button', { name: 'Revoke Agent access', exact: true }).click();
  expect(state.revoked).toBe(0);
  await page.getByRole('dialog').getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => state.revoked).toBe(1);
  await expect(page.locator('.agent-panel')).toContainText('Awaiting installation');
  await expect(page.locator('.agent-panel pre')).toHaveCount(0);
  state.holdStatus = true;
  await page.clock.fastForward(10100);
  await expect.poll(() => !!state.held).toBe(true);
  const failed = page.waitForEvent('requestfailed', request => request.url().endsWith('/new-agent/agent'));
  await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
  await failed;
  const reads = state.statusReads;
  await page.clock.fastForward(30100);
  expect(state.statusReads).toBe(reads);
  expect(errors).toEqual([]);
});

const preview = { backup_id: 'backup-one', servers: 2, credentials: 1, tags: 1, rules: 1, notification_channels: 1, network_checks: 1, agent_servers: 1, includes_credentials: true, already_imported: false, server_names: ['Imported host', 'Imported agent'] };
const opaqueBackup = Buffer.from([83, 77, 66, 75, 0, 0, 0, 1, 91, 182, 31, 201]);
async function backupFixture(page: Page) {
  const errors = await setup(page);
  const state = { exports: [] as Record<string, unknown>[], previews: [] as Record<string, unknown>[], imports: [] as Record<string, unknown>[], alreadyImported: false, failExport: false, failImport: false };
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path.startsWith('/api/backups/')) {
      const body = route.request().postDataJSON();
      if (path.endsWith('/export')) { state.exports.push(body); return state.failExport ? route.fulfill({ status: 403, json: { error: 'Account password rejected' } }) : route.fulfill({ contentType: 'application/octet-stream', body: opaqueBackup }); }
      if (path.endsWith('/preview')) { state.previews.push(body); return route.fulfill({ json: { preview: { ...preview, already_imported: state.alreadyImported }, digest: 'fixture-digest', activation: 'paused', visibility: 'private' } }); }
      if (path.endsWith('/import')) { state.imports.push(body); return state.failImport ? route.fulfill({ status: 409, json: { error: 'This backup was already restored into this account' } }) : route.fulfill({ status: 201, json: { preview, activation: 'paused', visibility: 'private' } }); }
    }
    return route.fulfill({ json: { active: 0 } });
  });
  await page.goto('/backups');
  await page.getByLabel('Current account password', { exact: true }).fill('account-password');
  await page.getByLabel('Backup password (at least 12 characters)', { exact: true }).fill('backup-password-123');
  return { state, errors };
}
async function pickBackup(page: Page) { await page.getByLabel('Choose backup file', { exact: true }).setInputFiles({ name: 'fixture.smbak', mimeType: 'application/octet-stream', buffer: opaqueBackup }); }

test('backup export downloads opaque encrypted bytes and reads JSON errors from Blob responses', async ({ page }) => {
  const { state, errors } = await backupFixture(page);
  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Export encrypted backup', exact: true }).click();
  const file = await download;
  expect(file.suggestedFilename()).toMatch(/\.smbak$/);
  expect(await readFile((await file.path())!)).toEqual(opaqueBackup);
  expect(state.exports[0]).toMatchObject({ password: 'backup-password-123', current_password: 'account-password', include_credentials: true });
  await expect(page.getByRole('button', { name: 'Export encrypted backup', exact: true })).toBeEnabled();
  state.failExport = true;
  await page.getByRole('button', { name: 'Export encrypted backup', exact: true }).click();
  await expect(page.getByText('Account password rejected', { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test('backup and Agent actions keep their names through slow loading-icon transitions and become usable again', async ({ page }) => {
  const { errors } = await backupFixture(page);
  const session = await page.context().newCDPSession(page);
  await session.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  await page.addStyleTag({ content: '.ant-btn-loading-icon-motion-appear-active, .ant-btn-loading-icon-motion-enter-active, .ant-btn-loading-icon-motion-leave-active { transition-duration: 60s !important; }' });
  let first: Route | undefined;
  let calls = 0;
  await page.route('**/api/backups/export', route => {
    calls++;
    if (calls === 1) { first = route; return; }
    return route.fulfill({ status: 403, json: { error: 'Second export handled' } });
  });
  const exportButton = page.getByRole('button', { name: 'Export encrypted backup', exact: true });
  await exportButton.click();
  await expect.poll(() => !!first).toBe(true);
  await expect(exportButton).toHaveAttribute('aria-busy', 'true');
  await expect(exportButton).toBeDisabled();
  await expect(exportButton).toHaveAccessibleName('Export encrypted backup');
  const downloaded = page.waitForEvent('download');
  await first!.fulfill({ contentType: 'application/octet-stream', body: opaqueBackup });
  await downloaded;
  await expect(exportButton).toHaveAttribute('aria-busy', 'false');
  await expect(exportButton).toBeEnabled();
  await exportButton.click();
  await expect(page.getByText('Second export handled', { exact: true })).toBeVisible();
  expect(calls).toBe(2);

  const { state } = await agentFixture(page);
  state.enrolled = true;
  await page.goto('/servers/new-agent');
  await page.addStyleTag({ content: '.ant-btn-loading-icon-motion-appear-active, .ant-btn-loading-icon-motion-enter-active, .ant-btn-loading-icon-motion-leave-active { transition-duration: 60s !important; }' });
  let enrollment: Route | undefined;
  await page.route('**/api/servers/new-agent/agent/enroll', route => { enrollment = route; });
  const generateButton = page.getByRole('button', { name: 'Generate new installation command', exact: true });
  await generateButton.click();
  await expect.poll(() => !!enrollment).toBe(true);
  await expect(generateButton).toHaveAccessibleName('Generate new installation command');
  await expect(generateButton).toHaveAttribute('aria-busy', 'true');
  await expect(generateButton).toBeDisabled();
  await enrollment!.fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: { command: 'install fixture', expires_at: new Date(observedAt + 900000).toISOString() } });
  await expect(generateButton).toHaveAttribute('aria-busy', 'false');
  await expect(generateButton).toBeEnabled();
  enrollment = undefined;
  await generateButton.click();
  await expect.poll(() => !!enrollment).toBe(true);
  await enrollment!.fulfill({ status: 400, json: { error: 'Second enrollment handled' } });
  await expect(page.locator('.agent-panel .ant-alert').filter({ hasText: 'Second enrollment handled' })).toBeVisible();
  expect(errors).toEqual([]);
});

test('restoring requires a matching preview and explicit confirmation, and password changes discard preview', async ({ page }) => {
  const { state, errors } = await backupFixture(page);
  await pickBackup(page);
  await page.getByRole('button', { name: 'Preview restore', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toBeEnabled();
  await page.getByLabel('Current account password', { exact: true }).fill('different-account-password');
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Preview restore', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toBeEnabled();
  await page.getByLabel('Backup password (at least 12 characters)', { exact: true }).fill('different-backup-password');
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Preview restore', exact: true }).click();
  await page.getByRole('button', { name: 'Restore this backup', exact: true }).click();
  expect(state.imports).toHaveLength(0);
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();
  expect(state.imports).toHaveLength(0);
  await page.getByRole('button', { name: 'Restore this backup', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => state.imports.length).toBe(1);
  expect(state.imports[0]).toMatchObject({ data: opaqueBackup.toString('base64'), digest: 'fixture-digest', password: 'different-backup-password', current_password: 'different-account-password' });
  await expect(page.getByLabel('Current account password', { exact: true })).toHaveValue('');
  await expect(page.getByLabel('Backup password (at least 12 characters)', { exact: true })).toHaveValue('');
  expect(errors).toEqual([]);
});

test('already-imported backups cannot be confirmed, oversized files never upload, and import conflicts require another preview', async ({ page }) => {
  const { state, errors } = await backupFixture(page);
  state.alreadyImported = true;
  await pickBackup(page);
  await page.getByRole('button', { name: 'Preview restore', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toBeDisabled();
  await page.getByLabel('Choose backup file', { exact: true }).setInputFiles({ name: 'large.smbak', mimeType: 'application/octet-stream', buffer: Buffer.alloc(17 * 1024 * 1024 + 1) });
  await expect(page.getByText('The backup file must be smaller than 17 MiB.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Preview restore', exact: true })).toBeDisabled();
  expect(state.previews).toHaveLength(1);
  expect(state.imports).toHaveLength(0);
  state.alreadyImported = false; state.failImport = true;
  await pickBackup(page);
  await page.getByRole('button', { name: 'Preview restore', exact: true }).click();
  await page.getByRole('button', { name: 'Restore this backup', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'OK', exact: true }).click();
  await expect(page.getByText('This backup was already restored into this account', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Restore this backup', exact: true })).toHaveCount(0);
  expect(state.imports).toHaveLength(1);
  expect(errors).toEqual([]);
});
