import React, { useEffect, useState, useCallback, useMemo } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Typography, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input, InputNumber, Select,
  App, Row, Col, Empty, Progress, Segmented, Tooltip,
} from 'antd';
import {
  ArrowLeftOutlined, EditOutlined, DeleteOutlined, DockerOutlined, KeyOutlined, SaveOutlined,
  WindowsOutlined, AppleOutlined, CopyOutlined, DownloadOutlined, CloudServerOutlined,
  ClockCircleOutlined, ThunderboltOutlined, ArrowDownOutlined, ArrowUpOutlined, DashboardOutlined,
  DatabaseOutlined, HddOutlined, LineChartOutlined,
} from '@ant-design/icons';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import { useTranslation } from 'react-i18next';
import { serversApi, Server, MetricPoint } from '../api/servers';
import { useMetrics, TimeRange } from '../hooks/useMetrics';
import AvailabilityPanel from '../components/AvailabilityPanel';
import MetricsChart from '../components/MetricsChart';
import ProcessTable from '../components/ProcessTable';
import ServiceTable from '../components/ServiceTable';
import PortTable from '../components/PortTable';
import SshTerminal from '../components/SshTerminal';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';
import { ServerDockerPanel } from './Docker';
import { formatBytes, formatUptime, getExpirationInfo, percentOf, severityColor } from '../utils/format';
import { downloadCSV, safeFilenamePart } from '../utils/csv';

const { Title } = Typography;
const { RangePicker } = DatePicker;

type PresetKey = '1h' | 'today' | 'yesterday' | '7d' | '30d';

function getPresetRange(key: PresetKey): TimeRange {
  const now = dayjs();
  switch (key) {
    case '1h':
      return { since: now.subtract(1, 'hour').toISOString(), until: now.toISOString() };
    case 'today':
      return { since: now.startOf('day').toISOString(), until: now.toISOString() };
    case 'yesterday':
      return {
        since: now.subtract(1, 'day').startOf('day').toISOString(),
        until: now.subtract(1, 'day').endOf('day').toISOString(),
      };
    case '7d':
      return { since: now.subtract(7, 'day').startOf('day').toISOString(), until: now.toISOString() };
    case '30d':
      return { since: now.subtract(30, 'day').startOf('day').toISOString(), until: now.toISOString() };
  }
}

async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Fall back for browsers that expose the API but deny clipboard access.
    }
  }

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    if (!document.execCommand('copy')) {
      throw new Error('Copy command failed');
    }
  } finally {
    textarea.remove();
  }
}

const CSV_COLUMNS: (keyof MetricPoint)[] = [
  'recorded_at', 'cpu_percent', 'memory_used', 'memory_total', 'disk_used',
  'load_1', 'load_5', 'load_15', 'network_rx_bytes', 'network_tx_bytes',
  'disk_rx_bytes', 'disk_tx_bytes', 'uptime_seconds', 'latency_ms',
];

function StatTile({ icon, label, value, hint, accent, percent }: {
  icon: React.ReactNode;
  label: string;
  value: string;
  hint?: string;
  accent?: string;
  percent?: number;
}) {
  return (
    <div className="stat-tile">
      <span className="stat-tile-icon" style={accent ? { color: accent, background: `${accent}1f` } : undefined}>{icon}</span>
      <div className="stat-tile-body">
        <small>{label}</small>
        <strong>{value}</strong>
        {typeof percent === 'number' ? (
          <Progress percent={percent} showInfo={false} strokeColor={accent} railColor="rgba(128,140,170,.16)" />
        ) : hint ? <span className="stat-tile-hint">{hint}</span> : null}
      </div>
    </div>
  );
}

