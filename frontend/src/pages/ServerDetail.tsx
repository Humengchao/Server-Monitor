import React, { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Typography, Descriptions, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input, InputNumber, message } from 'antd';
import { ArrowLeftOutlined, EditOutlined } from '@ant-design/icons';
import { serversApi, Server } from '../api/servers';
import { useMetrics } from '../hooks/useMetrics';
import MetricsChart from '../components/MetricsChart';
import SshTerminal from '../components/SshTerminal';
import TagSelect from '../components/TagSelect';

const { Title } = Typography;

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [server, setServer] = useState<Server | null>(null);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const { metrics, history, loading: metricsLoading } = useMetrics(id!);

  const loadServer = async () => {
    try {
      const res = await serversApi.list();
      const found = (res.data || []).find((s: Server) => s.id === id);
      setServer(found || null);
    } catch {
      message.error('Failed to load server');
    }
    setLoading(false);
  };

  useEffect(() => {
    loadServer();
  }, [id]);

  const handleEdit = () => {
    if (!server) return;
    form.setFieldsValue({
      name: server.name,
      host: server.host,
      port: server.port,
      ssh_username: server.ssh_username,
      ssh_host_key: server.ssh_host_key || '',
    });
    setTagValues(server.tags?.map((t) => t.id) || []);
    setModalOpen(true);
  };

  const handleSubmit = async (values: any) => {
    if (!server) return;
    try {
      await serversApi.update(server.id, values);
      await serversApi.setTags(server.id, tagValues);
      message.success('Server updated');
      setModalOpen(false);
      loadServer();
    } catch (err: any) {
      message.error(err.response?.data?.error || 'Update failed');
    }
  };

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;
  if (!server) return <div>Server not found</div>;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Space>
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/dashboard')}>Back</Button>
          <Title level={4} style={{ margin: 0 }}>{server.name}</Title>
          {server.tags?.map((t) => (
            <Tag key={t.id} color={t.color}>{t.name}</Tag>
          ))}
        </Space>
        <Button icon={<EditOutlined />} onClick={handleEdit}>Edit</Button>
      </div>

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

      <Modal
        title="Edit Server"
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={() => form.submit()}
        width={520}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label="Server Name" rules={[{ required: true }]}>
            <Input placeholder="My Web Server" />
          </Form.Item>
          <Form.Item name="host" label="Host / IP" rules={[{ required: true }]}>
            <Input placeholder="192.168.1.100" />
          </Form.Item>
          <Form.Item name="port" label="SSH Port" initialValue={22}>
            <InputNumber min={1} max={65535} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="ssh_username" label="SSH Username" rules={[{ required: true }]}>
            <Input placeholder="root" />
          </Form.Item>
          <Form.Item name="ssh_password" label="SSH Password">
            <Input.Password placeholder="Leave blank to keep unchanged" />
          </Form.Item>
          <Form.Item name="ssh_key" label="SSH Private Key">
            <Input.TextArea rows={4} placeholder="Paste private key content" />
          </Form.Item>
          <Form.Item name="ssh_host_key" label="SSH Host Key (optional)">
            <Input.TextArea rows={2} placeholder="Paste server public key for host verification" />
          </Form.Item>
          <Form.Item label="Tags">
            <TagSelect value={tagValues} onChange={setTagValues} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
