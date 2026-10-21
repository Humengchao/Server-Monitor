import { test, expect } from '@playwright/test';
import type { Page, Route } from '@playwright/test';
import type { AIAction, AIPlugin, AIProvider, AISkill, AITask, AITaskDetail } from '../src/api/ai';
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
  const state = { providers: [structuredClone(provider)], skills: [] as AISkill[], plugins: [] as AIPlugin[], modelReads: [] as string[], tasks: [task('history-one')], details: new Map<string, AITaskDetail>(), errors, writes, held: null as Route | null, holdCreate: false, holdRead: '', reads: [] as string[] };
  state.details.set('history-one', { task: state.tasks[0], events: [{ id: 1, kind: 'assistant', content: 'The requested service is configured.', created_at: new Date(observedAt).toISOString() }], actions: [] });
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname;
    if (request.method() !== 'GET') state.writes.push({ path, method: request.method(), body: request.postData() ? request.postDataJSON() : {} });
    if (path === '/api/servers') return route.fulfill({ json: [host('server-one', { name: 'Production web' }), host('server-two', { name: 'Staging web' })] });
    if (path === '/api/alerts/summary') return route.fulfill({ json: { active: 0 } });
    if (path.endsWith('/models')) { state.modelReads.push(path); return route.fulfill({ json: { models: [{ id: 'discovered-model-one' }, { id: 'discovered-model-two' }], fetched_at: new Date(observedAt).toISOString(), source: 'provider' } }); }
    if (path === '/api/ai/skills') {
      if (request.method() === 'POST') { const saved = { ...request.postDataJSON(), id: 'skill-one', created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString() }; state.skills.push(saved); return route.fulfill({ status: 201, json: saved }); }
      return route.fulfill({ json: state.skills });
    }
    if (path.startsWith('/api/ai/skills/')) {
      const id = path.split('/').at(-1);
      if (request.method() === 'DELETE') { state.skills = state.skills.filter(skill => skill.id !== id); return route.fulfill({ status: 204 }); }
      const skill = state.skills.find(skill => skill.id === id); if (skill) Object.assign(skill, request.postDataJSON()); return route.fulfill({ json: skill });
    }
    if (path === '/api/ai/plugins') {
      if (request.method() === 'POST') { const { bearer_token, ...fields } = request.postDataJSON(); const saved = { ...fields, id: 'plugin-one', has_bearer_token: !!bearer_token, created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString() }; state.plugins.push(saved); return route.fulfill({ status: 201, json: saved }); }
      return route.fulfill({ json: state.plugins });
    }
    if (path.startsWith('/api/ai/plugins/') && !path.endsWith('/test') && !path.endsWith('/tools')) {
      const id = path.split('/').at(-1);
      if (request.method() === 'DELETE') { state.plugins = state.plugins.filter(plugin => plugin.id !== id); return route.fulfill({ status: 204 }); }
      const plugin = state.plugins.find(plugin => plugin.id === id); if (plugin) { const { bearer_token, ...fields } = request.postDataJSON(); Object.assign(plugin, fields, { has_bearer_token: !!bearer_token || plugin.has_bearer_token }); } return route.fulfill({ json: plugin });
    }
    if (path === '/api/ai/providers') {
      if (request.method() === 'POST') { const body = request.postDataJSON(); const { api_key, ...publicFields } = body; const saved = { ...provider, ...publicFields, id: 'provider-new', has_api_key: !!api_key }; state.providers.push(saved); return route.fulfill({ status: 201, json: saved }); }
      return route.fulfill({ json: state.providers });
    }
    if (path === '/api/ai/providers/provider-one') {
      if (request.method() === 'DELETE') { state.providers = []; return route.fulfill({ status: 204 }); }
      if (request.method() === 'PUT') { const { api_key: key, ...publicFields } = request.postDataJSON(); state.providers[0] = { ...state.providers[0], ...publicFields, has_api_key: !!key || state.providers[0].has_api_key }; return route.fulfill({ json: state.providers[0] }); }
    }
    if (path.endsWith('/test')) return route.fulfill({ json: { ok: true, message: 'Connection verified', tool_count: 2 } });
    if (path === '/api/ai/tasks') {
      if (request.method() === 'POST') {
        if (state.holdCreate) { state.held = route; return; }
        const body = request.postDataJSON(), created = { ...task('created-one', 'running'), ...body, skills: state.skills.filter(skill => body.skill_ids?.includes(skill.id)).map(({ id, name }) => ({ id, name })), plugins: state.plugins.filter(plugin => body.plugin_ids?.includes(plugin.id)).map(({ id, name }) => ({ id, name })) };
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
  await expect(page.getByText('Your task, monitoring data and command results from Production web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  state.holdCreate = true;
  await start.click();
  await start.dispatchEvent('click');
  await expect.poll(() => state.writes.filter(item => item.path === '/api/ai/tasks').length).toBe(1);
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const submitted = state.writes.find(item => item.path === '/api/ai/tasks')!.body;
  expect(submitted).toMatchObject({ scope: 'server', server_id: 'server-one', provider_id: provider.id, prompt: 'Install and start Nginx' });
  expect(submitted.request_id).toMatch(/^[0-9a-f]{8}-[0-9a-f-]{27}$/i);
  const created = { ...task('created-one', 'running'), prompt: String(submitted.prompt) };
  state.details.set(created.id, { task: created, events: [], actions: [] });
  await state.held!.fulfill({ status: 202, json: created });
  await expect(page).toHaveURL(/task=created-one/);
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toBeVisible();
  expect(state.errors).toEqual([]);
});

for (const viewport of [{ width: 1366, height: 768 }, { width: 390, height: 600 }]) {
  test(`Enter sends once, Shift+Enter inserts a line, and IME confirmation never sends at ${viewport.width}px`, async ({ page }) => {
    const state = await setup(page);
    await page.setViewportSize(viewport);
    await page.goto('/ai');
    const input = page.getByRole('textbox', { name: 'Task instructions' });
    await expect(page.getByRole('button', { name: 'Start task', exact: true })).toBeDisabled();
    await input.press('Enter');
    expect(state.writes).toHaveLength(0);
    await input.fill('Check services');
    await input.press('Shift+Enter');
    await input.press('a');
    await expect(input).toHaveValue('Check services\na');
    await input.dispatchEvent('compositionstart');
    await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter' });
    await input.dispatchEvent('compositionend');
    await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', isComposing: true });
    await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 229 });
    await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', repeat: true });
    expect(state.writes).toHaveLength(0);
    state.holdCreate = true;
    await input.press('Enter');
    await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter' });
    await expect.poll(() => state.writes.filter(item => item.path === '/api/ai/tasks').length).toBe(1);
    expect(state.writes[0].body.prompt).toBe('Check services\na');
    const created = task('keyboard', 'completed');
    state.details.set(created.id, { task: created, events: [{ id: 1, kind: 'assistant', phase: 'final', content: 'Services verified.', created_at: created.created_at }], actions: [] });
    await state.held!.fulfill({ status: 202, json: created });
    await expect(page).toHaveURL(/task=keyboard/);
    await input.fill('检查内存');
    await input.press('Enter');
    await expect.poll(() => state.writes.filter(item => item.path.endsWith('/messages')).length).toBe(1);
    expect(state.writes.find(item => item.path.endsWith('/messages'))?.body.message).toBe('检查内存');
    expect(state.errors).toEqual([]);
  });
}

