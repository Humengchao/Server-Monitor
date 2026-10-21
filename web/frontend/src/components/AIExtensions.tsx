import { apiError } from '../utils/apiError';
import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Empty, Form, Input, Modal, Space, Switch, Tabs, Tag, Typography } from 'antd';
import { ApiOutlined, BookOutlined, DeleteOutlined, EditOutlined, ExperimentOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { aiApi } from '../api/ai';
import type { AIPlugin, AIPluginInput, AISkill, AISkillInput } from '../api/ai';
import AIMarkdown from './AIMarkdown';

export default function AIExtensions({ skills, plugins, onChanged, onClose }: { skills: AISkill[]; plugins: AIPlugin[]; onChanged: () => Promise<void>; onClose: () => void }) {
  const { t } = useTranslation('ai');
  const { message, modal } = App.useApp();
  const [tab, setTab] = useState('skills');
  const [skill, setSkill] = useState<AISkill | 'new' | null>(null);
  const [plugin, setPlugin] = useState<AIPlugin | 'new' | null>(null);
  const [preview, setPreview] = useState(false);
  const [busy, setBusy] = useState(false);
  const [toolCounts, setToolCounts] = useState<Record<string, number>>({});
  const [skillForm] = Form.useForm<AISkillInput>();
  const [pluginForm] = Form.useForm<AIPluginInput>();
  const content = Form.useWatch('content', skillForm);
  const pluginURL = Form.useWatch('url', pluginForm);
  const clearToken = Form.useWatch('clear_token', pluginForm);
  const mounted = useRef(false);
  const request = useRef<AbortController | null>(null);
  const confirmation = useRef<ReturnType<typeof modal.confirm> | null>(null);
  const upload = useRef<HTMLInputElement>(null);
  const importGeneration = useRef(0);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; request.current?.abort(); confirmation.current?.destroy(); }; }, []);
  const mutate = async (operation: (signal: AbortSignal) => Promise<unknown>, success: string) => {
    if (request.current || !mounted.current) return false;
    const controller = new AbortController(); request.current = controller; setBusy(true);
    try { await operation(controller.signal); if (!mounted.current || controller.signal.aborted) return false; message.success(success); await onChanged(); return mounted.current && !controller.signal.aborted; }
    catch (error) { if (mounted.current && !controller.signal.aborted) message.error(apiError(error, t('operationFailed'))); return false; }
    finally { if (request.current === controller) request.current = null; if (mounted.current) setBusy(false); }
  };
  const editSkill = (value: AISkill | 'new') => { importGeneration.current++; skillForm.resetFields(); setSkill(value); setPreview(false); skillForm.setFieldsValue(value === 'new' ? { name: '', description: '', content: '' } : value); };
  const editPlugin = (value: AIPlugin | 'new') => { pluginForm.resetFields(); setPlugin(value); pluginForm.setFieldsValue(value === 'new' ? { name: '', url: '', bearer_token: '', clear_token: false, enabled: true } : { ...value, bearer_token: '', clear_token: false }); };
  const remove = (value: AISkill | AIPlugin, type: 'skills' | 'plugins') => {
    confirmation.current = modal.confirm({ title: t('deleteExtension'), content: t('deleteExtensionHint', { name: value.name }), okButtonProps: { danger: true }, onOk: async () => {
      if (!await mutate(signal => type === 'skills' ? aiApi.deleteSkill(value.id, signal) : aiApi.deletePlugin(value.id, signal), t('extensionRemoved'))) throw new Error('delete failed');
      if (type === 'skills' && skill && skill !== 'new' && skill.id === value.id) { importGeneration.current++; setSkill(null); skillForm.resetFields(); }
      if (type === 'plugins' && plugin && plugin !== 'new' && plugin.id === value.id) { setPlugin(null); pluginForm.resetFields(); }
    } });
  };
  const importMarkdown = async (file?: File) => {
    if (!file) return;
    const generation = ++importGeneration.current;
    if (file.size > 32768) { message.error(t('skillTooLarge')); return; }
    try {
      const text = await file.text(); if (!mounted.current || generation !== importGeneration.current) return;
      skillForm.setFieldsValue({ content: text, ...(!skillForm.getFieldValue('name') ? { name: file.name.replace(/\.(md|markdown|txt)$/i, '').slice(0, 128) } : {}) });
      setPreview(false);
    } catch { if (mounted.current && generation === importGeneration.current) message.error(t('importFailed')); }
  };
  const needsNewToken = plugin && plugin !== 'new' && plugin.has_bearer_token && pluginURL?.trim() !== plugin.url;
  return <Modal open title={<Space><ApiOutlined />{t('extensions')}</Space>} className="ai-extension-modal" width={850} onCancel={() => { if (!busy) onClose(); }} footer={null} destroyOnHidden>
    <Typography.Paragraph type="secondary">{t('extensionsHint')}</Typography.Paragraph>
    <Tabs activeKey={tab} onChange={value => { if (!busy) setTab(value); }} items={[
      { key: 'skills', label: <Space><BookOutlined />Skills</Space>, children: <>
        <Typography.Paragraph type="secondary">{t('skillsHint')}</Typography.Paragraph>
        <div className="ai-extension-list">{skills.map(value => <article className="ai-extension-row" key={value.id}><span className="ai-extension-icon"><BookOutlined /></span><div><strong>{value.name}</strong><p>{value.description || t('markdownInstructions')}</p></div><Space><Button size="small" disabled={busy} icon={<EditOutlined />} aria-label={`${t('edit')}: ${value.name}`} onClick={() => editSkill(value)} /><Button size="small" disabled={busy} danger icon={<DeleteOutlined />} aria-label={`${t('remove')}: ${value.name}`} onClick={() => remove(value, 'skills')} /></Space></article>)}</div>
        {!skills.length && !skill && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('noSkills')} />}
        {!skill ? <Button icon={<PlusOutlined />} aria-label={t('addSkill')} disabled={busy} onClick={() => editSkill('new')}>{t('addSkill')}</Button> : <Form form={skillForm} layout="vertical" disabled={busy} className="ai-extension-form" onFinish={async values => {
          importGeneration.current++;
          const payload = { ...values, name: values.name.trim(), description: values.description?.trim() || '' };
          if (await mutate(signal => skill === 'new' ? aiApi.createSkill(payload, signal) : aiApi.updateSkill(skill.id, { ...payload, expected_updated_at: skill.updated_at }, signal), t('skillSaved'))) { setSkill(null); skillForm.resetFields(); }
        }}>
          <Form.Item name="name" label={t('skillName')} rules={[{ required: true, whitespace: true, message: t('required') }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="description" label={t('skillDescription')}><Input maxLength={512} /></Form.Item>
          <div className="ai-skill-toolbar"><Space><Button size="small" icon={<UploadOutlined />} aria-label={t('importMarkdown')} onClick={() => upload.current?.click()}>{t('importMarkdown')}</Button><Button size="small" onClick={() => setPreview(value => !value)}>{t(preview ? 'editMarkdown' : 'previewMarkdown')}</Button></Space><span>Markdown · 32 KB</span></div>
          <input className="ai-file-input" type="file" ref={upload} accept=".md,.markdown,.txt,text/markdown,text/plain" aria-label={t('importMarkdown')} onChange={event => { void importMarkdown(event.target.files?.[0]); event.target.value = ''; }} />
          <Form.Item name="content" label={t('skillContent')} rules={[{ required: true, whitespace: true, message: t('required') }, { validator: (_, value) => new TextEncoder().encode(value || '').length <= 32768 ? Promise.resolve() : Promise.reject(new Error(t('skillTooLarge'))) }]} hidden={preview}><Input.TextArea rows={12} placeholder={t('skillPlaceholder')} className="ai-skill-editor" onChange={() => { importGeneration.current++; }} /></Form.Item>
          {preview && <div className="ai-skill-preview"><AIMarkdown>{content || t('emptyPreview')}</AIMarkdown></div>}
          <Space><Button type="primary" htmlType="submit" loading={busy} aria-label={t('saveSkill')} aria-busy={busy}>{t('saveSkill')}</Button><Button onClick={() => { importGeneration.current++; setSkill(null); skillForm.resetFields(); }}>{t('cancel')}</Button></Space>
        </Form>}
      </> },
      { key: 'plugins', label: <Space><ApiOutlined />{t('plugins')}</Space>, children: <>
        <Typography.Paragraph type="secondary">{t('pluginsHint')}</Typography.Paragraph>
        <div className="ai-extension-list">{plugins.map(value => <article className="ai-extension-row" key={value.id}><span className="ai-extension-icon"><ApiOutlined /></span><div><strong>{value.name}</strong><p>{value.url}</p><Tag>{t(value.enabled ? 'enabled' : 'disabled')}</Tag>{toolCounts[value.id] != null && <Tag>{t('pluginToolCount', { count: toolCounts[value.id] })}</Tag>}</div><Space wrap><Button size="small" disabled={busy} icon={<ExperimentOutlined />} aria-label={`${t('testPlugin')}: ${value.name}`} onClick={() => { void mutate(async signal => { const result = await aiApi.testPlugin(value.id, signal); if (mounted.current && !signal.aborted && result.data.tool_count != null) setToolCounts(counts => ({ ...counts, [value.id]: result.data.tool_count! })); }, t('pluginTestPassed')); }}>{t('testPlugin')}</Button><Button size="small" disabled={busy} icon={<EditOutlined />} aria-label={`${t('edit')}: ${value.name}`} onClick={() => editPlugin(value)} /><Button size="small" disabled={busy} danger icon={<DeleteOutlined />} aria-label={`${t('remove')}: ${value.name}`} onClick={() => remove(value, 'plugins')} /></Space></article>)}</div>
        {!plugins.length && !plugin && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('noPlugins')} />}
        {!plugin ? <Button icon={<PlusOutlined />} aria-label={t('addPlugin')} disabled={busy} onClick={() => editPlugin('new')}>{t('addPlugin')}</Button> : <Form form={pluginForm} layout="vertical" disabled={busy} className="ai-extension-form" onFinish={async values => {
          const payload = { ...values, name: values.name.trim(), url: values.url.trim(), bearer_token: values.clear_token ? '' : values.bearer_token || '', clear_token: !!values.clear_token };
          if (await mutate(signal => plugin === 'new' ? aiApi.createPlugin(payload, signal) : aiApi.updatePlugin(plugin.id, { ...payload, expected_updated_at: plugin.updated_at }, signal), t('pluginSaved'))) { setPlugin(null); pluginForm.resetFields(); }
        }}>
          <Form.Item name="name" label={t('pluginName')} rules={[{ required: true, whitespace: true, message: t('required') }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="url" label={t('pluginURL')} rules={[{ required: true, message: t('required') }, { validator: (_, value) => { try { const url = new URL(value); return url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash ? Promise.resolve() : Promise.reject(new Error(t('invalidURL'))); } catch { return Promise.reject(new Error(t('invalidURL'))); } } }]}><Input placeholder="https://mcp.example.com/mcp" autoComplete="off" maxLength={2048} /></Form.Item>
          <Form.Item name="bearer_token" label={t('pluginToken')} dependencies={['url', 'clear_token']} extra={plugin !== 'new' && plugin.has_bearer_token ? t('endpointToken') : t('optionalToken')} rules={[{ required: !!needsNewToken && !clearToken, message: t('required') }]}><Input.Password disabled={busy || clearToken} autoComplete="new-password" maxLength={4096} placeholder={plugin !== 'new' && plugin.has_bearer_token ? t('keyKept') : undefined} /></Form.Item>
          {plugin !== 'new' && plugin.has_bearer_token && <Form.Item name="clear_token" label={t('clearToken')} valuePropName="checked" htmlFor=""><Switch aria-label={t('clearToken')} onChange={() => pluginForm.setFieldValue('bearer_token', '')} /></Form.Item>}
          <Form.Item name="enabled" label={t('enabled')} valuePropName="checked" htmlFor=""><Switch aria-label={t('enabled')} /></Form.Item>
          <Alert showIcon type="info" title={t('pluginScopeHint')} />
          <Space><Button type="primary" htmlType="submit" loading={busy} aria-label={t('savePlugin')} aria-busy={busy}>{t('savePlugin')}</Button><Button onClick={() => { setPlugin(null); pluginForm.resetFields(); }}>{t('cancel')}</Button></Space>
        </Form>}
      </> },
    ]} />
  </Modal>;
}
