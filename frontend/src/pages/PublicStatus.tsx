import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Button, Progress, Skeleton } from 'antd';
import {
  ApiOutlined,
  CheckCircleFilled,
  ClockCircleOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  GlobalOutlined,
  LockOutlined,
  MoonOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  TranslationOutlined,
  WarningFilled,
  WindowsOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';

type NodeStatus = 'online' | 'degraded' | 'offline';
type OverallStatus = 'operational' | 'degraded' | 'outage';

interface PublicNode {
  alias: string;
  server_type: string;
  status: NodeStatus;
  cpu_cores: number;
  cpu_percent: number;
  memory_percent: number;
  uptime_days: number;
}

interface PublicStatusResponse {
  overall: OverallStatus;
  generated_at: string;
  summary: { total: number; online: number; degraded: number; offline: number };
  nodes: PublicNode[];
  privacy: { anonymized: boolean; hidden_fields: string[] };
}

const apiURL = `${window.location.origin}/api/public/status`;

export default function PublicStatus() {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<PublicStatusResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

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
    loadStatus(true);
    const timer = window.setInterval(() => loadStatus(), 15000);
    return () => window.clearInterval(timer);
  }, [loadStatus]);

  const toggleLanguage = () => {
    const next = i18n.language.startsWith('zh') ? 'en' : 'zh';
    i18n.changeLanguage(next);
    localStorage.setItem('lang', next);
  };

  const updatedLabel = useMemo(() => {
    if (!data) return '—';
    return new Date(data.generated_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }, [data]);

  const overall = error ? 'outage' : (data?.overall || 'operational');

  return (
    <div className="probe-page">
      <section className="probe-hero">
        <div className="probe-hero-art" />
        <div className="probe-hero-overlay" />
        <nav className="probe-nav">
          <Link to="/status" className="probe-brand">
            <span><MoonOutlined /></span>
            <div><strong>{t('probe.brand')}</strong><small>PUBLIC STATUS</small></div>
          </Link>
          <div className="probe-nav-actions">
            <span className="probe-privacy-chip"><SafetyCertificateOutlined /> {t('probe.privacyMode')}</span>
            <Button type="text" icon={<TranslationOutlined />} onClick={toggleLanguage}>
              {i18n.language.startsWith('zh') ? 'EN' : '中文'}
            </Button>
            <Link to="/login" className="probe-console-link"><LockOutlined /> {t('probe.console')}</Link>
          </div>
        </nav>

        <div className="probe-hero-content">
          <div className={`probe-live-badge ${overall}`}><span /> {t('probe.live')}</div>
          <h1>{t('probe.title')}</h1>
          <p>{t('probe.subtitle')}</p>
          <div className={`probe-overall ${overall}`}>
            <div className="probe-overall-icon">
              {overall === 'operational' ? <CheckCircleFilled /> : <WarningFilled />}
            </div>
            <div>
              <small>{t('probe.currentStatus')}</small>
              <strong>{t(`probe.status.${overall}`)}</strong>
            </div>
            <div className="probe-updated"><ClockCircleOutlined /> {t('probe.updatedAt', { time: updatedLabel })}</div>
          </div>
        </div>
      </section>

      <main className="probe-main">
        <section className="probe-summary-grid">
          <div><span className="summary-orb violet"><CloudServerOutlined /></span><small>{t('probe.total')}</small><strong>{data?.summary.total ?? '—'}</strong></div>
          <div><span className="summary-orb green"><CheckCircleFilled /></span><small>{t('probe.online')}</small><strong>{data?.summary.online ?? '—'}</strong></div>
          <div><span className="summary-orb amber"><WarningFilled /></span><small>{t('probe.degraded')}</small><strong>{data?.summary.degraded ?? '—'}</strong></div>
          <div><span className="summary-orb slate"><ApiOutlined /></span><small>{t('probe.offline')}</small><strong>{data?.summary.offline ?? '—'}</strong></div>
        </section>

        <section className="probe-section">
          <div className="probe-section-heading">
            <div><span>{t('probe.nodesEyebrow')}</span><h2>{t('probe.nodesTitle')}</h2><p>{t('probe.nodesSubtitle')}</p></div>
            <Button icon={<ReloadOutlined />} loading={loading} onClick={() => loadStatus(true)}>{t('common.refresh')}</Button>
          </div>

          {loading && !data ? (
            <div className="probe-node-grid">{[1, 2, 3].map((item) => <div className="probe-node-card" key={item}><Skeleton active /></div>)}</div>
          ) : error && !data ? (
            <div className="probe-error"><WarningFilled /><h3>{t('probe.unavailable')}</h3><p>{t('probe.unavailableHint')}</p><Button onClick={() => loadStatus(true)}>{t('probe.retry')}</Button></div>
          ) : data?.nodes.length ? (
            <div className="probe-node-grid">
              {data.nodes.map((node) => <ProbeNodeCard key={node.alias} node={node} />)}
            </div>
          ) : (
            <div className="probe-error empty"><CloudServerOutlined /><h3>{t('probe.noNodes')}</h3><p>{t('probe.noNodesHint')}</p></div>
          )}
        </section>

        <section className="probe-privacy-note">
          <div className="privacy-mascot"><SafetyCertificateOutlined /></div>
          <div><span>{t('probe.privacyEyebrow')}</span><h3>{t('probe.privacyTitle')}</h3><p>{t('probe.privacyDescription')}</p></div>
          <div className="privacy-tags"><span>{t('probe.hidden.ip')}</span><span>{t('probe.hidden.credentials')}</span><span>{t('probe.hidden.identity')}</span></div>
        </section>
      </main>

      <footer className="probe-footer">
        <div><MoonOutlined /> {t('probe.brand')}</div>
        <span>{t('probe.footer')}</span>
        <span className="probe-footer-online"><i /> {t('probe.autoRefresh')}</span>
      </footer>
    </div>
  );
}

function ProbeNodeCard({ node }: { node: PublicNode }) {
  const { t } = useTranslation();
  return (
    <article className={`probe-node-card ${node.status}`}>
      <div className="node-card-head">
        <div className="node-avatar">{node.server_type === 'windows' ? <WindowsOutlined /> : <CloudServerOutlined />}</div>
        <div><span>{t('probe.anonymousNode')}</span><h3>{node.alias}</h3></div>
        <div className={`node-status ${node.status}`}><i />{t(`probe.nodeStatus.${node.status}`)}</div>
      </div>
      <div className="node-meta">
        <span><DashboardOutlined /> {node.cpu_cores || '—'} {t('card.core')}</span>
        <span><GlobalOutlined /> {node.server_type === 'windows' ? 'Windows' : 'Linux'}</span>
        <span><ClockCircleOutlined /> {node.uptime_days}d</span>
      </div>
      <div className="node-metric">
        <div><span>CPU</span><strong>{node.cpu_percent}%</strong></div>
        <Progress percent={node.cpu_percent} showInfo={false} strokeColor="#7b79f2" railColor="rgba(111, 119, 154, .12)" />
      </div>
      <div className="node-metric">
        <div><span>{t('card.memory')}</span><strong>{node.memory_percent}%</strong></div>
        <Progress percent={node.memory_percent} showInfo={false} strokeColor="#45c6ad" railColor="rgba(111, 119, 154, .12)" />
      </div>
      <div className="node-card-foot"><span>{t('probe.locationHidden')}</span><span>✦ {t('probe.protected')}</span></div>
    </article>
  );
}
