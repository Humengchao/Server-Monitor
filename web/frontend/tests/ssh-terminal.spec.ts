import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { observedAt, sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const zh: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/zh.json', import.meta.url), 'utf8'));

test('SSH preserves UTF-8 output, resizes, and retains its session across theme and language changes', async ({ page }) => {
  const host = sampleFleet()[0];
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  const sessions: { received: string[]; closed: boolean; hangUp: () => void }[] = [];
  await page.routeWebSocket('**/api/ssh/idle-host', ws => {
    const session = { received: [] as string[], closed: false, hangUp: () => ws.close() };
    sessions.push(session);
    ws.onClose(() => { session.closed = true; });
    ws.onMessage(message => {
      const text = typeof message === 'string' ? message : message.toString();
      session.received.push(text);
      if (text === '\r') ws.send('\r\ncommand complete\r\nroot@host:/# ');
    });
    const output = Buffer.from('你好，终端 👋\r\n');
    ws.send(output.subarray(0, 1));
    ws.send(output.subarray(1, 7));
    ws.send(output.subarray(7, output.length - 4));
    ws.send(output.subarray(output.length - 4));
    ws.send('root@host:/# ');
  });
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'dark');
    localStorage.setItem('token', 'fixture-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route(/^https?:\/\/[^/]+\/api\//, route => {
    const path = new URL(route.request().url()).pathname;
    const headers = { Date: new Date(observedAt).toUTCString() };
    if (path === '/api/servers') return route.fulfill({ headers, json: [host] });
    if (path === `/api/servers/${host.id}`) return route.fulfill({ headers, json: host });
    if (path.endsWith('/metrics/latest')) return route.fulfill({ headers, json: host.latest_metrics });
    if (path.endsWith('/metrics')) return route.fulfill({ headers, json: [host.latest_metrics] });
    return route.fulfill({ json: [] });
  });

  await page.goto(`/servers/${host.id}`);
  await page.getByRole('tab', { name: en['terminal.title'], exact: true }).click();
  const terminal = page.locator('.server-detail-terminal');
  const screen = terminal.locator('.xterm-rows');
  await expect(terminal.getByRole('status')).toContainText(en['common.connected']);
  await expect(screen).toContainText('你好，终端 👋');
  await expect(screen).toContainText('root@host:/#');
  await expect(screen).not.toContainText('\uFFFD');
  expect(sessions).toHaveLength(1);
  await expect.poll(() => sessions[0].received.some(frame => frame.startsWith('\x01') && frame.includes('"resize"'))).toBe(true);

  await terminal.locator('.xterm').click();
  await page.keyboard.type('pwd');
  await page.keyboard.press('Enter');
  await expect(screen).toContainText('command complete');
  expect(sessions[0].received.filter(frame => !frame.startsWith('\x01')).join('')).toBe('pwd\r');

  const initialResizeCount = sessions[0].received.filter(frame => frame.startsWith('\x01')).length;
  await page.setViewportSize({ width: 1000, height: 850 });
  await expect.poll(() => sessions[0].received.filter(frame => frame.startsWith('\x01')).length).toBeGreaterThan(initialResizeCount);

  await page.getByRole('button', { name: en['theme.light'], exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: '切换到中文', exact: true }).click();
  await expect(terminal.getByRole('status')).toContainText(zh['common.connected']);
  await expect(screen).toContainText('command complete');
  expect(sessions).toHaveLength(1);
  expect(sessions[0].closed).toBe(false);

  // Explicitly replacing an active shell must close the previous socket.
  await terminal.getByRole('button', { name: zh['terminal.reconnect'], exact: true }).click();
  await expect.poll(() => sessions.length).toBe(2);
  await expect.poll(() => sessions[0].closed).toBe(true);
  await expect(screen).toContainText('你好，终端 👋');
  await expect(screen).not.toContainText('command complete');
  await expect(terminal.locator('.xterm')).toHaveCount(1);

  sessions[1].hangUp();
  await expect(terminal.getByRole('status')).toContainText(zh['common.disconnected']);
  await expect(screen).toContainText(zh['terminal.disconnected']);
  await terminal.getByRole('button', { name: zh['terminal.reconnect'], exact: true }).click();
  await expect.poll(() => sessions.length).toBe(3);
  await expect(terminal.getByRole('status')).toContainText(zh['common.connected']);

  await page.locator('.detail-back').click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect.poll(() => sessions[2].closed).toBe(true);
  expect(errors).toEqual([]);
});
