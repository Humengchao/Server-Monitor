import { test, expect } from '@playwright/test';
import type { APIResponse, Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { host } from '../tests/fixtures/fleet';

const en: Record<string, string> = JSON.parse(await readFile(new URL('../src/i18n/en.json', import.meta.url), 'utf8'));
const fixtureID = '11111111-1111-4111-8111-111111111111';
const privatePages = [
  { path: '/dashboard', menu: en['nav.servers'], selector: '.dashboard-page' },
  { path: '/docker', menu: en['nav.docker'], heading: en['docker.title'] },
  { path: '/files', menu: en['nav.files'], selector: '.files-page' },
  { path: '/network', menu: 'Network', selector: '.network-page' },
  { path: '/logs', menu: 'Logs', selector: '.logs-page' },
  { path: '/notifications', menu: 'Notifications', selector: '.notifications-page' },
  { path: '/backups', menu: 'Backups', selector: '.backups-page' },
  { path: '/credentials', menu: en['nav.credentials'], heading: en['credential.title'] },
  { path: '/alerts', menu: en['nav.alerts'], selector: '.alerts-page' },
  { path: '/login-history', menu: en['nav.loginHistory'], heading: en['loginHistory.title'] },
  { path: '/settings', menu: en['nav.settings'], selector: '.settings-page' },
  { path: '/ai', menu: 'AI assistant', selector: '.ai-page' },
];
const pagePaths = ['/login', '/register', '/status', `/servers/${fixtureID}`, ...privatePages.map(page => page.path)];

function securityHeaders(response: APIResponse) {
  const headers = response.headers();
  expect(headers['x-content-type-options']).toBe('nosniff');
  expect(headers['x-frame-options']).toBe('DENY');
  expect(headers['referrer-policy']).toBe('strict-origin-when-cross-origin');
}

test('real health, database readiness and unauthenticated API protection', async ({ request }) => {
  for (const [path, status] of [['/api/health', 'ok'], ['/api/ready', 'ready']]) {
    const response = await request.get(path, { maxRedirects: 0 });
    expect(response.status(), path).toBe(200);
    expect(await response.json()).toEqual({ status });
    expect(response.headers()['cache-control']).toContain('no-store');
    securityHeaders(response);
  }
  for (const path of ['/api/auth/me', '/api/servers', '/api/credentials']) {
    const response = await request.get(path, { maxRedirects: 0 });
    // Never inspect a protected response body, even if authorization regresses.
    expect(response.status(), path).toBe(401);
    securityHeaders(response);
    await response.dispose();
  }
});

test('all deep links and hashed assets have correct deployment headers', async ({ request }) => {
  let html = '';
  for (const path of pagePaths) {
    const response = await request.get(path, { maxRedirects: 0 });
    expect(response.status(), path).toBe(200);
    expect(response.headers()['content-type']).toContain('text/html');
    expect(response.headers()['cache-control']).toContain('no-cache');
    securityHeaders(response);
    html = await response.text();
    expect(html, path).toContain('id="root"');
    await response.dispose();
  }
  const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^"?#]+\.(?:js|css))"/g)].map(match => match[1]);
  expect(assets.some(asset => asset.endsWith('.js'))).toBe(true);
  expect(assets.some(asset => asset.endsWith('.css'))).toBe(true);
  for (const path of new Set(assets)) {
    const response = await request.get(path, { maxRedirects: 0 });
    expect(response.status(), path).toBe(200);
    expect(response.headers()['content-type']).toMatch(path.endsWith('.js') ? /javascript/ : /text\/css/);
    expect(response.headers()['cache-control']).toContain('immutable');
    expect(response.headers()['cache-control']).toMatch(/max-age=31536000/);
    securityHeaders(response);
    await response.dispose();
  }
  const missing = await request.get('/assets/production-smoke-missing.js', { maxRedirects: 0 });
  expect(missing.status()).toBe(404);
});

