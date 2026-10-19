import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import { sampleFleet } from './fixtures/fleet';

async function fixture(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('lang', 'en');
    localStorage.setItem('theme', 'light');
    localStorage.setItem('token', 'navigation-fixture');
    localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Administrator-with-a-very-long-account-name' }));
  });
  await page.route('**/api.frankfurter.dev/**', route => route.fulfill({ json: [] }));
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    return route.fulfill({ json: [] });
  });
}

test('short desktop keeps every navigation item separate from its status footer', async ({ page }, testInfo) => {
  await fixture(page);
  await page.setViewportSize({ width: 1100, height: 600 });
  await page.goto('/credentials');
  const menu = page.locator('.sidebar-navigation');
  const settings = menu.getByRole('menuitem', { name: /Settings$/ });
  await settings.scrollIntoViewIfNeeded();
  await expect(settings).toBeInViewport({ ratio: 1 });
  const itemBox = (await settings.boundingBox())!;
  const footerBox = (await page.locator('.sidebar-footer').boundingBox())!;
  expect(itemBox.y + itemBox.height).toBeLessThanOrEqual(footerBox.y);
  expect(footerBox.y + footerBox.height).toBeLessThanOrEqual(600);
  await settings.click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('short-desktop.png'), fullPage: true, animations: 'disabled' });
});

test('phone navigation closes on Escape, backdrop and selection and restores focus', async ({ page }, testInfo) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/credentials');
  const open = page.getByRole('button', { name: 'Open navigation', exact: true });
  const navigation = page.getByRole('dialog', { name: 'Navigation', exact: true });
  await expect(page.getByRole('navigation')).not.toBeVisible();
  await open.click();
  await expect(navigation).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(navigation).not.toBeVisible();
  await expect(open).toBeFocused();
  await open.click();
  await expect(navigation).toBeVisible();
  await page.mouse.click(360, 400);
  await expect(navigation).not.toBeVisible();
  await open.click();
  await navigation.getByRole('menuitem', { name: /Settings$/ }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(navigation).not.toBeVisible();
  await expect(open).toHaveAttribute('aria-expanded', 'false');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await open.click();
  await expect(navigation).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('phone-navigation-dark.png'), animations: 'disabled' });
  await page.setViewportSize({ width: 1280, height: 720 });
  await expect(navigation).not.toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(navigation).not.toBeVisible();
});

test('failed alert refresh is not presented as healthy and can be retried', async ({ page }) => {
  await fixture(page);
  let failed = true;
  await page.route('**/api/alerts/summary', route => route.fulfill(failed
    ? { status: 503, json: { error: 'unavailable' } }
    : { json: { active: 0 } }));
  await page.goto('/credentials');
  const footer = page.locator('.sidebar-footer');
  await expect(footer).toContainText('Alert status unavailable');
  await expect(footer.locator('.sidebar-status-dot')).toHaveClass(/unknown/);
  failed = false;
  await footer.getByRole('button', { name: 'Refresh alert status' }).click();
  await expect(footer).toContainText('No active alerts');
  await expect(footer.locator('.sidebar-status-dot')).not.toHaveClass(/unknown/);
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await expect(footer).toContainText('Network disconnected');
  await expect(footer.locator('.sidebar-status-dot')).toHaveClass(/unknown/);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect(footer).toContainText('No active alerts');
});

test('late badge request cannot replace a newer navigation result', async ({ page }) => {
  await fixture(page);
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let requests = 0;
  await page.route('**/api/alerts/summary', async route => {
    const request = ++requests;
    if (request === 1) await gate;
    await route.fulfill({ json: { active: request === 1 ? 9 : 3 } });
  });
  await page.goto('/credentials');
  await expect.poll(() => requests).toBe(1);
  const canceled = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/alerts/summary');
  await page.getByRole('menuitem', { name: /Settings$/ }).click();
  await canceled;
  await expect(page.locator('.sidebar-footer')).toContainText('3 active alert(s)');
  release();
  await expect.poll(() => requests).toBe(2);
  await expect(page.locator('.sidebar-footer')).toContainText('3 active alert(s)');
});

test('phone navigation preserves the active-file transfer confirmation', async ({ page }) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route('**/api/servers', route => route.fulfill({ json: sampleFleet().slice(0, 1) }));
  await page.route('**/api/servers/*/files?**', route => route.fulfill({ json: {
    path: '/home/deploy', parent: '/home', truncated: false,
    entries: [{ name: 'config.txt', path: '/home/deploy/config.txt', size: 8, modified_at: '2026-10-19T08:00:00Z', mode: '-rw-r--r--', is_file: true, is_dir: false, is_symlink: false }],
  } }));
  await page.route('**/api/servers/*/files/metadata?**', route => route.fulfill({ json: { exists: false, version: '' } }));
  let upload: Route | undefined;
  await page.route('**/api/servers/*/files/upload?**', route => { upload = route; });
  await page.goto('/files?server=idle-host');
  await expect(page.getByRole('button', { name: 'config.txt', exact: true })).toBeVisible();
  const transfer = await page.evaluateHandle(() => {
    const data = new DataTransfer();
    data.items.add(new File(['upload'], 'upload.txt', { type: 'text/plain' }));
    return data;
  });
  await page.locator('.file-dropzone').dispatchEvent('drop', { dataTransfer: transfer });
  await expect.poll(() => !!upload).toBe(true);
  await page.getByRole('button', { name: 'Open navigation', exact: true }).click();
  await page.getByRole('dialog', { name: 'Navigation', exact: true }).getByRole('menuitem', { name: /Settings$/ }).click();
  const confirmation = page.getByRole('dialog', { name: 'Leave this file operation?', exact: true });
  await expect(confirmation).toBeVisible();
  await expect(page.getByRole('dialog', { name: 'Navigation', exact: true })).not.toBeVisible();
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page).toHaveURL(/\/files\?server=idle-host$/);
  await expect(page.getByRole('button', { name: 'Upload files', exact: true })).toBeDisabled();
  await upload!.fulfill({ json: { size: 6 } });
  await expect(page.getByRole('button', { name: 'Upload files', exact: true })).toBeEnabled();
});

test('loading a new page keeps the navigation and header available', async ({ page }) => {
  await fixture(page);
  await page.goto('/credentials');
  await expect(page.getByRole('heading', { name: 'Credentials', exact: true })).toBeVisible();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  await page.route(/\/assets\/Settings-[^/]+\.js$/, async route => {
    await gate;
    await route.continue();
  });
  await page.getByRole('menuitem', { name: /Settings$/ }).click();
  await expect(page.getByRole('status', { name: 'Loading...', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Navigation', exact: true })).toBeVisible();
  await expect(page.getByRole('banner')).toBeVisible();
  release();
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
});
