import React from 'react';
import { Card, Tag, Progress, Typography, Space } from 'antd';
import {
  WindowsOutlined,
  CloudServerOutlined,
  ArrowUpOutlined,
  ArrowDownOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  HddOutlined,
  ClockCircleOutlined,
  CalendarOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { Server } from '../api/servers';
import { useNavigate } from 'react-router-dom';

const { Text } = Typography;

function formatBytes(bytes: number): string {
  if (!bytes) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function formatGB(bytes: number): string {
  if (!bytes) return '0 GB';
  return (bytes / 1024 / 1024 / 1024).toFixed(1) + ' GB';
}

function formatUptime(seconds: number): string {
  if (!seconds) return '0d';
  const totalDays = Math.floor(seconds / 86400);
  const years = Math.floor(totalDays / 365);
  const remDays = totalDays % 365;
  const months = Math.floor(remDays / 30);
  const days = remDays % 30;
  const parts: string[] = [];
  if (years > 0) parts.push(years + 'y');
  if (months > 0) parts.push(months + 'm');
  if (days > 0 || parts.length === 0) parts.push(days + 'd');
  return parts.join(' ');
}

function diffYMD(from: Date, to: Date): { years: number; months: number; days: number } {
  let years = to.getFullYear() - from.getFullYear();
  let months = to.getMonth() - from.getMonth();
  let days = to.getDate() - from.getDate();
  if (days < 0) {
    months--;
    const prevMonth = new Date(to.getFullYear(), to.getMonth(), 0);
    days += prevMonth.getDate();
  }
  if (months < 0) {
    years--;
    months += 12;
  }
  return { years, months, days };
}

function getExpirationInfo(expiresAt?: string | null, lang?: string): { text: string; color: string } | null {
  if (!expiresAt) return null;
  const now = new Date();
  const exp = new Date(expiresAt);
  const isExpired = exp.getTime() < now.getTime();
  const from = isExpired ? exp : now;
  const to = isExpired ? now : exp;
  const { years, months, days } = diffYMD(from, to);

  const parts: string[] = [];
  if (years > 0) parts.push(lang === 'zh' ? `${years}年` : `${years}y`);
  if (months > 0) parts.push(lang === 'zh' ? `${months}月` : `${months}m`);
  if (days > 0 || parts.length === 0) parts.push(lang === 'zh' ? `${days}天` : `${days}d`);
  const diffStr = parts.join('');

  if (isExpired) return { text: lang === 'zh' ? `已过期${diffStr}` : `Expired ${diffStr}`, color: '#ff4d4f' };
  if (years > 0) return { text: lang === 'zh' ? `${diffStr}后到期` : `${diffStr} left`, color: '#52c41a' };
  if (months > 0) return { text: lang === 'zh' ? `${diffStr}后到期` : `${diffStr} left`, color: months <= 1 ? '#ff4d4f' : '#faad14' };
  return { text: lang === 'zh' ? `${diffStr}后到期` : `${diffStr} left`, color: '#ff4d4f' };
}

interface Props {
  server: Server;
  observedAt: number;
}

function ServerCard({ server, observedAt }: Props) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const m = server.latest_metrics;
  const cpuPercent = m ? Math.round(m.cpu_percent) : 0;
  const memPercent = m && m.memory_total ? Math.round((m.memory_used / m.memory_total) * 100) : 0;

  const isOnline = observedAt > 0 && !!m?.recorded_at && observedAt - new Date(m.recorded_at).getTime() < 120000;
  const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';
  const expInfo = getExpirationInfo(server.expires_at, lang);

  return (
    <Card
      hoverable
      className="server-card"
      onClick={() => navigate(`/servers/${server.id}`)}
      tabIndex={0}
      onKeyDown={(event) => { if (event.key === 'Enter') navigate(`/servers/${server.id}`); }}
    >
      <div className="server-card-header">
        <div className={`server-platform ${server.server_type === 'windows' ? 'windows' : 'linux'}`}>
          {server.server_type === 'windows' ? <WindowsOutlined /> : <CloudServerOutlined />}
        </div>
        <div className="server-identity">
          <Text strong ellipsis title={server.name}>{server.name}</Text>
          <Text type="secondary" ellipsis title={server.host}>{server.host}</Text>
        </div>
        <div className={`status-pill ${isOnline ? 'online' : 'offline'}`}>
          <span />{isOnline ? t('dashboard.online') : t('dashboard.offline')}
        </div>
      </div>

      <div className="server-tags">
        {server.tags?.map((tag) => (
          <Tag key={tag.id} color={tag.color}>
            {tag.name}
          </Tag>
        ))}
      </div>

      <div className="server-specs">
        <Space size={5}><DashboardOutlined /><Text type="secondary">{server.cpu_cores || 0} {t('card.core')}</Text></Space>
        <Space size={5}><DatabaseOutlined /><Text type="secondary">{formatGB(server.memory_total)}</Text></Space>
        <Space size={5}><HddOutlined /><Text type="secondary">{formatGB(server.disk_total)}</Text></Space>
        <Space size={5}><ClockCircleOutlined /><Text type="secondary">{formatUptime(m?.uptime_seconds || 0)}</Text></Space>
        {expInfo && (
          <Space size={5}><CalendarOutlined style={{ color: expInfo.color }} /><Text style={{ color: expInfo.color }}>{expInfo.text}</Text></Space>
        )}
      </div>

      {m ? (
        <div className="server-metrics">
          <div className="metric-progress">
            <div><Text type="secondary">{t('card.cpu')}</Text><strong>{cpuPercent}%</strong></div>
            <Progress percent={cpuPercent} showInfo={false} strokeColor={cpuPercent > 80 ? '#ff5d6c' : '#5d7df7'} trailColor="rgba(128, 140, 170, .14)" />
          </div>
          <div className="metric-progress">
            <div><Text type="secondary">{t('card.memory')}</Text><strong>{memPercent}%</strong></div>
            <Progress percent={memPercent} showInfo={false} strokeColor="#18b690" trailColor="rgba(128, 140, 170, .14)" />
          </div>
          <div className="throughput-grid">
            <div>
            <Text type="secondary">{t('card.network')}</Text>
            <Space size={4}>
              <ArrowDownOutlined className="rx" />
              <Text>{formatBytes(m.network_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined className="tx" />
              <Text>{formatBytes(m.network_tx_bytes)}/s</Text>
            </Space>
            </div>
            <div>
            <Text type="secondary">{t('card.disk')}</Text>
            <Space size={4}>
              <ArrowDownOutlined className="rx" />
              <Text>{formatBytes(m.disk_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined className="tx" />
              <Text>{formatBytes(m.disk_tx_bytes)}/s</Text>
            </Space>
            </div>
          </div>
        </div>
      ) : <div className="metrics-unavailable"><span /><Text type="secondary">{t('metrics.noData')}</Text></div>}
    </Card>
  );
}

function isOnlineAt(s: Server, observedAt: number): boolean {
  const at = s.latest_metrics?.recorded_at;
  return observedAt > 0 && !!at && observedAt - new Date(at).getTime() < 120000;
}

// Memoized: the dashboard replaces the whole servers array every poll, so we
// only re-render a card when its own displayed data (or online state) changes.
export default React.memo(ServerCard, (prev, next) => {
  const a = prev.server;
  const b = next.server;
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.host === b.host &&
    a.server_type === b.server_type &&
    a.expires_at === b.expires_at &&
    a.cpu_cores === b.cpu_cores &&
    a.memory_total === b.memory_total &&
    a.disk_total === b.disk_total &&
    isOnlineAt(a, prev.observedAt) === isOnlineAt(b, next.observedAt) &&
    JSON.stringify(a.tags || []) === JSON.stringify(b.tags || []) &&
    JSON.stringify(a.latest_metrics) === JSON.stringify(b.latest_metrics)
  );
});
