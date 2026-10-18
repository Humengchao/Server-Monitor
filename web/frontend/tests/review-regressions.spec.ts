import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { host, observedAt } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
async function setup(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en'); localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'old-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Review' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
}

test('a late rejection of the previous token does not expire a successfully rotated session', async ({ page }) => {
  await setup(page);
  let oldRequest: Route | undefined;
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/alerts/summary') { oldRequest = route; return; }
    if (path === '/api/auth/password') return route.fulfill({ json: { token: 'replacement-token' } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/settings');
  await expect.poll(() => !!oldRequest).toBe(true);
  await page.locator('#current_password').fill('old-password');
  await page.locator('#new_password').fill('new-password');
  await page.locator('#confirm_password').fill('new-password');
  await page.getByRole('button', { name: en['settings.updatePassword'], exact: true }).click();
  await expect.poll(() => page.evaluate(() => localStorage.getItem('token'))).toBe('replacement-token');
  const oldResponse = page.waitForResponse(response => response.url().endsWith('/alerts/summary'));
  await oldRequest!.fulfill({ status: 401, json: { error: 'revoked' } });
  await oldResponse;
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBe('replacement-token');
  // A rejection of the current token must still protect the session.
  await page.route('**/api/credentials', route => route.fulfill({ status: 401, json: { error: 'expired' } }));
  await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
  await expect(page.getByRole('dialog').filter({ hasText: en['files.sessionExpired'] })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeNull();
});

test('alert totals include every firing event and server pagination can reach older active and resolved incidents', async ({ page }) => {
  await setup(page);
  const active = Array.from({ length: 201 }, (_, index) => ({ id: 201 - index, rule_id: 'firing', rule_name: `Incident ${201 - index}`,
    metric: 'cpu', server_id: 'one', server_name: 'Host', value: 95, comparator: '>', threshold: 90, duration_seconds: 60,
    started_at: new Date(observedAt - index * 60000).toISOString(), resolved_at: null }));
  const resolved = [{ ...active[0], id: 999, rule_name: 'Recovered incident', resolved_at: new Date(observedAt).toISOString() }];
  const states: string[] = [];
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/alerts/summary') return route.fulfill({ json: { active: 201 } });
    if (url.pathname === '/api/alerts/events/page') {
      const state = url.searchParams.get('state')!; states.push(state);
      const data = state === 'resolved' ? resolved : active;
      const offset = Number(url.searchParams.get('cursor') || 0);
      const limit = Number(url.searchParams.get('limit'));
      return route.fulfill({ json: { events: data.slice(offset, offset + limit), next_cursor: offset + limit < data.length ? String(offset + limit) : undefined } });
    }
    return route.fulfill({ json: [] });
  });
  await page.goto('/alerts');
  await expect(page.locator('.overview-card strong').first()).toHaveText('201');
  await expect(page.locator('.alert-entry')).toHaveCount(100);
  await page.getByRole('button', { name: en['alerts.nextPage'], exact: true }).click();
  await expect(page.getByText('Incident 101', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: en['alerts.nextPage'], exact: true }).click();
  await expect(page.getByText('Incident 1', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: en['alerts.nextPage'], exact: true })).toBeDisabled();
  await page.getByRole('button', { name: en['alerts.previousPage'], exact: true }).click();
  await expect(page.getByText('Incident 101', { exact: true })).toBeVisible();
  await page.getByRole('radio', { name: en['alerts.filterResolved'], exact: true }).locator('..').click();
  await expect(page.getByText('Recovered incident', { exact: true })).toBeVisible();
  await expect(page.locator('.overview-card strong').first()).toHaveText('201');
  await expect(page.getByRole('button', { name: en['alerts.previousPage'], exact: true })).toBeDisabled();
  expect(states).toContain('resolved');
});

for (const scope of ['fleet', 'detail'] as const) {
  test(`Docker ${scope}: a completed action cannot restart reads after leaving`, async ({ page }) => {
    await setup(page);
    const server = host('one', { has_docker: true });
    const container = { id: 'a'.repeat(64), name: 'Stopped container', state: 'exited', status: 'Exited', image: 'test', created: '', ports: '' };
    let action: Route | undefined;
    let inventories = 0;
    await page.route('**/api/**', route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/servers') return route.fulfill({ json: [server] });
      if (path === '/api/servers/one') return route.fulfill({ json: server });
      if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
      if (path.endsWith('/metrics/latest')) return route.fulfill({ json: server.latest_metrics });
      if (path.endsWith('/docker/containers')) { inventories++; return route.fulfill({ json: [container] }); }
      if (path.endsWith('/start')) { action = route; return; }
      return route.fulfill({ json: [] });
    });
    await page.goto(scope === 'fleet' ? '/docker?server=one' : '/servers/one');
    if (scope === 'detail') await page.getByRole('tab', { name: en['docker.title'], exact: true }).click();
    await page.getByRole('button', { name: en['docker.start'], exact: true }).click();
    await expect.poll(() => !!action).toBe(true);
    const before = inventories;
    await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
    await expect(page).toHaveURL(/credentials$/);
    // Navigation changes the URL before a lazy route commits and unmounts Docker.
    await expect(page.getByRole('heading', { name: en['credential.title'], exact: true })).toBeVisible();
    const completed = page.waitForResponse(response => response.url().endsWith('/start'));
    await action!.fulfill({ json: { message: 'started' } });
    await completed;
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    expect(inventories).toBe(before);
  });
}