test('Enter cannot submit a draft without its required server', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai?scope=server');
  const input = page.getByRole('textbox', { name: 'Task instructions' });
  await input.fill('Read logs');
  await input.press('Enter');
  await expect(input).toHaveValue('Read logs');
  expect(state.writes).toHaveLength(0);
});

test('each turn collapses progress and command output while keeping results and user messages visible', async ({ page }, testInfo) => {
  const state = await setup(page);
  const selected = task('compact-conversation', 'running');
  const at = selected.created_at;
  const detail: AITaskDetail = { task: selected, events: [
    { id: 1, kind: 'user', content: 'Check service health', created_at: at },
    { id: 2, kind: 'assistant', phase: 'progress', content: 'First I will inspect the service state.', created_at: at },
    { id: 3, kind: 'tool', action_id: 'action-one', content: 'internal action record', created_at: at },
    { id: 4, kind: 'tool', action_id: 'action-one', content: 'duplicate completion record', created_at: at },
    { id: 5, kind: 'assistant', phase: 'final', content: '## Health result\n\nAll services are healthy.', created_at: at },
    { id: 6, kind: 'user', content: 'Check disk next', created_at: at },
    { id: 7, kind: 'assistant', phase: 'progress', content: 'Now checking available disk capacity.', created_at: at },
  ], actions: [action({ requires_approval: false, status: 'succeeded', command: 'systemctl status nginx', summary: 'Read service health', output: 'active (running)', exit_code: 0 })] };
  state.details.set(selected.id, detail);
  await page.goto('/ai?task=compact-conversation');
  const turns = page.locator('.ai-conversation-turn');
  await expect(turns).toHaveCount(2);
  await expect(page.getByText('Check service health', { exact: true })).toBeVisible();
  await expect(page.getByText('Check disk next', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Health result', exact: true })).toBeVisible();
  await expect(turns.first().locator('.ai-process')).not.toHaveAttribute('open');
  await expect(turns.last().locator('.ai-process')).not.toHaveAttribute('open');
  await expect(page.locator('.ai-action-card')).toHaveCount(1);
  await expect(page.locator('.ai-command')).not.toBeVisible();
  await expect(turns.first().getByText('First I will inspect the service state.', { exact: true })).not.toBeVisible();
  await expect(turns.last().locator('.ai-process-status')).toContainText('Now checking available disk capacity.');
  await expect(turns.last().locator('.ai-event-final')).toHaveCount(0);
  await expect(page.getByText('PRIVATE REASONING MUST NOT RENDER')).toHaveCount(0);
  await turns.first().locator('.ai-process > summary').click();
  await expect(page.locator('.ai-command')).toBeVisible();
  await page.locator('.ai-action-output summary').click();
  await expect(page.locator('.ai-action-output pre')).toHaveText('active (running)');
  detail.task.status = 'completed';
  detail.events.push({ id: 8, kind: 'assistant', phase: 'final', content: 'Disk capacity is sufficient.', created_at: at });
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.getByText('Disk capacity is sufficient.', { exact: true })).toBeVisible();
  await expect(turns.first().locator('.ai-process')).toHaveAttribute('open', '');
  await expect(turns.last().locator('.ai-process')).not.toHaveAttribute('open');
  await expect(page.locator('.ai-action-output pre')).toBeVisible();
  await turns.first().locator('.ai-process > summary').click();
  await page.reload();
  await expect(page.getByText('Disk capacity is sufficient.', { exact: true })).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath('ai-compact-results.png'), fullPage: true, animations: 'disabled' });
  await page.setViewportSize({ width: 320, height: 600 });
  await page.reload();
  await expect(page.getByText('Disk capacity is sufficient.', { exact: true })).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath('ai-compact-results-mobile.png'), fullPage: true, animations: 'disabled' });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(state.errors).toEqual([]);
});

test('pending approval stays outside collapsed details and failure or unknown outcomes remain clear', async ({ page }) => {
  const state = await setup(page);
  const selected = task('compact-approval', 'awaiting_approval');
  const detail: AITaskDetail = { task: selected, events: [
    { id: 1, kind: 'user', content: 'Update the application', created_at: selected.created_at },
    { id: 2, kind: 'assistant', phase: 'progress', content: 'The old files need removal first.', created_at: selected.created_at },
  ], actions: [action()] };
  state.details.set(selected.id, detail);
  await page.goto('/ai?task=compact-approval');
  await expect(page.locator('.ai-process')).not.toHaveAttribute('open');
  await expect(page.getByRole('button', { name: 'Approve action', exact: true })).toBeVisible();
  await expect(page.locator('.ai-command')).toBeVisible();
  await expect(page.locator('.ai-process .ai-approval-buttons')).toHaveCount(0);
  detail.task = { ...detail.task, status: 'interrupted', error: 'The connection was interrupted.' };
  detail.actions[0] = { ...detail.actions[0], status: 'interrupted', unknown: true, error: 'Connection interrupted' };
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.getByText('The connection was interrupted.', { exact: true })).toBeVisible();
  await expect(page.locator('.ai-conversation-turn > .ant-alert')).toContainText('Execution outcome is unknown');
  await expect(page.locator('.ai-process-warning')).toHaveText('1 unsuccessful');
  await expect(page.locator('.ai-process')).not.toHaveAttribute('open');
  await expect(page.locator('.ai-event-final')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Approve action', exact: true })).toHaveCount(0);
  expect(state.errors).toEqual([]);
});

test('a legacy completed turn keeps its answer visible when a new user message starts another turn', async ({ page }) => {
  const state = await setup(page);
  const selected = task('legacy-turns', 'awaiting_input');
  state.details.set(selected.id, { task: selected, events: [
    { id: 1, kind: 'user', content: 'Inspect memory', created_at: selected.created_at },
    { id: 2, kind: 'assistant', content: 'I will check memory.', created_at: selected.created_at },
    { id: 3, kind: 'tool', action_id: 'action-one', content: 'Read result', created_at: selected.created_at },
    { id: 4, kind: 'assistant', content: 'Memory usage is normal.', created_at: selected.created_at },
    { id: 5, kind: 'user', content: 'Check another server', created_at: selected.created_at },
    { id: 6, kind: 'assistant', content: 'Which server should I inspect?', created_at: selected.created_at },
  ], actions: [action({ requires_approval: false, status: 'succeeded', summary: 'Read memory', command: 'free -h' })] });
  await page.goto('/ai?task=legacy-turns');
  await expect(page.getByText('Memory usage is normal.', { exact: true })).toBeVisible();
  await expect(page.getByText('Which server should I inspect?', { exact: true })).toBeVisible();
  await expect(page.getByText('I will check memory.', { exact: true })).not.toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toBeVisible();
  expect(state.errors).toEqual([]);
});