export default function ServerDetail() {
  const { t, i18n } = useTranslation();
  // modal (not the static Modal.*) so confirm dialogs inherit the dark theme.
  const { message, modal } = App.useApp();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [server, setServer] = useState<Server | null>(null);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);
  const [dockerInstalled, setDockerInstalled] = useState<boolean | null>(null);
  const [notes, setNotes] = useState('');
  const [notesChanged, setNotesChanged] = useState(false);
  const [activeTab, setActiveTab] = useState('metrics');
  const [activePreset, setActivePreset] = useState<PresetKey | null>('1h');
  const [timeRange, setTimeRange] = useState<TimeRange>(() => getPresetRange('1h'));

  const { metrics, history, loading: metricsLoading, observedAt } = useMetrics(id!, timeRange);

  // Presets whose window ends "now" keep sliding: recompute the range every
  // 30s so the chart stays live instead of freezing at the moment of the
  // click. Fixed windows (yesterday, custom ranges) stay as picked.
  useEffect(() => {
    if (!activePreset || activePreset === 'yesterday') return;
    const timer = window.setInterval(() => {
      setTimeRange(getPresetRange(activePreset));
    }, 30000);
    return () => window.clearInterval(timer);
  }, [activePreset]);

  const presets: { key: PresetKey; label: string }[] = [
    { key: '1h', label: t('preset.1h') },
    { key: 'today', label: t('preset.today') },
    { key: 'yesterday', label: t('preset.yesterday') },
    { key: '7d', label: t('preset.7d') },
    { key: '30d', label: t('preset.30d') },
  ];

  const loadServer = useCallback(async () => {
    try {
      const res = await serversApi.get(id!);
      const found = res.data;
      setServer(found);
      setDockerInstalled(found.has_docker);
      setNotes(found.notes || '');
      setNotesChanged(false);
    } catch (err: any) {
      if (err?.response?.status === 404) {
        setServer(null);
      } else {
        message.error(t('server.loadFailed'));
      }
    }
    setLoading(false);
  }, [id, message, t]);

  useEffect(() => {
    loadServer();
  }, [id]);

  const handlePreset = (key: PresetKey) => {
    setActivePreset(key);
    setTimeRange(getPresetRange(key));
  };

  const handleRangeChange = (dates: [Dayjs | null, Dayjs | null] | null) => {
    if (dates && dates[0] && dates[1]) {
      setActivePreset(null);
      setTimeRange({
        since: dates[0].toISOString(),
        until: dates[1].toISOString(),
      });
    }
  };

  const handleEdit = () => {
    if (!server) return;
    setSelectedCredential(server.credential_id || undefined);
    // Drop any password/key typed in a previously cancelled edit; empty
    // secret fields mean "keep current" on the backend.
    form.resetFields();
    form.setFieldsValue({
      name: server.name,
      host: server.host,
      port: server.port,
      ssh_username: server.ssh_username,
      ssh_host_key: server.ssh_host_key || '',
      expires_at: server.expires_at ? dayjs(server.expires_at) : null,
      server_type: server.server_type || 'linux',
      billing_price: server.billing_price || 0,
      billing_currency: server.billing_currency || 'CNY',
      billing_cycle: server.billing_cycle || 'year',
      traffic_limit_gb: Number(((server.traffic_limit_bytes || 0) / 1024 / 1024 / 1024).toFixed(2)),
      public_location: server.public_location || '',
    });
    setTagValues(server.tags?.map((tag) => tag.id) || []);
    setModalOpen(true);
  };

  const handleSubmit = async (values: any) => {
    if (!server) return;
    try {
      const payload = {
        ...values,
        name: typeof values.name === 'string' ? values.name.trim() : values.name,
        host: typeof values.host === 'string' ? values.host.trim() : values.host,
        ssh_username: typeof values.ssh_username === 'string' ? values.ssh_username.trim() : values.ssh_username,
        credential_id: selectedCredential || null,
        server_type: values.server_type || 'linux',
        expires_at: values.expires_at ? values.expires_at.toISOString() : null,
        traffic_limit_bytes: Math.round((values.traffic_limit_gb || 0) * 1024 * 1024 * 1024),
        notes: server.notes || '',
      };
      await serversApi.update(server.id, payload);
      await serversApi.setTags(server.id, tagValues);
      message.success(t('server.updated'));
      setModalOpen(false);
      loadServer();
    } catch (err: any) {
      message.error(err.response?.data?.error || t('server.updateFailed'));
    }
  };

  const handleDelete = () => {
    if (!server) return;
    modal.confirm({
      title: t('server.delete'),
      content: t('server.deleteConfirm', { name: server.name }),
      okText: t('common.delete'),
      okType: 'danger',
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success(t('server.deleted'));
          navigate('/dashboard');
        } catch {
          message.error(t('server.deleteFailed'));
        }
      },
    });
  };

  const handleSaveNotes = async () => {
    if (!server) return;
    try {
      await serversApi.update(server.id, {
        name: server.name,
        host: server.host,
        port: server.port,
        ssh_username: server.ssh_username,
        ssh_host_key: server.ssh_host_key || '',
        credential_id: server.credential_id,
        server_type: server.server_type || 'linux',
        expires_at: server.expires_at || null,
        billing_price: server.billing_price || 0,
        billing_currency: server.billing_currency || 'CNY',
        billing_cycle: server.billing_cycle || 'year',
        traffic_limit_bytes: server.traffic_limit_bytes || 0,
        public_location: server.public_location || '',
        notes,
      });
      message.success(t('server.notesSaved'));
      setServer((current) => (current ? { ...current, notes } : current));
      setNotesChanged(false);
    } catch {
      message.error(t('server.notesSaveFailed'));
    }
  };

  const handleCopyHost = async () => {
    if (!server?.host) return;
    try {
      await copyToClipboard(server.host);
      message.success(t('server.hostCopied'));
    } catch {
      message.error(t('server.hostCopyFailed'));
    }
  };

  // Exports exactly the window currently charted, so what you see is what you
  // get in the spreadsheet.
  const handleExportCSV = () => {
    if (!server || history.length === 0) {
      message.warning(t('metrics.noData'));
      return;
    }
    const rows: string[][] = [[...CSV_COLUMNS]];
    for (const point of history) {
      rows.push(CSV_COLUMNS.map((column) => String(point[column] ?? '')));
    }
    const stamp = dayjs(timeRange.since).format('YYYYMMDD-HHmm');
    downloadCSV(`${safeFilenamePart(server.name)}-metrics-${stamp}.csv`, rows);
    message.success(t('metrics.exported', { count: history.length }));
  };

  const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';
  const expInfo = useMemo(() => getExpirationInfo(server?.expires_at, lang), [server?.expires_at, lang]);
  const isOnline = observedAt > 0 && !!metrics?.recorded_at
    && observedAt - new Date(metrics.recorded_at).getTime() < 120000;
  const cpuPercent = Math.round(metrics?.cpu_percent || 0);
  const memPercent = metrics ? percentOf(metrics.memory_used, metrics.memory_total) : 0;
  const diskPercent = metrics && server ? percentOf(metrics.disk_used, server.disk_total) : 0;

  if (loading) return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '60vh' }}><Spin size="large" /></div>;
  if (!server) return <div className="empty-state"><Empty description={t('server.notFound')} /></div>;

  return (
    <div className={`server-detail-page${activeTab === 'terminal' ? ' server-detail-page--terminal' : ''}`}>
      <div className="detail-hero">
        <div className="detail-hero-main">
          <Button className="detail-back" icon={<ArrowLeftOutlined />} onClick={() => navigate('/dashboard')}>
            {t('common.back')}
          </Button>
          <div className={`detail-avatar ${server.server_type === 'windows' ? 'windows' : 'linux'}`}>
            {server.server_type === 'windows' ? <WindowsOutlined /> : <CloudServerOutlined />}
          </div>
          <div className="detail-identity">
            <div className="detail-identity-line">
              <Title level={3}>{server.name}</Title>
              <span className={`status-pill ${isOnline ? 'online' : 'offline'}`}>
                <span />{isOnline ? t('dashboard.online') : t('dashboard.offline')}
              </span>
            </div>
            <div className="detail-meta">
              <button type="button" className="copyable-host" title={t('server.copyHost')} onClick={handleCopyHost}>
                {server.host}:{server.port}<CopyOutlined aria-hidden="true" />
              </button>
              <span className="detail-meta-sep" />
              <span><KeyOutlined /> {server.credential_name || server.ssh_username}</span>
              {server.public_location && (<><span className="detail-meta-sep" /><span>{server.public_location}</span></>)}
              {expInfo && (<><span className="detail-meta-sep" /><span style={{ color: expInfo.color }}>{expInfo.text}</span></>)}
            </div>
            {!!server.tags?.length && (
              <div className="detail-tags">
                {server.tags.map((tag) => <Tag key={tag.id} color={tag.color}>{tag.name}</Tag>)}
              </div>
            )}
          </div>
        </div>
        <Space wrap className="detail-actions">
          <Tooltip title={dockerInstalled === true ? '' : t('docker.noServers')}>
            <Button
              icon={<DockerOutlined />}
              disabled={dockerInstalled !== true}
              onClick={() => navigate(`/docker?server=${id}&expand=true`)}
            >
              {t('server.docker')}
            </Button>
          </Tooltip>
          <Button icon={<EditOutlined />} onClick={handleEdit}>{t('common.edit')}</Button>
          <Button danger icon={<DeleteOutlined />} onClick={handleDelete}>{t('common.delete')}</Button>
        </Space>
      </div>

      <div className="stat-tile-grid">
        <StatTile
          icon={<DashboardOutlined />}
          label={t('metrics.cpu')}
          value={`${cpuPercent}%`}
          accent={severityColor(cpuPercent, 'blue')}
          percent={cpuPercent}
        />
        <StatTile
          icon={<DatabaseOutlined />}
          label={t('metrics.memory')}
          value={metrics ? `${formatBytes(metrics.memory_used)} / ${formatBytes(metrics.memory_total)}` : '—'}
          accent={severityColor(memPercent, 'green')}
          percent={memPercent}
        />
        <StatTile
          icon={<HddOutlined />}
          label={t('metrics.disk')}
          value={metrics ? `${formatBytes(metrics.disk_used)} / ${formatBytes(server.disk_total)}` : '—'}
          accent={severityColor(diskPercent, 'violet')}
          percent={diskPercent}
        />
        <StatTile
          icon={<ClockCircleOutlined />}
          label={t('metrics.uptime')}
          value={formatUptime(metrics?.uptime_seconds || 0)}
          hint={t('detail.cores', { count: server.cpu_cores || 0 })}
          accent="#4bb3d6"
        />
        <StatTile
          icon={<ThunderboltOutlined />}
          label={t('metrics.latency')}
          value={metrics?.latency_ms ? `${metrics.latency_ms} ms` : '—'}
          hint={metrics ? t('detail.load', { load: `${metrics.load_1.toFixed(2)} / ${metrics.load_5.toFixed(2)} / ${metrics.load_15.toFixed(2)}` }) : undefined}
          accent="#e8944a"
        />
        <StatTile
          icon={<ArrowDownOutlined />}
          label={t('metrics.totalDownload')}
          value={formatBytes(metrics?.network_rx_total_bytes || 0)}
          hint={`${formatBytes(metrics?.network_rx_bytes || 0)}/s`}
          accent="#39b8a4"
        />
        <StatTile
          icon={<ArrowUpOutlined />}
          label={t('metrics.totalUpload')}
          value={formatBytes(metrics?.network_tx_total_bytes || 0)}
          hint={`${formatBytes(metrics?.network_tx_bytes || 0)}/s`}
          accent="#8d6dd7"
        />
        <StatTile
          icon={<LineChartOutlined />}
          label={t('detail.sampledAt')}
          value={metrics?.recorded_at ? new Date(metrics.recorded_at).toLocaleTimeString() : '—'}
          hint={t('detail.addedOn', { date: new Date(server.created_at).toLocaleDateString() })}
          accent="#6f8cf5"
        />
      </div>

      <Tabs className="server-detail-tabs" activeKey={activeTab} onChange={setActiveTab} items={[
        {
          key: 'metrics',
          label: t('metrics.title'),
          children: (
            <Card className="panel-card" loading={metricsLoading}>
              <div className="chart-toolbar">
                <Segmented
                  value={activePreset ?? ''}
                  onChange={(value) => handlePreset(value as PresetKey)}
                  options={presets.map((p) => ({ label: p.label, value: p.key }))}
                />
                <Space wrap>
                  <RangePicker
                    showTime
                    disabledDate={(current) => current && current.isAfter(dayjs(), 'day')}
                    onChange={handleRangeChange}
                  />
                  <Tooltip title={t('metrics.exportHint')}>
                    <Button icon={<DownloadOutlined />} onClick={handleExportCSV} disabled={history.length === 0}>
                      {t('metrics.export')}
                    </Button>
                  </Tooltip>
                </Space>
              </div>
              <MetricsChart history={history} />
            </Card>
          ),
        },
        {
          key: 'availability',
          label: t('uptime.title'),
          // Only mounted while selected, so its census requests don't fire for
          // visitors who never open the tab.
          children: activeTab === 'availability' ? (
            <Card className="panel-card">
              <AvailabilityPanel serverId={id!} />
            </Card>
          ) : null,
        },
        {
          key: 'processes',
          label: t('process.title'),
          // Rendered only while selected. Tabs keeps a visited pane mounted, so
          // otherwise the 5-second SSH poll would keep running in the
          // background after switching away. The terminal pane deliberately
          // stays mounted, which is why this is per-pane and not
          // destroyOnHidden on the whole Tabs.
          children: activeTab === 'processes' ? (
            <Card className="panel-card">
              <ProcessTable serverId={id!} serverType={server.server_type || 'linux'} />
            </Card>
          ) : null,
        },
        {
          key: 'services',
          label: t('service.title'),
          // Same reasoning as the process pane: mounted only while selected, so
          // its polling stops when the operator switches away.
          children: activeTab === 'services' ? (
            <Card className="panel-card">
              <ServiceTable serverId={id!} serverType={server.server_type || 'linux'} />
            </Card>
          ) : null,
        },
        {
          key: 'ports',
          label: t('port.title'),
          children: activeTab === 'ports' ? (
            <Card className="panel-card">
              <PortTable serverId={id!} />
            </Card>
          ) : null,
        },
        {
          key: 'terminal',
          label: t('terminal.title'),
          children: (
            <div className="server-detail-terminal">
              <SshTerminal serverId={id!} />
            </div>
          ),
        },
        {
          key: 'docker',
          label: t('docker.title'),
          children: dockerInstalled === true ? (
            <ServerDockerPanel serverId={id!} version={server.docker_version} />
          ) : (
            <div className="empty-state"><Empty description={t('docker.noServers')} /></div>
          ),
        },
        {
          key: 'notes',
          label: t('server.notes'),
          children: (
            <Card className="panel-card">
              <Input.TextArea
                rows={12}
                value={notes}
                onChange={(e) => { setNotes(e.target.value); setNotesChanged(true); }}
                placeholder={t('server.notesPlaceholder')}
              />
              <div style={{ marginTop: 16, textAlign: 'right' }}>
                <Button
                  type="primary"
                  icon={<SaveOutlined />}
                  disabled={!notesChanged}
                  onClick={handleSaveNotes}
                >
                  {t('common.save')}
                </Button>
              </div>
            </Card>
          ),
        },
      ]} />

      <Modal
        title={t('server.edit')}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={() => form.submit()}
        width={680}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label={t('server.serverName')} rules={[{ required: true }]}>
            <Input placeholder={t('server.serverNamePlaceholder')} />
          </Form.Item>
          <Form.Item name="host" label={t('server.host')} rules={[{ required: true }]}>
            <Input placeholder={t('server.hostPlaceholder')} />
          </Form.Item>
          <Form.Item name="port" label={t('server.sshPort')} initialValue={22}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="server_type" label={t('server.type')} initialValue="linux">
            <Select>
              <Select.Option value="linux"><AppleOutlined /> Linux</Select.Option>
              <Select.Option value="windows"><WindowsOutlined /> Windows</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item label={t('server.credential')}>
            <CredentialSelect value={selectedCredential} onChange={setSelectedCredential} serverType={form.getFieldValue('server_type') || 'linux'} />
          </Form.Item>
          {!selectedCredential && (
            <>
              <Form.Item name="ssh_username" label={t('server.sshUsername')} rules={[{ required: true }]}>
                <Input placeholder={t('server.sshUsernamePlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_password" label={t('server.sshPassword')}>
                <Input.Password placeholder={t('server.sshKeyEditPlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_key" label={t('server.sshKey')}>
                <Input.TextArea rows={4} placeholder={t('server.sshKeyEditPlaceholder')} />
              </Form.Item>
            </>
          )}
          <Form.Item name="ssh_host_key" label={t('server.sshHostKey')}>
            <Input.TextArea rows={2} placeholder={t('server.sshHostKeyPlaceholder')} />
          </Form.Item>
          <Form.Item name="expires_at" label={t('server.expiresAt')}>
            <DatePicker showTime style={{ width: '100%' }} placeholder={t('server.expiresAtPlaceholder')} />
          </Form.Item>
          <Form.Item name="public_location" label={t('server.publicLocation')}>
            <Input placeholder={t('server.publicLocationPlaceholder')} maxLength={128} />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_price" label={t('server.billingPrice')}>
                <InputNumber min={0} precision={2} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_currency" label={t('server.billingCurrency')}>
                <Select options={[{ value: 'CNY', label: '¥ CNY' }, { value: 'USD', label: '$ USD' }, { value: 'EUR', label: '€ EUR' }]} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_cycle" label={t('server.billingCycle')}>
                <Select options={[
                  { value: 'month', label: t('server.cycleMonth') },
                  { value: 'quarter', label: t('server.cycleQuarter') },
                  { value: 'half_year', label: t('server.cycleHalfYear') },
                  { value: 'year', label: t('server.cycleYear') },
                ]} />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="traffic_limit_gb" label={t('server.trafficLimit')}>
            <InputNumber min={0} precision={2} suffix="GB" style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item label={t('server.tags')}>
            <TagSelect value={tagValues} onChange={setTagValues} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
