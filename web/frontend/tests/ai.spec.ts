import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import type { AIAction, AIProvider, AITask, AITaskDetail } from '../src/api/ai';
import { host, observedAt } from './fixtures/fleet';

const provider: AIProvider = { id: 'provider-one', name: 'My DeepSeek', kind: 'deepseek', base_url: 'https://api.deepseek.com', model: 'deepseek-flash', has_api_key: true, enabled: true, created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString() };
function task(id: string, status: AITask['status'] = 'completed'): AITask {
  return { id, server_id: 'server-one', server_name: 'Production web', server_type: 'linux', provider_id: provider.id, provider_name: provider.name, model: provider.model,
    prompt: `Task ${id}`, status, error: '', created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString(), prompt_tokens: 120, completion_tokens: 45 };
}
function action(overrides: Partial<AIAction> = {}): AIAction {
  return { id: 'action-one', tool_name: 'run_command', arguments: { command: 'rm -rf /srv/old-release' }, summary: 'Remove old release files', command: 'rm -rf /srv/old-release', requires_approval: true, risk_reason: 'Deletes existing files irreversibly', status: 'pending', output: '', error: '', truncated: false, unknown: false, duration_ms: 0, created_at: new Date(observedAt).toISOString(), ...overrides };
}
async function setup(page: Page) {
  const errors: string[] = [], writes: Array<{ path: string; method: string; body: Record<string, unknown> }> = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => { localStorage.setItem('lang', 'en'); localStorage.setItem('theme', 'light'); localStorage.setItem('token', 'fixture-token'); localStorage.setItem('user', JSON.stringify({ id: 'fixture', username: 'Monitor' })); });
  const state = { providers: [structuredClone(provider)], tasks: [task('history-one')], details: new Map<string, AITaskDetail>(), errors, writes, held: null as Route | null, holdCreate: false, holdRead: '', reads: [] as string[] };
  state.details.set('history-one', { task: state.tasks[0], events: [{ id: 1, kind: 'assistant', content: 'The requested service is configured.', created_at: new Date(observedAt).toISOString() }], actions: [] });
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    if (request.method() !== 'GET') state.writes.push({ path, method: request.method(), body: request.postData() ? request.postDataJSON() : {} });
    if (path === '/api/servers') return route.fulfill({ json: [host('server-one', { name: 'Production web' }), host('server-two', { name: 'Staging web' })] });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    if (path === '/api/ai/providers') {
      if (request.method() === 'POST') { const body = request.postDataJSON(); const { api_key, ...publicFields } = body; const saved = { ...provider, ...publicFields, id: 'provider-new', has_api_key: !!api_key }; state.providers.push(saved); return route.fulfill({ status: 201, json: saved }); }
      return route.fulfill({ json: state.providers });
    }
    if (path === '/api/ai/providers/provider-one') {
      if (request.method() === 'DELETE') { state.providers = []; return route.fulfill({ status: 204 }); }
      if (request.method() === 'PUT') { const { api_key: key, ...publicFields } = request.postDataJSON(); state.providers[0] = { ...state.providers[0], ...publicFields, has_api_key: !!key || state.providers[0].has_api_key }; return route.fulfill({ json: state.providers[0] }); }
    }
    if (path.endsWith('/test')) return route.fulfill({ json: { ok: true, message: 'Connection verified' } });
    if (path === '/api/ai/tasks') {
      if (request.method() === 'POST') {
        if (state.holdCreate) { state.held = route; return; }
        const body = request.postDataJSON(), created = { ...task('created-one', 'running'), ...body };
        state.details.set(created.id, { task: created, events: [{ id: 1, kind: 'user', content: body.prompt, created_at: created.created_at }], actions: [] });
        state.tasks = [created, ...state.tasks]; return route.fulfill({ status: 202, json: created });
      }
      return route.fulfill({ json: { tasks: state.tasks } });
    }
    const match = path.match(/^\/api\/ai\/tasks\/([^/]+)(.*)$/);
    if (match) {
      const [, id, rest] = match, detail = state.details.get(id);
      if (!detail) return route.fulfill({ status: 404, json: { error: 'task not found' } });
      if (!rest) { state.reads.push(id); if (state.holdRead === id) { state.held = route; return; } return route.fulfill({ json: { ...detail, reasoning_content: 'PRIVATE REASONING MUST NOT RENDER' } }); }
      if (rest === '/cancel') { detail.task = { ...detail.task, status: 'canceled' }; return route.fulfill({ json: detail.task }); }
      if (rest === '/messages') { detail.task = { ...detail.task, status: 'running' }; return route.fulfill({ status: 202, json: detail.task }); }
      if (rest.endsWith('/decision')) {
        const approved = request.postDataJSON().decision === 'approve'; detail.task = { ...detail.task, status: approved ? 'running' : 'canceled' };
        detail.actions = detail.actions.map(item => ({ ...item, status: approved ? 'approved' : 'rejected' }));
        return route.fulfill({ json: detail.task });
      }
    }
    // Every business API is mocked, including unrelated navigation destinations.
    return route.fulfill({ json: [] });
  });
  return state;
}

