import React, { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Typography, Descriptions, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input, InputNumber, message } from 'antd';
import { ArrowLeftOutlined, EditOutlined, DeleteOutlined, DockerOutlined } from '@ant-design/icons';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import { serversApi, Server } from '../api/servers';
import { useMetrics, TimeRange } from '../hooks/useMetrics';
import MetricsChart from '../components/MetricsChart';
import SshTerminal from '../components/SshTerminal';
import TagSelect from '../components/TagSelect';

const { Title } = Typography;
const { RangePicker } = DatePicker;

type PresetKey = '1h' | 'today' | 'yesterday' | '7d' | '30d';

function getPresetRange(key: PresetKey): TimeRange {
  const now = dayjs();
  switch (key) {
    case '1h':
      return { since: now.subtract(1, 'hour').toISOString(), until: now.toISOString() };
    case 'today':
      return { since: now.startOf('day').toISOString(), until: now.toISOString() };
    case 'yesterday':
      return {
        since: now.subtract(1, 'day').startOf('day').toISOString(),
        until: now.subtract(1, 'day').endOf('day').toISOString(),
      };
    case '7d':
      return { since: now.subtract(7, 'day').startOf('day').toISOString(), until: now.toISOString() };
    case '30d':
      return { since: now.subtract(30, 'day').startOf('day').toISOString(), until: now.toISOString() };
  }
}

const presets: { key: PresetKey; label: string }[] = [
  { key: '1h', label: 'Last 1 Hour' },
  { key: 'today', label: 'Today' },
  { key: 'yesterday', label: 'Yesterday' },
  { key: '7d', label: 'Last 7 Days' },
  { key: '30d', label: 'Last 30 Days' },
];

export default function ServerDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [server, setServer] = useState<Server | null>(null);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [dockerInstalled, setDockerInstalled] = useState<boolean | null>(null);
  const [activePreset, setActivePreset] = useState<PresetKey>('1h');
  const [timeRange, setTimeRange] = useState<TimeRange>(() => getPresetRange('1h'));

  const { metrics, history, loading: metricsLoading } = useMetrics(id!, timeRange);

  const loadServer = async () => {
    try {
      const res = await serversApi.list();
      const found = (res.data || []).find((s: Server) => s.id === id);
      setServer(found || null);
      if (found) {
        setDockerInstalled(found.has_docker);
      }
    } catch {
      message.error('Failed to load server');
    }
    setLoading(false);
  };

  useEffect(() => {
    loadServer();
  }, [id]);

  const handlePreset = (key: PresetKey) => {
    setActivePreset(key);
    setTimeRange(getPresetRange(key));
  };

  const handleRangeChange = (dates: [Dayjs | null, Dayjs | null] | null) => {
    if (dates && dates[0] && dates[1]) {
      setActivePreset(null as any);
      setTimeRange({
        since: dates[0].toISOString(),
        until: dates[1].toISOString(),
      });
    }
  };

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

  const handleDelete = () => {
    if (!server) return;
    Modal.confirm({
      title: 'Delete Server',
      content: `Are you sure you want to delete "${server.name}"?`,
      okText: 'Delete',
      okType: 'danger',
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success('Server deleted');
          navigate('/dashboard');
        } catch {
          message.error('Failed to delete server');
        }
      },
    });
  };

  if (loading) return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '60vh' }}><Spin size="large" /></div>;
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
        <Space>
          <Button
            icon={<DockerOutlined />}
            disabled={dockerInstalled !== true}
            onClick={() => navigate(`/docker?server=${id}&expand=true`)}
          >
            Docker
          </Button>
          <Button icon={<EditOutlined />} onClick={handleEdit}>Edit</Button>
          <Button danger icon={<DeleteOutlined />} onClick={handleDelete}>Delete</Button>
        </Space>
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

              <Space style={{ marginBottom: 16 }} wrap>
                {presets.map((p) => (
                  <Button
                    key={p.key}
                    type={activePreset === p.key ? 'primary' : 'default'}
                    onClick={() => handlePreset(p.key)}
                  >
                    {p.label}
                  </Button>
                ))}
                <RangePicker showTime disabledDate={(current) => current && current.isAfter(dayjs(), 'day')} onChange={handleRangeChange} />
              </Space>

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
            <Input.TextArea rows={4} placeholder="Leave blank to keep unchanged" />
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