test('legacy progress followed by a same-timestamp action is not presented as a final result', async ({ page }) => {
  const state = await setup(page);
  const selected = task('legacy-no-result');
  const detail: AITaskDetail = { task: selected, events: [
    { id: 1, kind: 'user', content: 'Inspect the server', created_at: selected.created_at },
    { id: 2, kind: 'assistant', content: 'I will inspect the server now.', created_at: selected.created_at },
    { id: 3, kind: 'tool', action_id: 'action-one', content: 'Read result', created_at: selected.created_at },
  ], actions: [action({ requires_approval: false, status: 'succeeded', summary: 'Inspect server', command: 'uname -a' })] };
  state.details.set(selected.id, detail);
  await page.goto('/ai?task=legacy-no-result');
  await expect(page.locator('.ai-process')).not.toHaveAttribute('open');
  await expect(page.locator('.ai-event-final')).toHaveCount(0);
  await expect(page.locator('.ai-process .ai-event-assistant')).toContainText('I will inspect the server now.');
  detail.events.push(
    { id: 4, kind: 'user', content: 'Check memory too', created_at: selected.created_at },
    { id: 5, kind: 'assistant', content: 'Memory is healthy.', created_at: selected.created_at },
  );
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.locator('.ai-conversation-turn')).toHaveCount(2);
  await expect(page.locator('.ai-conversation-turn').first().locator('.ai-event-final')).toHaveCount(0);
  await expect(page.locator('.ai-conversation-turn').last().locator('.ai-event-final')).toHaveText(/Memory is healthy\./);
  expect(state.errors).toEqual([]);
});

for (const width of [1366, 320]) {
  test(`completed conversations open at the latest result's beginning at ${width}px`, async ({ page }, testInfo) => {
    const state = await setup(page);
    await page.setViewportSize({ width, height: 720 });
    const selected = task('result-position');
    const content = '## Health report\n\nThe services are healthy.\n\n' + Array.from({ length: 25 }, (_, i) => `### Detail ${i + 1}\n\nService status and capacity are verified.\n\n`).join('');
    state.details.set(selected.id, { task: selected, events: [
      { id: 1, kind: 'user', content: 'Review server health and resource usage', created_at: selected.created_at },
      { id: 2, kind: 'assistant', phase: 'progress', content: 'Inspecting services and resource metrics.', created_at: selected.created_at },
      { id: 3, kind: 'assistant', phase: 'final', content, created_at: selected.created_at },
    ], actions: [] });
    await page.goto('/ai?task=result-position');
    await expect(page.getByRole('heading', { name: 'Health report', exact: true })).toBeInViewport();
    await expect(page.getByText('The services are healthy.', { exact: true })).toBeInViewport({ ratio: 1 });
    const conversation = page.getByRole('region', { name: 'Task conversation', exact: true });
    expect(await conversation.evaluate(element => element.scrollTop)).toBeGreaterThan(40);
    expect(await conversation.evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight)).toBeGreaterThan(500);
    const input = page.getByRole('textbox', { name: 'Task instructions' });
    await input.fill('Next request');
    await expect(input).toBeFocused();
    await page.screenshot({ path: testInfo.outputPath(`ai-result-start-${width}.png`), fullPage: true, animations: 'disabled' });
    expect(await page.evaluate(() => document.documentElement.scrollTop)).toBe(0);
    await conversation.evaluate(element => element.scrollTo(0, 0));
    await page.evaluate(() => { window.history.pushState({}, '', '/ai'); window.dispatchEvent(new PopStateEvent('popstate')); });
    await expect(page.getByRole('heading', { name: 'What would you like to do?', exact: true })).toBeVisible();
    await page.evaluate(() => { window.history.pushState({}, '', '/ai?task=result-position'); window.dispatchEvent(new PopStateEvent('popstate')); });
    await expect(page.getByText('The services are healthy.', { exact: true })).toBeInViewport({ ratio: 1 });
    expect(state.errors).toEqual([]);
  });
}

for (const reading of ['following', 'history', 'expanded'] as const) {
  test(`a newly completed result respects ${reading} conversation reading`, async ({ page }) => {
    const state = await setup(page);
    await page.clock.install();
    await page.setViewportSize({ width: 320, height: 720 });
    const selected = task(`live-${reading}`, 'running');
    const detail: AITaskDetail = { task: selected, events: [
      { id: 1, kind: 'user', content: 'First request', created_at: selected.created_at },
      { id: 2, kind: 'assistant', phase: 'final', content: '## Previous report\n\n' + 'Existing report text.\n\n'.repeat(20), created_at: selected.created_at },
      { id: 3, kind: 'user', content: 'Inspect services now', created_at: selected.created_at },
      { id: 4, kind: 'assistant', phase: 'progress', content: 'Inspecting current service state.', created_at: selected.created_at },
    ], actions: [] };
    state.details.set(selected.id, detail);
    await page.goto(`/ai?task=${selected.id}`);
    const body = page.getByRole('region', { name: 'Task conversation', exact: true });
    const process = page.locator('.ai-conversation-turn').last().locator('.ai-process');
    await expect(process.locator('summary')).toBeInViewport();
    if (reading === 'history') {
      await body.evaluate(element => element.scrollTo(0, 0));
      await expect.poll(() => body.evaluate(element => element.scrollTop)).toBe(0);
    } else if (reading === 'expanded') {
      await process.locator('summary').click();
      await expect(process).toHaveAttribute('open', '');
    }
    await body.focus();
    const before = await body.evaluate(element => element.scrollTop);
    detail.task = { ...detail.task, status: 'completed' };
    detail.events.push({ id: 5, kind: 'assistant', phase: 'final', content: '## Current report\n\nThe current service check succeeded.\n\n' + 'Additional details.\n\n'.repeat(20), created_at: selected.created_at });
    await page.clock.runFor(2100);
    await expect(page.getByRole('heading', { name: 'Current report', exact: true })).toBeVisible();
    await expect(body).toBeFocused();
    if (reading === 'following') {
      await expect(page.getByRole('heading', { name: 'Current report', exact: true })).toBeInViewport();
      await expect(page.getByText('The current service check succeeded.', { exact: true })).toBeInViewport({ ratio: 1 });
    } else {
      expect(Math.abs((await body.evaluate(element => element.scrollTop)) - before)).toBeLessThanOrEqual(2);
      await expect(page.getByText('The current service check succeeded.', { exact: true })).not.toBeInViewport();
      if (reading === 'expanded') await expect(process).toHaveAttribute('open', '');
    }
    expect(state.errors).toEqual([]);
  });
}

test('the entire panel starts without choosing a server and keeps its scope for follow-ups', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai');
  await expect(page.getByRole('radio', { name: 'Entire panel', exact: true })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('radio', { name: /Standard/ })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('combobox', { name: 'Target server', exact: true })).toHaveCount(0);
  await expect(page.getByText('Your task and monitoring data from this account will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Analyze all servers and prioritize current risks', exact: true }).click();
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect(page).toHaveURL(/task=created-one/);
  const submitted = state.writes.find(item => item.path === '/api/ai/tasks')!.body;
  expect(submitted).toMatchObject({ scope: 'fleet', approval_mode: 'standard', provider_id: provider.id });
  expect(submitted).not.toHaveProperty('server_id');
  const header = page.locator('.ai-task-header');
  await expect(header.getByText('Server Monitor / All servers', { exact: true })).toBeVisible();
  await expect(header.getByText('linux', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('radiogroup', { name: 'Task scope', exact: true })).toHaveCount(0);
  await expect(header.getByRole('link', { name: 'Alert rules', exact: true })).toHaveAttribute('href', '/alerts');
  state.details.get('created-one')!.task.status = 'completed';
  await header.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Check the 24-hour history for the busiest server');
  await page.getByRole('button', { name: 'Send follow-up', exact: true }).click();
  expect(state.writes.find(item => item.path.endsWith('/messages'))?.body).toMatchObject({ message: 'Check the 24-hour history for the busiest server' });
  await page.getByRole('button', { name: 'New task', exact: true }).click();
  await expect(page.getByRole('radio', { name: 'Entire panel', exact: true })).toHaveAttribute('aria-checked', 'true');
  await expect(page.getByRole('combobox', { name: 'Target server', exact: true })).toHaveCount(0);
  expect(state.errors).toEqual([]);
});

