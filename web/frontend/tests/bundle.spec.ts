import { test, expect } from '@playwright/test';
import { readdir, readFile } from 'node:fs/promises';

// The suite runs against dist/, so it can also guard the shape of the build:
// the editor, chart and terminal bundles are lazy by design and must stay out
// of the pages that never use them.
const dist = new URL('../dist/', import.meta.url);
// The deployment smoke job visits the published site without building dist/.
// Bundle topology is checked by the local build job before deployment.
test.skip(!!process.env.SMOKE_BASE_URL, 'Requires the local production build');
const EDITOR = /\/codemirror-[\w-]+\.js$/;
const CHARTS = /\/charts-[\w-]+\.js$/;
const TERMINAL = /\/xterm-[\w-]+\.js$/;

async function staticImports(file: string): Promise<string[]> {
  const code = await readFile(new URL(`assets/${file}`, dist), 'utf8');
  return [...code.matchAll(/from"(\.\/[^"]+\.js)"/g)].map(match => match[1]);
}

async function chunk(prefix: string): Promise<string> {
  const files = await readdir(new URL('assets/', dist));
  const match = files.find(file => file.startsWith(`${prefix}-`) && file.endsWith('.js'));
  expect(match, `${prefix} chunk`).toBeTruthy();
  return match!;
}

test('the entry page does not preload the editor, chart or terminal bundles', async () => {
  const html = await readFile(new URL('index.html', dist), 'utf8');
  const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^"]+\.js)"/g)].map(match => match[1]);
  expect(assets.length).toBeGreaterThan(0);
  expect(assets.filter(asset => EDITOR.test(asset) || CHARTS.test(asset) || TERMINAL.test(asset))).toEqual([]);

  const entry = assets.find(asset => /\/index-[\w-]+\.js$/.test(asset))!;
  const imports = await staticImports(entry.replace('/assets/', ''));
  expect(imports.filter(asset => EDITOR.test(asset) || CHARTS.test(asset) || TERMINAL.test(asset))).toEqual([]);
});

test('the dashboard and detail pages only load the heavy bundles they render', async () => {
  const dashboard = await staticImports(await chunk('Dashboard'));
  expect(dashboard.filter(asset => EDITOR.test(asset) || CHARTS.test(asset) || TERMINAL.test(asset))).toEqual([]);

  const detail = await staticImports(await chunk('ServerDetail'));
  expect(detail.some(asset => CHARTS.test(asset))).toBe(true);
  expect(detail.filter(asset => EDITOR.test(asset) || TERMINAL.test(asset))).toEqual([]);
});
