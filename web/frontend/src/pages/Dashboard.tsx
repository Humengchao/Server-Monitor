import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import {
  Row, Col, Button, Modal, Form, Input, InputNumber, Select, Segmented, Tooltip,
  Typography, Space, App, Card, Skeleton, Empty, Result, Alert
} from 'antd';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import {
  PlusOutlined, ReloadOutlined, FilterOutlined, SafetyOutlined, WindowsOutlined, DesktopOutlined,
  CloudServerOutlined, CheckCircleOutlined, DisconnectOutlined, SearchOutlined, AppstoreOutlined,
  BarsOutlined, DashboardOutlined, DatabaseOutlined, WalletOutlined, SortAscendingOutlined,
  CheckSquareOutlined, RiseOutlined, ClockCircleOutlined, AlertOutlined, CalendarOutlined, WarningOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import ServerCard from '../components/ServerCard';
import ServerTable from '../components/ServerTable';
import FleetResources from '../components/FleetResources';
import BatchActionBar from '../components/BatchActionBar';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';
import { serversApi, Server, Tag } from '../api/servers';
import { convertCurrency, currencySymbol, useExchangeRates } from '../hooks/useExchangeRates';
import { useFleetUptime, windowPercent } from '../hooks/useFleetUptime';
import { useFiringAlerts } from '../hooks/useFiringAlerts';
import { availabilityColor } from '../api/uptime';
import { formatDateTime, formatTime, monthlyCost } from '../utils/format';
import { compareServers, highResourceUsage, needsRenewal, serverStatus, summarizeFleet, RESOURCE_WARNING_PERCENT, RENEWAL_WINDOW_DAYS } from '../utils/fleet';
import type { FleetSort, ServerStatus } from '../utils/fleet';
import { hasPollError } from '../utils/pollError';
import { usePolling } from '../hooks/usePolling';

const { Title, Text } = Typography;

const POLL_INTERVAL_MS = 3000;
// Show the stale banner only after several consecutive failed background
// polls, so a single dropped request does not flash an alarm.
const POLL_STALE_AFTER_MS = 3 * POLL_INTERVAL_MS;

type StatusFilter = 'all' | ServerStatus;
type FocusFilter = 'all' | 'alerts' | 'resource' | 'expiry' | 'issues';
type SortKey = FleetSort;
type ViewMode = 'grid' | 'list';

interface ServerFormValues {
  name: string;
  host: string;
  port?: number;
  ssh_username?: string;
  ssh_password?: string;
  ssh_key?: string;
  ssh_host_key?: string;
  server_type?: string;
  expires_at?: Dayjs | null;
  billing_price?: number;
  billing_currency?: string;
  billing_cycle?: string;
  traffic_limit_gb?: number;
  public_location?: string;
}

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

// ipwho.is supports HTTPS on the free tier; the previous ip-api.com endpoint
// was HTTP-only and got blocked as mixed content on HTTPS deployments.
async function lookupIP(ip: string): Promise<string> {
  try {
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), 3000);
    const res = await fetch(
      `https://ipwho.is/${ip}?fields=success,country,city,connection`,
      { signal: ac.signal },
    );
    clearTimeout(timer);
    const data = await res.json();
    if (data.success) {
      const isp = data.connection?.isp || '';
      return [isp, data.country, data.city].filter(Boolean).join(' ');
    }
  } catch { /* timeout or network error */ }
  return '';
}