test('Agent installers and all binary targets are available without full downloads', async ({ request }) => {
  for (const name of ['install.sh', 'install.ps1']) {
    const response = await request.get(`/api/agent/${name}`, { maxRedirects: 0 });
    expect(response.status(), name).toBe(200);
    const body = await response.body();
    expect(body.length).toBeGreaterThan(100);
    expect(body.length).toBeLessThan(128 * 1024);
    expect(body.toString()).not.toContain('<!doctype html>');
  }
  for (const os of ['linux', 'windows']) {
    for (const arch of ['amd64', 'arm64']) {
      // APIRequestContext buffers the whole body before returning. Fetch lets us
      // cancel immediately if a proxy ignores Range, without downloading a binary.
      const response = await fetch(new URL(`/api/agent/download/${os}/${arch}`, test.info().project.use.baseURL), {
        headers: { Range: 'bytes=0-15' }, redirect: 'error', credentials: 'omit', signal: AbortSignal.timeout(10000),
      });
      if (response.status !== 206 || response.headers.get('content-length') !== '16') {
        await response.body?.cancel();
        throw new Error(`${os}/${arch}: expected a 16-byte partial response, got HTTP ${response.status}`);
      }
      expect(response.headers.get('content-range')).toMatch(/^bytes 0-15\/[1-9]\d*$/);
      expect(response.headers.get('content-type')).toContain('application/octet-stream');
      const bytes = Buffer.from(await response.arrayBuffer());
      expect(bytes.length).toBe(16);
      expect(bytes.subarray(0, os === 'linux' ? 4 : 2).toString('hex')).toBe(os === 'linux' ? '7f454c46' : '4d5a');
    }
  }
});

async function isolateBrowser(page: Page, origin: string) {
  const faults: string[] = [], loaded = new Set<string>();
  const timestamp = new Date().toISOString();
  const server = host(fixtureID, { name: 'Deployment fixture', has_docker: true });
  server.latest_metrics!.recorded_at = timestamp;
  const metric = server.latest_metrics!;
  const taskID = '22222222-2222-4222-8222-222222222222';
  const actionID = '33333333-3333-4333-8333-333333333333';
  const aiTask = { id: taskID, scope: 'server', server_id: fixtureID, server_name: server.name, server_type: 'linux', provider_id: 'fixture-provider', provider_name: 'Fixture provider', model: 'fixture', prompt: 'Deployment fixture health', status: 'completed', error: '', steps: 2, prompt_tokens: 20, completion_tokens: 10, approval_mode: 'standard', skills: [], plugins: [], created_at: timestamp, updated_at: timestamp };
  const aiDetail = { task: aiTask, events: [
    { id: 1, kind: 'user', content: aiTask.prompt, created_at: timestamp },
    { id: 2, kind: 'assistant', phase: 'progress', content: 'Checking fixture health.', created_at: timestamp },
    { id: 3, kind: 'tool', action_id: actionID, content: 'Inspect server', created_at: timestamp },
    { id: 4, kind: 'assistant', phase: 'final', content: '# Deployment result\nAll fixture checks passed.', created_at: timestamp },
  ], actions: [{ id: actionID, tool_name: 'run_command', arguments: { command: 'uptime' }, summary: 'Inspect server', command: 'uptime', requires_approval: false, risk_reason: '', mutating: false, status: 'succeeded', output: 'fixture healthy', error: '', exit_code: 0, truncated: false, unknown: false, duration_ms: 10, created_at: timestamp, completed_at: timestamp }] };
  const file = { name: 'fixture.yaml', path: '/tmp/fixture.yaml', size: 14, modified_at: timestamp, mode: '-rw-r--r--', is_file: true, is_dir: false, is_symlink: false };
  const fixtures: Record<string, unknown> = {
    '/api/auth/me': { id: 'fixture', username: 'Fixture' },
    '/api/auth/login-history': { records: [], total: 0 },
    '/api/servers': [server],
    [`/api/servers/${fixtureID}`]: server,
    '/api/servers/uptime': { servers: [{ server_id: fixtureID, windows: [] }], basis: 'observed', generated_at: timestamp },
    [`/api/servers/${fixtureID}/uptime`]: { server_id: fixtureID, days: [], outages: [], basis: 'observed', generated_at: timestamp },
    [`/api/servers/${fixtureID}/metrics/latest`]: metric,
    [`/api/servers/${fixtureID}/metrics`]: [metric],
    [`/api/servers/${fixtureID}/docker/containers`]: [],
    [`/api/servers/${fixtureID}/docker/stats`]: [],
    [`/api/servers/${fixtureID}/files`]: { path: '/tmp', parent: '/', entries: [file], truncated: false },
    [`/api/servers/${fixtureID}/files/audit`]: [],
    [`/api/servers/${fixtureID}/files/text`]: { path: file.path, content: 'enabled: true\n', revision: 'fixture-only' },
    '/api/alerts/summary': { active: 0, servers: [] },
    '/api/alerts/rules': [], '/api/alerts/events': [], '/api/alerts/events/page': { events: [] },
    '/api/credentials': [], '/api/tags': [], '/api/network/checks': [],
    '/api/notifications/channels': [], '/api/notifications/deliveries': { deliveries: [] },
    '/api/ai/providers': [], '/api/ai/skills': [], '/api/ai/plugins': [], '/api/ai/tasks': { tasks: [aiTask] },
    [`/api/ai/tasks/${taskID}`]: aiDetail,
    '/api/public/status': {
      overall: 'operational', generated_at: timestamp, nodes: [], privacy: { anonymized: false, hidden_fields: [] },
      summary: { total: 0, online: 0, degraded: 0, offline: 0, memory_used: 0, memory_total: 0, disk_used: 0, disk_total: 0, traffic_total_bytes: 0, network_rx_bytes: 0, network_tx_bytes: 0, remaining_value: 0 },
    },
  };
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en'); localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'production-smoke-fixture-not-a-token');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Fixture' }));
  });
  page.on('pageerror', error => faults.push(error.message));
  page.on('response', response => {
    const url = new URL(response.url());
    if (url.origin === origin && url.pathname.startsWith('/assets/')) {
      loaded.add(url.pathname);
      if (response.status() !== 200) faults.push(`Asset ${url.pathname}: ${response.status()}`);
    }
  });
  page.on('requestfailed', request => {
    if (new URL(request.url()).pathname.startsWith('/assets/')) faults.push(`Asset request failed: ${new URL(request.url()).pathname}`);
  });
  await page.routeWebSocket('**/*', socket => {
    const url = new URL(socket.url());
    if (url.host !== new URL(origin).host || url.pathname !== `/api/ssh/${fixtureID}`) faults.push(`Unexpected WebSocket: ${url.pathname}`);
    // No connectToServer(): this socket terminates entirely in the test process.
    socket.send('fixture-terminal-ready\r\n');
  });
  await page.route('**/*', async route => {
    const request = route.request(), url = new URL(request.url());
    if (request.method() === 'GET' && url.origin === origin && url.pathname in fixtures) {
      return route.fulfill({ headers: { Date: new Date().toUTCString() }, json: fixtures[url.pathname] });
    }
    if (request.method() === 'GET' && url.origin === 'https://api.frankfurter.dev') return route.fulfill({ json: [] });
    if (request.method() === 'GET' && url.origin === origin &&
        (pagePaths.includes(url.pathname) || url.pathname.startsWith('/assets/') ||
         url.pathname === '/status-assets/anime-ops-guardian.jpg' || url.pathname === '/favicon.svg')) {
      // Strip credentials even if a future browser change attaches them to assets.
      const headers = { ...request.headers() };
      delete headers.authorization; delete headers.cookie;
      return route.continue({ headers });
    }
    faults.push(`Blocked unexpected request: ${request.method()} ${url.pathname}`);
    return route.abort('blockedbyclient');
  });
  return { faults, loaded };
}

