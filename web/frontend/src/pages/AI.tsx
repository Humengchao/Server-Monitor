import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, App, AutoComplete, Button, Drawer, Empty, Form, Input, Modal, Select, Space, Spin, Switch, Tag, Typography } from 'antd';
import { ApiOutlined, BellOutlined, CheckOutlined, CloseOutlined, CloudServerOutlined, CodeOutlined, DashboardOutlined, DeleteOutlined, EditOutlined, ExperimentOutlined, HistoryOutlined, KeyOutlined, PlusOutlined, ReloadOutlined, RobotOutlined, SafetyCertificateOutlined, SendOutlined, SettingOutlined, StopOutlined } from '@ant-design/icons';
import { Link, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { aiApi } from '../api/ai';
import type { AIAction, AIApprovalMode, AIModelCatalog, AIPlugin, AIProvider, AIProviderInput, AIProviderKind, AISkill, AITask, AITaskDetail, AITaskInput, AITaskScope, AITaskStatus } from '../api/ai';
import { serversApi } from '../api/servers';
import type { Server } from '../api/servers';
import { usePolling } from '../hooks/usePolling';
import { useMediaQuery } from '../hooks/useMediaQuery';
import { formatDateTime } from '../utils/format';
import AIMarkdown from '../components/AIMarkdown';
import AIExtensions from '../components/AIExtensions';
import '../i18n/ai';
import './AI.css';

const activeStatuses: AITaskStatus[] = ['queued', 'running', 'awaiting_approval', 'awaiting_input', 'cancel_requested'];
const statusColors: Partial<Record<AITaskStatus | AIAction['status'], string>> = { running: 'processing', queued: 'processing', awaiting_approval: 'warning', awaiting_input: 'gold', completed: 'success', succeeded: 'success', failed: 'error', rejected: 'error', interrupted: 'warning' };
const deepseek = { kind: 'deepseek' as const, base_url: 'https://api.deepseek.com', model: 'deepseek-flash' };
const providerPresets: Record<AIProviderKind, { name: string; base_url: string; model: string }> = {
  deepseek: { name: 'DeepSeek', ...deepseek }, openai: { name: 'OpenAI', base_url: 'https://api.openai.com/v1', model: '' },
  qwen: { name: 'Qwen / 千问', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', model: '' }, moonshot: { name: 'Moonshot / Kimi', base_url: 'https://api.moonshot.cn/v1', model: '' },
  zhipu: { name: 'Zhipu / 智谱', base_url: 'https://open.bigmodel.cn/api/paas/v4', model: '' }, siliconflow: { name: 'SiliconFlow', base_url: 'https://api.siliconflow.cn/v1', model: '' },
  gemini: { name: 'Gemini', base_url: 'https://generativelanguage.googleapis.com/v1beta/openai', model: '' }, cpa: { name: 'CPA / API proxy', base_url: '', model: '' }, compatible: { name: 'Compatible API', base_url: '', model: '' },
};
function errorText(error: unknown, fallback: string): string { return (error as { response?: { data?: { error?: string } } })?.response?.data?.error || fallback; }
function argumentsText(action: AIAction) { return JSON.stringify(action.arguments, null, 2); }

function ProviderSettings({ providers, onChanged, onClose }: { providers: AIProvider[]; onChanged: () => Promise<void>; onClose: () => void }) {
  const { t } = useTranslation('ai');
  const { message, modal } = App.useApp();
  const [editing, setEditing] = useState<AIProvider | 'new' | null>(null);
  const [busy, setBusy] = useState(false);
  const [form] = Form.useForm<AIProviderInput>();
  const baseURL = Form.useWatch('base_url', form);
  const kind = Form.useWatch('kind', form);
  const apiKey = Form.useWatch('api_key', form);
  const [catalog, setCatalog] = useState<AIModelCatalog | null>(null);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelError, setModelError] = useState('');
  const modelRequest = useRef<AbortController | null>(null);
  const request = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const confirmation = useRef<ReturnType<typeof modal.confirm> | null>(null);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; request.current?.abort(); modelRequest.current?.abort(); confirmation.current?.destroy(); }; }, []);
  const refreshModels = useCallback(async () => {
    modelRequest.current?.abort();
    const controller = new AbortController(); modelRequest.current = controller;
    const values = form.getFieldsValue();
    const saved = editing && editing !== 'new' && values.base_url?.trim() === editing.base_url && values.kind === editing.kind && !values.api_key;
    if (!saved && !values.api_key?.trim()) { setModelError(t('modelsNeedKey')); setModelsLoading(false); return; }
    setModelsLoading(true); setModelError('');
    try {
      const result = saved ? await aiApi.models(editing.id, controller.signal) : await aiApi.previewModels({ kind: values.kind, base_url: values.base_url?.trim(), api_key: values.api_key }, controller.signal);
      if (mounted.current && !controller.signal.aborted) setCatalog(result.data);
    } catch (error) { if (mounted.current && !controller.signal.aborted) setModelError(errorText(error, t('modelsFailed'))); }
    finally { if (modelRequest.current === controller) { modelRequest.current = null; if (mounted.current) setModelsLoading(false); } }
  }, [editing, form, t]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setCatalog(null); setModelError(''); setModelsLoading(false);
      if (editing && editing !== 'new' && baseURL === editing.base_url && kind === editing.kind && !apiKey) void refreshModels();
    }, 0);
    const interval = window.setInterval(() => {
      if (document.visibilityState === 'visible' && editing && editing !== 'new' && baseURL === editing.base_url && kind === editing.kind && !form.getFieldValue('api_key')) void refreshModels();
    }, 300000);
    return () => { window.clearTimeout(timer); window.clearInterval(interval); modelRequest.current?.abort(); };
  }, [editing, baseURL, kind, apiKey, refreshModels, form]);
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
    if (await mutate(signal => editing && editing !== 'new' ? aiApi.updateProvider(editing.id, { ...payload, expected_updated_at: editing.updated_at }, signal) : aiApi.createProvider(payload, signal), t('providerSaved'))) {
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
        <Form.Item name="kind" label={t('kind')}><Select virtual={false} options={Object.entries(providerPresets).map(([value, preset]) => ({ value, label: value === 'compatible' ? t('compatible') : preset.name }))} onChange={(value: AIProviderKind) => { const preset = providerPresets[value]; form.setFieldsValue({ kind: value, name: preset.name, api_key: '', base_url: preset.base_url, model: preset.model }); }} /></Form.Item>
      </div>
      <Form.Item name="base_url" label={t('baseURL')} rules={[{ required: true, message: t('required') }, { validator: (_, value) => { try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash ? Promise.resolve() : Promise.reject(new Error(t('invalidURL'))); } catch { return Promise.reject(new Error(t('invalidURL'))); } } }]}><Input autoComplete="off" maxLength={2048} /></Form.Item>
      <Typography.Paragraph className="ai-provider-compatibility" type="secondary">{t('proxyHint')}</Typography.Paragraph>
      <Form.Item name="model" label={t('model')} rules={[{ required: true, whitespace: true, message: t('required') }]}><AutoComplete options={(catalog?.models || []).map(model => ({ value: model.id }))} filterOption={(input, option) => String(option?.value).toLowerCase().includes(input.toLowerCase())}><Input autoComplete="off" maxLength={128} placeholder={t('modelPlaceholder')} /></AutoComplete></Form.Item>
      <div className="ai-model-catalog"><Button size="small" aria-label={t('refreshModels')} icon={<ReloadOutlined spin={modelsLoading} />} disabled={busy || modelsLoading} onClick={() => { void refreshModels(); }}>{t('refreshModels')}</Button><span>{catalog ? t('modelsUpdated', { count: catalog.models.length, time: new Date(catalog.fetched_at).toLocaleTimeString() }) : t('modelsHint')}</span></div>
      {modelError && <Alert className="ai-model-error" type="warning" showIcon title={t('modelsFailed')} description={modelError} />}
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
    {approval && <div className="ai-risk-context"><strong>{t('approvalTitle')}</strong><span>{task.scope === 'fleet' ? t('fleetTarget') : task.server_name}</span><p>{action.risk_reason || t('riskReason')}</p></div>}
    {action.command && <pre className="ai-command" aria-label={t('command')}>{action.command}</pre>}
    <details className="ai-action-arguments"><summary>{t('arguments')}</summary><pre>{argumentsText(action)}</pre></details>
    {(action.output || action.error || action.status === 'succeeded') && <details className="ai-action-output" open={action.status === 'failed' || action.status === 'running'}><summary><span>{t('output')}</span><small>{t('expandOutput')}</small></summary><pre>{action.output || t('noOutput')}</pre>{action.error && <p role="status" className="ai-error-text">{action.error}</p>}</details>}
    {action.truncated && <p className="ai-action-note">{t('truncated')}</p>}
    {action.unknown && <Alert type="warning" showIcon title={t('unknown')} />}
    <footer><span>{action.exit_code == null ? '' : t('exitCode', { code: action.exit_code })}</span><span>{action.duration_ms > 0 ? t('duration', { duration: action.duration_ms }) : ''}</span></footer>
    {approval && <div className="ai-approval-buttons"><Button danger aria-label={t('reject')} disabled={busy} icon={<CloseOutlined />} onClick={() => onDecision(action, 'reject')}>{t('reject')}</Button><Button type="primary" danger aria-label={t('approve')} disabled={busy} icon={<CheckOutlined />} onClick={() => onDecision(action, 'approve')}>{t('approve')}</Button></div>}
  </article>;
}

