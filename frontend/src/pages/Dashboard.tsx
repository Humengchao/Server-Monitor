import React, { useState, useEffect, useCallback, useMemo } from 'react';
import {
  Row, Col, Button, Modal, Form, Input, InputNumber, Select, Segmented, Tooltip,
  Typography, Space, App, Card, Skeleton, Empty
} from 'antd';
import { DatePicker } from 'antd';
import dayjs from 'dayjs';
import {
  PlusOutlined, ReloadOutlined, FilterOutlined, SafetyOutlined, WindowsOutlined, AppleOutlined,
  CloudServerOutlined, CheckCircleOutlined, DisconnectOutlined, SearchOutlined, AppstoreOutlined,
  BarsOutlined, DashboardOutlined, DatabaseOutlined, WalletOutlined, SortAscendingOutlined,
  CheckSquareOutlined, RiseOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import ServerCard from '../components/ServerCard';
import ServerTable from '../components/ServerTable';
import BatchActionBar from '../components/BatchActionBar';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';
import { serversApi, Server, Tag } from '../api/servers';
import { convertCurrency, currencySymbol, useExchangeRates } from '../hooks/useExchangeRates';
import { useFleetUptime, windowPercent } from '../hooks/useFleetUptime';
import { availabilityColor } from '../api/uptime';
import { monthlyCost, percentOf } from '../utils/format';
import { usePolling } from '../hooks/usePolling';

const { Title, Text } = Typography;

const ONLINE_WINDOW_MS = 120000;

type StatusFilter = 'all' | 'online' | 'offline';
type SortKey = 'default' | 'name' | 'cpu' | 'memory' | 'disk' | 'uptime' | 'expiry';
type ViewMode = 'grid' | 'list';

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

function isOnline(server: Server, observedAt: number): boolean {
  const at = server.latest_metrics?.recorded_at;
  return observedAt > 0 && !!at && observedAt - new Date(at).getTime() < ONLINE_WINDOW_MS;
}

export default function Dashboard() {
  const { t, i18n } = useTranslation();
  // modal (not the static Modal.*) so confirm dialogs inherit the dark theme.
  const { message, modal, notification } = App.useApp();
  const [servers, setServers] = useState<Server[]>([]);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [filterTagIds, setFilterTagIds] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);
  const [refreshTimestamp, setRefreshTimestamp] = useState(0);
  const [query, setQuery] = useState('');
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [sortKey, setSortKey] = useState<SortKey>(() => (localStorage.getItem('dashboard_sort') as SortKey) || 'default');
  const [view, setView] = useState<ViewMode>(() => (localStorage.getItem('dashboard_view') as ViewMode) || 'grid');
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const ratesPerEUR = useExchangeRates();
  const uptime = useFleetUptime();

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
    const loginTime = new Date(logged_at).toLocaleString();

    // Helper to show/update the notification
    const showNotification = (currentDesc: string, lastDesc: string) => {
      notification.info({
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
        icon: <SafetyOutlined style={{ color: '#1890ff' }} />,
        placement: 'bottomRight',
        duration: 10,
      });
    };

    // Show basic notification immediately, then enrich with geolocation
    showNotification('', `IP: ${ip}  Time: ${loginTime}`);

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
        const currentDesc = currentLoc ? `IP: ${currentIP}\nLocation: ${currentLoc}` : `IP: ${currentIP}`;
        const lastDesc = lastLoc
          ? `IP: ${ip}\nLocation: ${lastLoc}\nTime: ${loginTime}`
          : `IP: ${ip}\nTime: ${loginTime}`;
        showNotification(currentDesc, lastDesc);
      })
      .catch(() => { clearTimeout(ipTimer); /* silently degrade */ });
  }, []);

  const loadServers = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    try {
      const res = await serversApi.list();
      setServers(res.data || []);
      // Judge online/offline against the server's clock (Date header), not the
      // browser's: local clock skew beyond the 2-minute threshold would
      // otherwise flip every card to offline (or keep dead ones online).
      const dateHeader = res.headers?.date;
      const serverNow = typeof dateHeader === 'string' ? Date.parse(dateHeader) : NaN;
      setRefreshTimestamp(Number.isNaN(serverNow) ? Date.now() : serverNow);
    } catch {
      if (showLoading) message.error(t('server.loadFailed'));
    }
    if (showLoading) setLoading(false);
  }, [message, t]);

  usePolling(() => loadServers(false), 3000, { leading: false });

  useEffect(() => {
    loadServers();
  }, [loadServers]);

  const handleSubmit = async (values: any) => {
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
    } catch (err: any) {
      message.error(err.response?.data?.error || t('server.operationFailed'));
    }
  };

  const allTags = useMemo(() => {
    const map = new Map<string, Tag>();
    servers.forEach((s) => s.tags?.forEach((tag) => map.set(tag.id, tag)));
    return Array.from(map.values());
  }, [servers]);

  const filteredServers = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const matched = servers.filter((s) => {
      if (filterTagIds.length > 0 && !filterTagIds.some((id) => s.tags?.some((tag) => tag.id === id))) return false;
      if (statusFilter !== 'all' && isOnline(s, refreshTimestamp) !== (statusFilter === 'online')) return false;
      if (!needle) return true;
      const haystack = [s.name, s.host, s.public_location, s.notes, ...(s.tags || []).map((tag) => tag.name)]
        .filter(Boolean).join(' ').toLowerCase();
      return haystack.includes(needle);
    });

    if (sortKey === 'default') return matched;
    const sorted = [...matched];
    sorted.sort((a, b) => {
      switch (sortKey) {
        case 'name':
          return a.name.localeCompare(b.name);
        case 'cpu':
          return (b.latest_metrics?.cpu_percent || 0) - (a.latest_metrics?.cpu_percent || 0);
        case 'memory':
          return percentOf(b.latest_metrics?.memory_used || 0, b.latest_metrics?.memory_total || 0)
            - percentOf(a.latest_metrics?.memory_used || 0, a.latest_metrics?.memory_total || 0);
        case 'disk':
          return percentOf(b.latest_metrics?.disk_used || 0, b.disk_total)
            - percentOf(a.latest_metrics?.disk_used || 0, a.disk_total);
        case 'uptime':
          return (b.latest_metrics?.uptime_seconds || 0) - (a.latest_metrics?.uptime_seconds || 0);
        case 'expiry': {
          // Hosts without an expiry date sort last rather than first.
          const av = a.expires_at ? new Date(a.expires_at).getTime() : Number.POSITIVE_INFINITY;
          const bv = b.expires_at ? new Date(b.expires_at).getTime() : Number.POSITIVE_INFINITY;
          return av - bv;
        }
        default:
          return 0;
      }
    });
    return sorted;
  }, [servers, filterTagIds, statusFilter, query, sortKey, refreshTimestamp]);

  const stats = useMemo(() => {
    const online = servers.filter((s) => isOnline(s, refreshTimestamp));
    const cpuValues = online.map((s) => s.latest_metrics?.cpu_percent || 0);
    const memValues = online
      .map((s) => percentOf(s.latest_metrics?.memory_used || 0, s.latest_metrics?.memory_total || 0));
    // With nothing online there is no average to report; "0%" would read as
    // "everything is idle" rather than "no data".
    const average = (values: number[]) =>
      (values.length ? `${Math.round(values.reduce((sum, v) => sum + v, 0) / values.length)}%` : '—');

    const displayCurrency = i18n.language?.startsWith('zh') ? 'CNY' : 'USD';
    const spend = servers.reduce((sum, s) => sum + convertCurrency(
      monthlyCost(s.billing_price || 0, s.billing_cycle || 'year'),
      s.billing_currency || 'CNY',
      displayCurrency,
      ratesPerEUR,
    ), 0);

    return {
      total: servers.length,
      online: online.length,
      offline: Math.max(servers.length - online.length, 0),
      avgCPU: average(cpuValues),
      avgMemory: average(memValues),
      spend,
      displayCurrency,
    };
  }, [servers, refreshTimestamp, ratesPerEUR, i18n.language]);

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

  // Stable identities: ServerCard is memoized and holds onto these callbacks
  // across renders it deliberately skips.
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
  ];

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

      {/* A CSS grid rather than antd columns: seven tiles never divide 24
          evenly, and antd's xl={3} left a 12.5% hole plus tiles too narrow for
          "100.00%". auto-fit gives every tile the same width and the row
          reflows to 4/3/2 columns as the viewport shrinks. */}
      <div className="overview-grid overview-grid-7">
        <Card className="overview-card overview-card-primary" variant="borderless">
          <div className="overview-icon"><CloudServerOutlined /></div>
          <div><Text type="secondary">{t('dashboard.totalServers')}</Text><strong>{stats.total}</strong></div>
        </Card>
        <Card className="overview-card overview-card-success" variant="borderless">
          <div className="overview-icon"><CheckCircleOutlined /></div>
          <div><Text type="secondary">{t('dashboard.online')}</Text><strong>{stats.online}</strong></div>
        </Card>
        <Card className={`overview-card ${stats.offline > 0 ? 'overview-card-danger' : 'overview-card-muted'}`} variant="borderless">
          <div className="overview-icon"><DisconnectOutlined /></div>
          <div><Text type="secondary">{t('dashboard.offline')}</Text><strong>{stats.offline}</strong></div>
        </Card>
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
              <strong>{currencySymbol(stats.displayCurrency)}{stats.spend.toFixed(stats.spend >= 100 ? 0 : 1)}</strong>
            </div>
          </Card>
        </Tooltip>
      </div>

      <div className="fleet-toolbar">
        <Segmented
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
            value={sortKey}
            onChange={setSortKey}
            options={sortOptions}
            suffixIcon={<SortAscendingOutlined />}
          />
          <Tooltip title={selecting ? t('batch.exitSelection') : t('batch.enterSelection')}>
            <Button
              type={selecting ? 'primary' : 'default'}
              icon={<CheckSquareOutlined />}
              onClick={() => (selecting ? exitSelection() : setSelecting(true))}
            />
          </Tooltip>
          <Segmented
            value={view}
            onChange={(value) => setView(value as ViewMode)}
            options={[
              { value: 'grid', icon: <Tooltip title={t('dashboard.gridView')}><AppstoreOutlined /></Tooltip> },
              { value: 'list', icon: <Tooltip title={t('dashboard.listView')}><BarsOutlined /></Tooltip> },
            ]}
          />
        </div>
      </div>

      <div className="section-heading">
        <div><Title level={4}>{t('dashboard.infrastructure')}</Title><Text type="secondary">{t('dashboard.realtime')}</Text></div>
        <Text type="secondary">{filteredServers.length} / {servers.length}</Text>
      </div>

      {loading ? (
        <Row gutter={[18, 18]}>{[1, 2, 3, 4].map((item) => <Col key={item} xs={24} sm={12} xl={6}><Card className="server-card"><Skeleton active /></Card></Col>)}</Row>
      ) : filteredServers.length === 0 ? (
        <div className="empty-state">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={servers.length === 0 ? t('server.empty') : t('dashboard.noMatches')}
          />
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
        />
      ) : (
        <Row gutter={[18, 18]}>
          {filteredServers.map((s) => (
            <Col key={s.id} xs={24} sm={12} lg={8} xl={6}>
              <ServerCard
                server={s}
                observedAt={refreshTimestamp}
                onEdit={handleEdit}
                onDelete={handleDelete}
                selectable={selecting}
                selected={selectedIds.includes(s.id)}
                onToggleSelect={toggleSelect}
                availability={availability.get(s.id)}
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
        onCancel={() => { setModalOpen(false); setEditingServer(null); }}
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
                <Input.Password placeholder={t('server.sshPasswordPlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_key" label={t('server.sshKey')}>
                <Input.TextArea rows={4} placeholder={t('server.sshKeyPlaceholder')} />
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
