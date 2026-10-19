import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, App, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Table, Tag, Typography } from 'antd';
import { Line, LineChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { serversApi } from '../api/servers';
import type { Server } from '../api/servers';
import { networkApi } from '../api/network';
import type { NetworkCheck, NetworkCheckInput, NetworkHistory } from '../api/network';
import { usePolling } from '../hooks/usePolling';
import '../i18n/networkLogs';
import './Operations.css';

const number = (value: number | null | undefined, suffix = '') => value == null ? '—' : `${value.toFixed(2)}${suffix}`;
const colors = ['#4f7cff', '#18b690', '#e8944a', '#d9639b'];
export default function Network() {
  const { t } = useTranslation('networkLogs');
  const { message } = App.useApp();
  const [params] = useSearchParams();
  const target = params.get('server') || '';
  const [checks, setChecks] = useState<NetworkCheck[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<NetworkCheck | 'new' | null>(null);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [selection, setSelection] = useState<{ target: string; ids: string[] } | null>(null);
  const selected = useMemo(() => selection?.target === target ? selection.ids : [], [selection, target]);
  const [hours, setHours] = useState(1);
  const historyKey = JSON.stringify([target, selected, hours]);
  const [historyResult, setHistoryResult] = useState<{ key: string; data: Record<string, NetworkHistory> } | null>(null);
  const history = useMemo(() => historyResult?.key === historyKey ? historyResult.data : {}, [historyResult, historyKey]);
  const [historyError, setHistoryError] = useState(false);
  const [form] = Form.useForm<NetworkCheckInput>();
  const request = useRef<AbortController | null>(null);
  const actions = useRef(new Set<AbortController>());
  const mounted = useRef(false);
  const load = useCallback(async () => {
    if (!mounted.current || request.current) return;
    const controller = new AbortController(); request.current = controller;
    try {
      const [list, hosts] = await Promise.all([networkApi.list(controller.signal), serversApi.list(controller.signal)]);
      if (controller.signal.aborted) return;
      setChecks(list.data || []); setServers(hosts.data || []); setError(false);
      setSelection(current => ({ target, ids: current?.target === target ? current.ids.filter(id => list.data.some(item => item.id === id))
        : list.data.filter(item => !target || item.target_server_id === target).slice(0, 4).map(item => item.id) }));
    } catch { if (!controller.signal.aborted) setError(true); }
    finally { if (request.current === controller) { request.current = null; if (!controller.signal.aborted) setLoading(false); } }
  }, [target]);
  useEffect(() => {
    mounted.current = true; const timer = window.setTimeout(() => { void load(); }, 0);
    const active = actions.current;
    return () => { mounted.current = false; window.clearTimeout(timer); request.current?.abort(); request.current = null; for (const controller of active) controller.abort(); };
  }, [load]);
  usePolling(load, 15000, { leading: false });
  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      setHistoryResult(null); setHistoryError(false);
      const until = new Date(), since = new Date(until.getTime() - hours * 3600000);
      try {
        const results = await Promise.all(selected.map(async id => [id, (await networkApi.history(id, since.toISOString(), until.toISOString(), controller.signal)).data] as const));
        if (!controller.signal.aborted) setHistoryResult({ key: historyKey, data: Object.fromEntries(results) });
      } catch { if (!controller.signal.aborted) setHistoryError(true); }
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [selected, hours, checks, historyKey]);
  const serverName = (id: string | null) => id ? servers.find(server => server.id === id)?.name || id : t('local');
  const paused = (check: NetworkCheck) => !check.enabled || servers.some(server =>
    (server.id === check.target_server_id || server.id === check.source_server_id)
      && (server as Server & { monitoring_enabled?: boolean }).monitoring_enabled === false);
  const open = (check: NetworkCheck | 'new') => {
    form.resetFields(); form.setFieldsValue(check === 'new' ? { name: '', source_server_id: null, target_server_id: params.get('server') || undefined, packets: 5, enabled: true, loss_threshold: 10, latency_threshold_ms: null } : check);
    setEditing(check);
  };
  const save = async (values: NetworkCheckInput) => {
    setSaving(true);
    try {
      const value = { ...values, source_server_id: values.source_server_id || null, latency_threshold_ms: values.latency_threshold_ms ?? null };
      if (editing === 'new') await networkApi.create(value); else if (editing) await networkApi.update(editing.id, value);
      if (mounted.current) { setEditing(null); await load(); }
    } catch { if (mounted.current) message.error(t('failed')); }
    finally { if (mounted.current) setSaving(false); }
  };
  const run = async (id: string) => {
    if (busy) return;
    const controller = new AbortController(); actions.current.add(controller); setBusy(id);
    try { await networkApi.run(id, controller.signal); if (!controller.signal.aborted) await load(); }
    catch { if (!controller.signal.aborted) message.error(t('failed')); }
    finally { actions.current.delete(controller); if (mounted.current) setBusy(null); }
  };
  const chart = useMemo(() => {
    const rows = new Map<number, Record<string, number | null>>();
    for (const [id, result] of Object.entries(history)) for (const sample of result.samples) {
      const time = Math.floor(Date.parse(sample.recorded_at) / 60000) * 60000;
      const row = rows.get(time) || { time }; row[id] = sample.avg_ms; rows.set(time, row);
    }
    return [...rows.values()].sort((a, b) => a.time! - b.time!);
  }, [history]);
  return <div className="operations-page network-page">
    <div className="page-heading"><div><Typography.Title level={2}>{t('network')}</Typography.Title><Typography.Text type="secondary">{t('networkHint')}</Typography.Text></div><Space><Button onClick={() => { void load(); }}>{t('refresh')}</Button><Button type="primary" onClick={() => open('new')}>{t('add')}</Button></Space></div>
    {error && <Alert type="warning" title={t('failed')} showIcon />}
    <Card className="panel-card"><Table<NetworkCheck> rowKey="id" loading={loading} dataSource={checks} scroll={{ x: 1100 }} pagination={{ pageSize: 20 }} columns={[
      { title: t('name'), dataIndex: 'name' },
      { title: t('source'), render: (_, check) => serverName(check.source_server_id) },
      { title: t('target'), render: (_, check) => serverName(check.target_server_id) },
      { title: t('status'), render: (_, check) => <Space direction="vertical" size={0}><Tag color={paused(check) ? 'default' : check.latest?.threshold_exceeded ? 'error' : check.latest?.status === 'ok' ? 'success' : 'default'}>{paused(check) ? t('paused') : t(check.latest?.status === 'loss' ? 'lossState' : check.latest?.status || 'unknown')}</Tag>{check.latest?.error_code && <small>{t(check.latest.error_code, { defaultValue: check.latest.error_code })}</small>}</Space> },
      { title: t('sent'), render: (_, check) => check.latest?.sent == null ? '—' : `${check.latest.sent} / ${check.latest.received}` },
      { title: t('loss'), render: (_, check) => number(check.latest?.loss_percent, '%') },
      { title: t('avg'), render: (_, check) => number(check.latest?.avg_ms, ' ms') },
      { title: t('minmax'), render: (_, check) => `${number(check.latest?.min_ms)} / ${number(check.latest?.max_ms)} ms` },
      { title: t('mdev'), render: (_, check) => number(check.latest?.mdev_ms, ' ms') },
      { title: t('time'), render: (_, check) => check.latest ? new Date(check.latest.recorded_at).toLocaleTimeString() : '—' },
      { title: '', render: (_, check) => <Space><Button size="small" disabled={paused(check) || !!busy} loading={busy === check.id} onClick={() => { void run(check.id); }}>{t('run')}</Button><Button size="small" onClick={() => open(check)}>{t('edit')}</Button><Popconfirm title={t('confirm')} onConfirm={async () => { try { await networkApi.delete(check.id); await load(); } catch { message.error(t('failed')); } }}><Button size="small" danger>{t('remove')}</Button></Popconfirm></Space> },
    ]} /></Card>
    <Card title={t('compare')} style={{ marginTop: 20 }}><div className="network-comparison-controls"><Select mode="multiple" maxCount={4} aria-label={t('chooseChecks')} placeholder={t('chooseChecks')} value={selected} onChange={ids => setSelection({ target, ids })} options={checks.map(check => ({ value: check.id, label: `${check.name} · ${serverName(check.source_server_id)}` }))} /><Select aria-label={t('timeRange')} value={hours} onChange={setHours} options={[{ value: 1, label: t('hour') }, { value: 24, label: t('day') }, { value: 168, label: t('week') }]} /></div>
      <Typography.Paragraph type="secondary">{t('historyHint')}</Typography.Paragraph>
      {historyError && <Alert type="warning" title={t('failed')} />}
      {Object.values(history).some(value => value.truncated) && <Alert type="info" title={t('partialHistory')} />}
      <ResponsiveContainer width="100%" height={260}><LineChart data={chart}><CartesianGrid stroke="var(--border)" strokeDasharray="3 3" /><XAxis dataKey="time" type="number" domain={['dataMin', 'dataMax']} tick={{ fill: 'var(--muted)' }} tickFormatter={value => new Date(value).toLocaleTimeString()} /><YAxis unit=" ms" tick={{ fill: 'var(--muted)' }} /><Tooltip contentStyle={{ background: 'var(--surface)', borderColor: 'var(--border)', color: 'var(--text)', borderRadius: 8 }} labelFormatter={value => new Date(Number(value)).toLocaleString()} /><Legend />{selected.map((id, index) => <Line key={id} name={checks.find(check => check.id === id)?.name || id} dataKey={id} stroke={colors[index]} dot={false} connectNulls={false} isAnimationActive={false} />)}</LineChart></ResponsiveContainer>
      <Table rowKey="id" scroll={{ x: 700 }} pagination={false} dataSource={selected.map(id => ({ id, name: checks.find(check => check.id === id)?.name, ...history[id]?.summary }))} columns={[
        { title: t('name'), dataIndex: 'name' }, { title: t('sent'), render: (_, row) => row.sent == null ? '—' : `${row.sent} / ${row.received}` },
        { title: t('loss'), render: (_, row) => number(row.loss_percent, '%') }, { title: t('avg'), render: (_, row) => number(row.avg_ms, ' ms') },
        { title: t('known'), dataIndex: 'known_samples' }, { title: t('unknownCount'), dataIndex: 'unknown_samples' },
      ]} />
      <Typography.Title level={4}>{t('history')}</Typography.Title>
      <Table rowKey="key" scroll={{ x: 900 }} pagination={{ pageSize: 20 }} dataSource={selected.flatMap(id => (history[id]?.samples || []).map(sample => ({ ...sample, key: `${id}:${sample.recorded_at}`, name: checks.find(check => check.id === id)?.name })))} columns={[
        { title: t('name'), dataIndex: 'name' }, { title: t('time'), render: (_, sample) => new Date(sample.recorded_at).toLocaleString() },
        { title: t('status'), render: (_, sample) => t(sample.status === 'loss' ? 'lossState' : sample.status) },
        { title: t('sent'), render: (_, sample) => sample.sent == null ? '—' : `${sample.sent} / ${sample.received}` },
        { title: t('loss'), render: (_, sample) => number(sample.loss_percent, '%') }, { title: t('avg'), render: (_, sample) => number(sample.avg_ms, ' ms') },
        { title: t('minmax'), render: (_, sample) => `${number(sample.min_ms)} / ${number(sample.max_ms)} ms` },
        { title: t('mdev'), render: (_, sample) => number(sample.mdev_ms, ' ms') },
      ]} />
    </Card>
    <Modal open={editing !== null} title={t(editing === 'new' ? 'add' : 'edit')} onCancel={() => { if (!saving) setEditing(null); }} onOk={() => form.submit()} confirmLoading={saving}>
      <Form form={form} layout="vertical" onFinish={save} disabled={saving}>
        <Form.Item name="name" label={t('name')} rules={[{ required: true, max: 128 }]}><Input /></Form.Item>
        <Form.Item name="source_server_id" label={t('source')}><Select allowClear placeholder={t('local')} options={servers.map(server => ({ value: server.id, label: server.name }))} /></Form.Item>
        <Form.Item name="target_server_id" label={t('target')} rules={[{ required: true }]}><Select showSearch optionFilterProp="label" options={servers.map(server => ({ value: server.id, label: server.name }))} /></Form.Item>
        <Form.Item name="packets" label={t('packets')} rules={[{ required: true }]}><InputNumber min={1} max={10} precision={0} /></Form.Item>
        <Form.Item name="loss_threshold" label={t('lossThreshold')} rules={[{ required: true }]}><InputNumber min={0} max={100} /></Form.Item>
        <Form.Item name="latency_threshold_ms" label={t('latencyThreshold')}><InputNumber min={0} max={60000} /></Form.Item>
        <Form.Item name="enabled" label={t('enabled')} valuePropName="checked"><Switch /></Form.Item>
      </Form>
    </Modal>
  </div>;
}
