import React from 'react';
import { Card, Tag, Progress, Row, Col, Typography, Space } from 'antd';
import {
  CloudServerOutlined,
  ClockCircleOutlined,
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

function formatUptime(seconds: number): string {
  if (!seconds) return '-';
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

interface Props {
  server: Server;
}

export default function ServerCard({ server }: Props) {
  const navigate = useNavigate();
  const m = server.latest_metrics;

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
      extra={
        <Text type="secondary" copyable={{ text: server.host }}>
          {server.host}:{server.port}
        </Text>
      }
    >
      <Row gutter={[16, 16]}>
        {server.tags?.map((tag) => (
          <Tag key={tag.id} color={tag.color}>
            {tag.name}
          </Tag>
        ))}
      </Row>

      {m && (
        <Row gutter={[16, 16]} style={{ marginTop: 12 }}>
          <Col span={12}>
            <Text type="secondary">CPU</Text>
            <Progress
              percent={Math.round(m.cpu_percent)}
              size="small"
              strokeColor={m.cpu_percent > 80 ? '#ff4d4f' : '#52c41a'}
            />
          </Col>
          <Col span={12}>
            <Text type="secondary">Memory</Text>
            <Progress
              percent={Math.round((m.memory_used / m.memory_total) * 100) || 0}
              size="small"
              strokeColor="#1890ff"
              format={() => `${formatBytes(m.memory_used)} / ${formatBytes(m.memory_total)}`}
            />
          </Col>
          <Col span={12}>
            <Space>
              <ArrowDownOutlined style={{ color: '#52c41a' }} />
              <Text>{formatBytes(m.network_rx_bytes)}/s</Text>
            </Space>
          </Col>
          <Col span={12}>
            <Space>
              <ArrowUpOutlined style={{ color: '#1890ff' }} />
              <Text>{formatBytes(m.network_tx_bytes)}/s</Text>
            </Space>
          </Col>
        </Row>
      )}
    </Card>
  );
}
