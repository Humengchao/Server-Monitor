import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, App, Button, Empty, Form, Input, Modal, Select, Space, Spin, Switch, Tag, Typography } from 'antd';
import { CheckOutlined, CloseOutlined, CloudServerOutlined, CodeOutlined, DeleteOutlined, EditOutlined, ExperimentOutlined, HistoryOutlined, KeyOutlined, PlusOutlined, ReloadOutlined, RobotOutlined, SafetyCertificateOutlined, SendOutlined, SettingOutlined, StopOutlined } from '@ant-design/icons';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { aiApi } from '../api/ai';
import type { AIAction, AIProvider, AIProviderInput, AITask, AITaskDetail, AITaskInput, AITaskStatus } from '../api/ai';
import { serversApi } from '../api/servers';
import type { Server } from '../api/servers';
import { usePolling } from '../hooks/usePolling';
import { formatDateTime } from '../utils/format';
import '../i18n/ai';
import './AI.css';

const activeStatuses: AITaskStatus[] = ['queued', 'running', 'awaiting_approval', 'awaiting_input', 'cancel_requested'];
const statusColors: Partial<Record<AITaskStatus | AIAction['status'], string>> = { running: 'processing', queued: 'processing', awaiting_approval: 'warning', awaiting_input: 'gold', completed: 'success', succeeded: 'success', failed: 'error', rejected: 'error', interrupted: 'warning' };
const deepseek = { kind: 'deepseek' as const, base_url: 'https://api.deepseek.com', model: 'deepseek-flash' };
function errorText(error: unknown, fallback: string): string { return (error as { response?: { data?: { error?: string } } })?.response?.data?.error || fallback; }
function argumentsText(action: AIAction) { return JSON.stringify(action.arguments, null, 2); }

