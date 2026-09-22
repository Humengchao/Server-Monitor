import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const MB = 1024 ** 2;

function entry(name: string, over: Partial<{ size: number; is_dir: boolean; is_symlink: boolean; mode: string }> = {}) {
  const dir = !!over.is_dir;
  return { name, path: '/home/deploy/' + name, size: over.size ?? 2048, modified_at: '2030-06-01T08:00:00Z', mode: over.mode || (dir ? 'drwxr-xr-x' : '-rw-r--r--'), is_file: !dir, is_dir: dir, is_symlink: !!over.is_symlink };
}

async function fixture(page: Page) {
  const state = { uploads: 0, metadata: 0, errors: [] as string[] };
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
    if (path === '/api/servers') return route.fulfill({ json: sampleFleet().slice(0, 1) });
    if (path.endsWith('/files')) return route.fulfill({ json: { path: '/home/deploy', parent: '/home', truncated: false, entries: [
      entry('logs', { is_dir: true }),
      entry('nginx.conf', { size: 4210 }),
      entry('backup.tar.gz', { size: 118 * MB }),
      entry('current', { is_symlink: true, mode: 'lrwxrwxrwx', size: 17 }),
    ] } });
    if (path.endsWith('/files/metadata')) { state.metadata++; return route.fulfill({ json: { exists: false, version: '' } }); }
    if (path.endsWith('/files/upload')) { state.uploads++; return route.fulfill({ json: { size: 6 } }); }
    if (path.endsWith('/files/text')) return route.fulfill({ json: { path: '/home/deploy/nginx.conf', content: 'user www-data;\r\nworker_processes auto;\r\n', revision: 'r1' } });
    if (path.endsWith('/files/audit')) return route.fulfill({ json: [
      { id: 'a1', action: 'PUT text', path: '/home/deploy/nginx.conf', target: 'host', backup_path: '/home/deploy/.server-monitor-backups/nginx.conf.1', outcome: 'success', error_code: '', created_at: '2030-06-01T09:00:00Z' },
      { id: 'a2', action: 'POST upload', path: '/home/deploy/deploy.sh', target: 'host', backup_path: '', outcome: 'failed', error_code: 'permission_denied', created_at: '2030-06-01T08:30:00Z' },
      { id: 'a3', action: 'create', path: '/home/deploy/new.txt', target: 'host', backup_path: '', outcome: 'pending', error_code: '', created_at: '2030-06-01T08:00:00Z' },
    ] });
    return route.fulfill({ json: [] });
  });
  await page.goto('/files?server=idle-host');
  await expect(page.getByRole('button', { name: 'nginx.conf', exact: true })).toBeVisible();
  return state;
}

test('listing shows a home crumb, entry counts and a live filter', async ({ page }) => {
  const state = await fixture(page);
  const location = page.locator('.file-location');
  await expect(location.locator('.file-crumb-root .anticon')).toBeVisible();
  await expect(location).toContainText('deploy');
  await expect(location.locator('.file-count')).toHaveText(en['files.entryCount'].replace('{{count}}', '4'));
  await page.getByRole('searchbox', { name: en['files.filter'] }).fill('conf');
  await expect(page.locator('.file-count')).toHaveText(en['files.entryCountFiltered'].replace('{{count}}', '1').replace('{{total}}', '4'));
  await expect(page.locator('.ant-table-row')).toHaveCount(1);
  await expect(page.locator('.file-hints')).toContainText(en['files.limits']);
  expect(state.errors).toEqual([]);
});

test('row actions are labelled icon buttons and respect the text limit', async ({ page }) => {
  await fixture(page);
  const file = page.getByRole('row').filter({ hasText: 'nginx.conf' });
  for (const name of [en['files.openText'], en['files.download'], en['files.rename'], en['common.delete']]) {
    await expect(file.getByRole('button', { name, exact: true })).toBeEnabled();
  }
  await expect(file.locator('.file-actions .ant-btn')).toHaveCount(4);
  const directory = page.getByRole('row').filter({ hasText: 'logs' });
  await expect(directory.getByRole('button', { name: en['files.download'], exact: true })).toHaveCount(0);
  await expect(directory.locator('.file-actions .ant-btn')).toHaveCount(2);
  const large = page.getByRole('row').filter({ hasText: 'backup.tar.gz' });
  await expect(large.getByRole('button', { name: en['files.openText'], exact: true })).toBeDisabled();
  await expect(large.getByRole('button', { name: en['files.download'], exact: true })).toBeEnabled();
});

test('dropping a file on the listing uploads it into the open directory', async ({ page }) => {
  const state = await fixture(page);
  const transfer = await page.evaluateHandle(() => {
    const data = new DataTransfer();
    data.items.add(new File(['upload'], 'dropped.txt', { type: 'text/plain' }));
    return data;
  });
  const zone = page.locator('.file-dropzone');
  await zone.dispatchEvent('dragenter', { dataTransfer: transfer });
  await expect(page.locator('.file-drop-overlay')).toBeVisible();
  await expect(page.locator('.file-drop-overlay')).toContainText(en['files.dropHint']);
  await zone.dispatchEvent('drop', { dataTransfer: transfer });
  await expect.poll(() => state.uploads).toBe(1);
  expect(state.metadata).toBe(1);
  await expect(page.locator('.file-drop-overlay')).toHaveCount(0);
  expect(state.errors).toEqual([]);
});

test('operations history reads in words with outcomes and reasons', async ({ page }) => {
  await fixture(page);
  await page.getByRole('button', { name: en['files.history'], exact: true }).click();
  const dialog = page.getByRole('dialog').filter({ hasText: en['files.history'] });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText(en['files.historyAction.text']);
  await expect(dialog).toContainText(en['files.historyAction.upload']);
  await expect(dialog).toContainText(en['files.historyAction.create']);
  await expect(dialog).not.toContainText('PUT text');
  await expect(dialog.locator('.ant-tag').filter({ hasText: en['files.outcome.success'] })).toHaveCount(1);
  await expect(dialog.locator('.ant-tag').filter({ hasText: en['files.outcome.failed'] })).toHaveCount(1);
  await expect(dialog).toContainText(en['files.error.permission_denied']);
  await expect(dialog.getByRole('button', { name: en['files.download'] })).toHaveCount(1);
});

test('the editor status line reports lines, size and line endings', async ({ page }) => {
  await fixture(page);
  await page.getByRole('button', { name: 'nginx.conf', exact: true }).click();
  await expect(page.locator('.cm-content')).toBeVisible();
  const status = page.locator('.file-editor-status');
  await expect(status).toContainText(en['files.lines'].replace('{{count}}', '3'));
  await expect(status).toContainText('CRLF');
  await expect(status).toContainText('UTF-8');
  await expect(status).toContainText('/ 2 MiB');
});