type Submission = AITaskInput & { taskId?: string; workspace: string };
export default function AI() {
  const { t, i18n } = useTranslation('ai');
  const { message, modal } = App.useApp();
  const [params, setParams] = useSearchParams();
  const taskId = params.get('task') || '';
  const serverId = params.get('server') || '';
  const draftScope: AITaskScope = params.get('scope') === 'fleet' ? 'fleet' : serverId || params.get('scope') === 'server' ? 'server' : 'fleet';
  const workspace = taskId ? `task:${taskId}` : draftScope === 'fleet' ? 'fleet' : `server:${serverId}`;
  const [providers, setProviders] = useState<AIProvider[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [providerId, setProviderId] = useState('');
  const [prompt, setPrompt] = useState('');
  const [settings, setSettings] = useState(false);
  const compact = useMediaQuery('(max-width: 700px)');
  const [historyOpen, setHistoryOpen] = useState(false);
  const [extensionsOpen, setExtensionsOpen] = useState(false);
  const [skills, setSkills] = useState<AISkill[]>([]);
  const [plugins, setPlugins] = useState<AIPlugin[]>([]);
  const [extensionsError, setExtensionsError] = useState(false);
  const [skillIds, setSkillIds] = useState<string[]>([]);
  const [pluginIds, setPluginIds] = useState<string[]>([]);
  const [approvalMode, setApprovalMode] = useState<AIApprovalMode>('standard');
  const [loadError, setLoadError] = useState(false);
  const [metadataLoading, setMetadataLoading] = useState(true);
  const [page, setPage] = useState({ cursor: '', previous: [] as string[] });
  const [history, setHistory] = useState<{ page: typeof page; tasks: AITask[]; next?: string } | null>(null);
  const [historyError, setHistoryError] = useState(false);
  const [detail, setDetail] = useState<AITaskDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [uncertainSubmissions, setUncertainSubmissions] = useState<Record<string, Submission>>({});
  const uncertain = uncertainSubmissions[workspace];
  const mounted = useRef(false);
  const metadataRequest = useRef<AbortController | null>(null);
  const historyRequest = useRef<AbortController | null>(null);
  const detailRequest = useRef<AbortController | null>(null);
  const mutation = useRef<AbortController | null>(null);
  const selectedTask = useRef(taskId);
  const selectedWorkspace = useRef(workspace);
  const decisionModal = useRef<ReturnType<typeof modal.confirm> | null>(null);
  const current = detail?.task.id === taskId ? detail : null;
  const currentTask = current?.task;
  const rows = history?.page === page ? history.tasks : [];
  const enabledProviders = providers.filter(provider => provider.enabled && provider.has_api_key);
  const selectedProviderId = uncertain?.provider_id ?? providerId;
  const chosenProvider = providers.find(provider => provider.id === selectedProviderId);
  const chosenServer = servers.find(server => server.id === serverId);
  const selectedScope = uncertain?.scope ?? draftScope;
  const selectedMode = uncertain?.approval_mode ?? approvalMode;
  const selectedSkills = uncertain?.skill_ids ?? skillIds;
  const selectedPlugins = uncertain?.plugin_ids ?? pluginIds;
  const extensionsUnavailable = selectedSkills.some(id => !skills.some(skill => skill.id === id)) || selectedPlugins.some(id => !plugins.some(plugin => plugin.id === id && plugin.enabled));
  const canFollowup = currentTask && ['awaiting_input', 'completed'].includes(currentTask.status);
  const canCancel = currentTask && activeStatuses.includes(currentTask.status) && currentTask.status !== 'cancel_requested';
  const readMetadata = useCallback(async () => {
    if (!mounted.current) return;
    metadataRequest.current?.abort(); const controller = new AbortController(); metadataRequest.current = controller;
    try {
      const [providerResponse, serverResponse, skillResponse, pluginResponse] = await Promise.all([aiApi.providers(controller.signal), serversApi.list(controller.signal), aiApi.skills(controller.signal).catch(() => null), aiApi.plugins(controller.signal).catch(() => null)]);
      if (!mounted.current || controller.signal.aborted) return;
      setProviders(providerResponse.data || []); setServers(serverResponse.data || []); setLoadError(false);
      if (skillResponse) setSkills(skillResponse.data || []); if (pluginResponse) setPlugins(pluginResponse.data || []);
      setExtensionsError(!skillResponse || !pluginResponse);
      // A saved selection becoming unavailable must not redirect an existing
      // draft to a different provider without the user's choice.
      setProviderId(id => id || providerResponse.data.find(provider => provider.enabled && provider.has_api_key)?.id || '');
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
    selectedWorkspace.current = workspace;
  }, [workspace]);
  useEffect(() => {
    selectedTask.current = taskId;
    const timer = window.setTimeout(() => { setDetailError(null); setPrompt(''); void readTask(); }, 0);
    return () => { window.clearTimeout(timer); detailRequest.current?.abort(); detailRequest.current = null; decisionModal.current?.destroy(); decisionModal.current = null; };
  }, [taskId, readTask]);
  usePolling(async () => { await Promise.all([readTask(), readHistory()]); }, 2000, { leading: false, enabled: (!!taskId && detailError !== 'selectedMissing' && (!currentTask || activeStatuses.includes(currentTask.status))) || rows.some(task => activeStatuses.includes(task.status)) });
  const refreshRef = useRef({ readTask, readHistory });
  useEffect(() => { refreshRef.current = { readTask, readHistory }; }, [readTask, readHistory]);
  const selectTask = (id: string) => { if (!submitting) { setParams({ task: id }); setHistoryOpen(false); } };
  const freshTask = () => {
    if (submitting) return;
    setHistoryOpen(false); setPrompt(''); setApprovalMode('standard'); setSkillIds([]); setPluginIds([]);
    const scope = currentTask ? currentTask.scope || 'server' : selectedScope;
    const target = currentTask?.server_id || serverId;
    setParams(scope === 'fleet' ? {} : target ? { server: target } : { scope: 'server' });
  };
  const selectScope = (scope: AITaskScope) => {
    if (submitting || uncertain || taskId) return;
    setParams(scope === 'fleet' ? {} : serverId ? { server: serverId } : { scope: 'server' }, { replace: true });
  };
  const clearUncertain = (key: string) => setUncertainSubmissions(previous => {
    const next = { ...previous }; delete next[key]; return next;
  });
  const send = async (retry?: Submission) => {
    if (mutation.current || !mounted.current || (!retry && uncertain)) return;
    let value = retry;
    if (!value) {
      const text = prompt.trim(); if (!text) return;
      if (new TextEncoder().encode(text).length > 16000 || text.includes('\0')) { message.error(t('promptTooLong')); return; }
      if (taskId) { if (!currentTask || !canFollowup) return; }
      else if ((selectedScope === 'server' && !chosenServer) || !chosenProvider?.enabled || !chosenProvider.has_api_key || extensionsUnavailable) return;
      const scope = currentTask ? currentTask.scope || 'server' : selectedScope;
      value = { request_id: crypto.randomUUID(), prompt: text, scope, server_id: scope === 'server' ? currentTask?.server_id || serverId : undefined, provider_id: currentTask?.provider_id || providerId, approval_mode: currentTask?.approval_mode || approvalMode, skill_ids: currentTask?.skills?.map(skill => skill.id) || [...skillIds], plugin_ids: currentTask?.plugins?.map(plugin => plugin.id) || [...pluginIds], taskId: taskId || undefined, workspace };
    }
    setSubmitting(true);
    const controller = new AbortController(); mutation.current = controller;
    // A read started before the submission must not restore a completed task
    // over the newer queued/running response.
    detailRequest.current?.abort(); detailRequest.current = null;
    try {
      const response = value.taskId ? await aiApi.message(value.taskId, { message: value.prompt, request_id: value.request_id }, controller.signal)
        : await aiApi.createTask({ scope: value.scope, server_id: value.server_id, provider_id: value.provider_id, prompt: value.prompt, request_id: value.request_id, approval_mode: value.approval_mode, skill_ids: value.skill_ids, plugin_ids: value.plugin_ids } satisfies AITaskInput, controller.signal);
      if (!mounted.current || controller.signal.aborted) return;
      clearUncertain(value.workspace); setPage({ cursor: '', previous: [] });
      setDetail(previous => previous?.task.id === response.data.id ? { ...previous, task: response.data } : previous);
      if (selectedWorkspace.current === value.workspace) { setPrompt(''); setParams({ task: response.data.id }); }
    } catch (error) {
      if (!mounted.current || controller.signal.aborted) return;
      const status = (error as { response?: { status?: number } }).response?.status;
      // A retry can be rejected by a proxy or rate limiter before the server
      // checks the original idempotency key. That does not settle its outcome.
      const retryDeferred = !!retry && (status === 408 || status === 429);
      if (status == null || status >= 500 || retryDeferred) {
        const pending = value; setUncertainSubmissions(previous => ({ ...previous, [pending.workspace]: pending }));
        if (retryDeferred) message.error(errorText(error, t('operationFailed')), 8);
      } else { clearUncertain(value.workspace); message.error(errorText(error, t('operationFailed')), 8); }
    } finally {
      if (mutation.current === controller) mutation.current = null;
      if (mounted.current) {
        setSubmitting(false);
        // Release the mutation lock first: even an idempotent response that
        // already completed needs its events loaded, without active polling.
        await Promise.all([refreshRef.current.readTask(), refreshRef.current.readHistory()]);
      }
    }
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
      content: <div><Typography.Paragraph>{t('confirmHint', { server: task.scope === 'fleet' ? t('fleetTarget') : task.server_name })}</Typography.Paragraph><strong>{action.summary}</strong><pre className="ai-command">{action.command}</pre><pre className="ai-arguments-preview">{argumentsText(action)}</pre><Typography.Text type="warning">{action.risk_reason}</Typography.Text></div>,
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
  const historyPanel = <aside className="ai-sidebar" aria-label={t('history')}><header><span><HistoryOutlined />{t('history')}</span><Button type="text" size="small" icon={<PlusOutlined />} aria-label={t('newTask')} disabled={submitting} onClick={freshTask} /></header>
        {historyError && <Alert type="warning" title={t('loadFailed')} action={<Button size="small" onClick={() => { void readHistory(); }}>{t('retry')}</Button>} />}
        <div className="ai-history-list">{rows.map(task => <button key={task.id} type="button" className={`ai-history-item${task.id === taskId ? ' selected' : ''}`} aria-current={task.id === taskId ? 'page' : undefined} disabled={submitting} onClick={() => selectTask(task.id)}><strong>{task.prompt}</strong><span>{task.scope === 'fleet' ? t('fleetTarget') : task.server_name}</span><footer><Tag color={statusColors[task.status]}>{t(task.status)}</Tag><time>{new Date(task.created_at).toLocaleDateString(i18n.language)}</time></footer></button>)}{!rows.length && <div className="ai-history-empty"><HistoryOutlined /><span>{t('noHistory')}</span></div>}</div>
        <div className="ai-history-paging"><Button size="small" disabled={!page.previous.length} onClick={() => setPage(value => ({ cursor: value.previous[value.previous.length - 1], previous: value.previous.slice(0, -1) }))}>{t('previous')}</Button><Button size="small" disabled={history?.page !== page || !history.next} onClick={() => { if (history?.next) setPage({ cursor: history.next, previous: [...page.previous, page.cursor] }); }}>{t('next')}</Button></div>
      </aside>;
  return <div className="ai-page">
    <div className="page-heading ai-page-heading"><span className="ai-page-icon" aria-hidden="true"><RobotOutlined /></span><div><Typography.Text className="eyebrow">{t('eyebrow')}</Typography.Text><Typography.Title level={2}>{t('title')}</Typography.Title><Typography.Text type="secondary">{t('description')}</Typography.Text></div><Space className="ai-heading-actions" wrap><Button aria-label={t('extensions')} icon={<ApiOutlined />} onClick={() => setExtensionsOpen(true)}>{t('extensions')}</Button><Button aria-label={t('providers')} icon={<SettingOutlined />} onClick={() => setSettings(true)}>{t('providers')}</Button></Space></div>
    {loadError && <Alert type="error" showIcon title={t('loadFailed')} action={<Button size="small" onClick={() => { void readMetadata(); }}>{t('retry')}</Button>} />}
    <div className="ai-workspace">
      {compact ? <><div className="ai-mobile-toolbar"><Button type="text" icon={<HistoryOutlined />} aria-label={t('history')} onClick={() => setHistoryOpen(true)}>{t('history')}</Button><Button type="text" icon={<PlusOutlined />} aria-label={t('newTask')} disabled={submitting} onClick={freshTask}>{t('newTask')}</Button></div><Drawer open={historyOpen} title={t('history')} placement="left" width="min(320px, calc(100vw - 32px))" className="ai-history-drawer" onClose={() => setHistoryOpen(false)}>{historyPanel}</Drawer></> : historyPanel}
      <section className="ai-conversation" aria-label={t('title')}>
        {currentTask ? <header className="ai-task-header"><div><span className="ai-task-server">{currentTask.scope === 'fleet' ? <DashboardOutlined /> : <CloudServerOutlined />}{currentTask.scope === 'fleet' ? t('fleetTarget') : currentTask.server_name}{currentTask.scope !== 'fleet' && <Tag>{currentTask.server_type}</Tag>}</span><small>{currentTask.provider_name} · {currentTask.model}</small><div className="ai-task-capabilities"><Tag className={`ai-mode-tag ai-mode-${currentTask.approval_mode || 'standard'}`} icon={<SafetyCertificateOutlined />}>{t(`mode_${currentTask.approval_mode || 'standard'}`)}</Tag><Link className="ai-panel-link" to="/alerts"><BellOutlined aria-hidden="true" />{t('openAlerts')}</Link>{currentTask.skills?.map(skill => <Tag key={skill.id}>Skill · {skill.name}</Tag>)}{currentTask.plugins?.map(plugin => <Tag key={plugin.id}>MCP · {plugin.name}</Tag>)}</div></div><Space wrap><Tag color={statusColors[currentTask.status]}>{t(currentTask.status)}</Tag><Button type="text" icon={<ReloadOutlined />} aria-label={t('refresh')} onClick={() => { void readTask(); }} />{canCancel && <Button danger icon={<StopOutlined />} aria-label={t('cancelTask')} disabled={actionBusy || submitting} onClick={() => { void act(currentTask.id, signal => aiApi.cancel(currentTask.id, signal), t('cancelRequested')); }}>{t('cancelTask')}</Button>}</Space></header> : null}
        <div className="ai-conversation-body" role="region" aria-label={t('conversation')} tabIndex={0}>
          {!taskId && <div className="ai-welcome"><span className="ai-welcome-icon"><RobotOutlined /></span><Typography.Title level={3}>{t('welcomeTitle')}</Typography.Title><p>{t('welcomeHint')}</p><div className="ai-prompt-examples">{(selectedScope === 'fleet' ? ['exampleFleet', 'exampleMetrics', 'exampleAlert'] : ['exampleInspect', 'exampleInstall', 'exampleServerMetrics']).map(key => <button type="button" key={key} aria-label={t(key)} disabled={submitting || !!uncertain} onClick={() => setPrompt(t(key))}>{selectedScope === 'fleet' ? <DashboardOutlined /> : <CodeOutlined />}{t(key)}</button>)}</div></div>}
          {!taskId && <div className="ai-draft-settings">
          <div className="ai-scope-selector" role="radiogroup" aria-label={t('taskScope')}>{(['fleet', 'server'] as AITaskScope[]).map(scope => <button key={scope} type="button" role="radio" aria-label={t(`scope_${scope}`)} aria-checked={selectedScope === scope} className={selectedScope === scope ? 'selected' : ''} disabled={submitting || !!uncertain} onClick={() => selectScope(scope)}>{scope === 'fleet' ? <DashboardOutlined /> : <CloudServerOutlined />}<span>{t(`scope_${scope}`)}</span></button>)}</div>
          <div className="ai-scope-hint"><span>{t(selectedScope === 'fleet' ? 'fleetHint' : 'serverHint')}</span><Link className="ai-panel-link" to="/alerts"><BellOutlined aria-hidden="true" />{t('openAlerts')}</Link></div>
          {!taskId && <div className={`ai-composer-targets${selectedScope === 'fleet' ? ' ai-fleet-targets' : ''}`}>{selectedScope === 'server' && <label><span>{t('server')}</span><Select aria-label={t('server')} showSearch optionFilterProp="label" placeholder={t('selectServer')} value={chosenServer?.id} onChange={id => setParams({ server: id }, { replace: true })} disabled={submitting || !!uncertain} loading={metadataLoading} options={servers.map(server => ({ value: server.id, label: server.name }))} /></label>}<label><span>{t('provider')}</span><Select aria-label={t('provider')} placeholder={t('selectProvider')} value={chosenProvider?.enabled && chosenProvider.has_api_key ? selectedProviderId : undefined} onChange={setProviderId} disabled={submitting || !!uncertain} loading={metadataLoading} options={enabledProviders.map(provider => ({ value: provider.id, label: `${provider.name} · ${provider.model}` }))} /></label></div>}
          {!taskId && <><div className="ai-permission-selector" role="radiogroup" aria-label={t('approvalMode')}>{(['readonly', 'standard', 'automatic'] as AIApprovalMode[]).map(mode => <button key={mode} type="button" role="radio" aria-checked={selectedMode === mode} className={`ai-permission-option ${selectedMode === mode ? 'selected' : ''} ai-permission-${mode}`} disabled={submitting || !!uncertain} onClick={() => setApprovalMode(mode)}><span><SafetyCertificateOutlined />{t(`mode_${mode}`)}</span><small>{t(`modeBrief_${mode}`)}</small></button>)}</div><p className={`ai-mode-explanation ${selectedMode === 'automatic' ? 'automatic' : ''}`}>{t(`modeHint_${selectedMode}`)}</p><details className="ai-task-extension-picker"><summary><ApiOutlined />{t('taskExtensions')}<span>{selectedSkills.length + selectedPlugins.length ? t('extensionsSelected', { count: selectedSkills.length + selectedPlugins.length }) : t('optional')}</span></summary><div className="ai-composer-targets"><label><span>Skills</span><Select mode="multiple" aria-label="Skills" placeholder={t('selectSkills')} value={selectedSkills} maxCount={8} maxTagCount="responsive" onChange={setSkillIds} disabled={submitting || !!uncertain} options={skills.map(skill => ({ value: skill.id, label: skill.name }))} /></label><label><span>{t('plugins')}</span><Select mode="multiple" aria-label={t('plugins')} placeholder={t('selectPlugins')} value={selectedPlugins} maxCount={4} maxTagCount="responsive" onChange={setPluginIds} disabled={submitting || !!uncertain} options={plugins.filter(plugin => plugin.enabled).map(plugin => ({ value: plugin.id, label: plugin.name }))} /></label></div>{selectedMode === 'readonly' && selectedPlugins.length > 0 && <p>{t('readonlyPlugins')}</p>}<Button size="small" type="link" onClick={() => setExtensionsOpen(true)}>{t('manageExtensions')}</Button></details></>}
          {!taskId && (extensionsError || extensionsUnavailable) && <Alert type="warning" title={t(extensionsError ? 'extensionsLoadFailed' : 'extensionsUnavailable')} action={extensionsError ? <Button size="small" onClick={() => { void readMetadata(); }}>{t('retry')}</Button> : undefined} />}
          {!taskId && selectedProviderId && (!chosenProvider?.enabled || !chosenProvider.has_api_key) && enabledProviders.length > 0 && <Alert type="warning" title={t('providerUnavailable')} />}
          {!taskId && !metadataLoading && !enabledProviders.length && <Alert type="info" title={t('noProvider')} action={<Button onClick={() => setSettings(true)}>{t('addProvider')}</Button>} />}
          </div>}
          {taskId && !current && !detailError && <div className="ai-task-loading"><Spin /></div>}
          {detailError && <Alert type="warning" showIcon title={t(detailError)} action={<Button onClick={() => { void readTask(); }}>{t('retry')}</Button>} />}
          {currentTask?.error && <Alert type="error" showIcon title={currentTask.error} />}
          {currentTask && ['cancel_requested', 'canceled', 'interrupted'].includes(currentTask.status) && <Alert type="info" showIcon title={t(currentTask.status === 'canceled' ? 'canceledHint' : 'cancelHint')} />}
          {current && <div className="ai-timeline">{timeline.map(item => item.action ? <ActionCard key={item.key} action={item.action} task={current.task} canDecide={current.task.status === 'awaiting_approval'} busy={actionBusy || submitting} onDecision={decide} /> : item.event && <article key={item.key} className={`ai-event ai-event-${item.event.kind}`}><header><span>{item.event.kind === 'assistant' && <RobotOutlined />}{t(item.event.kind)}</span><time>{formatDateTime(item.event.created_at, i18n.language)}</time></header>{item.event.kind === 'assistant' ? <AIMarkdown>{item.event.content}</AIMarkdown> : <div>{item.event.content}</div>}</article>)}{!timeline.length && <p className="ai-event-waiting">{t('noEvents')}</p>}</div>}
        </div>
        {currentTask && <div className="ai-task-usage"><span>{t('tokens')}: {t('inputTokens')} {currentTask.prompt_tokens.toLocaleString()} · {t('outputTokens')} {currentTask.completion_tokens.toLocaleString()}</span><span>{t('updated', { time: new Date(currentTask.updated_at).toLocaleTimeString(i18n.language) })}</span></div>}
        <div className="ai-composer">
          {uncertain && <Alert type="warning" showIcon title={t('submitUncertain')} action={<Button disabled={submitting} loading={submitting} aria-label={t('retrySubmission')} onClick={() => { void send(uncertain); }}>{t('retrySubmission')}</Button>} />}
          {(!taskId || canFollowup) ? <><Input.TextArea aria-label={t('taskPrompt')} value={uncertain?.prompt ?? prompt} onChange={event => setPrompt(event.target.value)} placeholder={t(taskId ? 'followupPlaceholder' : selectedScope === 'fleet' ? 'fleetPlaceholder' : 'placeholder')} autoSize={{ minRows: 2, maxRows: 3 }} maxLength={16000} disabled={submitting || !!uncertain} /><div className="ai-composer-footer"><p><SafetyCertificateOutlined />{currentTask ? t(currentTask.scope === 'fleet' ? 'shareFleetSelected' : 'shareSelected', { server: currentTask.server_name, provider: currentTask.provider_name }) : selectedScope === 'fleet' && chosenProvider ? t('shareFleetSelected', { provider: chosenProvider.name }) : chosenServer && chosenProvider ? t('shareSelected', { server: chosenServer.name, provider: chosenProvider.name }) : t('shareHint')}</p><Button type="primary" icon={<SendOutlined />} aria-label={t(taskId ? 'followup' : 'submit')} aria-busy={submitting} loading={submitting} disabled={submitting || actionBusy || !!uncertain || !prompt.trim() || (!taskId && ((selectedScope === 'server' && !chosenServer) || !chosenProvider?.enabled || !chosenProvider.has_api_key || extensionsUnavailable))} onClick={() => { void send(); }}>{t(taskId ? 'followup' : 'submit')}</Button></div></> : currentTask ? <p className="ai-composer-state">{t(activeStatuses.includes(currentTask.status) ? currentTask.status === 'awaiting_input' ? 'waiting' : 'working' : 'readOnly')}</p> : null}
          <small className="ai-background-note">{t('leaveHint')}{currentTask && canCancel ? ` ${t('cancelHint')}` : ''}</small>
        </div>
      </section>
    </div>
    {settings && <ProviderSettings providers={providers} onChanged={readMetadata} onClose={() => setSettings(false)} />}
    {extensionsOpen && <AIExtensions skills={skills} plugins={plugins} onChanged={readMetadata} onClose={() => setExtensionsOpen(false)} />}
  </div>;
}