export default function Dashboard() {
  const { t, i18n } = useTranslation();
  // modal (not the static Modal.*) so confirm dialogs inherit the dark theme.
  const { message, modal, notification } = App.useApp();
  const [servers, setServers] = useState<Server[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const loadInFlightRef = useRef(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [savingServer, setSavingServer] = useState(false);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [form] = Form.useForm<ServerFormValues>();
  const serverType = Form.useWatch('server_type', form) || 'linux';
  const sshHostKey = Form.useWatch('ssh_host_key', form);
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [filterTagIds, setFilterTagIds] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);
  const [refreshTimestamp, setRefreshTimestamp] = useState(0);
  // Timestamp of the last successful poll; drives the stale-data banner.
  const lastPollSuccessRef = useRef(0);
  const [pollStale, setPollStale] = useState(false);
  const [query, setQuery] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [focusFilter, setFocusFilter] = useState<FocusFilter>('all');
  const [sortKey, setSortKey] = useState<SortKey>(() => (localStorage.getItem('dashboard_sort') as SortKey) || 'default');
  const [view, setView] = useState<ViewMode>(() => (localStorage.getItem('dashboard_view') as ViewMode) || 'grid');
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const ratesPerEUR = useExchangeRates();
  const uptime = useFleetUptime();
  const firing = useFiringAlerts();

  useEffect(() => { localStorage.setItem('dashboard_view', view); }, [view]);
  useEffect(() => { localStorage.setItem('dashboard_sort', sortKey); }, [sortKey]);

  useEffect(() => {
    const raw = localStorage.getItem('last_login');
    if (!raw) return;
    localStorage.removeItem('last_login');

    let parsed: { ip: string; logged_at: string };
    try {
      parsed = JSON.parse(raw);
    } catch {
      return;
    }

    const { ip, logged_at } = parsed;
    const loginTime = formatDateTime(logged_at, i18n.language);

    // Helper to show/update the notification. Both calls share one key, so
    // the geolocation-enriched copy replaces the instant one instead of
    // stacking a second toast on top of it.
    const showNotification = (currentDesc: string, lastDesc: string) => {
      notification.info({
        key: 'login-info',
        title: t('notification.loginSuccess'),
        description: (
          <div style={{ whiteSpace: 'pre-line' }}>
            {currentDesc ? (
              <>
                <div><Text strong>{t('notification.currentLogin')}</Text></div>
                <div>{currentDesc}</div>
                <div style={{ marginTop: 8 }}><Text strong>{t('notification.previousLogin')}</Text></div>
              </>
            ) : (
              <div><Text strong>{t('notification.previousLogin')}</Text></div>
            )}
            <div>{lastDesc}</div>
          </div>
        ),
        icon: <SafetyOutlined style={{ color: '#4f7cff' }} />,
        placement: 'bottomRight',
        duration: 10,
      });
    };

    // Show basic notification immediately, then enrich with geolocation
    showNotification('', `${t('notification.ipLabel')} ${ip}  ${t('notification.timeLabel')} ${loginTime}`);

    const ac = new AbortController();
    const ipTimer = setTimeout(() => ac.abort(), 4000);
    fetch('https://api.ipify.org?format=json', { signal: ac.signal })
      .then((r) => r.json())
      .then(async (data) => {
        clearTimeout(ipTimer);
        const currentIP = data.ip;
        const [currentLoc, lastLoc] = await Promise.all([
          lookupIP(currentIP),
          lookupIP(ip),
        ]);
        const currentDesc = currentLoc
          ? `${t('notification.ipLabel')} ${currentIP}\n${t('notification.locationLabel')} ${currentLoc}`
          : `${t('notification.ipLabel')} ${currentIP}`;
        const lastDesc = lastLoc
          ? `${t('notification.ipLabel')} ${ip}\n${t('notification.locationLabel')} ${lastLoc}\n${t('notification.timeLabel')} ${loginTime}`
          : `${t('notification.ipLabel')} ${ip}\n${t('notification.timeLabel')} ${loginTime}`;
        showNotification(currentDesc, lastDesc);
      })
      .catch(() => { clearTimeout(ipTimer); /* silently degrade */ });
  }, [notification, t, i18n.language]);

  const loadServers = useCallback(async (showLoading = true) => {
    if (loadInFlightRef.current) return;
    loadInFlightRef.current = true;
    if (showLoading) {
      setLoading(true);
      setLoadError(false);
    }
    try {
      const res = await serversApi.list();
      setServers(res.data || []);
      setLoadError(false);
      lastPollSuccessRef.current = Date.now();
      setPollStale(false);
      // Judge online/offline against the server's clock (Date header), not the
      // browser's: local clock skew beyond the 2-minute threshold would
      // otherwise flip every card to offline (or keep dead ones online).
      const dateHeader = res.headers?.date;
      const serverNow = typeof dateHeader === 'string' ? Date.parse(dateHeader) : NaN;
      setRefreshTimestamp(Number.isNaN(serverNow) ? Date.now() : serverNow);
    } catch {
      if (showLoading) {
        setLoadError(true);
        message.error(t('server.loadFailed'));
      }
      // Background failures stay quiet at first, but once the outage outlasts
      // a few poll cycles the stale banner surfaces; it clears on recovery.
      const lastSuccess = lastPollSuccessRef.current;
      if (lastSuccess > 0 && Date.now() - lastSuccess >= POLL_STALE_AFTER_MS) setPollStale(true);
    } finally {
      if (showLoading) setLoading(false);
      loadInFlightRef.current = false;
    }
  }, [message, t]);

  usePolling(() => loadServers(false), POLL_INTERVAL_MS, { leading: false });

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadServers(); }, 0);
    return () => window.clearTimeout(timer);
  }, [loadServers]);

  const handleSubmit = async (values: ServerFormValues) => {
    setSavingServer(true);
    try {
      const payload = {
        ...values,
        name: typeof values.name === 'string' ? values.name.trim() : values.name,
        host: typeof values.host === 'string' ? values.host.trim() : values.host,
        ssh_username: typeof values.ssh_username === 'string' ? values.ssh_username.trim() : values.ssh_username,
        // When a shared credential is selected, do not submit stale values
        // from the unmounted direct-auth fields.
        ssh_password: selectedCredential ? undefined : values.ssh_password,
        ssh_key: serverType === 'windows' || selectedCredential ? undefined : values.ssh_key,
        ssh_host_key: serverType === 'windows' ? undefined : values.ssh_host_key,
        port: serverType === 'windows' ? undefined : values.port,
        credential_id: selectedCredential || null,
        server_type: serverType,
        expires_at: values.expires_at ? values.expires_at.toISOString() : null,
        traffic_limit_bytes: Math.round((values.traffic_limit_gb || 0) * 1024 * 1024 * 1024),
        notes: editingServer?.notes || '',
      };
      if (editingServer) {
        await serversApi.update(editingServer.id, payload);
        await serversApi.setTags(editingServer.id, tagValues);
        message.success(t('server.updated'));
      } else {
        const res = await serversApi.create(payload);
        if (tagValues.length > 0) {
          await serversApi.setTags(res.data.id, tagValues);
        }
        message.success(t('server.added'));
      }
      setModalOpen(false);
      form.resetFields();
      setTagValues([]);
      setSelectedCredential(undefined);
      setEditingServer(null);
      loadServers();
    } catch (err: unknown) {
      message.error(apiError(err, t('server.operationFailed')));
    } finally {
      setSavingServer(false);
    }
  };

  const allTags = useMemo(() => {
    const map = new Map<string, Tag>();
    servers.forEach((s) => s.tags?.forEach((tag) => map.set(tag.id, tag)));
    return Array.from(map.values());
  }, [servers]);

  const scopedServers = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return servers.filter((s) => {
      if (filterTagIds.length > 0 && !filterTagIds.some((id) => s.tags?.some((tag) => tag.id === id))) return false;
      if (statusFilter !== 'all' && serverStatus(s, refreshTimestamp) !== statusFilter) return false;
      if (!needle) return true;
      const haystack = [s.name, s.host, s.public_location, s.notes, ...(s.tags || []).map((tag) => tag.name)]
        .filter(Boolean).join(' ').toLowerCase();
      return haystack.includes(needle);
    });

  }, [servers, filterTagIds, statusFilter, query, refreshTimestamp]);

  // Counts follow search / tag / status, so each shortcut describes exactly
  // the set it will show. A host can belong to more than one attention group.
  const focusGroups = useMemo(() => ({
    all: scopedServers,
    alerts: scopedServers.filter(server => !!firing.byServer.get(server.id)?.length),
    resource: scopedServers.filter(server => highResourceUsage(server, refreshTimestamp)),
    expiry: scopedServers.filter(server => needsRenewal(server, refreshTimestamp)),
    issues: scopedServers.filter(server => hasPollError(server)),
  }), [scopedServers, firing.byServer, refreshTimestamp]);

  const filteredServers = useMemo(() => [...focusGroups[focusFilter]]
    .sort((a, b) => compareServers(a, b, sortKey, refreshTimestamp)),
  [focusGroups, focusFilter, sortKey, refreshTimestamp]);

  const hasFilters = !!query.trim() || filterTagIds.length > 0 || statusFilter !== 'all' || focusFilter !== 'all';
  const clearFilters = () => {
    setQuery('');
    setFilterTagIds([]);
    setStatusFilter('all');
    setFocusFilter('all');
  };

  const fleet = useMemo(() => summarizeFleet(servers, refreshTimestamp), [servers, refreshTimestamp]);

  const stats = useMemo(() => {
    const displayCurrency = i18n.language?.startsWith('zh') ? 'CNY' : 'USD';
    const spend = servers.reduce((sum, s) => sum + convertCurrency(
      monthlyCost(s.billing_price || 0, s.billing_cycle || 'year'),
      s.billing_currency || 'CNY',
      displayCurrency,
      ratesPerEUR,
    ), 0);

    return {
      ...fleet,
      avgCPU: fleet.avgCPU === null ? '—' : `${Math.round(fleet.avgCPU)}%`,
      avgMemory: fleet.avgMemory === null ? '—' : `${Math.round(fleet.avgMemory)}%`,
      spend,
      displayCurrency,
    };
  }, [servers, fleet, ratesPerEUR, i18n.language]);

  // 24h availability per server, plus the fleet mean for the overview tile.
  const availability = useMemo(() => {
    const map = new Map<string, number | undefined>();
    for (const server of servers) {
      map.set(server.id, windowPercent(uptime.byServer.get(server.id), '24h'));
    }
    return map;
  }, [servers, uptime.byServer]);

  const fleetAvailability = useMemo(() => {
    const values = [...availability.values()].filter((v): v is number => typeof v === 'number');
    if (values.length === 0) return null;
    return values.reduce((sum, v) => sum + v, 0) / values.length;
  }, [availability]);

  const handleEdit = useCallback((server: Server) => {
    setEditingServer(server);
    setSelectedCredential(server.credential_id || undefined);
    // Clear leftovers from a previous add/edit first: antd preserves values of
    // unmounted fields, so a password typed for another server would otherwise
    // ride along on submit and silently overwrite this server's credentials.
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
  }, [form]);

  const handleDelete = useCallback((server: Server) => {
    modal.confirm({
      title: t('server.delete'),
      content: t('server.deleteConfirm', { name: server.name }),
      okText: t('common.delete'),
      okType: 'danger',
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success(t('server.deleted'));
          loadServers();
        } catch {
          message.error(t('server.deleteFailed'));
        }
      },
    });
  }, [t, modal, message, loadServers]);

  const statusOptions = [
    { label: t('dashboard.filterAll'), value: 'all' as const },
    { label: `${t('dashboard.online')} ${stats.online}`, value: 'online' as const },
    { label: `${t('dashboard.offline')} ${stats.offline}`, value: 'offline' as const },
    { label: `${t('dashboard.pending')} ${stats.pending}`, value: 'pending' as const },
  ];

  const focusOptions = [
    { value: 'all', label: t('dashboard.focusAll'), icon: <CloudServerOutlined aria-hidden /> },
    { value: 'alerts', label: t('dashboard.focusAlerts'), icon: <AlertOutlined aria-hidden /> },
    { value: 'resource', label: t('dashboard.focusResource', { percent: RESOURCE_WARNING_PERCENT }), icon: <DashboardOutlined aria-hidden /> },
    { value: 'expiry', label: t('dashboard.focusExpiry', { days: RENEWAL_WINDOW_DAYS }), icon: <CalendarOutlined aria-hidden /> },
    { value: 'issues', label: t('dashboard.focusIssues'), icon: <WarningOutlined aria-hidden /> },
  ] as const;

  // Selection survives filtering, but a server that was deleted elsewhere must
  // not linger in it, so reconcile against the live list.
  const selectedServers = useMemo(
    () => servers.filter((s) => selectedIds.includes(s.id)),
    [servers, selectedIds],
  );

  const toggleSelect = useCallback((server: Server) => {
    setSelectedIds((prev) => (prev.includes(server.id)
      ? prev.filter((id) => id !== server.id)
      : [...prev, server.id]));
  }, []);

  const exitSelection = useCallback(() => {
    setSelecting(false);
    setSelectedIds([]);
  }, []);

  const sortOptions: { value: SortKey; label: string }[] = [
    { value: 'default', label: t('dashboard.sortDefault') },
    { value: 'name', label: t('dashboard.sortName') },
    { value: 'cpu', label: t('dashboard.sortCpu') },
    { value: 'memory', label: t('dashboard.sortMemory') },
    { value: 'disk', label: t('dashboard.sortDisk') },
    { value: 'uptime', label: t('dashboard.sortUptime') },
    { value: 'expiry', label: t('dashboard.sortExpiry') },
  ];

  return (
    <div className="dashboard-page">
      <div className="page-heading">
        <div>
          <Text className="eyebrow">{t('dashboard.overview')}</Text>
          <Title level={2}>{t('server.title')}</Title>
          <Text type="secondary">{t('dashboard.subtitle')}</Text>
        </div>
        <Space wrap className="page-actions">
          <Button icon={<ReloadOutlined />} onClick={() => loadServers()} loading={loading}>{t('common.refresh')}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => {
            setEditingServer(null);
            form.resetFields();
            setTagValues([]);
            setSelectedCredential(undefined);
            setModalOpen(true);
          }}>
            {t('server.add')}
          </Button>
        </Space>
      </div>

      {(pollStale || (loadError && refreshTimestamp > 0)) && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('dashboard.pollStale')}
          description={t('dashboard.pollStaleHint')}
        />
      )}

      <div className="overview-grid overview-grid-8">
        <Card className="overview-card overview-card-primary" variant="borderless">
          <div className="overview-icon"><CloudServerOutlined /></div>
          <div><Text type="secondary">{t('dashboard.totalServers')}</Text><strong>{refreshTimestamp ? stats.total : '—'}</strong></div>
        </Card>
        <Card className="overview-card overview-card-success" variant="borderless">
          <div className="overview-icon"><CheckCircleOutlined /></div>
          <div><Text type="secondary">{t('dashboard.online')}</Text><strong>{refreshTimestamp ? stats.online : '—'}</strong></div>
        </Card>
        <Card className={`overview-card ${stats.offline > 0 ? 'overview-card-danger' : 'overview-card-muted'}`} variant="borderless">
          <div className="overview-icon"><DisconnectOutlined /></div>
          <div><Text type="secondary">{t('dashboard.offline')}</Text><strong>{refreshTimestamp ? stats.offline : '—'}</strong></div>
        </Card>
        <Tooltip title={t('dashboard.pendingHint')}>
          <Card className="overview-card overview-card-muted" variant="borderless">
            <div className="overview-icon"><ClockCircleOutlined /></div>
            <div><Text type="secondary">{t('dashboard.pending')}</Text><strong>{refreshTimestamp ? stats.pending : '—'}</strong></div>
          </Card>
        </Tooltip>
        <Card className="overview-card overview-card-accent" variant="borderless">
          <div className="overview-icon"><DashboardOutlined /></div>
          <div><Text type="secondary">{t('dashboard.avgCpu')}</Text><strong>{stats.avgCPU}</strong></div>
        </Card>
        <Card className="overview-card overview-card-teal" variant="borderless">
          <div className="overview-icon"><DatabaseOutlined /></div>
          <div><Text type="secondary">{t('dashboard.avgMemory')}</Text><strong>{stats.avgMemory}</strong></div>
        </Card>
        <Tooltip title={t('uptime.badgeHint')}>
          <Card className="overview-card overview-card-uptime" variant="borderless">
            <div className="overview-icon"><RiseOutlined /></div>
            <div>
              <Text type="secondary">{t('uptime.overviewLabel')}</Text>
              <strong style={fleetAvailability === null ? undefined : { color: availabilityColor(fleetAvailability) }}>
                {fleetAvailability === null ? '—' : `${fleetAvailability.toFixed(2)}%`}
              </strong>
            </div>
          </Card>
        </Tooltip>
        <Tooltip title={t('dashboard.monthlySpendHint')}>
          <Card className="overview-card overview-card-amber" variant="borderless">
            <div className="overview-icon"><WalletOutlined /></div>
            <div>
              <Text type="secondary">{t('dashboard.monthlySpend')}</Text>
              <strong>{refreshTimestamp ? `${currencySymbol(stats.displayCurrency)}${stats.spend.toFixed(stats.spend >= 100 ? 0 : 1)}` : '—'}</strong>
            </div>
          </Card>
        </Tooltip>
      </div>

      {servers.length > 0 && <FleetResources fleet={fleet} />}

      <div className="fleet-toolbar">
        <Segmented
          aria-label={t('dashboard.statusFilter')}
          value={statusFilter}
          onChange={(value) => setStatusFilter(value as StatusFilter)}
          options={statusOptions}
        />
        <div className="fleet-toolbar-tools">
          <Input
            allowClear
            className="fleet-search"
            prefix={<SearchOutlined />}
            placeholder={t('dashboard.searchPlaceholder')}
            aria-label={t('dashboard.searchPlaceholder')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          {allTags.length > 0 && (
            <Select
              mode="multiple"
              placeholder={<Space><FilterOutlined />{t('server.filterByTag')}</Space>}
              value={filterTagIds}
              onChange={setFilterTagIds}
              className="tag-filter"
              maxTagCount="responsive"
              allowClear
            >
              {allTags.map((tag) => (
                <Select.Option key={tag.id} value={tag.id}>
                  <span style={{ color: tag.color }}>●</span> {tag.name}
                </Select.Option>
              ))}
            </Select>
          )}
          <Select
            className="sort-select"
            aria-label={t('dashboard.sortLabel')}
            value={sortKey}
            onChange={setSortKey}
            options={sortOptions}
            suffixIcon={<SortAscendingOutlined />}
          />
          <Tooltip title={selecting ? t('batch.exitSelection') : t('batch.enterSelection')}>
            <Button
              type={selecting ? 'primary' : 'default'}
              aria-label={selecting ? t('batch.exitSelection') : t('batch.enterSelection')}
              icon={<CheckSquareOutlined />}
              onClick={() => (selecting ? exitSelection() : setSelecting(true))}
            />
          </Tooltip>
          <Segmented
            value={view}
            onChange={(value) => setView(value as ViewMode)}
            options={[
              { value: 'grid', icon: <Tooltip title={t('dashboard.gridView')}><AppstoreOutlined aria-label={t('dashboard.gridView')} /></Tooltip> },
              { value: 'list', icon: <Tooltip title={t('dashboard.listView')}><BarsOutlined aria-label={t('dashboard.listView')} /></Tooltip> },
            ]}
          />
        </div>
      </div>

      {servers.length > 0 && (
        <div className="fleet-focus" role="group" aria-label={t('dashboard.focusLabel')}>
          {focusOptions.map(option => {
            const unavailable = option.value === 'alerts' && (firing.loading || firing.error);
            return (
              <Tooltip key={option.value} title={option.value === 'expiry' ? t('dashboard.focusExpiryHint')
                : option.value === 'resource' ? t('dashboard.focusResourceHint', { percent: RESOURCE_WARNING_PERCENT })
                : option.value === 'alerts' && firing.error ? t('dashboard.alertsUnavailable') : undefined}>
                <button
                  type="button"
                  className={`fleet-focus-button${focusFilter === option.value ? ' is-active' : ''}`}
                  aria-pressed={focusFilter === option.value}
                  disabled={unavailable}
                  onClick={() => setFocusFilter(option.value)}
                >
                  {option.icon}<span>{option.label}</span>
                  <strong>{unavailable ? '—' : focusGroups[option.value].length}</strong>
                </button>
              </Tooltip>
            );
          })}
        </div>
      )}
      {focusFilter === 'alerts' && firing.error && <Text type="warning">{t('dashboard.alertsUnavailable')}</Text>}

      <div className="section-heading">
        <div><Title level={4}>{t('dashboard.infrastructure')}</Title>
          <Text type="secondary" title={refreshTimestamp ? formatDateTime(refreshTimestamp, i18n.language) : undefined}>
            {refreshTimestamp ? t('dashboard.updatedAt', { time: formatTime(refreshTimestamp, i18n.language) }) : t('common.loading')}
          </Text>
        </div>
        <Space>
          <Text type="secondary">{t('dashboard.resultCount', { count: filteredServers.length, total: servers.length })}</Text>
          {hasFilters && <Button size="small" type="link" onClick={clearFilters}>{t('dashboard.clearFilters')}</Button>}
        </Space>
      </div>

      {loading ? (
        <Row gutter={[18, 18]}>{[1, 2, 3, 4].map((item) => <Col key={item} xs={24} sm={12} xl={6}><Card className="server-card"><Skeleton active /></Card></Col>)}</Row>
      ) : loadError && servers.length === 0 ? (
        <Result
          status="error"
          title={t('server.loadFailed')}
          extra={<Button type="primary" icon={<ReloadOutlined />} onClick={() => { void loadServers(); }}>{t('common.refresh')}</Button>}
        />
      ) : filteredServers.length === 0 ? (
        <div className="empty-state">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={servers.length === 0 ? t('server.empty') : t('dashboard.noMatches')}
          >
            {servers.length > 0 && <Button onClick={clearFilters}>{t('dashboard.clearFilters')}</Button>}
            {servers.length === 0 && (
              <Button
                type="primary"
                icon={<PlusOutlined />}
                onClick={() => {
                  setEditingServer(null);
                  form.resetFields();
                  setTagValues([]);
                  setSelectedCredential(undefined);
                  setModalOpen(true);
                }}
              >
                {t('server.add')}
              </Button>
            )}
          </Empty>
        </div>
      ) : view === 'list' ? (
        <ServerTable
          servers={filteredServers}
          observedAt={refreshTimestamp}
          onEdit={handleEdit}
          onDelete={handleDelete}
          selectedIds={selecting ? selectedIds : undefined}
          onSelectionChange={setSelectedIds}
          availability={availability}
          firing={firing.byServer}
        />
      ) : (
        <Row gutter={[18, 18]}>
          {filteredServers.map((s) => (
            <Col key={s.id} xs={24} sm={12} lg={8} xl={6}>
              <ServerCard
                server={s}
                observedAt={refreshTimestamp}
                selectable={selecting}
                selected={selectedIds.includes(s.id)}
                onToggleSelect={toggleSelect}
                availability={availability.get(s.id)}
                firing={firing.byServer.get(s.id)}
              />
            </Col>
          ))}
        </Row>
      )}

      {selecting && selectedServers.length > 0 && (
        <BatchActionBar
          selected={selectedServers}
          total={filteredServers.length}
          onSelectAll={() => setSelectedIds(filteredServers.map((s) => s.id))}
          onClear={exitSelection}
          onChanged={() => loadServers(false)}
        />
      )}

      <Modal
        title={editingServer ? t('server.edit') : t('server.add')}
        open={modalOpen}
        onCancel={() => { if (!savingServer) { setModalOpen(false); setEditingServer(null); } }}
        onOk={() => form.submit()}
        confirmLoading={savingServer}
        width={680}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit} disabled={savingServer}>
          <Form.Item name="name" label={t('server.serverName')} rules={[{ required: true }]}>
            <Input placeholder={t('server.serverNamePlaceholder')} maxLength={128} />
          </Form.Item>
          <Form.Item name="host" label={t('server.host')} rules={[{ required: true }]}>
            <Input placeholder={t('server.hostPlaceholder')} />
          </Form.Item>
          {serverType !== 'windows' && <Form.Item name="port" label={t('server.sshPort')} initialValue={22}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>}
          <Form.Item name="server_type" label={t('server.type')} initialValue="linux">
            <Select onChange={(value) => {
              setSelectedCredential(undefined);
              form.setFieldsValue(value === 'windows'
                ? { port: undefined, ssh_key: undefined, ssh_host_key: undefined }
                : { port: form.getFieldValue('port') || 22 });
            }}>
              <Select.Option value="linux"><DesktopOutlined /> Linux</Select.Option>
              <Select.Option value="windows"><WindowsOutlined /> Windows</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item label={t('server.credential')}>
            <CredentialSelect value={selectedCredential} onChange={setSelectedCredential} serverType={serverType} />
          </Form.Item>
          {!selectedCredential && (
            <>
              <Form.Item name="ssh_username" label={t(serverType === 'windows' ? 'server.username' : 'server.sshUsername')} rules={[{ required: true }]}>
                <Input placeholder={t(serverType === 'windows' ? 'server.usernamePlaceholder' : 'server.sshUsernamePlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_password" label={t(serverType === 'windows' ? 'server.password' : 'server.sshPassword')} rules={serverType === 'windows' && !editingServer ? [{ required: true }] : undefined}>
                <Input.Password placeholder={editingServer ? t('server.sshKeyEditPlaceholder') : t('server.sshPasswordPlaceholder')} />
              </Form.Item>
              {serverType !== 'windows' && <Form.Item name="ssh_key" label={t('server.sshKey')}>
                <Input.TextArea rows={4} placeholder={editingServer ? t('server.sshKeyEditPlaceholder') : t('server.sshKeyPlaceholder')} />
              </Form.Item>}
            </>
          )}
          {serverType !== 'windows' && <Form.Item
            name="ssh_host_key"
            label={t('server.sshHostKey')}
            extra={sshHostKey?.trim() ? undefined : <Text type="warning">{t('server.sshHostKeyWarning')}</Text>}
          >
            <Input.TextArea rows={2} placeholder={t('server.sshHostKeyPlaceholder')} />
          </Form.Item>}
          <Form.Item name="expires_at" label={t('server.expiresAt')}>
            <DatePicker showTime style={{ width: '100%' }} placeholder={t('server.expiresAtPlaceholder')} />
          </Form.Item>
          <Form.Item name="public_location" label={t('server.publicLocation')}>
            <Input placeholder={t('server.publicLocationPlaceholder')} maxLength={128} />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_price" label={t('server.billingPrice')} initialValue={0}>
                <InputNumber min={0} precision={2} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_currency" label={t('server.billingCurrency')} initialValue="CNY">
                <Select options={[{ value: 'CNY', label: '¥ CNY' }, { value: 'USD', label: '$ USD' }, { value: 'EUR', label: '€ EUR' }]} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_cycle" label={t('server.billingCycle')} initialValue="year">
                <Select options={[
                  { value: 'month', label: t('server.cycleMonth') },
                  { value: 'quarter', label: t('server.cycleQuarter') },
                  { value: 'half_year', label: t('server.cycleHalfYear') },
                  { value: 'year', label: t('server.cycleYear') },
                ]} />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="traffic_limit_gb" label={t('server.trafficLimit')} initialValue={0}>
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