test('switching to single-server scope requires an owned target before submitting', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Read the monitoring history and install Nginx');
  await page.getByRole('radio', { name: 'Single server', exact: true }).click();
  await expect(page).toHaveURL(/scope=server/);
  await expect(page.getByRole('button', { name: 'Start task', exact: true })).toBeDisabled();
  await page.getByRole('combobox', { name: 'Target server', exact: true }).click();
  await page.getByTitle('Staging web', { exact: true }).click();
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path === '/api/ai/tasks')?.body.scope).toBe('server');
  expect(state.writes.find(item => item.path === '/api/ai/tasks')?.body).toMatchObject({ server_id: 'server-two', prompt: 'Read the monitoring history and install Nginx' });
  expect(state.errors).toEqual([]);
});

test('a lost fleet submission stays isolated from a single-server draft and retries its original scope', async ({ page }) => {
  const state = await setup(page);
  let requests = 0;
  await page.route('**/api/ai/tasks', route => {
    if (route.request().method() !== 'POST') return route.fallback();
    requests++;
    if (requests === 1) { state.writes.push({ path: '/api/ai/tasks', method: 'POST', body: route.request().postDataJSON() }); return route.abort('failed'); }
    return route.fallback();
  });
  await page.goto('/ai');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Analyze monitoring for all servers');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Retry same submission', exact: true })).toBeVisible();
  await page.evaluate(() => { window.history.pushState({}, '', '/ai?server=server-one'); window.dispatchEvent(new PopStateEvent('popstate')); });
  await expect(page.getByRole('button', { name: 'Retry same submission', exact: true })).toHaveCount(0);
  await expect(page.getByRole('radio', { name: 'Single server', exact: true })).toHaveAttribute('aria-checked', 'true');
  await page.getByRole('radio', { name: 'Entire panel', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toHaveValue('Analyze monitoring for all servers');
  await page.getByRole('button', { name: 'Retry same submission', exact: true }).click();
  await expect(page).toHaveURL(/task=created-one/);
  const attempts = state.writes.filter(item => item.path === '/api/ai/tasks');
  expect(attempts).toHaveLength(2);
  expect(attempts[1].body).toEqual(attempts[0].body);
  expect(attempts[1].body.scope).toBe('fleet');
  expect(attempts[1].body).not.toHaveProperty('server_id');
  expect(state.errors).toEqual([]);
});