async function startBatch(page: Page) {
  await setup(page);
  await page.addInitScript(() => {
    const open = XMLHttpRequest.prototype.open;
    const send = XMLHttpRequest.prototype.send;
    const urls = new WeakMap<XMLHttpRequest, string>();
    XMLHttpRequest.prototype.open = function (method: string, url: string | URL, ...args: [boolean?, string?, string?]) {
      urls.set(this, String(url)); open.call(this, method, url, args[0] ?? true, args[1], args[2]);
    };
    XMLHttpRequest.prototype.send = function (body) {
      if (urls.get(this)?.includes('/bulk/exec')) document.documentElement.dataset.batchTimeout = String(this.timeout);
      send.call(this, body);
    };
  });
  let batch: Route | undefined;
  let requests = 0;
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/servers') return route.fulfill({ json: [host('one')] });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (path === '/api/servers/bulk/exec') { batch = route; requests++; return; }
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/dashboard');
  await page.getByRole('button', { name: en['batch.enterSelection'], exact: true }).click();
  await page.locator('.server-card').click();
  await page.getByRole('button', { name: en['batch.exec'] }).click();
  await page.getByPlaceholder(en['batch.commandPlaceholder']).fill('uptime');
  await page.getByRole('button', { name: en['batch.runOn'].replace('{{count}}', '1') }).click();
  await page.getByRole('button', { name: en['batch.run'], exact: true }).click();
  await expect.poll(() => !!batch).toBe(true);
  return { route: batch!, requests: () => requests };
}

test('batch execution allows the full server deadline and aborts transport when the page leaves', async ({ page }) => {
  const batch = await startBatch(page);
  await expect(page.locator('html')).toHaveAttribute('data-batch-timeout', '600000');
  await page.getByRole('button', { name: en['batch.run'] }).dispatchEvent('click');
  expect(batch.requests()).toBe(1);
  const failed = page.waitForEvent('requestfailed', request => request.url().includes('/bulk/exec'));
  await page.evaluate(() => { history.pushState({}, '', '/credentials'); window.dispatchEvent(new PopStateEvent('popstate')); });
  await failed;
  await expect(page.getByText(en['batch.execUncertain'], { exact: true })).toBeVisible();
  expect(batch.requests()).toBe(1);
});

test('a lost batch response explains uncertain execution and never retries automatically', async ({ page }) => {
  const batch = await startBatch(page);
  await batch.route.abort('failed');
  await expect(page.getByText(en['batch.execUncertain'], { exact: true })).toBeVisible();
  expect(batch.requests()).toBe(1);
});

test('changing alert state cancels an older page and rule ordering uses uncapped firing counts', async ({ page }) => {
  await setup(page);
  let held: Route | undefined;
  const rule = { id: 'firing', name: 'Z firing rule', metric: 'cpu', server_id: null, comparator: '>', threshold: 90,
    duration_seconds: 60, enabled: true, webhook_url: '', firing_count: 501 };
  const resolved = { id: 1, rule_id: 'firing', rule_name: 'Recovered event', metric: 'cpu', server_id: 'one',
    server_name: 'Host', value: 95, threshold: 90, comparator: '>', duration_seconds: 60,
    started_at: new Date(observedAt - 60000).toISOString(), resolved_at: new Date(observedAt).toISOString() };
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/alerts/summary') return route.fulfill({ json: { active: 501 } });
    if (url.pathname === '/api/alerts/rules') return route.fulfill({ json: [{ ...rule, id: 'armed', name: 'A armed rule', firing_count: 0 }, rule] });
    if (url.pathname === '/api/alerts/events/page') {
      if (url.searchParams.get('state') === 'active') { held = route; return; }
      return route.fulfill({ json: { events: [resolved] } });
    }
    return route.fulfill({ json: [] });
  });
  await page.goto('/alerts');
  await expect.poll(() => !!held).toBe(true);
  const canceled = page.waitForEvent('requestfailed', request => request.url().includes('/events/page?state=active'));
  await page.getByRole('radio', { name: en['alerts.filterResolved'], exact: true }).locator('..').click();
  await canceled;
  await expect(page.getByText('Recovered event', { exact: true })).toBeVisible();
  await held!.fulfill({ json: { events: [] } });
  await expect(page.getByText('Recovered event', { exact: true })).toBeVisible();
  await page.getByRole('radio', { name: en['alerts.rules'] }).locator('..').click();
  await expect(page.locator('.ant-table-tbody .ant-table-row').first()).toContainText('Z firing rule');
});
