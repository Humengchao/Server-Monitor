import React from 'react';
import { Card, Tag, Progress, Typography, Space } from 'antd';
import {
  CloudServerOutlined,
  ArrowUpOutlined,
  ArrowDownOutlined,
} from '@ant-design/icons';
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
  return (bytes / 1024 / 1024 / 1024).toFixed(0) + ' GB';
}

function formatUptime(seconds: number): string {
  if (!seconds) return '-';
  const d = Math.floor(seconds / 86400);
  return d > 0 ? `${d}d` : `${Math.floor(seconds / 3600)}h`;
}

interface Props {
  server: Server;
}

export default function ServerCard({ server }: Props) {
  const navigate = useNavigate();
  const m = server.latest_metrics;
  const cpuPercent = m ? Math.round(m.cpu_percent) : 0;
  const memPercent = m && m.memory_total ? Math.round((m.memory_used / m.memory_total) * 100) : 0;

  const specs = [];
  if (server.cpu_cores > 0) specs.push(`${server.cpu_cores} Core`);
  if (server.memory_total > 0) specs.push(formatGB(server.memory_total));
  if (server.disk_total > 0) specs.push(formatGB(server.disk_total));
  if (m) specs.push(formatUptime(m.uptime_seconds));

  return (
    <Card
      hoverable
      style={{ borderRadius: 12 }}
      onClick={() => navigate(`/servers/${server.id}`)}
      title={
        <Space>
          <CloudServerOutlined />
          <span>{server.name}</span>
        </Space>
      }
    >
      {server.tags?.length > 0 && (
        <div style={{ marginBottom: 8 }}>
          {server.tags.map((tag) => (
            <Tag key={tag.id} color={tag.color}>
              {tag.name}
            </Tag>
          ))}
        </div>
      )}

      {specs.length > 0 && (
        <div style={{ marginBottom: 12, display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          {specs.map((s, i) => (
            <Text key={i} type="secondary" style={{ fontSize: 12 }}>{s}</Text>
          ))}
        </div>
      )}

      {m && (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ textAlign: 'center', flex: 1 }}>
            <Progress
              type="circle"
              percent={cpuPercent}
              size={64}
              strokeColor={cpuPercent > 80 ? '#ff4d4f' : '#52c41a'}
            />
            <div style={{ marginTop: 2 }}><Text type="secondary" style={{ fontSize: 11 }}>CPU</Text></div>
          </div>
          <div style={{ textAlign: 'center', flex: 1 }}>
            <Progress
              type="circle"
              percent={memPercent}
              size={64}
              strokeColor="#1890ff"
            />
            <div style={{ marginTop: 2 }}><Text type="secondary" style={{ fontSize: 11 }}>Memory</Text></div>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1, alignItems: 'center' }}>
            <Space size={4}>
              <ArrowDownOutlined style={{ color: '#52c41a', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.network_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined style={{ color: '#1890ff', fontSize: 12 }} />
              <Text type="secondary" style={{ fontSize: 12 }}>{formatBytes(m.network_tx_bytes)}/s</Text>
            </Space>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1, alignItems: 'center' }}>
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
