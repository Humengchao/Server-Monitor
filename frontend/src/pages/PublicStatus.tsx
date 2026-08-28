import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Button, Input, Skeleton } from 'antd';
import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  BarsOutlined,
  CalendarOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  DollarOutlined,
  EnvironmentOutlined,
  GithubOutlined,
  HddOutlined,
  LockOutlined,
  MoonOutlined,
  ReloadOutlined,
  RiseOutlined,
  SearchOutlined,
  SwapOutlined,
  TranslationOutlined,
  WarningFilled,
  WifiOutlined,
  WindowsOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { convertCurrency, currencySymbol, useExchangeRates } from '../hooks/useExchangeRates';
import { availabilityColor } from '../api/uptime';

type NodeStatus = 'online' | 'degraded' | 'offline';
type OverallStatus = 'operational' | 'degraded' | 'outage';
type FilterKey = 'all' | `tag:${string}`;

interface PublicNode {
  alias: string;
  name: string;
  location: string;
  tags: Array<{ name: string; color: string }>;
  server_type: string;
  status: NodeStatus;
  cpu_cores: number;
  cpu_percent: number;
  load_1: number;
  load_5: number;
  load_15: number;
  memory_used: number;
  memory_total: number;
  memory_percent: number;
  disk_used: number;
  disk_total: number;
  disk_percent: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  network_rx_total_bytes: number;
  network_tx_total_bytes: number;
  traffic_limit_bytes: number;
  traffic_percent: number;
  uptime_seconds: number;
  expires_at: string | null;
  remaining_days: number;
  billing_price: number;
  billing_currency: string;
  billing_cycle: string;
  remaining_value: number;
  latency_ms: number;
  packet_loss_percent: number;
  /** Observed 30-day availability; null when the node is too new to score. */
  availability_30d: number | null;
}

interface PublicStatusResponse {
  overall: OverallStatus;
  generated_at: string;
  summary: {
    total: number;
    online: number;
    degraded: number;
    offline: number;
    memory_used: number;
    memory_total: number;
    disk_used: number;
    disk_total: number;
    traffic_total_bytes: number;
    network_rx_bytes: number;
    network_tx_bytes: number;
    remaining_value: number;
  };
  nodes: PublicNode[];
  privacy: { anonymized: boolean; hidden_fields: string[] };
}

const apiURL = `${window.location.origin}/api/public/status`;

const formatBytes = (value: number, digits = 1) => {
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  const amount = value / 1024 ** index;
  return `${amount.toFixed(index > 2 ? 1 : digits)} ${units[index]}`;
};

const formatRate = (value: number) => `${formatBytes(value)}/s`;

const formatUptime = (seconds: number, zh: boolean) => {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  if (zh) return `${days} 天 ${hours} 小时`;
  return `${days}d ${hours}h`;
};

