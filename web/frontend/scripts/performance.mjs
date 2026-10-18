// Run against a production preview: node scripts/performance.mjs http://127.0.0.1:4174
// Every API is intercepted; this never connects to managed hosts.
import { chromium, expect } from '@playwright/test';

const baseURL = process.argv[2] || 'http://127.0.0.1:4174';
const browser = await chromium.launch({ headless: true });
const results = { measuredAt: new Date().toISOString(), dashboard: [], editor: null };
async function pageWithAuth() {
  const page = await browser.newPage();
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('token', 'performance-fixture');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Benchmark' }));
    window.largeTextBytes = 0;
    const encode = TextEncoder.prototype.encode;
    TextEncoder.prototype.encode = function (text) {
      if (text?.length > 1024 * 1024) window.largeTextBytes += text.length;
      return encode.call(this, text);
    };
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  return page;
}
const metric = () => ({ recorded_at: new Date().toISOString(), cpu_percent: 10, memory_used: 1024,
  memory_total: 4096, disk_used: 1024, network_rx_bytes: 0, network_tx_bytes: 0, disk_rx_bytes: 0,
  disk_tx_bytes: 0, load_1: 0, load_5: 0, load_15: 0, uptime_seconds: 10, latency_ms: 1 });
const host = id => ({ id, name: id, host: '192.0.2.1', port: 22, ssh_username: 'monitor', server_type: 'linux',
  cpu_cores: 2, memory_total: 4096, disk_total: 8192, tags: [], latest_metrics: metric() });
const taskTime = async session => (await session.send('Performance.getMetrics')).metrics.find(m => m.name === 'TaskDuration').value;
try {
  for (const count of [100, 500, 1000]) {
    const page = await pageWithAuth();
    const hosts = Array.from({ length: count }, (_, i) => host(`host-${i}`));
    let reads = 0;
    await page.route('**/api/**', route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/servers') {
        if (++reads > 1) return; // Observe clock work independently of network/data refresh.
        return route.fulfill({ json: hosts });
      }
      if (path === '/api/servers/uptime') return route.fulfill({ json: { servers: [] } });
      if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0, servers: [] } });
      return route.fulfill({ json: [] });
    });
    await page.goto(baseURL + '/dashboard');
    await expect(page.locator('.server-card')).toHaveCount(count, { timeout: 30000 });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const session = await page.context().newCDPSession(page);
    await session.send('Performance.enable');
    const before = await taskTime(session);
    await page.waitForTimeout(5500);
    results.dashboard.push({ hosts: count, observationMs: 5500, mainThreadMs: Math.round(((await taskTime(session)) - before) * 1000) });
    await page.close();
  }
  const page = await pageWithAuth();
  const content = '0123456789abcdef'.repeat(127) + '\n';
  const text = content.repeat(Math.floor((2 * 1024 * 1024 - 1024) / content.length));
  const entry = { name: 'large.txt', path: '/tmp/large.txt', size: text.length, modified_at: new Date().toISOString(),
    mode: '-rw-r--r--', is_file: true, is_dir: false, is_symlink: false };
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/servers') return route.fulfill({ json: [host('editor')] });
    if (path.endsWith('/files/text')) return route.fulfill({ json: { content: text, revision: 'fixture' } });
    if (path.endsWith('/files')) return route.fulfill({ json: { path: '/tmp', parent: '/', entries: [entry], truncated: false } });
    return route.fulfill({ json: [] });
  });
  await page.goto(baseURL + '/files?server=editor');
  await page.getByRole('button', { name: 'large.txt', exact: true }).click();
  await expect(page.locator('.cm-content')).toBeVisible();
  await page.locator('.cm-content').focus();
  await page.keyboard.press('Control+Home');
  const session = await page.context().newCDPSession(page);
  await session.send('Performance.enable');
  await page.evaluate(() => { window.largeTextBytes = 0; });
  const before = await taskTime(session);
  const started = performance.now();
  for (let i = 0; i < 20; i++) await page.keyboard.insertText('x');
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  results.editor = { initialBytes: text.length, insertedCharacters: 20, elapsedMs: Math.round(performance.now() - started),
    mainThreadMs: Math.round(((await taskTime(session)) - before) * 1000), largeTextBytesEncoded: await page.evaluate(() => window.largeTextBytes) };
  await page.close();
  console.log(JSON.stringify(results, null, 2));
} finally { await browser.close(); }