test('published pages and nested lazy modules render with isolated business data', async ({ page, baseURL }) => {
  const state = await isolateBrowser(page, baseURL!);
  for (const [path, selector] of [['/login', '.auth-page'], ['/register', '.auth-register-page'], ['/status', '.probe-glass-page']]) {
    await page.goto(path);
    await expect(page.locator(selector)).toBeVisible();
  }
  await page.goto(`/servers/${fixtureID}`);
  await expect(page.locator('.server-detail-page')).toBeVisible();
  await expect(page.locator('.recharts-surface').first()).toBeVisible();
  await page.getByRole('tab', { name: en['terminal.title'], exact: true }).click();
  await expect(page.locator('.xterm-rows')).toContainText('fixture-terminal-ready');

  for (const item of privatePages) {
    await page.locator('.sidebar-navigation').getByRole('menuitem').filter({ hasText: item.menu }).click();
    await expect.poll(() => new URL(page.url()).pathname).toBe(item.path);
    if (item.selector) await expect(page.locator(item.selector)).toBeVisible();
    else await expect(page.getByRole('heading', { name: item.heading!, exact: true })).toBeVisible();
    if (item.path === '/files') {
      await page.getByRole('button', { name: 'fixture.yaml', exact: true }).click();
      await expect(page.locator('.cm-content')).toContainText('enabled: true');
      await expect(page.locator('.cm-line span[class]').first()).toBeVisible();
      await page.locator('.ant-modal-footer').getByRole('button', { name: en['files.close'], exact: true }).click();
    }
    if (item.path === '/ai') {
      await page.locator('.ai-history-item').filter({ hasText: 'Deployment fixture health' }).click();
      await expect(page.locator('.ai-process')).not.toHaveAttribute('open');
      await expect(page.locator('.ai-event-final')).toContainText('All fixture checks passed.');
      await expect(page.getByText('All fixture checks passed.', { exact: true })).toBeInViewport();
    }
    expect(state.faults, item.path).toEqual([]);
  }
  for (const prefix of ['charts', 'codemirror', 'xterm']) {
    expect([...state.loaded].some(path => new RegExp(`/assets/${prefix}-[^/]+\\.js$`).test(path)), `${prefix} lazy bundle`).toBe(true);
  }
  expect(state.faults).toEqual([]);
});