function ProviderSettings({ providers, onChanged, onClose }: { providers: AIProvider[]; onChanged: () => Promise<void>; onClose: () => void }) {
  const { t } = useTranslation('ai');
  const { message, modal } = App.useApp();
  const [editing, setEditing] = useState<AIProvider | 'new' | null>(null);
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm<AIProviderInput>();
  const baseURL = Form.useWatch('base_url', form);
  const request = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const confirmation = useRef<ReturnType<typeof modal.confirm> | null>(null);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; request.current?.abort(); confirmation.current?.destroy(); }; }, []);
  const mutate = async (operation: (signal: AbortSignal) => Promise<unknown>, success: string) => {
    if (request.current) return false;
    const controller = new AbortController(); request.current = controller; setBusy(true);
    try {
      await operation(controller.signal);
      if (!mounted.current || controller.signal.aborted) return false;
      message.success(success); await onChanged(); return true;
    } catch (error) { if (!controller.signal.aborted && mounted.current) message.error(errorText(error, t('operationFailed'))); return false; }
    finally { if (request.current === controller) request.current = null; if (mounted.current) setBusy(false); }
  };
  const edit = (provider: AIProvider | 'new') => {
    form.resetFields(); setEditing(provider);
    form.setFieldsValue(provider === 'new' ? { ...deepseek, name: 'DeepSeek', api_key: '', enabled: true } : { ...provider, api_key: '' });
  };
  const save = async (values: AIProviderInput) => {
    const payload = { ...values, name: values.name.trim(), base_url: values.base_url.trim(), model: values.model.trim(), api_key: values.api_key || '' };
    if (await mutate(signal => editing && editing !== 'new' ? aiApi.updateProvider(editing.id, payload, signal) : aiApi.createProvider(payload, signal), t('providerSaved'))) {
      if (mounted.current) { form.resetFields(); setEditing(null); }
    }
  };
  const needsKey = editing === 'new' || !!editing && (!editing.has_api_key || baseURL?.trim() !== editing.base_url);
  return <Modal open title={<Space><KeyOutlined />{t('providerSettings')}</Space>} className="ai-provider-modal" width={760} onCancel={() => { if (!busy) onClose(); }} footer={null} destroyOnHidden>
    <Typography.Paragraph type="secondary">{t('providerHint')}</Typography.Paragraph>
    <div className="ai-provider-list">
      {providers.map(provider => <div className="ai-provider-row" key={provider.id}>
        <span className="ai-provider-symbol" aria-hidden="true"><RobotOutlined /></span>
        <div className="ai-provider-info"><strong>{provider.name}</strong><span title={provider.base_url}>{provider.model} · {provider.base_url}</span><Space size={4}><Tag>{t(provider.enabled ? 'enabled' : 'disabled')}</Tag><Tag icon={<KeyOutlined />}>{t(provider.has_api_key ? 'savedKey' : 'missingKey')}</Tag></Space></div>
        <div className="ai-provider-actions">
          <Button size="small" aria-label={`${t('test')}: ${provider.name}`} disabled={busy || !provider.has_api_key} icon={<ExperimentOutlined />} onClick={() => { void mutate(signal => aiApi.testProvider(provider.id, signal), t('testPassed')); }}>{t('test')}</Button>
          <Button size="small" aria-label={`${t('edit')}: ${provider.name}`} disabled={busy} icon={<EditOutlined />} onClick={() => edit(provider)} />
          <Button size="small" danger aria-label={`${t('remove')}: ${provider.name}`} disabled={busy} icon={<DeleteOutlined />} onClick={() => {
            confirmation.current = modal.confirm({ title: t('deleteProvider'), content: `${provider.name} — ${t('deleteProviderHint')}`, okButtonProps: { danger: true }, onOk: async () => {
              if (!await mutate(signal => aiApi.deleteProvider(provider.id, signal), t('providerRemoved'))) throw new Error('delete failed');
              if (mounted.current && editing !== 'new' && editing?.id === provider.id) { form.resetFields(); setEditing(null); }
            } });
          }} />
        </div>
      </div>)}
      {!providers.length && !editing && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('noProvider')} />}
    </div>
    {!editing ? <Button icon={<PlusOutlined />} disabled={busy} onClick={() => edit('new')}>{t('addProvider')}</Button> : <Form form={form} layout="vertical" onFinish={save} disabled={busy} className="ai-provider-form">
      <Typography.Title level={5}>{t(editing === 'new' ? 'addProvider' : 'editProvider')}</Typography.Title>
      <div className="ai-provider-form-grid">
        <Form.Item name="name" label={t('providerName')} rules={[{ required: true, whitespace: true, message: t('required') }]}><Input maxLength={128} /></Form.Item>
        <Form.Item name="kind" label={t('kind')}><Select options={[{ value: 'deepseek', label: t('deepseek') }, { value: 'compatible', label: t('compatible') }]} onChange={kind => form.setFieldsValue({ api_key: '', ...(kind === 'deepseek' ? deepseek : { base_url: '', model: '' }) })} /></Form.Item>
      </div>
      <Form.Item name="base_url" label={t('baseURL')} rules={[{ required: true, message: t('required') }, { validator: (_, value) => { try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash ? Promise.resolve() : Promise.reject(new Error(t('invalidURL'))); } catch { return Promise.reject(new Error(t('invalidURL'))); } } }]}><Input autoComplete="off" maxLength={2048} /></Form.Item>
      <Form.Item name="model" label={t('model')} rules={[{ required: true, whitespace: true, message: t('required') }]}><Input autoComplete="off" maxLength={128} /></Form.Item>
      <Form.Item name="api_key" label={t('apiKey')} dependencies={['base_url']} extra={editing !== 'new' && editing?.has_api_key ? t('endpointKey') : undefined} rules={[{ required: needsKey, message: t('required') }]}>
        <Input.Password autoComplete="new-password" maxLength={4096} placeholder={editing !== 'new' && editing?.has_api_key ? t('keyKept') : undefined} />
      </Form.Item>
      <Form.Item name="enabled" label={t('enabled')} htmlFor="" valuePropName="checked"><Switch aria-label={t('enabled')} /></Form.Item>
      <Space><Button type="primary" htmlType="submit" loading={busy} disabled={busy} aria-label={t('save')} aria-busy={busy}>{t('save')}</Button><Button disabled={busy} onClick={() => { setEditing(null); form.resetFields(); }}>{t('cancel')}</Button></Space>
    </Form>}
  </Modal>;
}