test('a task needs one owned server and submits immediately once without a planning confirmation', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai?server=foreign-server');
  const prompt = page.getByRole('textbox', { name: 'Task instructions' });
  await prompt.fill('Install and start Nginx');
  const start = page.getByRole('button', { name: 'Start task', exact: true });
  await expect(start).toBeDisabled();
  await page.getByRole('combobox', { name: 'Target server', exact: true }).click();
  await page.getByTitle('Production web', { exact: true }).click();
  await expect(page.getByText('Task content and command results from Production web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  state.holdCreate = true;
  await start.click();
  await start.dispatchEvent('click');
  await expect.poll(() => state.writes.filter(item => item.path === '/api/ai/tasks').length).toBe(1);
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const submitted = state.writes.find(item => item.path === '/api/ai/tasks')!.body;
  expect(submitted).toMatchObject({ server_id: 'server-one', provider_id: provider.id, prompt: 'Install and start Nginx' });
  expect(submitted.request_id).toMatch(/^[0-9a-f]{8}-[0-9a-f-]{27}$/i);
  const created = { ...task('created-one', 'running'), prompt: String(submitted.prompt) };
  state.details.set(created.id, { task: created, events: [], actions: [] });
  await state.held!.fulfill({ status: 202, json: created });
  await expect(page).toHaveURL(/task=created-one/);
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toBeVisible();
  expect(state.errors).toEqual([]);
});

test('a lost submission response retries the same request id without producing a second job', async ({ page }) => {
  const state = await setup(page); let requests = 0; const ids: string[] = [];
  state.details.set('recovered', { task: task('recovered', 'queued'), events: [], actions: [] });
  await page.route('**/api/ai/tasks', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    ids.push(route.request().postDataJSON().request_id); requests++;
    return requests === 1 ? route.abort('failed') : route.fulfill({ status: 202, json: task('recovered', 'queued') });
  });
  await page.goto('/ai?server=server-one');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Configure Nginx');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect(page.getByText(/submission response was lost/)).toBeVisible();
  await page.getByRole('button', { name: 'Retry same submission', exact: true }).click();
  await expect(page).toHaveURL(/task=recovered/);
  expect(requests).toBe(2); expect(ids[0]).toBe(ids[1]);
  expect(state.errors).toEqual([]);
});

