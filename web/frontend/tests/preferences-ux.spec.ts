import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

async function setup(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route(url => url.pathname.startsWith('/api/'), route => route.fulfill({ json: [] }));
}

function historyPage(offset: number) {
  return { total: 60, records: [{ id: offset + 1, user_id: 'fixture', ip: `192.0.2.${offset + 1}`,
    user_agent: 'browser', success: true, logged_at: '2026-10-19T08:00:00Z' }] };
}

test('login history retains the correct page after a failed change and retries the requested page', async ({ page }) => {
  await setup(page);
  let rejectSecondPage = true;
  await page.route('**/auth/login-history?**', route => {
    const offset = Number(new URL(route.request().url()).searchParams.get('offset'));
    return offset === 20 && rejectSecondPage
      ? route.fulfill({ status: 503, json: { error: 'unavailable' } })
      : route.fulfill({ json: historyPage(offset) });
  });
  await page.goto('/login-history');
  await expect(page.getByRole('cell', { name: '192.0.2.1', exact: true })).toBeVisible();
  await page.locator('.ant-pagination-item[title="2"]').click();
  await expect(page.getByRole('alert')).toContainText(en['loginHistory.loadFailed']);
  await expect(page.locator('.ant-pagination-item-active')).toHaveAttribute('title', '1');
  await expect(page.getByRole('cell', { name: '192.0.2.1', exact: true })).toBeVisible();
  // The API uses a fixed 20-record page; an automatic size selector would lie about the fetch size.
  await expect(page.locator('.ant-pagination-options-size-changer')).toHaveCount(0);
  rejectSecondPage = false;
  await page.getByRole('alert').getByRole('button', { name: en['common.refresh'] }).click();
  await expect(page.getByRole('cell', { name: '192.0.2.21', exact: true })).toBeVisible();
  await expect(page.locator('.ant-pagination-item-active')).toHaveAttribute('title', '2');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('a superseded login-history request is canceled and cannot replace a newer page', async ({ page }) => {
  await setup(page);
  let held: Route | undefined;
  await page.route('**/auth/login-history?**', route => {
    const offset = Number(new URL(route.request().url()).searchParams.get('offset'));
    if (offset === 20) { held = route; return; }
    return route.fulfill({ json: historyPage(offset) });
  });
  await page.goto('/login-history');
  await expect(page.getByRole('cell', { name: '192.0.2.1', exact: true })).toBeVisible();
  await page.locator('.ant-pagination-item[title="2"]').click();
  await expect.poll(() => !!held).toBe(true);
  const canceled = page.waitForEvent('requestfailed', request => new URL(request.url()).searchParams.get('offset') === '20');
  await page.locator('.ant-pagination-item[title="3"]').dispatchEvent('click');
  await canceled;
  await expect(page.getByRole('cell', { name: '192.0.2.41', exact: true })).toBeVisible();
  await held!.fulfill({ json: historyPage(20) });
  await expect(page.locator('.ant-pagination-item-active')).toHaveAttribute('title', '3');
  await expect(page.getByRole('cell', { name: '192.0.2.41', exact: true })).toBeVisible();
});

test('credential load failure has a persistent retry and never claims the collection is empty', async ({ page }) => {
  await setup(page);
  let unavailable = true;
  await page.route('**/api/credentials', route => unavailable
    ? route.fulfill({ status: 503, json: { error: 'unavailable' } })
    : route.fulfill({ json: [{ id: 'credential', name: 'Recovered credential', ssh_username: 'root',
      has_password: false, has_key: true, credential_type: 'linux', created_at: '2026-10-19T08:00:00Z' }] }));
  await page.goto('/credentials');
  await expect(page.getByRole('alert')).toContainText(en['credential.loadFailed']);
  await expect(page.getByText(en['credential.empty'], { exact: true })).toHaveCount(0);
  unavailable = false;
  await page.getByRole('alert').getByRole('button', { name: en['common.refresh'] }).click();
  await expect(page.getByRole('row').filter({ hasText: 'Recovered credential' })).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('tag creation locks a pending submission and preserves the draft after failure', async ({ page }) => {
  await setup(page);
  let held: Route | undefined;
  let creates = 0;
  const payloads: unknown[] = [];
  await page.route('**/api/tags', route => {
    if (route.request().method() === 'POST') {
      creates++;
      payloads.push(route.request().postDataJSON());
      held = route;
      return;
    }
    return route.fulfill({ json: [] });
  });
  await page.goto('/settings');
  await page.getByRole('button', { name: new RegExp(en['settings.createTag'] + '$') }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel(en['settings.tagName'], { exact: true }).fill('  Production  ');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => creates).toBe(1);
  await expect(dialog.getByLabel(en['settings.tagName'], { exact: true })).toBeDisabled();
  await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
  await dialog.locator('form').dispatchEvent('submit');
  await page.keyboard.press('Escape');
  await expect(dialog).toBeVisible();
  expect(creates).toBe(1);
  expect(payloads[0]).toMatchObject({ name: 'Production', color: '#4f7cff' });
  await held!.fulfill({ status: 503, json: { error: 'unavailable' } });
  await expect(dialog.getByLabel(en['settings.tagName'], { exact: true })).toBeEnabled();
  await expect(dialog.getByLabel(en['settings.tagName'], { exact: true })).toHaveValue('  Production  ');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => creates).toBe(2);
  await held!.fulfill({ json: { id: 'new-tag', name: 'Production', color: '#4f7cff' } });
  await expect(dialog).not.toBeVisible();
});

test('a credential created during a slow initial read survives the late old response', async ({ page }) => {
  await setup(page);
  let initial: Route | undefined;
  let reads = 0;
  const credential = { id: 'new', name: 'New credential', ssh_username: 'root', has_password: true, has_key: false,
    credential_type: 'linux', created_at: '2026-10-19T08:00:00Z' };
  await page.route('**/api/credentials', route => {
    if (route.request().method() === 'POST') return route.fulfill({ json: credential });
    if (++reads === 1) { initial = route; return; }
    return route.fulfill({ json: [credential] });
  });
  await page.goto('/credentials');
  await expect.poll(() => !!initial).toBe(true);
  await page.locator('.page-heading').getByRole('button', { name: new RegExp(en['credential.add'] + '$') }).click();
  const dialog = page.getByRole('dialog');
  await dialog.locator('#name').fill('New credential');
  await dialog.locator('#ssh_username').fill('root');
  await dialog.locator('#ssh_password').fill('fixture-password');
  const canceled = page.waitForEvent('requestfailed', request => request.url().endsWith('/credentials') && request.method() === 'GET');
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await canceled;
  await expect(page.getByRole('row').filter({ hasText: 'New credential' })).toBeVisible();
  await initial!.fulfill({ json: [] });
  await expect(page.getByRole('row').filter({ hasText: 'New credential' })).toBeVisible();
});

test('settings keeps the tag action and long account details readable on a narrow phone', async ({ page }) => {
  await setup(page);
  await page.setViewportSize({ width: 320, height: 740 });
  await page.addInitScript(() => {
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'LongUsername'.repeat(10) }));
  });
  await page.route('**/api/tags', route => route.fulfill({ json: [{ id: 'one', name: 'Production'.repeat(10), color: '#4f7cff' }] }));
  await page.goto('/settings');
  const heading = page.locator('.settings-tags-card .ant-card-head-title');
  const create = page.getByRole('button', { name: new RegExp(en['settings.createTag'] + '$') });
  await expect(create).toBeVisible();
  const headingBox = (await heading.boundingBox())!;
  const actionBox = (await create.boundingBox())!;
  expect(actionBox.y).toBeGreaterThanOrEqual(headingBox.y + headingBox.height);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
  expect(await heading.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true);
  await create.click();
  await expect(page.getByRole('dialog')).toBeVisible();
});
