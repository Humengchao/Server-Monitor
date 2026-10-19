import { test, expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import type { NotificationChannel, NotificationDelivery } from '../src/api/notifications';

async function setup(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en'); localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'test-session'); localStorage.setItem('user', JSON.stringify({ id: 'user', username: 'Tester' }));
  });
  const errors: string[] = []; page.on('pageerror', error => errors.push(error.message)); return errors;
}
const initial: NotificationChannel = {
  id: 'c1', name: 'Ops Telegram', type: 'telegram', enabled: true, config: { chat_id: '-1001' },
  secret_fields: ['bot_token'], created_at: '2026-01-01T12:00:00Z', updated_at: '2026-01-01T12:00:00Z',
};

test('editing a channel leaves saved secrets blank and preserves them on save', async ({ page }) => {
  const errors = await setup(page); let channel = { ...initial }; let submitted: Record<string, unknown> | undefined;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/notifications/channels/c1' && route.request().method() === 'PUT') {
      submitted = route.request().postDataJSON(); channel = { ...channel, ...submitted, config: { chat_id: '-2002' } } as NotificationChannel;
      return route.fulfill({ json: channel });
    }
    if (path === '/api/notifications/channels') return route.fulfill({ json: [channel] });
    if (path === '/api/notifications/deliveries') return route.fulfill({ json: { deliveries: [] } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/notifications'); await page.getByRole('button', { name: 'Edit channel', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByLabel('Bot token', { exact: true })).toHaveValue('');
  await expect(dialog.getByPlaceholder('Saved; leave blank to keep')).toBeVisible();
  await dialog.getByLabel('Name', { exact: true }).fill('Renamed Ops');
  await dialog.getByLabel('Chat ID', { exact: true }).fill('-2002');
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog).toHaveCount(0); await expect(page.getByText('Renamed Ops', { exact: true })).toBeVisible();
  expect(submitted?.config).toMatchObject({ chat_id: '-2002' });
  expect((submitted?.config as { bot_token?: string }).bot_token || '').toBe('');
  expect(errors).toEqual([]);
});

