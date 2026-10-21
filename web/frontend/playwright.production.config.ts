import { defineConfig } from '@playwright/test';

const target = process.env.SMOKE_BASE_URL;
if (!target) throw new Error('Production smoke requires an explicit SMOKE_BASE_URL');
const url = new URL(target);
const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
if ((url.protocol !== 'https:' && !(loopback && url.protocol === 'http:')) ||
    url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
  throw new Error('SMOKE_BASE_URL must be an HTTPS origin (HTTP is allowed only for loopback testing)');
}

export default defineConfig({
  testDir: './tests-production',
  outputDir: './test-results-production',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  timeout: 90000,
  globalTimeout: 180000,
  expect: { timeout: 10000 },
  use: {
    baseURL: url.origin,
    browserName: 'chromium',
    actionTimeout: 10000,
    navigationTimeout: 20000,
    serviceWorkers: 'block',
    // The browser only sees fixtures. Real API checks never record bodies in traces.
    trace: 'off',
    screenshot: 'only-on-failure',
  },
  reporter: process.env.CI ? [['list'], ['github']] : [['list']],
});
