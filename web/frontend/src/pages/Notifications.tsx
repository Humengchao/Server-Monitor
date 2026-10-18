import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Empty, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Tabs, Tag, Typography } from 'antd';
import { DeleteOutlined, EditOutlined, ExperimentOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { notificationsApi } from '../api/notifications';
import type { DeliveryStatus, NotificationChannel, NotificationChannelInput, NotificationDelivery, NotificationType } from '../api/notifications';
import { usePolling } from '../hooks/usePolling';
import { formatDateTime } from '../utils/format';
import '../i18n/notifications';

const types: NotificationType[] = ['telegram', 'wecom', 'dingtalk', 'email', 'webhook'];
const statuses: DeliveryStatus[] = ['pending', 'sending', 'retry', 'succeeded', 'failed', 'canceled'];
const colors: Record<DeliveryStatus, string> = { pending: 'default', sending: 'processing', retry: 'warning', succeeded: 'success', failed: 'error', canceled: 'default' };
const docs: Partial<Record<NotificationType, string>> = {
  telegram: 'https://core.telegram.org/bots/api#sendmessage', wecom: 'https://developer.work.weixin.qq.com/document/path/91770',
  dingtalk: 'https://open.dingtalk.com/document/robots/customize-robot-security-settings',
};
function errorText(error: unknown, fallback: string) { return (error as { response?: { data?: { error?: string } } })?.response?.data?.error || fallback; }

export default function Notifications() {
  const { t, i18n } = useTranslation('notifications');
  const { message, modal } = App.useApp();
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [tab, setTab] = useState('channels');
  const [page, setPage] = useState<{ status?: DeliveryStatus; channel_id?: string; cursor: string; previous: string[] }>({ cursor: '', previous: [] });
  const [result, setResult] = useState<{ page: typeof page; rows: NotificationDelivery[]; next?: string }>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel>();
  const [form] = Form.useForm<NotificationChannelInput>();
  const kind = Form.useWatch('type', form) as NotificationType | undefined;
  const tlsMode = Form.useWatch(['config', 'tls_mode'], form);
  const request = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; request.current?.abort(); }; }, []);
  const load = useCallback(async () => {
    if (!mounted.current || busyRef.current) return;
    request.current?.abort(); const controller = new AbortController(); request.current = controller; setLoading(true);
    try {
      const [channelResponse, deliveryResponse] = await Promise.all([
        notificationsApi.channels(controller.signal), notificationsApi.deliveries(page, controller.signal),
      ]);
      if (controller.signal.aborted || !mounted.current || request.current !== controller) return;
      setChannels(channelResponse.data); setResult({ page, rows: deliveryResponse.data.deliveries, next: deliveryResponse.data.next_cursor }); setLoadError(false);
    } catch { if (!controller.signal.aborted && mounted.current) setLoadError(true); }
    finally { if (mounted.current && request.current === controller) { request.current = null; setLoading(false); } }
  }, [page]);
  const loadRef = useRef(load);
  useEffect(() => { loadRef.current = load; const timer = window.setTimeout(() => void load(), 0); return () => { window.clearTimeout(timer); request.current?.abort(); }; }, [load]);
  usePolling(load, 10000, { leading: false });

  const mutate = async (action: () => Promise<unknown>, success?: string) => {
    if (busyRef.current) return false;
    busyRef.current = true; setBusy(true); request.current?.abort();
    try { await action(); if (mounted.current && success) message.success(success); return true; }
    catch (error) { if (mounted.current) message.error(errorText(error, t('failed'))); return false; }
    finally { busyRef.current = false; if (mounted.current) { setBusy(false); void loadRef.current(); } }
  };
  const edit = (channel?: NotificationChannel) => {
    setEditing(channel); form.resetFields();
    form.setFieldsValue(channel ? { name: channel.name, type: channel.type, enabled: channel.enabled, config: channel.config } : { type: 'telegram', enabled: true, config: {} });
    setOpen(true);
  };
  const save = async (values: NotificationChannelInput) => {
    const payload = { ...values, config: Object.fromEntries(Object.entries(values.config || {}).filter(([, value]) => value != null)) };
    if (values.type === 'telegram') payload.config.message_thread_id = values.config?.message_thread_id || 0;
    const saved = await mutate(() => editing ? notificationsApi.update(editing.id, payload) : notificationsApi.create(payload), t('saved'));
    if (saved && mounted.current) { setOpen(false); form.resetFields(); }
  };
  const secret = (field: 'bot_token' | 'webhook_url' | 'secret' | 'password' | 'bearer_token', required = false) => <Form.Item
    name={['config', field]} label={t(field)} rules={[{ required: required && !editing?.secret_fields.includes(field), message: t('required') }]}>
    <Input.Password autoComplete="new-password" placeholder={editing?.secret_fields.includes(field) ? t('kept') : undefined} />
  </Form.Item>;
  const valueTime = (value: string | null) => value ? formatDateTime(value, i18n.language) : '—';
  const rowActions = (channel: NotificationChannel) => <Space>
    <Button size="small" icon={<ExperimentOutlined />} disabled={busy || !channel.enabled} onClick={() => void mutate(() => notificationsApi.test(channel.id), t('testQueued'))}>{t('test')}</Button>
    <Button size="small" aria-label={t('edit')} icon={<EditOutlined />} disabled={busy} onClick={() => edit(channel)} />
    <Button size="small" danger aria-label={t('remove')} icon={<DeleteOutlined />} disabled={busy} onClick={() => modal.confirm({
      title: t('remove'), content: t('deleteConfirm'), okButtonProps: { danger: true },
      onOk: async () => { if (!await mutate(() => notificationsApi.remove(channel.id), t('deleted'))) throw new Error('delete failed'); },
    })} />
  </Space>;
  const rows = result?.page === page ? result.rows : [];

  return <div style={{ padding: 24 }}>
    <Space style={{ width: '100%', justifyContent: 'space-between', flexWrap: 'wrap' }}>
      <div><Typography.Title level={3}>{t('title')}</Typography.Title><Typography.Paragraph type="secondary">{t('hint')}</Typography.Paragraph></div>
      <Space><Button icon={<ReloadOutlined />} loading={loading} disabled={busy} onClick={() => void load()}>{t('refresh')}</Button><Button type="primary" icon={<PlusOutlined />} disabled={busy} onClick={() => edit()}>{t('add')}</Button></Space>
    </Space>
    {loadError && <Alert type="error" showIcon title={t('loadFailed')} style={{ marginBottom: 16 }} />}
    <Tabs activeKey={tab} onChange={setTab} items={[
      { key: 'channels', label: t('channels'), children: <Card><Table<NotificationChannel> rowKey="id" dataSource={channels} loading={loading && !channels.length} pagination={false}
        scroll={{ x: 700 }} locale={{ emptyText: <Empty description={t('noChannels')} /> }} columns={[
          { title: t('name'), dataIndex: 'name' }, { title: t('type'), render: (_, channel) => t(channel.type) },
          { title: t('enabled'), render: (_, channel) => <Switch checked={channel.enabled} disabled={busy} checkedChildren={t('enabled')} unCheckedChildren={t('paused')}
            onChange={enabled => void mutate(() => notificationsApi.update(channel.id, { enabled }))} /> },
          { title: '', render: (_, channel) => rowActions(channel) },
        ]} /></Card> },
      { key: 'deliveries', label: t('deliveries'), children: <Card>
        <Alert type="info" title={t('deliveryHint')} style={{ marginBottom: 16 }} />
        <Space wrap style={{ marginBottom: 16 }}>
          <Select aria-label={t('status')} allowClear placeholder={t('status')} style={{ width: 180 }} value={page.status}
            options={statuses.map(status => ({ value: status, label: t(status === 'failed' ? 'failedStatus' : status) }))}
            onChange={status => setPage(current => ({ ...current, status, cursor: '', previous: [] }))} />
          <Select aria-label={t('channel')} allowClear placeholder={t('channel')} style={{ width: 200 }} value={page.channel_id}
            options={channels.map(channel => ({ value: channel.id, label: channel.name }))}
            onChange={channel_id => setPage(current => ({ ...current, channel_id, cursor: '', previous: [] }))} />
        </Space>
        <Table<NotificationDelivery> rowKey="id" dataSource={rows} loading={loading && !rows.length} pagination={false} scroll={{ x: 1050 }}
          locale={{ emptyText: <Empty description={t('noDeliveries')} /> }} columns={[
            { title: '#', render: (_, row) => <Space orientation="vertical"><span>{row.id}</span>{row.retry_of && <Typography.Text type="secondary">{t('retryOf', { id: row.retry_of })}</Typography.Text>}</Space> },
            { title: t('channel'), dataIndex: 'channel_name' },
            { title: t('subject'), width: 240, render: (_, row) => <Space orientation="vertical" size={0}><strong>{row.subject || row.transition}</strong><Typography.Text type="secondary">{row.message}</Typography.Text></Space> },
            { title: t('status'), render: (_, row) => <Tag color={colors[row.status]}>{t(row.status === 'failed' ? 'failedStatus' : row.status)}</Tag> },
            { title: t('attempts'), dataIndex: 'attempts' },
            { title: t('created'), render: (_, row) => valueTime(row.created_at) },
            { title: t('lastAttempt'), render: (_, row) => valueTime(row.last_attempt_at) },
            { title: t('nextAttempt'), render: (_, row) => valueTime(row.next_attempt_at) },
            { title: t('error'), dataIndex: 'last_error', width: 240 },
            { title: '', render: (_, row) => <Button size="small" disabled={busy || !row.channel_id || row.status === 'sending'} onClick={() => modal.confirm({
              title: t('resendTitle'), content: t('resendConfirm'), okText: t('resend'), onOk: async () => {
                if (!await mutate(() => notificationsApi.retry(row.id), t('resendQueued'))) throw new Error('retry failed');
              },
            })}>{t('resend')}</Button> },
          ]} />
        <Space style={{ marginTop: 16 }}>
          <Button disabled={loading || !page.previous.length} onClick={() => setPage(current => ({ ...current, cursor: current.previous[current.previous.length - 1], previous: current.previous.slice(0, -1) }))}>{t('previous')}</Button>
          <span>{t('page', { number: page.previous.length + 1 })}</span>
          <Button disabled={loading || result?.page !== page || !result.next} onClick={() => { if (result?.next) setPage({ ...page, cursor: result.next, previous: [...page.previous, page.cursor] }); }}>{t('next')}</Button>
        </Space>
      </Card> },
    ]} />
    <Modal open={open} title={t(editing ? 'edit' : 'add')} onCancel={() => { if (!busy) setOpen(false); }} onOk={() => form.submit()} confirmLoading={busy} okText={t('save')} cancelText={t('cancel')} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={save} disabled={busy} preserve={false} onValuesChange={changed => {
        if (changed.type) { form.setFieldValue('config', {}); form.setFieldValue('clear_secret_fields', []); }
      }}>
        <Form.Item name="name" label={t('name')} rules={[{ required: true, whitespace: true, message: t('required') }]}><Input maxLength={128} /></Form.Item>
        <Form.Item name="type" label={t('type')}><Select disabled={!!editing} options={types.map(type => ({ value: type, label: t(type) }))} /></Form.Item>
        <Alert type="info" title={t('secretHint')} style={{ marginBottom: 16 }} />
        {kind === 'telegram' && <>
          {secret('bot_token', true)}<Form.Item name={['config', 'chat_id']} label={t('chat_id')} rules={[{ required: true, message: t('required') }]}><Input /></Form.Item>
          <Form.Item name={['config', 'message_thread_id']} label={t('message_thread_id')}><InputNumber min={1} precision={0} style={{ width: '100%' }} /></Form.Item>
        </>}
        {(kind === 'wecom' || kind === 'dingtalk' || kind === 'webhook') && secret('webhook_url', true)}
        {(kind === 'wecom' || kind === 'dingtalk') && <Typography.Paragraph type="secondary">{t('robotHint')}</Typography.Paragraph>}
        {kind === 'dingtalk' && secret('secret')}
        {kind === 'webhook' && secret('bearer_token')}
        {kind === 'email' && <>
          <Alert type="info" title={t('smtpHint')} style={{ marginBottom: 16 }} />
          <Form.Item name={['config', 'smtp_host']} label={t('smtp_host')} rules={[{ required: true }]}><Input placeholder="smtp.example.com" /></Form.Item>
          <Form.Item name={['config', 'tls_mode']} label={t('tls_mode')}><Select placeholder={t('starttls')} options={['starttls', 'tls'].map(value => ({ value, label: t(value) }))} /></Form.Item>
          <Form.Item name={['config', 'smtp_port']} label={t('smtp_port')}><InputNumber min={1} max={65535} precision={0} placeholder={tlsMode === 'tls' ? '465' : '587'} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name={['config', 'username']} label={t('username')}><Input autoComplete="off" /></Form.Item>{secret('password')}
          <Form.Item name={['config', 'from']} label={t('from')} rules={[{ required: true }, { type: 'email' }]}><Input /></Form.Item>
          <Form.Item name={['config', 'to']} label={t('to')} rules={[{ required: true }]}><Select mode="tags" tokenSeparators={[',', ';']} /></Form.Item>
        </>}
        {editing && ['dingtalk', 'webhook', 'email'].includes(editing.type) && <Form.Item name="clear_secret_fields" label={t('clearSecrets')}><Select mode="multiple" allowClear options={editing.secret_fields.filter(field => ['secret', 'bearer_token', 'password'].includes(field)).map(value => ({ value, label: t(value) }))} /></Form.Item>}
        <Form.Item name="enabled" label={t('enabled')} valuePropName="checked"><Switch /></Form.Item>
        {kind && docs[kind] && <Typography.Link href={docs[kind]} target="_blank" rel="noreferrer">{t('docs')}</Typography.Link>}
      </Form>
    </Modal>
  </div>;
}
