import React, { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Typography, Descriptions, Tag, Space, Button, Card, Tabs, message, Spin } from 'antd';
import { ArrowLeftOutlined } from '@ant-design/icons';
import { serversApi, Server } from '../api/servers';
import { useMetrics } from '../hooks/useMetrics';
import MetricsChart from '../components/MetricsChart';
import SshTerminal from '../components/SshTerminal';

const { Title } = Typography;

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [server, setServer] = useState<Server | null>(null);
  const [loading, setLoading] = useState(true);
  const { metrics, history, loading: metricsLoading } = useMetrics(id!, 10000);

  useEffect(() => {
    const load = async () => {
      try {
        const res = await serversApi.list();
        const found = (res.data || []).find((s: Server) => s.id === id);
        setServer(found || null);
      } catch {
        message.error('Failed to load server');
      }
      setLoading(false);
    };
    load();
  }, [id]);

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;
  if (!server) return <div>Server not found</div>;

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/dashboard')}>Back</Button>
        <Title level={4} style={{ margin: 0 }}>{server.name}</Title>
        {server.tags?.map((t) => (
          <Tag key={t.id} color={t.color}>{t.name}</Tag>
        ))}
      </Space>

      <Descriptions bordered size="small" column={2} style={{ marginBottom: 24 }}>
        <Descriptions.Item label="Host">{server.host}</Descriptions.Item>
        <Descriptions.Item label="SSH Port">{server.port}</Descriptions.Item>
        <Descriptions.Item label="SSH User">{server.ssh_username}</Descriptions.Item>
        <Descriptions.Item label="Added">{new Date(server.created_at).toLocaleString()}</Descriptions.Item>
      </Descriptions>

      <Tabs defaultActiveKey="metrics" items={[
        {
          key: 'metrics',
          label: 'Metrics',
          children: (
            <Card loading={metricsLoading}>
              {metrics && (
                <Descriptions bordered size="small" column={4} style={{ marginBottom: 16 }}>
                  <Descriptions.Item label="CPU">{(metrics.cpu_percent || 0).toFixed(1)}%</Descriptions.Item>
                  <Descriptions.Item label="Memory Used">{((metrics.memory_used || 0) / 1024 / 1024).toFixed(0)} MB</Descriptions.Item>
                  <Descriptions.Item label="Memory Total">{((metrics.memory_total || 0) / 1024 / 1024).toFixed(0)} MB</Descriptions.Item>
                  <Descriptions.Item label="Uptime">{Math.floor((metrics.uptime_seconds || 0) / 3600)}h</Descriptions.Item>
                </Descriptions>
              )}
              <MetricsChart history={history} />
            </Card>
          ),
        },
        {
          key: 'terminal',
          label: 'SSH Terminal',
          children: (
            <Card>
              <SshTerminal serverId={id!} />
            </Card>
          ),
        },
      ]} />
    </div>
  );
}
