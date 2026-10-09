import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { sampleFleet } from './fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));

const container = { id: 'a'.repeat(64), name: 'web', image: 'nginx:1.27', state: 'running', status: 'Up 2 days', ports: '', created: '2 days ago' };

test('the container terminal opens a shell, relays keystrokes and can reconnect', async ({ page }) => {
  const hosts = sampleFleet().slice(0, 1);
  hosts[0].has_docker = true;
  hosts[0].docker_version = '24.0.7';
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));

  // Play the container's shell: greet with a prompt, answer "ls", and let the
  // test hang up to exercise the disconnect and reconnect path.
  const sessions: { received: string[]; closed: boolean; hangUp: () => void }[] = [];
  await page.routeWebSocket(/\/api\/ws\/servers\/idle-host\/docker\/containers\/a+\/exec$/, ws => {
    const session = { received: [] as string[], closed: false, hangUp: () => ws.close() };
    sessions.push(session);
    ws.onClose(() => { session.closed = true; });
    ws.onMessage(message => {
      const text = typeof message === 'string' ? message : message.toString();
      session.received.push(text);
      if (text === '\r') {
        ws.send('\r\nbin etc usr\r\n');
        // SSH reads can split a multibyte character at any byte boundary.
        const unicode = Buffer.from('中文目录\r\n');
        ws.send(unicode.subarray(0, 1));
        ws.send(unicode.subarray(1, 5));
        ws.send(unicode.subarray(5));
        ws.send('root@web:/# ');
      }
    });
    ws.send('root@web:/# ');
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
    if (path === '/api/servers') return route.fulfill({ json: hosts });
    if (path.endsWith('/docker/containers')) return route.fulfill({ json: [container] });
    return route.fulfill({ json: [] });
  });

  await page.goto('/docker');
  await page.locator('.ant-collapse-header').first().click();
  await page.getByRole('row').filter({ hasText: 'web' }).getByRole('button', { name: en['docker.exec'], exact: true }).click();

  const drawer = page.getByRole('dialog');
  const screen = drawer.locator('.xterm-rows');
  await expect(drawer.getByRole('status')).toHaveText(en['common.connected']);
  await expect(screen).toContainText('root@web:/#');
  await expect.poll(() => sessions[0].received.some(m => m.startsWith('\x01') && m.includes('"resize"'))).toBe(true);

  await drawer.locator('.xterm').click();
  await page.keyboard.type('ls');
  await page.keyboard.press('Enter');
  await expect(screen).toContainText('bin etc usr');
  await expect(screen).toContainText('中文目录');
  await expect(screen).not.toContainText('\uFFFD');
  expect(sessions[0].received.filter(m => !m.startsWith('\x01')).join('')).toContain('ls\r');

  sessions[0].hangUp();
  await expect(drawer.getByRole('status')).toHaveText(en['common.disconnected']);
  await expect(screen).toContainText(en['docker.execDisconnected']);

  await drawer.getByRole('button', { name: en['terminal.reconnect'] }).click();
  await expect.poll(() => sessions.length).toBe(2);
  await expect(drawer.getByRole('status')).toHaveText(en['common.connected']);
  await expect(screen).toContainText('root@web:/#');
  await expect(screen).not.toContainText('bin etc usr');

  // Closing a live drawer releases its socket; reopening creates one shell.
  await drawer.getByRole('button', { name: 'Close', exact: true }).click();
  await expect.poll(() => sessions[1].closed).toBe(true);
  await expect(drawer).not.toBeVisible();
  await page.getByRole('row').filter({ hasText: 'web' }).getByRole('button', { name: en['docker.exec'], exact: true }).click();
  await expect.poll(() => sessions.length).toBe(3);
  await expect(drawer.getByRole('status')).toHaveText(en['common.connected']);
  await expect(drawer.locator('.xterm')).toHaveCount(1);
  await expect(screen).toContainText('root@web:/#');
  await expect(screen).not.toContainText('bin etc usr');
  expect(errors).toEqual([]);
});
