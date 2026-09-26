import React, { useEffect, useState, useCallback, useMemo, Suspense, lazy, useRef } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Typography, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input,
  App, Empty, Progress, Segmented, Tooltip, Result,
} from 'antd';
import {
  ArrowLeftOutlined, EditOutlined, DeleteOutlined, DockerOutlined, KeyOutlined, SaveOutlined,
  WindowsOutlined, CopyOutlined, DownloadOutlined, CloudServerOutlined,
  ClockCircleOutlined, ThunderboltOutlined, ArrowDownOutlined, ArrowUpOutlined, DashboardOutlined,
  DatabaseOutlined, HddOutlined, LineChartOutlined, FolderOpenOutlined,
} from '@ant-design/icons';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import { useTranslation } from 'react-i18next';
import { serversApi, Server, MetricPoint } from '../api/servers';
import { isMetricFresh, useMetrics, TimeRange } from '../hooks/useMetrics';
import AvailabilityPanel from '../components/AvailabilityPanel';
import MetricsChart from '../components/MetricsChart';
import ProcessTable from '../components/ProcessTable';
import ServiceTable from '../components/ServiceTable';
import PortTable from '../components/PortTable';
import PollErrorNotice from '../components/PollErrorNotice';
import ServerForm from '../components/ServerForm';
import { buildServerPayload, serverFormValues } from '../utils/serverForm';
import type { ServerFormValues } from '../utils/serverForm';
import { copyToClipboard } from '../utils/clipboard';
import { formatBytes, formatDate, formatDateTime, formatTime, formatUptime, getExpirationInfo, percentOf, severityColor } from '../utils/format';
import type { ServerStatus } from '../utils/fleet';
import { downloadCSV, safeFilenamePart } from '../utils/csv';

// xterm and the Docker panel are only reachable through their own tabs, so
// they load on demand instead of weighing down every visit to this page.
const SshTerminal = lazy(() => import('../components/SshTerminal'));
const ServerDockerPanel = lazy(() => import('./Docker').then((m) => ({ default: m.ServerDockerPanel })));

const tabFallback = (
  <div style={{ display: 'flex', justifyContent: 'center', padding: 48 }}><Spin /></div>
);
import { usePolling } from '../hooks/usePolling';

const { Title } = Typography;
const { RangePicker } = DatePicker;