function ActionCard({ action, task, canDecide, busy, onDecision }: { action: AIAction; task: AITask; canDecide: boolean; busy: boolean; onDecision: (action: AIAction, decision: 'approve' | 'reject') => void }) {
  const { t } = useTranslation('ai');
  const approval = canDecide && action.requires_approval && action.status === 'pending';
  return <article className={`ai-action-card${approval ? ' requires-approval' : ''}`} aria-label={`${t('action')}: ${action.summary || action.tool_name}`}>
    <header><span className="ai-action-icon" aria-hidden="true">{approval ? <SafetyCertificateOutlined /> : <CodeOutlined />}</span><div><strong>{action.summary || action.tool_name}</strong><small>{action.tool_name}</small></div><Tag color={statusColors[action.status]}>{t(action.status)}</Tag></header>
    {approval && <div className="ai-risk-context"><strong>{t('approvalTitle')}</strong><span>{task.server_name}</span><p>{action.risk_reason || t('riskReason')}</p></div>}
    {action.command && <pre className="ai-command" aria-label={t('command')}>{action.command}</pre>}
    <details className="ai-action-arguments"><summary>{t('arguments')}</summary><pre>{argumentsText(action)}</pre></details>
    {(action.output || action.error || action.status === 'succeeded') && <div className="ai-action-output"><span>{t('output')}</span><pre>{action.output || t('noOutput')}</pre>{action.error && <p role="status" className="ai-error-text">{action.error}</p>}</div>}
    {action.truncated && <p className="ai-action-note">{t('truncated')}</p>}
    {action.unknown && <Alert type="warning" showIcon title={t('unknown')} />}
    <footer><span>{action.exit_code == null ? '' : t('exitCode', { code: action.exit_code })}</span><span>{action.duration_ms > 0 ? t('duration', { duration: action.duration_ms }) : ''}</span></footer>
    {approval && <div className="ai-approval-buttons"><Button danger aria-label={t('reject')} disabled={busy} icon={<CloseOutlined />} onClick={() => onDecision(action, 'reject')}>{t('reject')}</Button><Button type="primary" danger aria-label={t('approve')} disabled={busy} icon={<CheckOutlined />} onClick={() => onDecision(action, 'approve')}>{t('approve')}</Button></div>}
  </article>;
}