for (const decision of ['approve', 'reject'] as const) {
  test(`a high-risk action can only ${decision} its saved command after exact-action confirmation`, async ({ page }) => {
    const state = await setup(page);
    state.details.set('danger', { task: task('danger', 'awaiting_approval'), events: [{ id: 1, kind: 'tool', action_id: 'action-one', content: 'saved action', created_at: new Date(observedAt).toISOString() }], actions: [action()] });
    await page.goto('/ai?task=danger');
    await expect(page.locator('.ai-action-card')).toHaveCount(1);
    await expect(page.locator('.ai-action-card')).toContainText('rm -rf /srv/old-release');
    await expect(page.getByText('PRIVATE REASONING MUST NOT RENDER')).toHaveCount(0);
    const label = decision === 'approve' ? 'Approve action' : 'Reject and stop';
    await page.getByRole('button', { name: label, exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText('Production web');
    await expect(dialog.locator('.ai-command')).toHaveText('rm -rf /srv/old-release');
    expect(state.writes.filter(item => item.path.endsWith('/decision'))).toHaveLength(0);
    await dialog.getByRole('button', { name: label, exact: true }).click();
    await expect.poll(() => state.writes.filter(item => item.path.endsWith('/decision')).length).toBe(1);
    expect(state.writes.find(item => item.path.endsWith('/decision'))).toMatchObject({ path: '/api/ai/tasks/danger/actions/action-one/decision', body: { decision } });
    expect(Object.keys(state.writes.find(item => item.path.endsWith('/decision'))!.body)).toEqual(['decision']);
    if (decision === 'reject') await expect(page.getByText('The task was canceled. Steps already executed were not automatically rolled back.')).toBeVisible();
    expect(state.errors).toEqual([]);
  });
}

test('routine actions show output and token usage; follow-up and cancellation use the existing task', async ({ page }) => {
  const state = await setup(page);
  state.details.set('finished', { task: task('finished'), events: [{ id: 1, kind: 'assistant', content: 'Nginx is running.', created_at: new Date(observedAt).toISOString() }], actions: [action({ requires_approval: false, status: 'succeeded', command: 'systemctl status nginx', summary: 'Check nginx', output: 'active (running)', exit_code: 0, duration_ms: 120, truncated: true })] });
  await page.goto('/ai?task=finished');
  await expect(page.locator('.ai-action-card')).toContainText('active (running)');
  await expect(page.getByText('Output was truncated by the server.')).toBeVisible();
  await expect(page.locator('.ai-task-usage')).toContainText('Input 120');
  await expect(page.getByRole('button', { name: 'Approve action', exact: true })).toHaveCount(0);
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Check the configuration');
  await page.getByRole('button', { name: 'Send follow-up', exact: true }).click();
  await expect.poll(() => state.writes.some(item => item.path === '/api/ai/tasks/finished/messages')).toBe(true);
  await page.getByRole('button', { name: 'Cancel task', exact: true }).click();
  await expect(page.getByText('The task was canceled. Steps already executed were not automatically rolled back.')).toBeVisible();
  expect(state.writes.filter(item => item.path === '/api/ai/tasks')).toHaveLength(0);
});

test('changing or leaving a task cancels pending reads but never cancels the background job', async ({ page }) => {
  const state = await setup(page);
  state.details.set('running', { task: task('running', 'running'), events: [], actions: [] });
  state.tasks.unshift(task('running', 'running'));
  await page.clock.install();
  await page.goto('/ai?task=running');
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toBeVisible();
  state.holdRead = 'running';
  await page.clock.fastForward(2100); await expect.poll(() => !!state.held).toBe(true);
  const failed = page.waitForEvent('requestfailed', request => request.url().endsWith('/ai/tasks/running'));
  await page.locator('.ai-history-item').filter({ hasText: 'Task history-one' }).click();
  await failed;
  await expect(page.getByText('The requested service is configured.', { exact: true })).toBeVisible();
  await state.held!.fulfill({ json: { task: task('running', 'running'), events: [{ id: 99, kind: 'assistant', content: 'Late old task', created_at: new Date(observedAt).toISOString() }], actions: [] } });
  await expect(page.getByText('Late old task')).toHaveCount(0);
  await page.getByRole('menuitem').filter({ hasText: 'Credentials' }).click();
  await page.clock.fastForward(10000);
  expect(state.writes.filter(item => item.path.endsWith('/cancel'))).toHaveLength(0);
  expect(state.errors).toEqual([]);
});

test('provider edits keep saved keys blank, require a new key for another endpoint, and never persist secrets in browser storage', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  const key = dialog.getByLabel('API key', { exact: true });
  await expect(key).toHaveValue('');
  await expect(key).toHaveAttribute('placeholder', 'Key saved — leave blank to keep it');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.writes.some(item => item.method === 'PUT' && item.body.api_key === '')).toBe(true);
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  await dialog.getByLabel('API base URL', { exact: true }).fill('https://compatible.example.test/v1');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect(dialog.getByText('This field is required', { exact: true })).toBeVisible();
  expect(state.writes.filter(item => item.method === 'PUT')).toHaveLength(1);
  await key.fill('test-new-secret-key');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.writes.filter(item => item.method === 'PUT').length).toBe(2);
  expect(state.writes.filter(item => item.method === 'PUT')[1].body).toMatchObject({ base_url: 'https://compatible.example.test/v1', api_key: 'test-new-secret-key' });
  expect(await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }))).not.toContain('test-new-secret-key');
  await dialog.getByRole('button', { name: 'Test connection: My DeepSeek', exact: true }).click();
  await expect(page.getByText('Provider connection verified', { exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Delete: My DeepSeek', exact: true }).click();
  const confirmation = page.getByRole('dialog').filter({ hasText: 'Delete this provider?' });
  await confirmation.getByRole('button', { name: 'OK', exact: true }).click();
  await expect.poll(() => state.providers.length).toBe(0);
  expect(state.errors).toEqual([]);
});

test('provider creation starts with DeepSeek defaults and allows a custom compatible model without leaking a hidden key', async ({ page }) => {
  const state = await setup(page); state.providers = [];
  await page.goto('/ai?server=server-one');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Add provider' }).click();
  await expect(dialog.getByLabel('API base URL', { exact: true })).toHaveValue('https://api.deepseek.com');
  await expect(dialog.getByLabel('Model', { exact: true })).toHaveValue('deepseek-flash');
  const enabled = dialog.getByRole('switch', { name: 'Enabled', exact: true });
  await expect(enabled).toBeChecked();
  await dialog.locator('.ant-form-item-label label').filter({ hasText: /^Enabled$/ }).click();
  await expect(enabled).toBeChecked();
  await dialog.getByLabel('API key', { exact: true }).fill('discard-deepseek-key');
  const kind = dialog.getByLabel('Provider type', { exact: true });
  await kind.click();
  await page.getByTitle('Compatible API', { exact: true }).click();
  await expect(dialog.getByLabel('API key', { exact: true })).toHaveValue('');
  await dialog.getByLabel('Display name', { exact: true }).fill('Private compatible');
  await dialog.getByLabel('API base URL', { exact: true }).fill('http://compatible.example.test/v1');
  await dialog.getByLabel('Model', { exact: true }).fill('custom-model-2026');
  await dialog.getByLabel('API key', { exact: true }).fill('new-compatible-key');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect(dialog.getByText('Enter an HTTPS API address without credentials, query parameters or a fragment', { exact: true })).toBeVisible();
  expect(state.writes.filter(item => item.method === 'POST' && item.path === '/api/ai/providers')).toHaveLength(0);
  await dialog.getByLabel('API base URL', { exact: true }).fill('https://compatible.example.test/v1');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.writes.some(item => item.path === '/api/ai/providers' && item.method === 'POST')).toBe(true);
  expect(state.writes.find(item => item.path === '/api/ai/providers' && item.method === 'POST')!.body).toMatchObject({ kind: 'compatible', model: 'custom-model-2026', api_key: 'new-compatible-key', enabled: true });
  expect(state.writes.map(item => JSON.stringify(item.body)).join('')).not.toContain('discard-deepseek-key');
  expect(state.errors).toEqual([]);
});

test('the workspace fits a phone in both themes and unknown task ids never expose the previous task', async ({ page }, testInfo) => {
  const state = await setup(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/ai?server=server-one');
  await expect(page.getByRole('heading', { name: 'What would you like to do?' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('ai-mobile-light.png'), fullPage: true, animations: 'disabled' });
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('ai-mobile-dark.png'), fullPage: true, animations: 'disabled' });
  await page.goto('/ai?task=foreign-task');
  await expect(page.getByText('This task is unavailable or you do not have access to it.', { exact: true })).toBeVisible();
  await expect(page.getByText('The requested service is configured.')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toHaveCount(0);
  expect(state.errors).toEqual([]);
});