export default function PublicStatus() {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<PublicStatusResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [filter, setFilter] = useState<FilterKey>('all');
  const [query, setQuery] = useState('');
  const [view, setView] = useState<'card' | 'list'>('card');
  const [light, setLight] = useState(false);
  const ratesPerEUR = useExchangeRates();

  const loadStatus = useCallback(async (showLoading = false) => {
    if (showLoading) setLoading(true);
    try {
      const response = await fetch(apiURL, { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error('status request failed');
      setData(await response.json());
      setError(false);
    } catch {
      setError(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => loadStatus(true), 0);
    // A public page can have many concurrent visitors; 10s keeps the status
    // fresh enough without each open tab eating into the per-IP rate limit.
    const timer = window.setInterval(() => loadStatus(), 10000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, [loadStatus]);

  const zh = i18n.language.startsWith('zh');
  const displayCurrency = zh ? 'CNY' : 'USD';
  const toggleLanguage = () => {
    const next = zh ? 'en' : 'zh';
    i18n.changeLanguage(next);
    localStorage.setItem('lang', next);
  };

  const publicTags = useMemo(() => {
    const tags = new Map<string, { name: string; color: string; count: number }>();
    for (const node of data?.nodes || []) {
      for (const tag of node.tags || []) {
        const current = tags.get(tag.name);
        tags.set(tag.name, { name: tag.name, color: tag.color, count: (current?.count || 0) + 1 });
      }
    }
    return [...tags.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [data]);

  const visibleNodes = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return (data?.nodes || []).filter((node) => {
      const matchesSearch = !normalized || `${node.name} ${node.location} ${node.server_type} ${(node.tags || []).map((tag) => tag.name).join(' ')}`.toLowerCase().includes(normalized);
      if (!matchesSearch) return false;
      if (filter.startsWith('tag:')) return (node.tags || []).some((tag) => tag.name === filter.slice(4));
      return true;
    });
  }, [data, filter, query]);

  const filters: Array<{ key: FilterKey; label: string; count: number; color?: string }> = [
    { key: 'all', label: t('probe.filter.all'), count: data?.nodes.length || 0 },
    ...publicTags.map((tag) => ({ key: `tag:${tag.name}` as FilterKey, label: tag.name, count: tag.count, color: tag.color })),
  ];

  const overall = error ? 'outage' : (data?.overall || 'operational');
  const updatedAt = data ? new Date(data.generated_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '—';
  const hasRemainingValue = (data?.nodes || []).some((node) => node.remaining_value > 0);
  const remainingValueLabel = useMemo(() => {
    const valued = (data?.nodes || []).filter((node) => node.remaining_value > 0);
    const total = valued.reduce((sum, node) => (
      sum + convertCurrency(node.remaining_value, node.billing_currency || 'CNY', displayCurrency, ratesPerEUR)
    ), 0);
    return `≈ ${currencySymbol(displayCurrency)}${total.toFixed(2)}`;
  }, [data, displayCurrency, ratesPerEUR]);

  return (
    <div className={`probe-page probe-glass-page ${light ? 'light' : ''}`}>
      <div className="probe-bg" />
      <div className="probe-bg-shade" />

      <header className="glass-header minimal-header">
        <div className="glass-header-status">
          <span className={`overall-dot ${overall}`} />
          <span>{t(`probe.status.${overall}`)}</span>
          <small>{t('probe.updatedAt', { time: updatedAt })}</small>
        </div>
        <div className="glass-header-actions">
          <Button type="text" icon={<TranslationOutlined />} onClick={toggleLanguage}>{zh ? 'EN' : '中文'}</Button>
          <Button type="text" icon={<MoonOutlined />} aria-label={t('probe.theme')} onClick={() => setLight((value) => !value)} />
          <Link to="/login" className="glass-admin-link"><LockOutlined /> {t('probe.console')}</Link>
        </div>
      </header>

      <main className="glass-shell compact-shell">
        <section className="glass-summary-grid">
          <SummaryCard icon={<DatabaseOutlined />} label={t('probe.memoryUsage')} value={`${formatBytes(data?.summary.memory_used || 0)} / ${formatBytes(data?.summary.memory_total || 0)}`} tone="purple" />
          <SummaryCard icon={<HddOutlined />} label={t('probe.diskUsage')} value={`${formatBytes(data?.summary.disk_used || 0)} / ${formatBytes(data?.summary.disk_total || 0)}`} tone="blue" />
          <SummaryCard icon={<SwapOutlined />} label={t('probe.totalTraffic')} value={formatBytes(data?.summary.traffic_total_bytes || 0)} tone="cyan" />
          <SummaryCard icon={<ArrowUpOutlined />} label={t('probe.realtimeUpload')} value={formatRate(data?.summary.network_tx_bytes || 0)} tone="pink" />
          <SummaryCard icon={<ArrowDownOutlined />} label={t('probe.realtimeDownload')} value={formatRate(data?.summary.network_rx_bytes || 0)} tone="green" />
          {hasRemainingValue
            ? <SummaryCard icon={<DollarOutlined />} label={t('probe.remainingValue')} value={remainingValueLabel} tone="amber" />
            : <SummaryCard icon={<CloudServerOutlined />} label={t('probe.online')} value={`${data?.summary.online || 0} / ${data?.summary.total || 0}`} tone="amber" />}
        </section>

        <section className="glass-toolbar">
          <div className="glass-filter-list">
            {filters.map((item) => (
              <button className={filter === item.key ? 'active' : ''} key={item.key} onClick={() => setFilter(item.key)}>
                {item.color && <i className="filter-tag-dot" style={{ backgroundColor: item.color }} />}
                {item.label}<em>{item.count}</em>
              </button>
            ))}
          </div>
          <div className="glass-tools">
            <div className="view-switch">
              <button className={view === 'card' ? 'active' : ''} onClick={() => setView('card')} aria-label={t('probe.cardView')}><AppstoreOutlined /></button>
              <button className={view === 'list' ? 'active' : ''} onClick={() => setView('list')} aria-label={t('probe.listView')}><BarsOutlined /></button>
            </div>
            <Input allowClear prefix={<SearchOutlined />} value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('probe.searchPlaceholder')} />
            <Button className="probe-refresh" icon={<ReloadOutlined />} loading={loading} onClick={() => loadStatus(true)} />
          </div>
        </section>

        {loading && !data ? (
          <div className="glass-node-grid">{[1, 2, 3, 4].map((item) => <div className="glass-node-card" key={item}><Skeleton active /></div>)}</div>
        ) : error && !data ? (
          <div className="glass-empty"><WarningFilled /><h3>{t('probe.unavailable')}</h3><p>{t('probe.unavailableHint')}</p><Button onClick={() => loadStatus(true)}>{t('probe.retry')}</Button></div>
        ) : visibleNodes.length ? (
          <div className={`glass-node-grid ${view === 'list' ? 'list' : ''}`}>
            {visibleNodes.map((node) => (
              <ProbeNodeCard
                key={node.alias}
                node={node}
                zh={zh}
                displayCurrency={displayCurrency}
                ratesPerEUR={ratesPerEUR}
              />
            ))}
          </div>
        ) : (
          <div className="glass-empty"><CloudServerOutlined /><h3>{t('probe.noMatches')}</h3><p>{t('probe.noMatchesHint')}</p></div>
        )}

      </main>

      <footer className="glass-footer github-footer">
        <a href="https://github.com/Humengchao" target="_blank" rel="noreferrer" aria-label="Humengchao on GitHub">
          <GithubOutlined />
          <span>github.com/Humengchao</span>
        </a>
      </footer>
    </div>
  );
}

function SummaryCard({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: string; tone: string }) {
  return <article className="glass-summary-card"><span className={`summary-icon ${tone}`}>{icon}</span><div><small>{label}</small><strong>{value}</strong></div></article>;
}

function ProbeNodeCard({
  node,
  zh,
  displayCurrency,
  ratesPerEUR,
}: {
  node: PublicNode;
  zh: boolean;
  displayCurrency: string;
  ratesPerEUR: Record<string, number>;
}) {
  const { t } = useTranslation();
  const cycleLabel = t(`probe.cycle.${node.billing_cycle || 'year'}`);
  const symbol = currencySymbol(node.billing_currency);
  const displaySymbol = currencySymbol(displayCurrency);
  const convertedRemainingValue = convertCurrency(
    node.remaining_value,
    node.billing_currency || 'CNY',
    displayCurrency,
    ratesPerEUR,
  );
  const totalTraffic = node.network_rx_total_bytes + node.network_tx_total_bytes;
  const expiryText = node.expires_at ? new Date(node.expires_at).toLocaleDateString() : t('probe.notConfigured');

  return (
    <article className={`glass-node-card ${node.status}`}>
      <header className="glass-node-head">
        <span className="node-system-icon">{node.server_type === 'windows' ? <WindowsOutlined /> : <CloudServerOutlined />}</span>
        <div className="node-title"><h2>{node.name || node.alias}</h2><small><EnvironmentOutlined /> {node.location || t('probe.locationUnset')}<b>·</b>{node.server_type === 'windows' ? 'Windows' : 'Linux'}</small></div>
        <span className={`node-online ${node.status}`}><i /> {t(`probe.nodeStatus.${node.status}`)}</span>
      </header>

      <div className="node-plan-row">
        <span><DashboardOutlined /> {t('probe.uptime')} <strong>{formatUptime(node.uptime_seconds, zh)}</strong></span>
        {node.billing_price > 0 && <span><DollarOutlined /> <strong>{symbol}{node.billing_price.toFixed(2)}</strong> / {cycleLabel}</span>}
      </div>

      <div className="resource-stack">
        <ResourceBar label="CPU" percent={node.cpu_percent} detail={`${node.cpu_cores || '—'} ${t('card.core')}`} tone="purple" extra={<span className="load-averages">{t('probe.load')}: {node.load_1.toFixed(2)} / {node.load_5.toFixed(2)} / {node.load_15.toFixed(2)}</span>} />
        <ResourceBar label={t('card.memory')} percent={node.memory_percent} detail={`${formatBytes(node.memory_used)} / ${formatBytes(node.memory_total)}`} tone="green" />
        <ResourceBar label={t('card.disk')} percent={node.disk_percent} detail={`${formatBytes(node.disk_used)} / ${formatBytes(node.disk_total)}`} tone="blue" />
        <ResourceBar label={t('probe.traffic')} percent={node.traffic_percent} detail={node.traffic_limit_bytes > 0 ? `${formatBytes(totalTraffic)} / ${formatBytes(node.traffic_limit_bytes)}` : `${formatBytes(totalTraffic)} / ${t('probe.unlimited')}`} tone="pink" />
      </div>

      <div className="traffic-detail-grid">
        <MetricCell icon={<ArrowUpOutlined />} label={t('probe.realtimeUpload')} value={formatRate(node.network_tx_bytes)} tone="pink" />
        <MetricCell icon={<ArrowDownOutlined />} label={t('probe.realtimeDownload')} value={formatRate(node.network_rx_bytes)} tone="green" />
        <MetricCell icon={<ArrowUpOutlined />} label={t('probe.totalUpload')} value={formatBytes(node.network_tx_total_bytes)} tone="purple" />
        <MetricCell icon={<ArrowDownOutlined />} label={t('probe.totalDownload')} value={formatBytes(node.network_rx_total_bytes)} tone="blue" />
      </div>

      <div className="node-bottom-grid">
        <div><CalendarOutlined /><span>{t('probe.expiry')}<strong>{expiryText}{node.remaining_days > 0 ? ` · ${node.remaining_days} ${t('probe.days')}` : ''}</strong></span></div>
        {node.billing_price > 0 && <div><DollarOutlined /><span>{t('probe.remainingValue')}<strong>{node.remaining_value > 0 ? `≈ ${displaySymbol}${convertedRemainingValue.toFixed(2)}` : '—'}</strong></span></div>}
        <div><WifiOutlined /><span>{t('probe.latency')}<strong>{node.status === 'offline' || node.latency_ms <= 0 ? '—' : `${node.latency_ms} ms`}</strong></span></div>
        <div><SwapOutlined /><span>{t('probe.packetLoss')}<strong>{node.packet_loss_percent}%</strong></span></div>
        <div><RiseOutlined /><span>{t('probe.availability')}<strong style={node.availability_30d === null ? undefined : { color: availabilityColor(node.availability_30d) }}>{node.availability_30d === null ? '—' : `${node.availability_30d.toFixed(2)}%`}</strong></span></div>
      </div>

      {(node.tags || []).length > 0 && <footer className="node-tag-row">{node.tags.map((tag) => <span key={tag.name} style={{ color: tag.color, borderColor: `${tag.color}66`, backgroundColor: `${tag.color}18` }}>{tag.name}</span>)}</footer>}
    </article>
  );
}

function ResourceBar({ label, percent, detail, tone, extra }: { label: string; percent: number; detail: string; tone: string; extra?: React.ReactNode }) {
  return <div className="resource-row"><div><span>{label}</span>{extra}<strong>{percent}% <small>{detail}</small></strong></div><div className="resource-track"><i className={tone} style={{ width: `${Math.max(0, Math.min(100, percent))}%` }} /></div></div>;
}

function MetricCell({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: string; tone: string }) {
  return <div className="traffic-cell"><span className={tone}>{icon}</span><div><small>{label}</small><strong>{value}</strong></div></div>;
}