for (const action of ['delete', 'resend'] as const) {
  test(`leaving notifications dismisses a pending ${action} confirmation without sending it`, async ({ page }) => {
    const errors = await setup(page);
    const writes: string[] = [];
    await page.route('**/api/**', route => {
      const request = route.request(), path = new URL(request.url()).pathname;
      if (request.method() !== 'GET') { writes.push(path); return route.fulfill({ status: 204 }); }
      if (path === '/api/notifications/channels') return route.fulfill({ json: [initial] });
      if (path === '/api/notifications/deliveries') return route.fulfill({ json: { deliveries: [{ id: 1, channel_id: initial.id, channel_name: initial.name, status: 'failed', attempts: 1, created_at: initial.created_at }] } });
      if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
      return route.fulfill({ json: [] });
    });
    await page.goto('/notifications');
    if (action === 'resend') {
      await page.getByRole('tab', { name: 'Delivery history', exact: true }).click();
      await page.getByRole('button', { name: 'Resend', exact: true }).click();
    } else await page.getByRole('button', { name: 'Delete', exact: true }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await page.evaluate(() => {
      window.history.pushState({}, '', '/credentials');
      window.dispatchEvent(new PopStateEvent('popstate'));
    });
    await expect(page).toHaveURL(/credentials$/);
    await expect(page.getByRole('dialog')).toHaveCount(0);
    expect(writes).toEqual([]);
    expect(errors).toEqual([]);
  });
}

test('test delivery shows errors and manual resend requires explicit duplicate warning', async ({ page }) => {
  const errors = await setup(page); let deliveries: NotificationDelivery[] = []; let retries = 0; let tests = 0;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/notifications/channels') return route.fulfill({ json: [initial] });
    if (path.endsWith('/channels/c1/test')) {
      tests++; deliveries = [{ id: 1, channel_id: 'c1', channel_name: initial.name, channel_type: 'telegram', transition: 'test', status: 'retry', attempts: 1,
        created_at: initial.created_at, last_attempt_at: initial.created_at, next_attempt_at: initial.updated_at, delivered_at: null, last_error: 'Telegram rejected notification (code 400)' }];
      return route.fulfill({ status: 202, json: { delivery_id: 1 } });
    }
    if (path === '/api/notifications/deliveries/1/retry') {
      expect(route.request().postDataJSON()).toEqual({ confirm_resend: true }); retries++;
      deliveries = [{ ...deliveries[0], id: 2, status: 'pending', attempts: 0, retry_of: 1 }, { ...deliveries[0], status: 'canceled' }];
      return route.fulfill({ status: 202, json: { delivery_id: 2 } });
    }
    if (path === '/api/notifications/deliveries') return route.fulfill({ json: { deliveries } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/notifications'); await page.getByRole('button', { name: /Send test$/ }).click();
  await expect.poll(() => tests).toBe(1); await page.getByRole('tab', { name: 'Delivery history', exact: true }).click();
  await expect(page.getByText('Telegram rejected notification (code 400)', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Resend', exact: true }).click();
  const dialog = page.getByRole('dialog'); await expect(dialog.getByText(/can produce a duplicate/)).toBeVisible(); expect(retries).toBe(0);
  await dialog.getByRole('button', { name: 'Resend', exact: true }).click();
  await expect.poll(() => retries).toBe(1); await expect(page.getByText('Retry of #1', { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test('changing provider before saving does not retain a hidden bot token', async ({ page }) => {
  const errors = await setup(page); let submitted: Record<string, unknown> | undefined;
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/notifications/channels' && route.request().method() === 'POST') {
      submitted = route.request().postDataJSON(); return route.fulfill({ status: 201, json: { ...initial, ...submitted, config: {}, secret_fields: ['webhook_url'] } });
    }
    if (path === '/api/notifications/channels') return route.fulfill({ json: [] });
    if (path === '/api/notifications/deliveries') return route.fulfill({ json: { deliveries: [] } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    return route.fulfill({ json: [] });
  });
  await page.goto('/notifications'); await page.getByRole('button', { name: /Add channel$/ }).click();
  const dialog = page.getByRole('dialog'); await dialog.getByLabel('Name', { exact: true }).fill('Generic');
  await dialog.getByLabel('Bot token', { exact: true }).fill('123:discarded');
  await dialog.getByLabel('Type', { exact: true }).click(); await page.getByTitle('Generic webhook', { exact: true }).click();
  await dialog.getByLabel('Webhook URL', { exact: true }).fill('https://example.invalid/hook?key=test');
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect.poll(() => submitted?.type).toBe('webhook'); expect(submitted?.config).toEqual({ webhook_url: 'https://example.invalid/hook?key=test' });
  expect(errors).toEqual([]);
});

test('an alert rule can send to several saved channels', async ({ page }) => {
  const errors = await setup(page); let submitted: Record<string, unknown> | undefined;
  const channels = [initial, { ...initial, id: 'c2', name: 'Ops Email', type: 'email' }];
  const rule = { id: 'r1', user_id: 'user', name: 'High CPU', server_id: null, metric: 'cpu', comparator: '>', threshold: 90,
    duration_seconds: 300, enabled: true, webhook_url: '', channel_ids: ['c1'], created_at: initial.created_at, firing_count: 0 };
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/alerts/rules/r1' && route.request().method() === 'PUT') {
      submitted = route.request().postDataJSON(); return route.fulfill({ json: { ...rule, ...submitted } });
    }
    if (path === '/api/alerts/rules') return route.fulfill({ json: [rule] });
    if (path === '/api/alerts/events/page') return route.fulfill({ json: { events: [] } });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    if (path === '/api/notifications/channels') return route.fulfill({ json: channels });
    return route.fulfill({ json: [] });
  });
  await page.goto('/alerts'); await page.getByRole('radio', { name: /Rules$/ }).locator('..').click();
  await page.getByRole('button', { name: 'Edit', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Notification channels', { exact: true }).click(); await page.getByTitle('Ops Email', { exact: true }).click();
  await dialog.getByLabel('Rule name', { exact: true }).click();
  await dialog.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => submitted?.channel_ids).toEqual(['c1', 'c2']); expect(errors).toEqual([]);
});
