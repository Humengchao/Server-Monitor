import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { observedAt, sampleFleet } from './fixtures/fleet';

const summary = {
  active: 206,
  servers: [
    { server_id: 'busy-host', active: 205, rule_names: Array.from({ length: 205 }, (_, i) => `CPU rule ${i + 1}`) },
    { server_id: 'idle-host', active: 1, rule_names: ['Older offline alert'] },
  ],
};

async function fixture(page: Page) {
  const state = { errors: [] as string[], eventsRead: 0, summariesRead: 0, fail: false, hold: false, held: [] as Route[] };
  page.on('pageerror', error => state.errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/servers') return route.fulfill({ headers: { Date: new Date(observedAt).toUTCString() }, json: sampleFleet() });
    if (url.pathname === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    if (url.pathname === '/api/alerts/events') {
      state.eventsRead++;
      // This is the old endpoint's newest page, which cannot include the older host.
      return route.fulfill({ json: Array.from({ length: 200 }, (_, i) => ({
        id: i + 1, server_id: 'busy-host', rule_name: `CPU rule ${i + 1}`, resolved_at: null,
      })) });
    }
    if (url.pathname === '/api/alerts/summary') {
      if (url.searchParams.get('by_server') !== '1') return route.fulfill({ json: { active: summary.active } });
      state.summariesRead++;
      if (state.hold) { state.held.push(route); return; }
      return state.fail ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fulfill({ json: summary });
    }
    return route.fulfill({ json: [] });
  });
  return state;
}

test('dashboard counts and filters include alerts beyond the event history page', async ({ page }) => {
  const state = await fixture(page);
  await page.goto('/dashboard');
  const busy = page.locator('.server-card').filter({ hasText: 'busy-host' });
  const idle = page.locator('.server-card').filter({ hasText: 'idle-host' });
  await expect(busy.locator('.alert-pill')).toHaveText('205');
  await expect(idle.locator('.alert-pill')).toHaveText('1');
  await idle.locator('.alert-pill').hover();
  await expect(page.getByRole('tooltip')).toContainText('Older offline alert');
  await page.getByRole('button', { name: 'Firing alerts 2', exact: true }).click();
  await expect(page.locator('.server-card')).toHaveCount(2);
  await page.getByRole('radio', { name: 'List view' }).locator('..').click();
  await expect(page.getByRole('row').filter({ hasText: 'busy-host' }).locator('.alert-pill')).toHaveText('205');
  await expect(page.getByRole('row').filter({ hasText: 'idle-host' }).locator('.alert-pill')).toHaveText('1');
  expect(state.eventsRead).toBe(0);
  expect(state.errors).toEqual([]);
});

test('an unavailable summary is unknown until the next successful poll', async ({ page }) => {
  await page.clock.install();
  const state = await fixture(page);
  state.fail = true;
  await page.goto('/dashboard');
  await expect(page.getByRole('button', { name: 'Firing alerts —', exact: true })).toBeDisabled();
  state.fail = false;
  await page.clock.fastForward(20100);
  await expect(page.getByRole('button', { name: 'Firing alerts 2', exact: true })).toBeEnabled();
  await expect(page.locator('.server-card').filter({ hasText: 'idle-host' }).locator('.alert-pill')).toHaveText('1');
  expect(state.errors).toEqual([]);
});

test('leaving the dashboard cancels a pending summary and a fresh visit reloads it', async ({ page }) => {
  const state = await fixture(page);
  state.hold = true;
  await page.goto('/dashboard');
  await expect.poll(() => state.held.length).toBe(1);
  const failed = page.waitForEvent('requestfailed', request => request.url().includes('/alerts/summary?by_server=1'));
  await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
  await failed;
  state.hold = false;
  await page.getByRole('menuitem').filter({ hasText: 'Servers' }).click();
  await expect(page.getByRole('button', { name: 'Firing alerts 2', exact: true })).toBeEnabled();
  await state.held[0].fulfill({ json: { active: 0, servers: [] } });
  await expect(page.locator('.server-card').filter({ hasText: 'busy-host' }).locator('.alert-pill')).toHaveText('205');
  expect(state.summariesRead).toBe(2);
  expect(state.errors).toEqual([]);
});
