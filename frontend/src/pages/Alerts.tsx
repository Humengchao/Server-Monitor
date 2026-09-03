import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  App, Button, Card, Col, Empty, Form, Input, InputNumber, Modal, Row, Segmented, Select,
  Skeleton, Space, Switch, Table, Tag, Tooltip, Typography,
} from 'antd';
import {
  AlertOutlined, ApiOutlined, BellOutlined, CheckCircleOutlined, DeleteOutlined, EditOutlined,
  ExperimentOutlined, HistoryOutlined, PlusOutlined, ReloadOutlined, ThunderboltOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { alertsApi, AlertEvent, AlertMetric, AlertRule, METRIC_UNITS, PERCENT_METRICS } from '../api/alerts';
import { serversApi, Server } from '../api/servers';
import { usePolling } from '../hooks/usePolling';

const { Title, Text } = Typography;

const METRICS: AlertMetric[] = ['cpu', 'memory', 'disk', 'load1', 'latency', 'offline'];

const DURATION_OPTIONS = [60, 180, 300, 600, 1800, 3600];

type Translate = (key: string, opts?: Record<string, unknown>) => string;

interface RuleFormValues {
  name: string;
  server_id?: string | null;
  metric: AlertMetric;
  comparator: '>' | '<';
  threshold: number;
  duration_seconds: number;
  enabled?: boolean;
  webhook_url?: string;
}

/** Reads the human-readable error the API returns, falling back to a label. */
function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

function formatDuration(seconds: number, t: Translate): string {
  if (seconds < 60) return t('alerts.durationSeconds', { count: seconds });
  if (seconds < 3600) return t('alerts.durationMinutes', { count: Math.round(seconds / 60) });
  return t('alerts.durationHours', { count: Math.round((seconds / 3600) * 10) / 10 });
}

function formatValue(event: AlertEvent): string {
  if (event.metric === 'offline') return '';
  const unit = METRIC_UNITS[event.metric] ?? '';
  return `${event.value.toFixed(1)}${unit}`;
}

/**
 * Re-renders the engine's English summary in the active language. Events whose
 * rule has since been deleted carry no metric, so they keep the stored text.
 */
function describeEvent(event: AlertEvent, t: Translate): string {
  if (!event.metric) return event.message;
  if (event.metric === 'offline') {
    return t('alerts.messageOffline', {
      duration: formatDuration(Math.round(event.value), t),
    });
  }
  const unit = METRIC_UNITS[event.metric] ?? '';
  return t('alerts.messageThreshold', {
    metric: t(`alerts.metric.${event.metric}`),
    value: `${event.value.toFixed(1)}${unit}`,
    comparator: event.comparator === '<' ? t('alerts.belowInline') : t('alerts.aboveInline'),
    threshold: `${event.threshold}${unit}`,
    duration: formatDuration(event.duration_seconds, t),
  });
}

// Time elapsed between two instants, rendered compactly ("4m", "2h 10m").
function formatElapsed(fromISO: string, toISO: string | null, t: Translate): string {
  const from = new Date(fromISO).getTime();
  const to = toISO ? new Date(toISO).getTime() : Date.now();
  const seconds = Math.max(0, Math.round((to - from) / 1000));
  if (seconds < 60) return t('alerts.durationSeconds', { count: seconds });
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return t('alerts.durationMinutes', { count: minutes });
  const hours = Math.floor(minutes / 60);
  return `${t('alerts.durationHours', { count: hours })} ${t('alerts.durationMinutes', { count: minutes % 60 })}`;
}

export default function Alerts() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const navigate = useNavigate();
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [events, setEvents] = useState<AlertEvent[]>([]);
  const [servers, setServers] = useState<Server[]>([]);
  const [loading, setLoading] = useState(true);
  const [view, setView] = useState<'events' | 'rules'>('events');
  const [modalOpen, setModalOpen] = useState(false);
  const [editingRule, setEditingRule] = useState<AlertRule | null>(null);
  const [testing, setTesting] = useState(false);
  const [form] = Form.useForm();
  const watchedMetric = Form.useWatch('metric', form) as AlertMetric | undefined;

  const load = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    try {
      const [ruleRes, eventRes] = await Promise.all([alertsApi.listRules(), alertsApi.listEvents(false, 200)]);
      setRules(ruleRes.data || []);
      setEvents(eventRes.data || []);
    } catch {
      if (showLoading) message.error(t('alerts.loadFailed'));
    }
    if (showLoading) setLoading(false);
  }, [message, t]);

  // Alerts change on the engine's cadence, not the collector's, so a slower
  // poll than the dashboard keeps this page current.
  usePolling(() => load(false), 15000, { leading: false });

  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    return () => window.clearTimeout(initial);
  }, [load]);

  useEffect(() => {
    serversApi.list().then((res) => setServers(res.data || [])).catch(() => undefined);
  }, []);

  const activeEvents = useMemo(() => events.filter((e) => !e.resolved_at), [events]);
  const [eventFilter, setEventFilter] = useState<'active' | 'resolved' | 'all'>('active');
  // Rules a reader needs to act on come first: currently firing, then armed,
  // then paused. Creation order buries a firing rule under paused ones.
  const sortedRules = useMemo(() => {
    const firingByRule = new Map<string, number>();
    for (const e of events) {
      if (!e.resolved_at && e.rule_id) firingByRule.set(e.rule_id, (firingByRule.get(e.rule_id) || 0) + 1);
    }
    const rank = (r: AlertRule) => (firingByRule.get(r.id) ? 0 : r.enabled ? 1 : 2);
    return [...rules].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
  }, [rules, events]);
  const shownEvents = useMemo(() => {
    if (eventFilter === 'active') return events.filter((e) => !e.resolved_at);
    if (eventFilter === 'resolved') return events.filter((e) => e.resolved_at);
    return events;
  }, [events, eventFilter]);
  const enabledRules = useMemo(() => rules.filter((r) => r.enabled).length, [rules]);
  const coveredServers = useMemo(() => {
    if (rules.some((r) => r.enabled && !r.server_id)) return servers.length;
    return new Set(rules.filter((r) => r.enabled && r.server_id).map((r) => r.server_id)).size;
  }, [rules, servers.length]);

  const openCreate = () => {
    setEditingRule(null);
    form.resetFields();
    form.setFieldsValue({
      metric: 'cpu', comparator: '>', threshold: 90, duration_seconds: 300, enabled: true, server_id: null,
    });
    setModalOpen(true);
  };

  const openEdit = (rule: AlertRule) => {
    setEditingRule(rule);
    form.resetFields();
    form.setFieldsValue({
      name: rule.name,
      server_id: rule.server_id,
      metric: rule.metric,
      comparator: rule.comparator,
      threshold: rule.threshold,
      duration_seconds: rule.duration_seconds,
      enabled: rule.enabled,
      webhook_url: rule.webhook_url,
    });
    setModalOpen(true);
  };

  const handleSubmit = async (values: RuleFormValues) => {
    const payload = {
      name: String(values.name || '').trim(),
      server_id: values.server_id || null,
      metric: values.metric,
      comparator: values.comparator,
      threshold: Number(values.threshold) || 0,
      duration_seconds: Number(values.duration_seconds) || 300,
      enabled: values.enabled !== false,
      webhook_url: String(values.webhook_url || '').trim(),
    };
    try {
      if (editingRule) {
        await alertsApi.updateRule(editingRule.id, payload);
        message.success(t('alerts.ruleUpdated'));
      } else {
        await alertsApi.createRule(payload);
        message.success(t('alerts.ruleCreated'));
      }
      setModalOpen(false);
      setEditingRule(null);
      form.resetFields();
      load(false);
    } catch (err: unknown) {
      message.error(apiError(err, t('alerts.saveFailed')));
    }
  };

  const handleToggle = async (rule: AlertRule, enabled: boolean) => {
    // Optimistic: the switch should feel instant even though the write is a
    // full rule update round trip.
    setRules((prev) => prev.map((r) => (r.id === rule.id ? { ...r, enabled } : r)));
    try {
      await alertsApi.updateRule(rule.id, {
        name: rule.name,
        server_id: rule.server_id,
        metric: rule.metric,
        comparator: rule.comparator,
        threshold: rule.threshold,
        duration_seconds: rule.duration_seconds,
        enabled,
        webhook_url: rule.webhook_url,
      });
      load(false);
    } catch {
      setRules((prev) => prev.map((r) => (r.id === rule.id ? { ...r, enabled: !enabled } : r)));
      message.error(t('alerts.saveFailed'));
    }
  };

  const handleDelete = (rule: AlertRule) => {
    modal.confirm({
      title: t('alerts.deleteRule'),
      content: t('alerts.deleteConfirm', { name: rule.name }),
      okText: t('common.delete'),
      okType: 'danger',
      onOk: async () => {
        try {
          await alertsApi.deleteRule(rule.id);
          message.success(t('alerts.ruleDeleted'));
          load(false);
        } catch {
          message.error(t('alerts.deleteFailed'));
        }
      },
    });
  };

  const handleTestWebhook = async () => {
    const url = String(form.getFieldValue('webhook_url') || '').trim();
    if (!url) {
      message.warning(t('alerts.webhookRequired'));
      return;
    }
    setTesting(true);
    try {
      await alertsApi.testWebhook(url);
      message.success(t('alerts.webhookSent'));
    } catch (err: unknown) {
      message.error(apiError(err, t('alerts.webhookFailed')));
    }
    setTesting(false);
  };

  const isPercent = PERCENT_METRICS.includes((watchedMetric || 'cpu') as AlertMetric);
  const isOffline = watchedMetric === 'offline';

  const ruleColumns = [
    {
      title: t('alerts.rule'),
      key: 'name',
      render: (_: unknown, rule: AlertRule) => (
        <div className="alert-rule-cell">
          <strong>{rule.name}</strong>
          <Text type="secondary">{rule.server_name || t('alerts.allServers')}</Text>
        </div>
      ),
    },
    {
      title: t('alerts.condition'),
      key: 'condition',
      render: (_: unknown, rule: AlertRule) => (
        <Space size={6} wrap>
          <Tag className="metric-chip" variant="filled">{t(`alerts.metric.${rule.metric}`)}</Tag>
          <Text>
            {rule.metric === 'offline'
              ? t('alerts.forDuration', { duration: formatDuration(rule.duration_seconds, t) })
              : `${rule.comparator} ${rule.threshold}${METRIC_UNITS[rule.metric]} · ${formatDuration(rule.duration_seconds, t)}`}
          </Text>
        </Space>
      ),
    },
    {
      title: t('common.status'),
      key: 'status',
      width: 150,
      render: (_: unknown, rule: AlertRule) => (
        rule.firing_count > 0
          ? <span className="alert-state firing"><i />{t('alerts.firingCount', { count: rule.firing_count })}</span>
          : <span className="alert-state ok"><i />{t('alerts.normal')}</span>
      ),
    },
    {
      title: t('alerts.notify'),
      key: 'webhook',
      width: 110,
      render: (_: unknown, rule: AlertRule) => (
        rule.webhook_url
          ? <Tooltip title={rule.webhook_url}><Tag color="blue" variant="filled"><ApiOutlined /> Webhook</Tag></Tooltip>
          : <Text type="secondary">—</Text>
      ),
    },
    {
      title: t('alerts.enabled'),
      key: 'enabled',
      width: 90,
      render: (_: unknown, rule: AlertRule) => (
        <Switch size="small" checked={rule.enabled} onChange={(checked) => handleToggle(rule, checked)} />
      ),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 100,
      render: (_: unknown, rule: AlertRule) => (
        <Space size={2}>
          <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(rule)} />
          <Button type="text" danger icon={<DeleteOutlined />} onClick={() => handleDelete(rule)} />
        </Space>
      ),
    },
  ];

  return (
    <div className="alerts-page">
      <div className="page-heading">
        <div>
          <Text className="eyebrow">{t('alerts.eyebrow')}</Text>
          <Title level={2}>{t('alerts.title')}</Title>
          <Text type="secondary">{t('alerts.subtitle')}</Text>
        </div>
        <Space wrap className="page-actions">
          <Button icon={<ReloadOutlined />} onClick={() => load()} loading={loading}>{t('common.refresh')}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{t('alerts.addRule')}</Button>
        </Space>
      </div>

      {/* Plain divs, not Row/Col: .overview-grid is a CSS grid, and an antd Col
          inside it resolves its span percentage against the grid track instead
          of the row, collapsing each tile to a quarter of its cell. */}
      <div className="overview-grid">
        <Card className={`overview-card ${activeEvents.length ? 'overview-card-danger' : 'overview-card-success'}`} variant="borderless">
          <div className="overview-icon">{activeEvents.length ? <AlertOutlined /> : <CheckCircleOutlined />}</div>
          <div><Text type="secondary">{t('alerts.active')}</Text><strong>{activeEvents.length}</strong></div>
        </Card>
        <Card className="overview-card overview-card-primary" variant="borderless">
          <div className="overview-icon"><BellOutlined /></div>
          <div><Text type="secondary">{t('alerts.rules')}</Text><strong>{rules.length}</strong></div>
        </Card>
        <Card className="overview-card overview-card-accent" variant="borderless">
          <div className="overview-icon"><ThunderboltOutlined /></div>
          <div><Text type="secondary">{t('alerts.enabledRules')}</Text><strong>{enabledRules}</strong></div>
        </Card>
        <Card className="overview-card overview-card-muted" variant="borderless">
          <div className="overview-icon"><HistoryOutlined /></div>
          <div><Text type="secondary">{t('alerts.coverage')}</Text><strong>{coveredServers}</strong></div>
        </Card>
      </div>

      <div className="section-heading">
        <Segmented
          value={view}
          onChange={(value) => setView(value as 'events' | 'rules')}
          options={[
            { label: t('alerts.timeline'), value: 'events', icon: <HistoryOutlined /> },
            { label: t('alerts.rules'), value: 'rules', icon: <BellOutlined /> },
          ]}
        />
        {view === 'events' && events.length > 0 && (
          <Space size={12}>
            {/* Defaults to "active": on a page whose job is to show what is
                broken, resolved history is context, not the headline. */}
            <Segmented
              size="small"
              value={eventFilter}
              onChange={(value) => setEventFilter(value as typeof eventFilter)}
              options={[
                { label: `${t('alerts.filterActive')} ${activeEvents.length}`, value: 'active' },
                { label: t('alerts.filterResolved'), value: 'resolved' },
                { label: t('alerts.filterAll'), value: 'all' },
              ]}
            />
            <Text type="secondary">{t('alerts.eventCount', { count: shownEvents.length })}</Text>
          </Space>
        )}
      </div>

      {loading ? (
        <Card><Skeleton active paragraph={{ rows: 6 }} /></Card>
      ) : view === 'rules' ? (
        <Card className="panel-card" styles={{ body: { padding: 0 } }}>
          <Table
            dataSource={sortedRules}
            columns={ruleColumns}
            rowKey="id"
            pagination={false}
            scroll={{ x: 720 }}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('alerts.noRules')} /> }}
          />
        </Card>
      ) : shownEvents.length === 0 ? (
        <div className="empty-state">
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={eventFilter === 'active' ? t('alerts.noActiveEvents') : t('alerts.noEvents')} />
        </div>
      ) : (
        <div className="alert-timeline">
          {shownEvents.map((event) => (
            <button
              type="button"
              key={event.id}
              className={`alert-entry ${event.resolved_at ? 'resolved' : 'firing'}`}
              onClick={() => navigate(`/servers/${event.server_id}`)}
            >
              <span className="alert-entry-icon">
                {event.resolved_at ? <CheckCircleOutlined /> : <AlertOutlined />}
              </span>
              <span className="alert-entry-body">
                <span className="alert-entry-title">
                  <strong>{event.rule_name || t('alerts.deletedRule')}</strong>
                  <Tag className="metric-chip" variant="filled">{t(`alerts.metric.${event.metric}`)}</Tag>
                  {event.server_name && <Text type="secondary">{event.server_name}</Text>}
                </span>
                <span className="alert-entry-message">{describeEvent(event, t)}</span>
              </span>
              <span className="alert-entry-meta">
                {formatValue(event) && <strong>{formatValue(event)}</strong>}
                <small>{new Date(event.started_at).toLocaleString()}</small>
                <small>
                  {event.resolved_at
                    ? t('alerts.lastedFor', { duration: formatElapsed(event.started_at, event.resolved_at, t) })
                    : t('alerts.firingFor', { duration: formatElapsed(event.started_at, null, t) })}
                </small>
              </span>
            </button>
          ))}
        </div>
      )}

      <Modal
        title={editingRule ? t('alerts.editRule') : t('alerts.addRule')}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditingRule(null); }}
        onOk={() => form.submit()}
        width={620}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit} className="alert-form">
          <Form.Item name="name" label={t('alerts.ruleName')} rules={[{ required: true }]}>
            <Input placeholder={t('alerts.ruleNamePlaceholder')} maxLength={128} />
          </Form.Item>
          <Form.Item name="server_id" label={t('alerts.scope')} tooltip={t('alerts.scopeHint')}>
            <Select
              allowClear
              placeholder={t('alerts.allServers')}
              options={servers.map((s) => ({ value: s.id, label: `${s.name} · ${s.host}` }))}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} sm={isOffline ? 24 : 10}>
              <Form.Item name="metric" label={t('alerts.metricLabel')} rules={[{ required: true }]}>
                <Select options={METRICS.map((m) => ({ value: m, label: t(`alerts.metric.${m}`) }))} />
              </Form.Item>
            </Col>
            {!isOffline && (
              <>
                <Col xs={10} sm={6}>
                  <Form.Item name="comparator" label={t('alerts.comparator')}>
                    <Select options={[
                      { value: '>', label: t('alerts.above') },
                      { value: '<', label: t('alerts.below') },
                    ]} />
                  </Form.Item>
                </Col>
                <Col xs={14} sm={8}>
                  <Form.Item name="threshold" label={t('alerts.threshold')} rules={[{ required: true }]}>
                    <InputNumber
                      min={0}
                      max={isPercent ? 100 : undefined}
                      step={isPercent ? 5 : 1}
                      suffix={METRIC_UNITS[(watchedMetric || "cpu") as AlertMetric] || undefined}
                      style={{ width: '100%' }}
                    />
                  </Form.Item>
                </Col>
              </>
            )}
          </Row>
          <Form.Item
            name="duration_seconds"
            label={isOffline ? t('alerts.offlineFor') : t('alerts.sustainedFor')}
            tooltip={isOffline ? t('alerts.offlineHint') : t('alerts.sustainedHint')}
          >
            <Select options={DURATION_OPTIONS.map((value) => ({ value, label: formatDuration(value, t) }))} />
          </Form.Item>
          <Form.Item name="webhook_url" label={t('alerts.webhook')} tooltip={t('alerts.webhookHint')}>
            <Input placeholder="https://hooks.example.com/alerts" allowClear />
          </Form.Item>
          <Space className="alert-form-footer">
            <Button icon={<ExperimentOutlined />} loading={testing} onClick={handleTestWebhook}>
              {t('alerts.testWebhook')}
            </Button>
            <Form.Item name="enabled" valuePropName="checked" noStyle>
              <Switch checkedChildren={t('alerts.enabled')} unCheckedChildren={t('alerts.paused')} />
            </Form.Item>
          </Space>
        </Form>
      </Modal>
    </div>
  );
}