type PresetKey = '1h' | 'today' | 'yesterday' | '7d' | '30d';

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

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
  // The server payload includes the API Date header, which lets us still
  // judge the embedded latest sample if the dedicated metrics request fails.
  const [serverObservedAt, setServerObservedAt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [savingServer, setSavingServer] = useState(false);
  const [savingNotes, setSavingNotes] = useState(false);
  const [form] = Form.useForm<ServerFormValues>();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);
  const [dockerInstalled, setDockerInstalled] = useState<boolean | null>(null);
  const [notes, setNotes] = useState('');
  const [notesChanged, setNotesChanged] = useState(false);
  const [activeTab, setActiveTab] = useState('metrics');
  const [activePreset, setActivePreset] = useState<PresetKey | null>('1h');
  const [timeRange, setTimeRange] = useState<TimeRange>(() => getPresetRange('1h'));
  const serverRequestRef = useRef<AbortController | null>(null);

  const { metrics, history, loading: metricsLoading, observedAt } = useMetrics(id!, timeRange);

  // Presets whose window ends "now" keep sliding: recompute the range every
  // 30s so the chart stays live instead of freezing at the moment of the
  // click. Fixed windows (yesterday, custom ranges) stay as picked.
  usePolling(
    () => { if (activePreset) setTimeRange(getPresetRange(activePreset)); },
    30000,
    { leading: false, enabled: !!activePreset && activePreset !== 'yesterday' },
  );

  const presets: { key: PresetKey; label: string }[] = [
    { key: '1h', label: t('preset.1h') },
    { key: 'today', label: t('preset.today') },
    { key: 'yesterday', label: t('preset.yesterday') },
    { key: '7d', label: t('preset.7d') },
    { key: '30d', label: t('preset.30d') },
  ];

  const loadServer = useCallback(async () => {
    serverRequestRef.current?.abort();
    const controller = new AbortController();
    serverRequestRef.current = controller;
    setLoading(true);
    setLoadError(false);
    setServerObservedAt(0);
    try {
      const res = await serversApi.get(id!, controller.signal);
      if (controller.signal.aborted || serverRequestRef.current !== controller) return;
      const found = res.data;
      setServer(found);
      const dateHeader = res.headers?.date;
      const apiNow = typeof dateHeader === 'string' ? Date.parse(dateHeader) : NaN;
      setServerObservedAt(Number.isFinite(apiNow) ? apiNow : Date.now());
      setDockerInstalled(found.has_docker);
      setNotes(found.notes || '');
      setNotesChanged(false);
    } catch (err: unknown) {
      if (controller.signal.aborted || serverRequestRef.current !== controller) return;
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 404) {
        setServer(null);
      } else {
        setLoadError(true);
        message.error(t('server.loadFailed'));
      }
    } finally {
      if (serverRequestRef.current === controller) {
        serverRequestRef.current = null;
        setLoading(false);
      }
    }
  }, [id, message, t]);

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadServer(); }, 0);
    return () => {
      window.clearTimeout(timer);
      serverRequestRef.current?.abort();
    };
  }, [loadServer]);

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
    form.setFieldsValue(serverFormValues(server));
    setTagValues(server.tags?.map((tag) => tag.id) || []);
    setModalOpen(true);
  };

  const handleSubmit = async (values: ServerFormValues) => {
    if (!server) return;
    setSavingServer(true);
    try {
      const payload = buildServerPayload(values, { credentialId: selectedCredential, notes: server.notes || '' });
      await serversApi.update(server.id, payload);
      await serversApi.setTags(server.id, tagValues);
      message.success(t('server.updated'));
      setModalOpen(false);
      loadServer();
    } catch (err: unknown) {
      message.error(apiError(err, t('server.updateFailed')));
    } finally {
      setSavingServer(false);
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
    setSavingNotes(true);
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
    } finally {
      setSavingNotes(false);
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
  // Fall back to the sample embedded in the server payload, so a failed metrics
  // request still leaves the header with the collector's last word.
  const latestSample = metrics ?? server?.latest_metrics ?? null;
  const sampleObservedAt = observedAt || serverObservedAt;
  const isOnline = isMetricFresh(latestSample, sampleObservedAt);
  // The same three states as the fleet list: never sampled is "pending", a stale
  // last sample is "offline". Capacity figures still come from the last sample
  // either way, but only a live host gets uptime, latency and rates.
  const detailStatus: ServerStatus = !latestSample?.recorded_at ? 'pending' : isOnline ? 'online' : 'offline';
  const live = detailStatus === 'online';
  const latestSampleTime = latestSample?.recorded_at ? formatDateTime(latestSample.recorded_at, i18n.language) : null;
  // Unknown capacity is not zero utilisation: a tile stays at an em dash until it
  // has a real reading rather than printing 0%.
  const cpuPercent = latestSample && Number.isFinite(latestSample.cpu_percent) ? Math.round(latestSample.cpu_percent) : null;
  const memPercent = latestSample && latestSample.memory_total > 0 ? percentOf(latestSample.memory_used, latestSample.memory_total) : null;
  const diskPercent = latestSample && server && server.disk_total > 0 ? percentOf(latestSample.disk_used, server.disk_total) : null;

  if (loading) return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '60vh' }}><Spin size="large" /></div>;
  if (loadError) return <Result status="error" title={t('server.loadFailed')} extra={<Button type="primary" onClick={() => { void loadServer(); }}>{t('common.refresh')}</Button>} />;
  if (!server) return (
    <Result
      status="404"
      title={t('server.notFound')}
      extra={<Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/dashboard')}>{t('common.back')}</Button>}
    />
  );

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
              <span className={`status-pill ${detailStatus}`} aria-live="polite">
                <span />{t(`dashboard.${detailStatus}`)}
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
              <span className="detail-meta-sep" />
              {/* A live host reads "latest sample"; a stale one is explicitly historical. */}
              <span className={`detail-sample-state ${detailStatus}`} title={detailStatus === 'pending' ? t('dashboard.pendingHint') : latestSampleTime || undefined}>
                <ClockCircleOutlined />
                {latestSampleTime ? t(live ? 'detail.liveSample' : 'dashboard.lastSample', { time: latestSampleTime }) : t('dashboard.pending')}
              </span>
            </div>
            {!!server.tags?.length && (
              <div className="detail-tags">
                {server.tags.map((tag) => <Tag key={tag.id} color={tag.color}>{tag.name}</Tag>)}
              </div>
            )}
          </div>
        </div>
        <Space wrap className="detail-actions">
          <Button icon={<FolderOpenOutlined />} onClick={() => navigate('/files?server=' + id)}>{t('nav.files')}</Button>
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

      <PollErrorNotice server={server} onRetried={() => { void loadServer(); }} />
      <div className={`stat-tile-grid${detailStatus === 'offline' ? ' is-stale' : ''}`}>
        <StatTile
          icon={<DashboardOutlined />}
          label={t('metrics.cpu')}
          value={cpuPercent === null ? '—' : `${cpuPercent}%`}
          accent={severityColor(cpuPercent ?? 0, 'blue')}
          percent={cpuPercent ?? undefined}
        />
        <StatTile
          icon={<DatabaseOutlined />}
          label={t('metrics.memory')}
          value={latestSample && memPercent !== null ? `${formatBytes(latestSample.memory_used)} / ${formatBytes(latestSample.memory_total)}` : '—'}
          accent={severityColor(memPercent ?? 0, 'green')}
          percent={memPercent ?? undefined}
        />
        <StatTile
          icon={<HddOutlined />}
          label={t('metrics.disk')}
          value={latestSample && diskPercent !== null ? `${formatBytes(latestSample.disk_used)} / ${formatBytes(server.disk_total)}` : '—'}
          accent={severityColor(diskPercent ?? 0, 'violet')}
          percent={diskPercent ?? undefined}
        />
        {/* Uptime, latency and rates describe the present. A stale sample has
            nothing to say about them, so they stay blank instead of borrowing
            values from whenever the host was last seen. */}
        <StatTile
          icon={<ClockCircleOutlined />}
          label={t('metrics.uptime')}
          value={live && latestSample ? formatUptime(latestSample.uptime_seconds) : '—'}
          hint={server.cpu_cores > 0 ? t('detail.cores', { count: server.cpu_cores }) : undefined}
          accent="#4bb3d6"
        />
        {/* Windows reports no load average; three zeros would read as idle. */}
        <StatTile
          icon={<ThunderboltOutlined />}
          label={t('metrics.latency')}
          value={live && latestSample?.latency_ms ? `${latestSample.latency_ms} ms` : '—'}
          hint={live && latestSample && server.server_type !== 'windows' ? t('detail.load', { load: `${latestSample.load_1.toFixed(2)} / ${latestSample.load_5.toFixed(2)} / ${latestSample.load_15.toFixed(2)}` }) : undefined}
          accent="#e8944a"
        />
        <StatTile
          icon={<ArrowDownOutlined />}
          label={t('metrics.totalDownload')}
          value={latestSample ? formatBytes(latestSample.network_rx_total_bytes || 0) : '—'}
          hint={live && latestSample ? `${formatBytes(latestSample.network_rx_bytes || 0)}/s` : undefined}
          accent="#39b8a4"
        />
        <StatTile
          icon={<ArrowUpOutlined />}
          label={t('metrics.totalUpload')}
          value={latestSample ? formatBytes(latestSample.network_tx_total_bytes || 0) : '—'}
          hint={live && latestSample ? `${formatBytes(latestSample.network_tx_bytes || 0)}/s` : undefined}
          accent="#8d6dd7"
        />
        {/* A live host only needs the clock; a stale sample may be days old, so
            it carries its date. */}
        <StatTile
          icon={<LineChartOutlined />}
          label={t('detail.sampledAt')}
          value={latestSample?.recorded_at ? (live ? formatTime(latestSample.recorded_at, i18n.language) : formatDateTime(latestSample.recorded_at, i18n.language)) : '—'}
          hint={t('detail.addedOn', { date: formatDate(server.created_at, i18n.language) })}
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
                    value={[dayjs(timeRange.since), dayjs(timeRange.until)]}
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
              <Suspense fallback={tabFallback}><SshTerminal serverId={id!} /></Suspense>
            </div>
          ),
        },
        {
          key: 'docker',
          label: t('docker.title'),
          // Docker stats are fetched over SSH; mount the panel only when it is
          // visible so switching to another tab stops its 15-second poll.
          children: activeTab === 'docker' && dockerInstalled === true ? (
            <Suspense fallback={tabFallback}><ServerDockerPanel serverId={id!} version={server.docker_version} /></Suspense>
          ) : activeTab === 'docker' ? (
            <div className="empty-state"><Empty description={t('docker.noServers')} /></div>
          ) : null,
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
                  loading={savingNotes}
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
        className="server-form-modal"
        title={t('server.edit')}
        open={modalOpen}
        onCancel={() => { if (!savingServer) setModalOpen(false); }}
        onOk={() => form.submit()}
        confirmLoading={savingServer}
        width={720}
      >
        <ServerForm
          form={form}
          editing
          credentialId={selectedCredential}
          onCredentialChange={setSelectedCredential}
          tagIds={tagValues}
          onTagsChange={setTagValues}
          disabled={savingServer}
          onFinish={handleSubmit}
        />
      </Modal>
    </div>
  );
}
