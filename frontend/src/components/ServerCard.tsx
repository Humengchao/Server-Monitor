import React from 'react';
import { Card, Tag, Progress, Typography, Space, Divider } from 'antd';
import {
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
  const d = (seconds / 86400).toFixed(1);
  return `${d}d`;
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
}

export default function ServerCard({ server }: Props) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const m = server.latest_metrics;
  const cpuPercent = m ? Math.round(m.cpu_percent) : 0;
  const memPercent = m && m.memory_total ? Math.round((m.memory_used / m.memory_total) * 100) : 0;

  const isOnline = m?.recorded_at && (Date.now() - new Date(m.recorded_at).getTime()) < 120000;
  const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';
  const expInfo = getExpirationInfo(server.expires_at, lang);

  return (
    <Card
      hoverable
      style={{ borderRadius: 12, minHeight: 240 }}
      onClick={() => navigate(`/servers/${server.id}`)}
      title={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <Space>
            <CloudServerOutlined />
            <span>{server.name}</span>
          </Space>
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: isOnline ? '#52c41a' : '#ff4d4f', display: 'inline-block' }} />
        </div>
      }
    >
      <div style={{ marginBottom: 8, minHeight: 22, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {server.tags?.map((tag) => (
          <Tag key={tag.id} color={tag.color}>
            {tag.name}
          </Tag>
        ))}
      </div>

      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, marginBottom: 8, flexWrap: 'wrap', gap: 4 }}>
        <Space size={4}><DashboardOutlined style={{ color: '#8c8c8c' }} /><Text type="secondary" style={{ fontSize: 12 }}>{server.cpu_cores || 0} {t('card.core')}</Text></Space>
        <Space size={4}><DatabaseOutlined style={{ color: '#8c8c8c' }} /><Text type="secondary" style={{ fontSize: 12 }}>{formatGB(server.memory_total)}</Text></Space>
        <Space size={4}><HddOutlined style={{ color: '#8c8c8c' }} /><Text type="secondary" style={{ fontSize: 12 }}>{formatGB(server.disk_total)}</Text></Space>
        <Space size={4}><ClockCircleOutlined style={{ color: '#8c8c8c' }} /><Text type="secondary" style={{ fontSize: 12 }}>{formatUptime(m?.uptime_seconds || 0)}</Text></Space>
        {expInfo && (
          <Space size={4}><CalendarOutlined style={{ color: expInfo.color }} /><Text style={{ fontSize: 12, color: expInfo.color }}>{expInfo.text}</Text></Space>
        )}
      </div>

      <Divider style={{ margin: '8px 0' }} />

      {m && (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ textAlign: 'center', flex: 1 }}>
            <Progress
              type="circle"
              percent={cpuPercent}
              size={64}
              strokeColor={cpuPercent > 80 ? '#ff4d4f' : '#52c41a'}
            />
            <div style={{ marginTop: 2 }}><Text type="secondary" style={{ fontSize: 11 }}>{t('card.cpu')}</Text></div>
          </div>
          <div style={{ textAlign: 'center', flex: 1 }}>
            <Progress
              type="circle"
              percent={memPercent}
              size={64}
              strokeColor="#1890ff"
            />
            <div style={{ marginTop: 2 }}><Text type="secondary" style={{ fontSize: 11 }}>{t('card.memory')}</Text></div>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1, alignItems: 'center' }}>
            <Text type="secondary" style={{ fontSize: 11, fontWeight: 500 }}>{t('card.network')}</Text>
            <Space size={4}>
              <ArrowDownOutlined style={{ color: '#52c41a', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.network_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined style={{ color: '#1890ff', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.network_tx_bytes)}/s</Text>
            </Space>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1, alignItems: 'center' }}>
            <Text type="secondary" style={{ fontSize: 11, fontWeight: 500 }}>{t('card.disk')}</Text>
            <Space size={4}>
              <ArrowDownOutlined style={{ color: '#722ed1', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.disk_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined style={{ color: '#eb2f96', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.disk_tx_bytes)}/s</Text>
            </Space>
          </div>
        </div>
      )}
    </Card>
  );
}