test('fleet alert approval shows the panel target and sends only the saved action decision', async ({ page }) => {
  const state = await setup(page);
  const selected = { ...task('fleet-alert', 'awaiting_approval'), scope: 'fleet' as const, server_id: '00000000-0000-0000-0000-000000000000', server_name: 'Server Monitor', server_type: 'panel' };
  state.details.set(selected.id, { task: selected, events: [], actions: [action({ tool_name: 'monitor_delete_alert', command: '', summary: 'Delete obsolete CPU alert', arguments: { rule_id: 'saved-rule-id' }, risk_reason: 'Deletes an alert rule' })] });
  await page.goto('/ai?task=fleet-alert');
  await page.getByRole('button', { name: 'Approve action', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText('This approval applies only to the saved action shown below on Server Monitor / All servers.', { exact: true })).toBeVisible();
  await expect(dialog.getByText(/saved-rule-id/)).toBeVisible();
  await dialog.getByRole('button', { name: 'Approve action', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path.endsWith('/decision'))?.body).toEqual({ decision: 'approve' });
  expect(state.errors).toEqual([]);
});

for (const viewport of [{ width: 1366, height: 768 }, { width: 390, height: 600 }]) {
  test(`the fleet draft keeps its composer on screen at ${viewport.width}x${viewport.height}`, async ({ page }, testInfo) => {
    const state = await setup(page);
    await page.addInitScript(() => localStorage.setItem('lang', 'zh'));
    await page.setViewportSize(viewport);
    await page.goto('/ai');
    await expect(page.getByRole('radio', { name: '整个面板', exact: true })).toHaveAttribute('aria-checked', 'true');
    await page.getByRole('button', { name: '分析所有服务器当前状态，优先指出风险', exact: true }).click();
    await expect(page.getByRole('textbox', { name: '任务说明', exact: true })).toBeInViewport({ ratio: 1 });
    await expect(page.getByRole('button', { name: '开始任务', exact: true })).toBeInViewport({ ratio: 1 });
    expect(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1 && document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.getByRole('radio', { name: /只读/ }).click();
    await expect(page.getByText('可读取监控数据并执行经过验证的只读操作；服务器变更、告警规则变更和插件调用均会被阻止。', { exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`ai-fleet-draft-${viewport.width}.png`), fullPage: true, animations: 'disabled' });
    expect(state.errors).toEqual([]);
  });
}

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

test('changing the server URL inside the workspace submits only to the newly selected server', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai?server=server-one');
  await expect(page.getByText('Your task, monitoring data and command results from Production web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  await page.evaluate(() => {
    window.history.pushState({}, '', '/ai?server=server-two');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.getByText('Your task, monitoring data and command results from Staging web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect the staging configuration');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path === '/api/ai/tasks')?.body.server_id).toBe('server-two');
  expect(state.errors).toEqual([]);
});

test('a lost response stays with its original submission when browser navigation switches tasks', async ({ page }) => {
  const state = await setup(page);
  state.providers.push({ ...provider, id: 'provider-two', name: 'Other provider' });
  state.holdCreate = true;
  await page.goto('/ai?server=server-one');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Install Nginx on production');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => !!state.held).toBe(true);
  const submitted = state.writes.find(item => item.path === '/api/ai/tasks')!.body;
  await page.evaluate(() => {
    window.history.pushState({}, '', '/ai?task=history-one');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page).toHaveURL(/task=history-one/);
  await state.held!.abort('failed');
  await expect(page.getByText('The requested service is configured.', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry same submission', exact: true })).toHaveCount(0);
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toBeEnabled();
  await page.getByRole('button', { name: 'New task', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Retry same submission', exact: true })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toHaveValue('Install Nginx on production');
  await page.evaluate(() => {
    window.history.pushState({}, '', '/ai?server=server-two');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await page.getByRole('combobox', { name: 'AI provider', exact: true }).click();
  await page.getByTitle('Other provider · deepseek-flash', { exact: true }).click();
  await page.evaluate(() => {
    window.history.pushState({}, '', '/ai?server=server-one');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.getByText('Your task, monitoring data and command results from Production web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  state.holdCreate = false;
  await page.getByRole('button', { name: 'Retry same submission', exact: true }).click();
  await expect(page).toHaveURL(/task=created-one/);
  const attempts = state.writes.filter(item => item.path === '/api/ai/tasks');
  expect(attempts).toHaveLength(2);
  expect(attempts[1].body).toEqual(submitted);
  expect(state.errors).toEqual([]);
});

test('a delayed creation response cannot redirect a newer server selection', async ({ page }) => {
  const state = await setup(page);
  state.holdCreate = true;
  await page.goto('/ai?server=server-one');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect production');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => !!state.held).toBe(true);
  await page.evaluate(() => {
    window.history.pushState({}, '', '/ai?server=server-two');
    window.dispatchEvent(new PopStateEvent('popstate'));
  });
  await expect(page.getByText('Your task, monitoring data and command results from Staging web will be sent to My DeepSeek.', { exact: true })).toBeVisible();
  await state.held!.fulfill({ status: 202, json: task('late-production-task', 'queued') });
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toBeEnabled();
  await expect(page).toHaveURL(/server=server-two/);
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toHaveValue('Inspect production');
  expect(state.errors).toEqual([]);
});

test('recovering a completed follow-up refreshes its events without waiting for active-task polling', async ({ page }) => {
  const state = await setup(page);
  const submissions: Record<string, unknown>[] = [];
  await page.route('**/api/ai/tasks/history-one/messages', route => {
    submissions.push(route.request().postDataJSON());
    const detail = state.details.get('history-one')!;
    if (submissions.length === 1) {
      detail.events.push({ id: 2, kind: 'assistant', content: 'The follow-up has already finished.', created_at: new Date(observedAt + 1000).toISOString() });
      return route.abort('failed');
    }
    return route.fulfill({ status: 202, json: detail.task });
  });
  await page.goto('/ai?task=history-one');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Check the completed result');
  await page.getByRole('button', { name: 'Send follow-up', exact: true }).click();
  await page.getByRole('button', { name: 'Retry same submission', exact: true }).click();
  await expect(page.getByText('The follow-up has already finished.', { exact: true })).toBeVisible();
  expect(submissions).toHaveLength(2);
  expect(submissions[0]).toEqual(submissions[1]);
  expect(state.errors).toEqual([]);
});

test('a stale task read cannot overwrite a follow-up that is now running', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai?task=history-one');
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toBeVisible();
  const stale = structuredClone(state.details.get('history-one')!);
  state.holdRead = 'history-one';
  await page.locator('.ai-task-header').getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect.poll(() => !!state.held).toBe(true);
  const oldRead = state.held!;
  const settled = new Promise<void>(resolve => {
    const done = (request: import('@playwright/test').Request) => { if (request === oldRead.request()) resolve(); };
    page.on('requestfinished', done); page.on('requestfailed', done);
  });
  state.holdRead = '';
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect the configuration');
  await page.getByRole('button', { name: 'Send follow-up', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toBeVisible();
  await oldRead.fulfill({ json: stale });
  await settled;
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel task', exact: true })).toBeVisible();
  expect(state.errors).toEqual([]);
});

test('disabling the selected provider never silently routes an existing draft to another provider', async ({ page }) => {
  const state = await setup(page);
  state.providers.push({ ...provider, id: 'provider-two', name: 'Other provider' });
  await page.goto('/ai?server=server-one');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect confidential configuration');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  await dialog.getByRole('switch', { name: 'Enabled', exact: true }).click();
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.providers[0].enabled).toBe(false);
  await expect(dialog.getByRole('button', { name: 'Add provider' })).toBeVisible();
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Start task', exact: true })).toBeDisabled();
  await expect(page.getByRole('textbox', { name: 'Task instructions' })).toHaveValue('Inspect confidential configuration');
  await page.getByRole('combobox', { name: 'AI provider', exact: true }).click();
  await page.getByTitle('Other provider · deepseek-flash', { exact: true }).click();
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path === '/api/ai/tasks')?.body.provider_id).toBe('provider-two');
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
  state.details.set('finished', { task: task('finished'), events: [{ id: 1, kind: 'assistant', phase: 'final', content: 'Nginx is running.', created_at: new Date(observedAt).toISOString() }], actions: [action({ requires_approval: false, status: 'succeeded', command: 'systemctl status nginx', summary: 'Check nginx', output: 'active (running)', exit_code: 0, duration_ms: 120, truncated: true })] });
  await page.goto('/ai?task=finished');
  await page.locator('.ai-process > summary').click();
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

test('assistant Markdown renders tables and code safely while command output stays literal and collapsed', async ({ page }, testInfo) => {
  const state = await setup(page);
  const externalRequests: string[] = [];
  page.on('request', request => { if (request.url().includes('tracking.invalid')) externalRequests.push(request.url()); });
  const markdown = '# Server health\n\n**2 services are healthy.**\n\n| Service | State |\n| --- | --- |\n| `nginx` | Running |\n| docker | Running |\n\n```bash\nsystemctl status nginx\n```\n\n[Docs](https://example.com/docs) [bad](javascript:alert%281%29) [data](data:text/html,test)\n\n![Private tracker](https://tracking.invalid/pixel?secret=test)\n\n<script>window.markdownExecuted = true</script>\n\n<img src="https://tracking.invalid/raw" onerror="alert(1)" />';
  state.details.set('markdown', { task: task('markdown'), events: [{ id: 1, kind: 'assistant', phase: 'final', content: markdown, created_at: new Date(observedAt).toISOString() }], actions: [action({ requires_approval: false, status: 'succeeded', command: 'printf "**literal**"', output: '**raw output**\n| name | value |', exit_code: 0 })] });
  await page.goto('/ai?task=markdown');
  const rendered = page.locator('.ai-event-assistant .ai-markdown');
  await expect(rendered.getByRole('heading', { name: 'Server health' })).toBeVisible();
  await expect(rendered.getByRole('table').getByRole('row')).toHaveCount(3);
  await expect(rendered.locator('strong')).toHaveText('2 services are healthy.');
  await expect(rendered.locator('script, img, iframe')).toHaveCount(0);
  await expect(rendered.locator('a')).toHaveCount(1);
  await expect(rendered.getByRole('link', { name: 'Docs' })).toHaveAttribute('rel', 'noopener noreferrer');
  expect(externalRequests).toEqual([]);
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await rendered.getByRole('button', { name: 'Copy code' }).click();
  expect((await page.evaluate(() => navigator.clipboard.readText())).replace(/\r\n/g, '\n')).toBe('systemctl status nginx\n');
  await page.locator('.ai-process > summary').click();
  const output = page.locator('.ai-action-output');
  await expect(output.locator('pre')).not.toBeVisible();
  await output.locator('summary').click();
  await expect(output.locator('pre')).toHaveText('**raw output**\n| name | value |');
  await expect(page.locator('.ai-command')).toHaveText('printf "**literal**"');
  await page.getByText('Copied', { exact: true }).waitFor({ state: 'hidden' });
  await page.evaluate(() => { window.scrollTo(0, 0); document.querySelector('.ai-conversation-body')?.scrollTo(0, 0); });
  await page.screenshot({ path: testInfo.outputPath('ai-markdown-desktop.png'), fullPage: true, animations: 'disabled' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Dark', exact: true }).click();
  await page.evaluate(() => window.scrollTo(0, 0));
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('ai-markdown-mobile-dark.png'), fullPage: true, animations: 'disabled' });
  expect(state.errors).toEqual([]);
});

for (const [mode, name] of [['readonly', 'Read only'], ['standard', 'Standard'], ['automatic', 'Fully automatic']] as const) {
  test(`${name} is explicit and fixed when the task starts; new tasks default to Standard`, async ({ page }) => {
    const state = await setup(page);
    await page.goto('/ai?server=server-one');
    await expect(page.getByRole('radio', { name: 'Standard', exact: false })).toHaveAttribute('aria-checked', 'true');
    await page.getByRole('radio', { name, exact: false }).click();
    await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect the selected server');
    await page.getByRole('button', { name: 'Start task', exact: true }).click();
    await expect.poll(() => state.writes.find(item => item.path === '/api/ai/tasks')?.body.approval_mode).toBe(mode);
    await expect(page.locator('.ai-task-capabilities')).toContainText(name);
    await expect(page.getByRole('radiogroup', { name: 'Approval level' })).toHaveCount(0);
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await page.getByRole('button', { name: 'New task', exact: true }).click();
    await expect(page.getByRole('radio', { name: 'Standard', exact: false })).toHaveAttribute('aria-checked', 'true');
  });
}

test('provider model discovery refreshes saved and new proxy configurations without changing the selected model', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  await expect.poll(() => state.modelReads).toContain('/api/ai/providers/provider-one/models');
  await expect(dialog.getByLabel('Model', { exact: true })).toHaveValue('deepseek-flash');
  await dialog.getByLabel('Provider type', { exact: true }).click();
  await page.getByTitle('CPA / API proxy', { exact: true }).click();
  await dialog.getByLabel('API base URL', { exact: true }).fill('https://proxy.example.test/v1');
  await dialog.getByLabel('API key', { exact: true }).fill('proxy-model-key');
  await dialog.getByRole('button', { name: 'Sync models', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path === '/api/ai/providers/models')?.body).toEqual({ kind: 'cpa', base_url: 'https://proxy.example.test/v1', api_key: 'proxy-model-key' });
  await dialog.getByLabel('Model', { exact: true }).fill('discovered');
  await page.getByTitle('discovered-model-two', { exact: true }).click();
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.method === 'PUT')?.body.model).toBe('discovered-model-two');
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain('proxy-model-key');
  expect(state.errors).toEqual([]);
});

test('model discovery failures preserve manual IDs and visible saved settings refresh periodically', async ({ page }) => {
  const state = await setup(page);
  await page.clock.install();
  await page.goto('/ai');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  await expect.poll(() => state.modelReads.length).toBe(1);
  await page.clock.fastForward(300100);
  await expect.poll(() => state.modelReads.length).toBe(2);
  await page.route('**/api/ai/providers/provider-one/models', route => route.fulfill({ status: 422, json: { code: 'models_not_supported', error: 'Provider does not publish a model directory' } }));
  await dialog.getByLabel('Model', { exact: true }).fill('my-manual-model');
  await dialog.getByRole('button', { name: 'Sync models', exact: true }).click();
  await expect(dialog.getByText('Provider does not publish a model directory', { exact: true })).toBeVisible();
  await expect(dialog.getByLabel('Model', { exact: true })).toHaveValue('my-manual-model');
  await dialog.getByRole('button', { name: 'Save provider', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.method === 'PUT')?.body.model).toBe('my-manual-model');
  expect(state.errors).toEqual([]);
});

test('Markdown Skills and MCP plugins are saved then explicitly bound to a task without exposing plugin tokens', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai?server=server-one');
  await page.getByRole('button', { name: 'Skills & plugins', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Add Skill', exact: true }).click();
  await dialog.locator('input[type="file"]').setInputFiles({ name: 'Service playbook.md', mimeType: 'text/markdown', buffer: Buffer.from('# Verify the service\n\nCheck its status after installation.') });
  await expect(dialog.getByLabel('Skill name', { exact: true })).toHaveValue('Service playbook');
  await dialog.getByRole('button', { name: 'Preview', exact: true }).click();
  await expect(dialog.getByRole('heading', { name: 'Verify the service' })).toBeVisible();
  await dialog.getByRole('button', { name: 'Save Skill', exact: true }).click();
  await expect.poll(() => state.skills.length).toBe(1);
  await dialog.getByRole('tab', { name: 'MCP plugins' }).click();
  await dialog.getByRole('button', { name: 'Add MCP plugin', exact: true }).click();
  await dialog.getByLabel('Plugin name', { exact: true }).fill('Operations tools');
  await dialog.getByLabel('MCP HTTP endpoint', { exact: true }).fill('https://mcp.example.test/mcp');
  await dialog.getByLabel('Bearer token', { exact: true }).fill('private-mcp-token');
  await dialog.getByRole('button', { name: 'Save plugin', exact: true }).click();
  await expect.poll(() => state.plugins.length).toBe(1);
  await dialog.getByRole('button', { name: 'Edit: Operations tools', exact: true }).click();
  await expect(dialog.getByLabel('Bearer token', { exact: true })).toHaveValue('');
  await dialog.getByLabel('MCP HTTP endpoint', { exact: true }).fill('https://other-mcp.example.test/mcp');
  await dialog.getByRole('button', { name: 'Save plugin', exact: true }).click();
  await expect(dialog.getByText('This field is required', { exact: true })).toBeVisible();
  expect(state.writes.filter(item => item.method === 'PUT' && item.path.includes('/plugins/'))).toHaveLength(0);
  await dialog.getByRole('switch', { name: 'Remove saved token', exact: true }).click();
  await dialog.getByRole('button', { name: 'Save plugin', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.method === 'PUT' && item.path.includes('/plugins/'))?.body).toMatchObject({ bearer_token: '', clear_token: true });
  await dialog.getByRole('button', { name: 'Test plugin: Operations tools', exact: true }).click();
  await expect(page.getByText('MCP connection and tools verified', { exact: true })).toBeVisible();
  await expect(dialog.getByText('2 tools', { exact: true })).toBeVisible();
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  await page.locator('.ai-task-extension-picker > summary').click();
  await page.getByRole('combobox', { name: 'Skills', exact: true }).click();
  await page.getByTitle('Service playbook', { exact: true }).click();
  await page.keyboard.press('Escape');
  await page.getByRole('combobox', { name: 'MCP plugins', exact: true }).click();
  await page.getByTitle('Operations tools', { exact: true }).click();
  await page.keyboard.press('Escape');
  await page.getByRole('textbox', { name: 'Task instructions' }).fill('Use the selected instructions to inspect the service');
  await page.getByRole('button', { name: 'Start task', exact: true }).click();
  await expect.poll(() => state.writes.find(item => item.path === '/api/ai/tasks')?.body).toMatchObject({ skill_ids: ['skill-one'], plugin_ids: ['plugin-one'], approval_mode: 'standard' });
  await expect(page.locator('.ai-task-capabilities')).toContainText('Skill · Service playbook');
  await expect(page.locator('.ai-task-capabilities')).toContainText('MCP · Operations tools');
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).not.toContain('private-mcp-token');
  expect(state.errors).toEqual([]);
});

test('a delayed Markdown import cannot overwrite another Skill editor', async ({ page }) => {
  const state = await setup(page);
  state.skills.push({ id: 'skill-existing', name: 'Existing playbook', description: '', content: '# Keep these instructions', created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString() });
  await page.goto('/ai');
  await page.getByRole('button', { name: 'Skills & plugins', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Add Skill', exact: true }).click();
  await page.evaluate(() => { File.prototype.text = () => new Promise<string>(resolve => { (window as unknown as { releaseImport: () => void }).releaseImport = () => resolve('# Late import from another editor'); }); });
  await dialog.locator('input[type="file"]').setInputFiles({ name: 'Delayed.md', mimeType: 'text/markdown', buffer: Buffer.from('# delayed') });
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await dialog.getByRole('button', { name: 'Edit: Existing playbook', exact: true }).click();
  await page.evaluate(async () => { (window as unknown as { releaseImport: () => void }).releaseImport(); await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
  await expect(dialog.getByLabel('Markdown instructions', { exact: true })).toHaveValue('# Keep these instructions');
  await expect(dialog.getByLabel('Skill name', { exact: true })).toHaveValue('Existing playbook');
  expect(state.errors).toEqual([]);
});

test('changing an API key invalidates its model directory and cancels older discovery responses', async ({ page }) => {
  const state = await setup(page);
  await page.goto('/ai');
  await page.getByRole('button', { name: 'Providers', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Edit: My DeepSeek', exact: true }).click();
  await expect(dialog.locator('.ai-model-catalog')).toContainText('2 models');
  const key = dialog.getByLabel('API key', { exact: true });
  await key.fill('new-account-key');
  await expect(dialog.locator('.ai-model-catalog')).not.toContainText('2 models');
  // Typing a new credential must not silently send it on every keystroke.
  expect(state.writes.filter(item => item.path.endsWith('/models'))).toHaveLength(0);
  let held: Route | undefined;
  await page.route('**/api/ai/providers/models', route => { held = route; });
  await dialog.getByRole('button', { name: 'Sync models', exact: true }).click();
  await expect.poll(() => !!held).toBe(true);
  await key.fill('another-account-key');
  await held!.fulfill({ json: { models: [{ id: 'stale-account-model' }], fetched_at: new Date(observedAt).toISOString(), source: 'provider' } });
  await expect(dialog.getByRole('button', { name: 'Sync models', exact: true })).toBeEnabled();
  await dialog.getByLabel('Model', { exact: true }).fill('stale');
  await expect(page.getByTitle('stale-account-model', { exact: true })).toHaveCount(0);
  // Clearing the draft key returns discovery to the saved credential.
  await key.fill('');
  await expect.poll(() => state.modelReads.length).toBe(2);
  await expect(dialog.locator('.ai-model-catalog')).toContainText('2 models');
  expect(state.errors).toEqual([]);
});

for (const type of ['Skill', 'plugin'] as const) {
  test(`deleting an unrelated ${type} keeps the current unsaved editor`, async ({ page }) => {
    const state = await setup(page);
    for (const id of ['editing', 'remove']) {
      const common = { id, name: id, created_at: new Date(observedAt).toISOString(), updated_at: new Date(observedAt).toISOString() };
      if (type === 'Skill') state.skills.push({ ...common, description: '', content: '# Existing instructions' });
      else state.plugins.push({ ...common, url: 'https://mcp.example.test/mcp', enabled: true, has_bearer_token: false });
    }
    await page.goto('/ai');
    await page.getByRole('button', { name: 'Skills & plugins', exact: true }).click();
    const dialog = page.getByRole('dialog');
    if (type === 'plugin') await dialog.getByRole('tab', { name: 'MCP plugins' }).click();
    await dialog.getByRole('button', { name: 'Edit: editing', exact: true }).click();
    const name = dialog.getByLabel(type === 'Skill' ? 'Skill name' : 'Plugin name', { exact: true });
    await name.fill('Unsaved draft');
    await dialog.getByRole('button', { name: 'Delete: remove', exact: true }).click();
    await page.getByRole('dialog').last().getByRole('button', { name: 'OK', exact: true }).click();
    await expect(dialog.getByRole('button', { name: 'Delete: remove', exact: true })).toHaveCount(0);
    await expect(name).toHaveValue('Unsaved draft');
    expect(state.errors).toEqual([]);
  });
}

for (const type of ['provider', 'Skill', 'plugin'] as const) {
  test(`a stale ${type} update sends the editor version and keeps its draft after a conflict`, async ({ page }) => {
    const state = await setup(page);
    const snapshot = new Date(observedAt).toISOString();
    state.skills.push({ id: 'skill-editing', name: 'Config', description: '', content: '# Original instructions', created_at: snapshot, updated_at: snapshot });
    state.plugins.push({ id: 'plugin-editing', name: 'Config', url: 'https://mcp.example.test/mcp', enabled: true, has_bearer_token: false, created_at: snapshot, updated_at: snapshot });
    const updates: Record<string, unknown>[] = [];
    const endpoint = type === 'provider' ? 'providers/provider-one' : type === 'Skill' ? 'skills/skill-editing' : 'plugins/plugin-editing';
    await page.route(`**/api/ai/${endpoint}`, route => {
      if (route.request().method() !== 'PUT') return route.fallback();
      updates.push(route.request().postDataJSON());
      return route.fulfill({ status: 409, json: { error: 'Settings changed; reload before saving' } });
    });
    await page.goto('/ai');
    await page.getByRole('button', { name: type === 'provider' ? 'Providers' : 'Skills & plugins', exact: true }).click();
    const dialog = page.getByRole('dialog');
    if (type === 'plugin') await dialog.getByRole('tab', { name: 'MCP plugins' }).click();
    await dialog.getByRole('button', { name: `Edit: ${type === 'provider' ? 'My DeepSeek' : 'Config'}`, exact: true }).click();
    const name = dialog.getByLabel(type === 'provider' ? 'Display name' : type === 'Skill' ? 'Skill name' : 'Plugin name', { exact: true });
    await name.fill('My unsaved changes');
    const save = dialog.getByRole('button', { name: type === 'provider' ? 'Save provider' : type === 'Skill' ? 'Save Skill' : 'Save plugin', exact: true });
    await save.click();
    await expect(page.getByText('Settings changed; reload before saving', { exact: true })).toBeVisible();
    await expect(name).toHaveValue('My unsaved changes');
    expect(updates[0].expected_updated_at).toBe(snapshot);
    await save.click();
    await expect.poll(() => updates.length).toBe(2);
    expect(updates[1].expected_updated_at).toBe(snapshot);
    expect(state.errors).toEqual([]);
  });
}

for (const [followup, status] of [[false, 429], [true, 408]] as const) {
  test(`recovering a lost ${followup ? 'follow-up' : 'creation'} response preserves its request ID after HTTP ${status}`, async ({ page }) => {
    const state = await setup(page);
    const endpoint = followup ? '/api/ai/tasks/history-one/messages' : '/api/ai/tasks';
    const ids: string[] = [];
    await page.route(`**${endpoint}`, route => {
      if (route.request().method() !== 'POST') return route.fallback();
      ids.push(route.request().postDataJSON().request_id);
      if (ids.length === 1) return route.abort('failed');
      if (ids.length === 2) return route.fulfill({ status, json: { error: 'Please retry this submission later' } });
      return route.fulfill({ status: 202, json: task('history-one') });
    });
    await page.goto(followup ? '/ai?task=history-one' : '/ai?server=server-one');
    await page.getByRole('textbox', { name: 'Task instructions' }).fill('Inspect the current service');
    await page.getByRole('button', { name: followup ? 'Send follow-up' : 'Start task', exact: true }).click();
    const retry = page.getByRole('button', { name: 'Retry same submission', exact: true });
    await retry.click();
    await expect.poll(() => ids.length).toBe(2);
    await expect(retry).toBeVisible();
    await expect(page.getByRole('textbox', { name: 'Task instructions' })).toBeDisabled();
    await expect(page.getByText('Please retry this submission later', { exact: true })).toBeVisible();
    await retry.click();
    await expect.poll(() => ids.length).toBe(3);
    expect(new Set(ids).size).toBe(1);
    await expect(page.getByText('The requested service is configured.', { exact: true })).toBeVisible();
    expect(state.errors).toEqual([]);
  });
}

for (const [followup, status] of [[false, 400], [true, 409]] as const) {
  test(`a definite HTTP ${status} rejection leaves ${followup ? 'follow-up' : 'task'} instructions editable`, async ({ page }) => {
    const state = await setup(page);
    const endpoint = followup ? '/api/ai/tasks/history-one/messages' : '/api/ai/tasks';
    await page.route(`**${endpoint}`, route => route.request().method() === 'POST' ? route.fulfill({ status, json: { error: 'Capacity reached; start a new task with fewer instructions' } }) : route.fallback());
    await page.goto(followup ? '/ai?task=history-one' : '/ai?server=server-one');
    const prompt = page.getByRole('textbox', { name: 'Task instructions' });
    await prompt.fill('Inspect the current service');
    await page.getByRole('button', { name: followup ? 'Send follow-up' : 'Start task', exact: true }).click();
    await expect(page.getByText('Capacity reached; start a new task with fewer instructions', { exact: true })).toBeVisible();
    await expect(prompt).toBeEnabled();
    await expect(prompt).toHaveValue('Inspect the current service');
    await expect(page.getByRole('button', { name: 'Retry same submission', exact: true })).toHaveCount(0);
    expect(state.errors).toEqual([]);
  });
}

for (const viewport of [{ width: 1440, height: 900 }, { width: 1366, height: 768 }, { width: 390, height: 844 }]) {
  test(`the AI workspace keeps its controls on one screen at ${viewport.width}x${viewport.height}`, async ({ page }, testInfo) => {
    const state = await setup(page);
    await page.addInitScript(() => localStorage.setItem('lang', 'zh'));
    await page.setViewportSize(viewport);
    const selected = { ...task('layout'), server_name: '移动云昆明', prompt: '查看服务器当前运行的服务' };
    const introduction = '## 服务器运行状态\n\n当前有 **36 个服务** 正常运行，未发现异常。\n\n| 服务 | 状态 | 说明 |\n| --- | --- | --- |\n| `nginx.service` | 运行中 | 网站服务 |\n| `docker.service` | 运行中 | 容器服务 |\n| `ssh.service` | 运行中 | 远程连接 |\n\n';
    state.tasks = [selected, ...Array.from({ length: 29 }, (_, index) => ({ ...task(`other-${index}`), prompt: `历史任务 ${index + 1} · 检查服务器状态` }))];
    state.details.set('layout', { task: selected, events: [
      { id: 1, kind: 'user', content: selected.prompt, created_at: selected.created_at },
      { id: 2, kind: 'assistant', phase: 'final', content: introduction + Array.from({ length: 20 }, (_, index) => `### 检查项 ${index + 1}\n\n服务启动正常，资源使用稳定。可以继续查看日志或检查监听端口。\n\n`).join(''), created_at: selected.created_at },
    ], actions: [action({ status: 'succeeded', requires_approval: false, command: 'systemctl list-units --type=service --state=running', output: Array.from({ length: 100 }, (_, index) => `service-${index}.service loaded active running`).join('\n'), exit_code: 0 })] });
    await page.goto('/ai?task=layout');
    const conversation = page.getByRole('region', { name: '任务对话', exact: true });
    const prompt = page.getByRole('textbox', { name: '任务说明', exact: true });
    const assertViewport = async () => {
      expect(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1 && document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
      await expect(prompt).toBeInViewport({ ratio: 1 });
      await expect(page.getByRole('button', { name: '发送补充说明', exact: true })).toBeInViewport({ ratio: 1 });
      expect(await conversation.evaluate(element => element.clientHeight)).toBeGreaterThan(90);
      expect(await conversation.evaluate(element => getComputedStyle(element).scrollbarWidth)).toBe('none');
    };
    await expect(page.getByRole('heading', { name: '服务器运行状态', exact: true })).toBeVisible();
    await assertViewport();
    await conversation.focus();
    await page.keyboard.press('End');
    await expect.poll(() => conversation.evaluate(element => element.scrollTop)).toBeGreaterThan(100);
    await expect(page.locator('.ai-action-output pre')).not.toBeVisible();
    await page.locator('.ai-process > summary').click();
    await page.locator('.ai-action-output summary').click();
    await expect(page.locator('.ai-action-output pre')).toBeVisible();
    expect(await page.locator('.ai-action-output pre').evaluate(element => element.clientHeight)).toBeLessThanOrEqual(180);
    await assertViewport();
    await conversation.focus();
    await page.keyboard.press('Home');
    await expect.poll(() => conversation.evaluate(element => element.scrollTop)).toBe(0);
    await conversation.hover();
    await page.mouse.wheel(0, 400);
    await expect.poll(() => conversation.evaluate(element => element.scrollTop)).toBeGreaterThan(100);
    await conversation.evaluate(element => element.scrollTo(0, 0));
    await prompt.fill('继续查看 Nginx 最近一小时的错误日志');
    await page.screenshot({ path: testInfo.outputPath(`ai-one-screen-${viewport.width}x${viewport.height}-zh.png`), fullPage: true, animations: 'disabled' });
    if (viewport.width < 701) {
      await expect(page.locator('.app-sidebar .brand-lockup')).not.toBeVisible();
      await page.getByRole('button', { name: '任务历史', exact: true }).click();
      await expect(page.getByRole('dialog').getByText('历史任务 1 · 检查服务器状态', { exact: true })).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await page.setViewportSize({ width: viewport.width, height: 600 });
      await assertViewport();
      await page.getByRole('button', { name: '新建任务', exact: true }).click();
      await expect(page.getByRole('combobox', { name: '目标服务器', exact: true })).toBeVisible();
      await expect(page.getByRole('button', { name: '开始任务', exact: true })).toBeInViewport({ ratio: 1 });
      expect(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 1)).toBe(true);
    }
    expect(state.errors).toEqual([]);
  });
}
