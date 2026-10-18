import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import type { AlertRule } from '../src/api/alerts';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

async function setup(page: Page) {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  return errors;
}

const statusSnapshot = {
  overall: 'degraded', generated_at: '2030-06-15T12:00:00Z',
  summary: { total: 0, online: 0, degraded: 0, offline: 0, memory_used: 0, memory_total: 0,
    disk_used: 0, disk_total: 0, traffic_total_bytes: 0, network_rx_bytes: 0, network_tx_bytes: 0, remaining_value: 0 },
  nodes: [], privacy: { anonymized: false, hidden_fields: [] },
};

test('the initial public status timeout shows an error and can recover', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  const held: Route[] = [];
  let respond = false;
  await page.route(url => url.pathname === '/api/public/status', route => {
    if (respond) return route.fulfill({ json: statusSnapshot });
    held.push(route);
  });
  await page.goto('/status');
  await expect.poll(() => held.length).toBe(1);
  await page.clock.fastForward(10100);
  // A slow initial request gets its full deadline; polling does not restart it.
  expect(held).toHaveLength(1);
  await page.clock.fastForward(5100);
  await expect(page.getByText(en['probe.unavailable'], { exact: true })).toBeVisible();
  await expect(page.locator('.overall-dot')).toHaveClass(/outage/);
  respond = true;
  await page.getByRole('button', { name: en['probe.retry'], exact: true }).click();
  await expect(page.locator('.overall-dot')).toHaveClass(/degraded/);
  await expect(page.getByText(en['probe.unavailable'], { exact: true })).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('a timed-out public refresh marks the snapshot stale and a later refresh clears it', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  let respond = true;
  let held: Route | undefined;
  await page.route(url => url.pathname === '/api/public/status', route => {
    if (respond) return route.fulfill({ json: statusSnapshot });
    held = route;
  });
  await page.goto('/status');
  await expect(page.locator('.overall-dot')).toHaveClass(/degraded/);
  respond = false;
  await page.clock.fastForward(10100);
  await expect.poll(() => !!held).toBe(true);
  await page.clock.fastForward(15100);
  await expect(page.locator('.glass-stale-banner')).toBeVisible();
  await expect(page.locator('.overall-dot')).toHaveClass(/degraded/);
  respond = true;
  await page.clock.fastForward(10100);
  await expect(page.locator('.glass-stale-banner')).toHaveCount(0);
  await expect(page.locator('.overall-dot')).toHaveClass(/degraded/);
  expect(errors).toEqual([]);
});

test('a public request superseded by manual refresh cannot replace the newer snapshot', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  let reads = 0;
  let held: Route | undefined;
  await page.route(url => url.pathname === '/api/public/status', route => {
    reads++;
    if (reads === 2) { held = route; return; }
    return route.fulfill({ json: { ...statusSnapshot, overall: reads > 2 ? 'operational' : 'degraded' } });
  });
  await page.goto('/status');
  await expect(page.locator('.overall-dot')).toHaveClass(/degraded/);
  await page.clock.fastForward(10100);
  await expect.poll(() => !!held).toBe(true);
  await page.getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await expect(page.locator('.overall-dot')).toHaveClass(/operational/);
  await held!.fulfill({ json: { ...statusSnapshot, overall: 'outage' } });
  await page.clock.fastForward(16000);
  await expect(page.locator('.overall-dot')).toHaveClass(/operational/);
  await expect(page.locator('.glass-stale-banner')).toHaveCount(0);
  expect(errors).toEqual([]);
});

function rule(enabled = true): AlertRule {
  return { id: 'cpu-rule', user_id: 'fixture', name: 'CPU guard', server_id: null,
    metric: 'cpu', comparator: '>', threshold: 90, duration_seconds: 60,
    enabled, webhook_url: '', created_at: '2030-01-01T00:00:00Z', firing_count: 0 };
}

test('an old alert poll cannot overwrite a saved toggle and repeated clicks cannot race writes', async ({ page }) => {
  const errors = await setup(page);
  await page.clock.install();
  let currentRule = rule();
  let reads = 0;
  let writes = 0;
  let oldRead: Route | undefined;
  let write: Route | undefined;
  let writtenRule: AlertRule | undefined;
  await page.route(url => url.pathname.startsWith('/api/'), route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/alerts/rules') {
      reads++;
      if (reads === 2) { oldRead = route; return; }
      return route.fulfill({ json: [currentRule] });
    }
    if (path === '/api/alerts/rules/cpu-rule') {
      writes++;
      write = route;
      writtenRule = { ...currentRule, ...route.request().postDataJSON() };
      return;
    }
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/alerts');
  await page.getByRole('radio', { name: en['alerts.rules'] }).locator('..').click();
  const toggle = page.getByRole('switch', { name: `${en['alerts.enabled']}: CPU guard`, exact: true });
  await expect(toggle).toBeChecked();
  await page.clock.fastForward(15100);
  await expect.poll(() => !!oldRead).toBe(true);
  await toggle.click();
  await expect.poll(() => writes).toBe(1);
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toBeDisabled();
  await toggle.dispatchEvent('click');
  await oldRead!.fulfill({ json: [rule(true)] });
  await page.getByRole('button', { name: en['common.refresh'], exact: true }).click();
  await page.clock.fastForward(1000);
  await expect(toggle).not.toBeChecked();
  expect(writes).toBe(1);
  expect(reads).toBe(2);
  currentRule = writtenRule!;
  await write!.fulfill({ json: currentRule });
  await expect.poll(() => reads).toBe(3);
  await expect(toggle).toBeEnabled();
  await expect(toggle).not.toBeChecked();
  expect(errors).toEqual([]);
});

test('alert requests are canceled on navigation and cannot launch a later refresh', async ({ page }) => {
  const errors = await setup(page);
  let reads = 0;
  let oldRead: Route | undefined;
  const failed: string[] = [];
  page.on('requestfailed', request => { if (new URL(request.url()).pathname === '/api/alerts/rules') failed.push(request.url()); });
  await page.route(url => url.pathname.startsWith('/api/'), route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/alerts/rules') { reads++; oldRead = route; return; }
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/alerts');
  await expect.poll(() => !!oldRead).toBe(true);
  await page.evaluate(() => {
    window.history.pushState({}, '', '/dashboard');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.locator('.alerts-page')).toHaveCount(0);
  await expect.poll(() => failed.length).toBe(1);
  await oldRead!.fulfill({ status: 503, json: { error: 'late response' } });
  await expect(page.getByText(en['alerts.loadFailed'], { exact: true })).toHaveCount(0);
  expect(reads).toBe(1);
  expect(errors).toEqual([]);
});
