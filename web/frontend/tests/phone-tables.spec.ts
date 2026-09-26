import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

const CHROME = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36';
const SCRIPT = 'python-requests/2.32';

async function fixture(page: Page) {
  const hosts = sampleFleet();
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
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
    if (path === '/api/servers') return route.fulfill({ headers, json: hosts });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/auth/login-history') return route.fulfill({ json: { total: 2, records: [
      { id: 2, user_id: 'u', ip: '203.0.113.7', user_agent: CHROME, success: true, logged_at: '2030-06-15T11:55:00Z' },
      { id: 1, user_id: 'u', ip: '45.155.205.99', user_agent: SCRIPT, success: false, logged_at: '2030-06-15T01:00:00Z' },
    ] } });
    return route.fulfill({ json: [] });
  });
  return errors;
}

async function openListView(page: Page) {
  await page.goto('/dashboard');
  await expect(page.locator('.server-card')).toHaveCount(4);
  await page.locator('.ant-segmented-item').filter({ has: page.locator(`[aria-label="${en['dashboard.listView']}"]`) }).click();
  await expect(page.getByRole('row').filter({ hasText: 'busy-host' })).toBeVisible();
}

test('the server list pins its name and actions on a desktop but not on a phone', async ({ page }) => {
  const errors = await fixture(page);
  await page.setViewportSize({ width: 1280, height: 900 });
  await openListView(page);
  const row = page.getByRole('row').filter({ hasText: 'busy-host' });
  await expect(row.locator('td').first()).toHaveClass(/ant-table-cell-fix-(start|left)/);
  await expect(row.locator('td').last()).toHaveClass(/ant-table-cell-fix-(end|right)/);

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(row.locator('td').first()).not.toHaveClass(/ant-table-cell-fix-(start|left)/);
  await expect(row.locator('td').last()).not.toHaveClass(/ant-table-cell-fix-(end|right)/);
  // Every column is still there, reachable by scrolling, so nothing was silently dropped.
  await expect(row.getByRole('button', { name: en['common.edit'], exact: true })).toHaveCount(1);
  expect(errors).toEqual([]);
});

test('login history names the browser and platform and keeps script agents verbatim', async ({ page }) => {
  const errors = await fixture(page);
  await page.goto('/login-history');
  const chrome = page.getByRole('row').filter({ hasText: '203.0.113.7' });
  await expect(chrome).toContainText('Chrome 140');
  await expect(chrome).toContainText('Windows');
  await expect(chrome).not.toContainText('AppleWebKit');
  await expect(chrome.locator('[title]').first()).toHaveAttribute('title', CHROME);
  const script = page.getByRole('row').filter({ hasText: '45.155.205.99' });
  await expect(script).toContainText(SCRIPT);
  await expect(script).toHaveClass(/login-row-failed/);
  await expect(chrome).not.toHaveClass(/login-row-failed/);

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('columnheader', { name: en['loginHistory.userAgent'] })).toHaveCount(0);
  await expect(script.getByText(en['common.failed'])).toBeVisible();
  expect(errors).toEqual([]);
});