type Submission = { request_id: string; prompt: string; server_id: string; provider_id: string; taskId?: string };
export default function AI() {
  const { t, i18n } = useTranslation('ai');
  const { message, modal } = App.useApp();
  const [params, setParams] = useSearchParams();
  const taskId = params.get('task') || '';
  const [providers, setProviders] = useState<AIProvider[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [serverId, setServerId] = useState(params.get('server') || '');
  const [providerId, setProviderId] = useState('');
  const [prompt, setPrompt] = useState('');
  const [settings, setSettings] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [metadataLoading, setMetadataLoading] = useState(true);
  const [page, setPage] = useState({ cursor: '', previous: [] as string[] });
  const [history, setHistory] = useState<{ page: typeof page; tasks: AITask[]; next?: string } | null>(null);
  const [historyError, setHistoryError] = useState(false);
  const [detail, setDetail] = useState<AITaskDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [uncertain, setUncertain] = useState<Submission | null>(null);
  const mounted = useRef(false);
  const metadataRequest = useRef<AbortController | null>(null);
  const historyRequest = useRef<AbortController | null>(null);
  const detailRequest = useRef<AbortController | null>(null);
  const mutation = useRef<AbortController | null>(null);
  const selectedTask = useRef(taskId);
  const decisionModal = useRef<ReturnType<typeof modal.confirm> | null>(null);
  const current = detail?.task.id === taskId ? detail : null;
  const currentTask = current?.task;
  const rows = history?.page === page ? history.tasks : [];
  const enabledProviders = providers.filter(provider => provider.enabled && provider.has_api_key);
  const chosenProvider = providers.find(provider => provider.id === providerId);
  const chosenServer = servers.find(server => server.id === serverId);
  const canFollowup = currentTask && ['awaiting_input', 'completed'].includes(currentTask.status);
  const canCancel = currentTask && activeStatuses.includes(currentTask.status) && currentTask.status !== 'cancel_requested';
  const readMetadata = useCallback(async () => {
    if (!mounted.current) return;
    metadataRequest.current?.abort(); const controller = new AbortController(); metadataRequest.current = controller;
    try {
      const [providerResponse, serverResponse] = await Promise.all([aiApi.providers(controller.signal), serversApi.list(controller.signal)]);
      if (!mounted.current || controller.signal.aborted) return;
      setProviders(providerResponse.data || []); setServers(serverResponse.data || []); setLoadError(false);
      setProviderId(id => providerResponse.data.some(provider => provider.id === id && provider.enabled && provider.has_api_key) ? id : providerResponse.data.find(provider => provider.enabled && provider.has_api_key)?.id || '');
    } catch { if (!controller.signal.aborted && mounted.current) setLoadError(true); }
    finally { if (metadataRequest.current === controller) { metadataRequest.current = null; if (mounted.current) setMetadataLoading(false); } }
  }, []);
  const readHistory = useCallback(async () => {
    if (!mounted.current || historyRequest.current) return;
    const controller = new AbortController(); historyRequest.current = controller;
    try {
      const response = await aiApi.tasks(page.cursor, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      setHistory({ page, tasks: response.data.tasks || [], next: response.data.next_cursor }); setHistoryError(false);
    } catch { if (!controller.signal.aborted && mounted.current) setHistoryError(true); }
    finally { if (historyRequest.current === controller) historyRequest.current = null; }
  }, [page]);
  const readTask = useCallback(async () => {
    if (!mounted.current || !taskId || detailRequest.current || mutation.current) return;
    const controller = new AbortController(); detailRequest.current = controller;
    try {
      const response = await aiApi.task(taskId, controller.signal);
      if (!mounted.current || controller.signal.aborted || selectedTask.current !== taskId) return;
      setDetail(response.data); setDetailError(null);
    } catch (error) {
      if (!controller.signal.aborted && mounted.current && selectedTask.current === taskId) setDetailError((error as { response?: { status?: number } }).response?.status === 404 ? 'selectedMissing' : 'taskFailed');
    } finally { if (detailRequest.current === controller) detailRequest.current = null; }
  }, [taskId]);
  useEffect(() => {
    mounted.current = true; const timer = window.setTimeout(() => { void readMetadata(); }, 0);
    return () => { mounted.current = false; window.clearTimeout(timer); metadataRequest.current?.abort(); historyRequest.current?.abort(); detailRequest.current?.abort(); mutation.current?.abort(); decisionModal.current?.destroy(); };
  }, [readMetadata]);
  useEffect(() => {
    const timer = window.setTimeout(() => { void readHistory(); }, 0);
    return () => { window.clearTimeout(timer); historyRequest.current?.abort(); historyRequest.current = null; };
  }, [readHistory]);
  useEffect(() => {
    selectedTask.current = taskId;
    const timer = window.setTimeout(() => { setDetailError(null); setPrompt(''); void readTask(); }, 0);
    return () => { window.clearTimeout(timer); detailRequest.current?.abort(); detailRequest.current = null; decisionModal.current?.destroy(); decisionModal.current = null; };
  }, [taskId, readTask]);
  usePolling(async () => { await Promise.all([readTask(), readHistory()]); }, 2000, { leading: false, enabled: (!!taskId && detailError !== 'selectedMissing' && (!currentTask || activeStatuses.includes(currentTask.status))) || rows.some(task => activeStatuses.includes(task.status)) });
  const refreshRef = useRef({ readTask, readHistory });
  useEffect(() => { refreshRef.current = { readTask, readHistory }; }, [readTask, readHistory]);
  const selectTask = (id: string) => { if (!submitting) { setUncertain(null); setParams({ task: id }); } };
  const freshTask = () => { if (submitting) return; setPrompt(''); setUncertain(null); setParams(serverId ? { server: serverId } : {}); };
  const send = async (retry?: Submission) => {
    if (mutation.current || !mounted.current) return;
    let value = retry;
    if (!value) {
      const text = prompt.trim(); if (!text) return;
      if (new TextEncoder().encode(text).length > 16000 || text.includes('\0')) { message.error(t('promptTooLong')); return; }
      if (taskId) { if (!currentTask || !canFollowup) return; }
      else if (!chosenServer || !chosenProvider?.enabled || !chosenProvider.has_api_key) return;
      value = { request_id: crypto.randomUUID(), prompt: text, server_id: currentTask?.server_id || serverId, provider_id: currentTask?.provider_id || providerId, taskId: taskId || undefined };
    }
    setSubmitting(true);
    const controller = new AbortController(); mutation.current = controller;
    try {
      const response = value.taskId ? await aiApi.message(value.taskId, { message: value.prompt, request_id: value.request_id }, controller.signal)
        : await aiApi.createTask({ server_id: value.server_id, provider_id: value.provider_id, prompt: value.prompt, request_id: value.request_id } satisfies AITaskInput, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      setUncertain(null); setPrompt(''); setPage({ cursor: '', previous: [] });
      setDetail(previous => previous?.task.id === response.data.id ? { ...previous, task: response.data } : previous);
      if (selectedTask.current === (value.taskId || '')) setParams({ task: response.data.id });
      if (response.data.id === selectedTask.current) { detailRequest.current?.abort(); detailRequest.current = null; await refreshRef.current.readTask(); }
    } catch (error) {
      if (!mounted.current || controller.signal.aborted) return;
      const status = (error as { response?: { status?: number } }).response?.status;
      if (status == null || status >= 500) setUncertain(value);
      else { setUncertain(null); message.error(errorText(error, t('operationFailed')), 8); }
    } finally { if (mutation.current === controller) mutation.current = null; if (mounted.current) setSubmitting(false); }
  };
  const act = async (task: string, operation: (signal: AbortSignal) => Promise<unknown>, success?: string) => {
    if (mutation.current || selectedTask.current !== task) return false;
    const controller = new AbortController(); mutation.current = controller; setActionBusy(true);
    detailRequest.current?.abort(); detailRequest.current = null;
    try { await operation(controller.signal); if (!mounted.current || controller.signal.aborted) return false; if (success) message.success(success); return true; }
    catch (error) { if (!controller.signal.aborted && mounted.current) message.error(errorText(error, t('operationFailed'))); return false; }
    finally { if (mutation.current === controller) mutation.current = null; if (mounted.current) { setActionBusy(false); await refreshRef.current.readTask(); await refreshRef.current.readHistory(); } }
  };
  const decide = (action: AIAction, decision: 'approve' | 'reject') => {
    if (!currentTask || actionBusy || decisionModal.current) return;
    const task = currentTask;
    decisionModal.current = modal.confirm({ title: t(decision === 'approve' ? 'confirmTitle' : 'rejectTitle'), width: 660,
      className: 'ai-approval-modal', okText: t(decision === 'approve' ? 'approve' : 'reject'), okButtonProps: { danger: true },
      content: <div><Typography.Paragraph>{t('confirmHint', { server: task.server_name })}</Typography.Paragraph><strong>{action.summary}</strong><pre className="ai-command">{action.command}</pre><pre className="ai-arguments-preview">{argumentsText(action)}</pre><Typography.Text type="warning">{action.risk_reason}</Typography.Text></div>,
      afterClose: () => { decisionModal.current = null; },
      onOk: async () => { if (!await act(task.id, signal => aiApi.decide(task.id, action.id, decision, signal), t('decisionSaved'))) throw new Error('decision not saved'); },
    });
  };
  const timeline = useMemo(() => {
    if (!current) return [];
    const actions = new Map(current.actions.map(action => [action.id, action]));
    const rendered = new Set<string>();
    const items: Array<{ key: string; time: string; event?: AITaskDetail['events'][number]; action?: AIAction }> = [];
    for (const event of current.events) {
      const action = event.action_id ? actions.get(event.action_id) : undefined;
      if (action) { if (!rendered.has(action.id)) { items.push({ key: `action-${action.id}`, time: action.created_at, action }); rendered.add(action.id); } }
      else items.push({ key: `event-${event.id}`, time: event.created_at, event });
    }
    for (const action of current.actions) if (!rendered.has(action.id)) items.push({ key: `action-${action.id}`, time: action.created_at, action });
    return items.sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
  }, [current]);
  return <div className="ai-page">
    <div className="page-heading ai-page-heading"><span className="ai-page-icon" aria-hidden="true"><RobotOutlined /></span><div><Typography.Text className="eyebrow">{t('eyebrow')}</Typography.Text><Typography.Title level={2}>{t('title')}</Typography.Title><Typography.Text type="secondary">{t('description')}</Typography.Text></div><Button aria-label={t('providers')} icon={<SettingOutlined />} onClick={() => setSettings(true)}>{t('providers')}</Button></div>
    {loadError && <Alert type="error" showIcon title={t('loadFailed')} action={<Button size="small" onClick={() => { void readMetadata(); }}>{t('retry')}</Button>} />}
    <div className="ai-workspace">
      <aside className="ai-sidebar" aria-label={t('history')}><header><span><HistoryOutlined />{t('history')}</span><Button type="text" size="small" icon={<PlusOutlined />} aria-label={t('newTask')} disabled={submitting} onClick={freshTask} /></header>
        {historyError && <Alert type="warning" title={t('loadFailed')} action={<Button size="small" onClick={() => { void readHistory(); }}>{t('retry')}</Button>} />}
        <div className="ai-history-list">{rows.map(task => <button key={task.id} type="button" className={`ai-history-item${task.id === taskId ? ' selected' : ''}`} aria-current={task.id === taskId ? 'page' : undefined} disabled={submitting} onClick={() => selectTask(task.id)}><strong>{task.prompt}</strong><span>{task.server_name}</span><footer><Tag color={statusColors[task.status]}>{t(task.status)}</Tag><time>{new Date(task.created_at).toLocaleDateString(i18n.language)}</time></footer></button>)}{!rows.length && <div className="ai-history-empty"><HistoryOutlined /><span>{t('noHistory')}</span></div>}</div>
        <div className="ai-history-paging"><Button size="small" disabled={!page.previous.length} onClick={() => setPage(value => ({ cursor: value.previous[value.previous.length - 1], previous: value.previous.slice(0, -1) }))}>{t('previous')}</Button><Button size="small" disabled={history?.page !== page || !history.next} onClick={() => { if (history?.next) setPage({ cursor: history.next, previous: [...page.previous, page.cursor] }); }}>{t('next')}</Button></div>
      </aside>
      <section className="ai-conversation" aria-label={t('title')}>
        {currentTask ? <header className="ai-task-header"><div><span className="ai-task-server"><CloudServerOutlined />{currentTask.server_name}<Tag>{currentTask.server_type}</Tag></span><small>{currentTask.provider_name} · {currentTask.model}</small></div><Space wrap><Tag color={statusColors[currentTask.status]}>{t(currentTask.status)}</Tag><Button type="text" icon={<ReloadOutlined />} aria-label={t('refresh')} onClick={() => { void readTask(); }} />{canCancel && <Button danger icon={<StopOutlined />} aria-label={t('cancelTask')} disabled={actionBusy || submitting} onClick={() => { void act(currentTask.id, signal => aiApi.cancel(currentTask.id, signal), t('cancelRequested')); }}>{t('cancelTask')}</Button>}</Space></header> : null}
        <div className="ai-conversation-body">
          {!taskId && <div className="ai-welcome"><span className="ai-welcome-icon"><RobotOutlined /></span><Typography.Title level={3}>{t('welcomeTitle')}</Typography.Title><p>{t('welcomeHint')}</p><div className="ai-execution-note"><CodeOutlined /><span>{t('executeHint')}</span></div></div>}
          {taskId && !current && !detailError && <div className="ai-task-loading"><Spin /></div>}
          {detailError && <Alert type="warning" showIcon title={t(detailError)} action={<Button onClick={() => { void readTask(); }}>{t('retry')}</Button>} />}
          {currentTask?.error && <Alert type="error" showIcon title={currentTask.error} />}
          {currentTask && ['cancel_requested', 'canceled', 'interrupted'].includes(currentTask.status) && <Alert type="info" showIcon title={t(currentTask.status === 'canceled' ? 'canceledHint' : 'cancelHint')} />}
          {current && <div className="ai-timeline">{timeline.map(item => item.action ? <ActionCard key={item.key} action={item.action} task={current.task} canDecide={current.task.status === 'awaiting_approval'} busy={actionBusy || submitting} onDecision={decide} /> : item.event && <article key={item.key} className={`ai-event ai-event-${item.event.kind}`}><header><span>{t(item.event.kind)}</span><time>{formatDateTime(item.event.created_at, i18n.language)}</time></header><div>{item.event.content}</div></article>)}{!timeline.length && <p className="ai-event-waiting">{t('noEvents')}</p>}</div>}
        </div>
        {currentTask && <div className="ai-task-usage"><span>{t('tokens')}: {t('inputTokens')} {currentTask.prompt_tokens.toLocaleString()} · {t('outputTokens')} {currentTask.completion_tokens.toLocaleString()}</span><span>{t('updated', { time: new Date(currentTask.updated_at).toLocaleTimeString(i18n.language) })}</span></div>}
        <div className="ai-composer">
          {uncertain && <Alert type="warning" showIcon title={t('submitUncertain')} action={<Button disabled={submitting} loading={submitting} aria-label={t('retrySubmission')} onClick={() => { void send(uncertain); }}>{t('retrySubmission')}</Button>} />}
          {!taskId && <div className="ai-composer-targets"><label><span>{t('server')}</span><Select aria-label={t('server')} showSearch optionFilterProp="label" placeholder={t('selectServer')} value={chosenServer?.id} onChange={setServerId} disabled={submitting || !!uncertain} loading={metadataLoading} options={servers.map(server => ({ value: server.id, label: server.name }))} /></label><label><span>{t('provider')}</span><Select aria-label={t('provider')} placeholder={t('selectProvider')} value={chosenProvider?.enabled && chosenProvider.has_api_key ? providerId : undefined} onChange={setProviderId} disabled={submitting || !!uncertain} loading={metadataLoading} options={enabledProviders.map(provider => ({ value: provider.id, label: `${provider.name} · ${provider.model}` }))} /></label></div>}
          {!taskId && !metadataLoading && !enabledProviders.length && <Alert type="info" title={t('noProvider')} action={<Button onClick={() => setSettings(true)}>{t('addProvider')}</Button>} />}
          {(!taskId || canFollowup) ? <><Input.TextArea aria-label={t('taskPrompt')} value={prompt} onChange={event => setPrompt(event.target.value)} placeholder={t(taskId ? 'followupPlaceholder' : 'placeholder')} autoSize={{ minRows: 3, maxRows: 8 }} maxLength={16000} disabled={submitting || !!uncertain} /><div className="ai-composer-footer"><p><SafetyCertificateOutlined />{currentTask ? t('shareSelected', { server: currentTask.server_name, provider: currentTask.provider_name }) : chosenServer && chosenProvider ? t('shareSelected', { server: chosenServer.name, provider: chosenProvider.name }) : t('shareHint')}</p><Button type="primary" icon={<SendOutlined />} aria-label={t(taskId ? 'followup' : 'submit')} aria-busy={submitting} loading={submitting} disabled={submitting || actionBusy || !!uncertain || !prompt.trim() || (!taskId && (!chosenServer || !chosenProvider?.enabled || !chosenProvider.has_api_key))} onClick={() => { void send(); }}>{t(taskId ? 'followup' : 'submit')}</Button></div></> : currentTask ? <p className="ai-composer-state">{t(activeStatuses.includes(currentTask.status) ? currentTask.status === 'awaiting_input' ? 'waiting' : 'working' : 'readOnly')}</p> : null}
          <small className="ai-background-note">{t('leaveHint')}{currentTask && canCancel ? ` ${t('cancelHint')}` : ''}</small>
        </div>
      </section>
    </div>
    {settings && <ProviderSettings providers={providers} onChanged={readMetadata} onClose={() => setSettings(false)} />}
  </div>;
}
