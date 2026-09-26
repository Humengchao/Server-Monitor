import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

const running = { id: 'a'.repeat(64), name: 'web', image: 'nginx:1.27', state: 'running', status: 'Up 2 days', ports: '0.0.0.0:80->80/tcp', created: '2 days ago' };
const exited = { id: 'b'.repeat(64), name: 'job', image: 'acme/job', state: 'exited', status: 'Exited (0) 1 hour ago', ports: '', created: '1 hour ago' };

async function fixture(page: Page) {
  const hosts = sampleFleet().slice(0, 1);
  hosts[0].has_docker = true;
  hosts[0].docker_version = '24.0.7';
  const state = { tails: [] as string[], errors: [] as string[] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === '/api/servers') return route.fulfill({ json: hosts });
    if (path.endsWith('/docker/containers')) return route.fulfill({ json: [running, exited] });
    if (path.endsWith('/docker/stats')) return route.fulfill({ json: [] });
    if (/\/docker\/containers\/[a-f]+\/logs$/.test(path)) {
      state.tails.push(url.searchParams.get('tail') || '');
      return route.fulfill({ json: { logs: Array.from({ length: 40 }, (_, index) => `line ${index + 1} of a fairly long log entry that would wrap when the viewport is narrow enough`).join('\n') } });
    }
    return route.fulfill({ json: [] });
  });
  await page.goto('/docker');
  await page.locator('.ant-collapse-header').first().click();
  await expect(page.getByText('web', { exact: true })).toBeVisible();
  return state;
}

test('container rows expose named icon controls that follow the container state', async ({ page }) => {
  const state = await fixture(page);
  const web = page.getByRole('row').filter({ hasText: 'web' });
  for (const name of [en['docker.stop'], en['docker.restart'], en['docker.logs'], en['docker.exec'], en['nav.files']]) {
    await expect(web.getByRole('button', { name, exact: true })).toBeEnabled();
  }
  await expect(web.getByRole('button', { name: en['docker.start'], exact: true })).toHaveCount(0);
  const job = page.getByRole('row').filter({ hasText: 'job' });
  await expect(job.getByRole('button', { name: en['docker.start'], exact: true })).toBeEnabled();
  await expect(job.getByRole('button', { name: en['docker.stop'], exact: true })).toHaveCount(0);
  await expect(job.getByRole('button', { name: en['nav.files'], exact: true })).toBeDisabled();
  expect(state.errors).toEqual([]);
});

test('the logs drawer can change depth, wrapping and refresh without reopening', async ({ page }) => {
  const state = await fixture(page);
  await page.getByRole('row').filter({ hasText: 'web' }).getByRole('button', { name: en['docker.logs'], exact: true }).click();
  const drawer = page.getByRole('dialog');
  await expect(drawer).toContainText('web');
  await expect(drawer.locator('.docker-logs')).toContainText('line 40');
  expect(state.tails).toEqual(['500']);

  await drawer.getByRole('combobox').click();
  await page.locator('.ant-select-item-option').filter({ hasText: en['docker.logsTail'].replace('{{count}}', '2000') }).click();
  await expect.poll(() => state.tails.at(-1)).toBe('2000');

  const logs = drawer.locator('.docker-logs');
  await expect(logs).toHaveCSS('white-space', 'pre-wrap');
  await drawer.getByRole('switch', { name: en['docker.logsWrap'] }).click();
  await expect(logs).toHaveCSS('white-space', 'pre');

  await drawer.getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await expect.poll(() => state.tails.length).toBe(3);
  await expect(drawer.getByRole('button', { name: en['docker.logsCopy'] })).toBeEnabled();
  expect(state.errors).toEqual([]);
});
