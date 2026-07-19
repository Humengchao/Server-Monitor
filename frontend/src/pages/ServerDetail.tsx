import React, { useEffect, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Typography, Descriptions, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input, InputNumber, App } from 'antd';
import { ArrowLeftOutlined, EditOutlined, DeleteOutlined, DockerOutlined, KeyOutlined, SaveOutlined } from '@ant-design/icons';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import { useTranslation } from 'react-i18next';
import { serversApi, Server } from '../api/servers';
import { useMetrics, TimeRange } from '../hooks/useMetrics';
import MetricsChart from '../components/MetricsChart';
import SshTerminal from '../components/SshTerminal';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';

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

export default function ServerDetail() {
  const { t, i18n } = useTranslation();
  const { message } = App.useApp();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [server, setServer] = useState<Server | null>(null);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);
  const [dockerInstalled, setDockerInstalled] = useState<boolean | null>(null);
  const [notes, setNotes] = useState('');
  const [notesChanged, setNotesChanged] = useState(false);
  const [activePreset, setActivePreset] = useState<PresetKey>('1h');
  const [timeRange, setTimeRange] = useState<TimeRange>(() => getPresetRange('1h'));

  const { metrics, history, loading: metricsLoading } = useMetrics(id!, timeRange);

  const presets: { key: PresetKey; label: string }[] = [
    { key: '1h', label: t('preset.1h') },
    { key: 'today', label: t('preset.today') },
    { key: 'yesterday', label: t('preset.yesterday') },
    { key: '7d', label: t('preset.7d') },
    { key: '30d', label: t('preset.30d') },
  ];

  const loadServer = useCallback(async () => {
    try {
      const res = await serversApi.list();
      const found = (res.data || []).find((s: Server) => s.id === id);
      setServer(found || null);
      if (found) {
        setDockerInstalled(found.has_docker);
        setNotes(found.notes || '');
        setNotesChanged(false);
      }
    } catch {
      message.error(t('server.loadFailed'));
    }
    setLoading(false);
  }, [id, message, t]);

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
    setSelectedCredential(server.credential_id || undefined);
    form.setFieldsValue({
      name: server.name,
      host: server.host,
      port: server.port,
      ssh_username: server.ssh_username,
      ssh_host_key: server.ssh_host_key || '',
      expires_at: server.expires_at ? dayjs(server.expires_at) : null,
    });
    setTagValues(server.tags?.map((t) => t.id) || []);
    setModalOpen(true);
  };

  const handleSubmit = async (values: any) => {
    if (!server) return;
    try {
      const payload = {
        ...values,
        credential_id: selectedCredential || null,
        expires_at: values.expires_at ? values.expires_at.toISOString() : null,
      };
      await serversApi.update(server.id, payload);
      await serversApi.setTags(server.id, tagValues);
      message.success(t('server.updated'));
      setModalOpen(false);
      loadServer();
    } catch (err: any) {
      message.error(err.response?.data?.error || t('server.updateFailed'));
    }
  };

  const handleDelete = () => {
    if (!server) return;
    Modal.confirm({
      title: t('server.delete'),
      content: t('server.deleteConfirm', { name: server.name }),
      okText: t('common.delete'),
      okType: 'danger',
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success(t('server.deleted'));
          navigate('/dashboard');
        } catch {
          message.error(t('server.deleteFailed'));
        }
      },
    });
  };

  const handleSaveNotes = async () => {
    if (!server) return;
    try {
      await serversApi.update(server.id, {
        name: server.name,
        host: server.host,
        port: server.port,
        notes,
      });
      message.success(t('server.notesSaved'));
      setNotesChanged(false);
    } catch {
      message.error(t('server.notesSaveFailed'));
    }
  };

  if (loading) return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '60vh' }}><Spin size="large" /></div>;
  if (!server) return <div>{t('server.notFound')}</div>;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Space>
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/dashboard')}>{t('common.back')}</Button>
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
            {t('server.docker')}
          </Button>
          <Button icon={<EditOutlined />} onClick={handleEdit}>{t('common.edit')}</Button>
          <Button danger icon={<DeleteOutlined />} onClick={handleDelete}>{t('common.delete')}</Button>
        </Space>
      </div>

      <Descriptions bordered size="small" column={2} style={{ marginBottom: 24 }}>
        <Descriptions.Item label={t('server.hostLabel')}>{server.host}</Descriptions.Item>
        <Descriptions.Item label={t('server.sshPortLabel')}>{server.port}</Descriptions.Item>
        <Descriptions.Item label={t('server.sshUserLabel')}>{server.ssh_username}</Descriptions.Item>
        {server.credential_name && (
          <Descriptions.Item label={t('server.credentialLabel')}>
            <Space><KeyOutlined />{server.credential_name}</Space>
          </Descriptions.Item>
        )}
        <Descriptions.Item label={t('server.addedLabel')}>{new Date(server.created_at).toLocaleString()}</Descriptions.Item>
        {server.expires_at && (
          <Descriptions.Item label={t('server.expiresAt')}>
            {(() => {
              const now = new Date();
              const exp = new Date(server.expires_at!);
              const isExpired = exp.getTime() < now.getTime();
              const from = isExpired ? exp : now;
              const to = isExpired ? now : exp;
              let years = to.getFullYear() - from.getFullYear();
              let months = to.getMonth() - from.getMonth();
              let days = to.getDate() - from.getDate();
              if (days < 0) { months--; days += new Date(to.getFullYear(), to.getMonth(), 0).getDate(); }
              if (months < 0) { years--; months += 12; }
              const parts: string[] = [];
              const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';
              if (years > 0) parts.push(lang === 'zh' ? `${years}年` : `${years}y`);
              if (months > 0) parts.push(lang === 'zh' ? `${months}月` : `${months}m`);
              if (days > 0 || parts.length === 0) parts.push(lang === 'zh' ? `${days}天` : `${days}d`);
              const diffStr = parts.join('');
              if (isExpired) return `${exp.toLocaleString()} (${lang === 'zh' ? `已过期${diffStr}` : `Expired ${diffStr}`})`;
              return `${exp.toLocaleString()} (${lang === 'zh' ? `${diffStr}后到期` : `${diffStr} left`})`;
            })()}
          </Descriptions.Item>
        )}
      </Descriptions>

      <Tabs defaultActiveKey="metrics" items={[
        {
          key: 'metrics',
          label: t('metrics.title'),
          children: (
            <Card loading={metricsLoading}>
              {metrics && (
                <Descriptions bordered size="small" column={4} style={{ marginBottom: 16 }}>
                  <Descriptions.Item label={t('metrics.cpu')}>{(metrics.cpu_percent || 0).toFixed(1)}%</Descriptions.Item>
                  <Descriptions.Item label={t('metrics.memoryUsed')}>{((metrics.memory_used || 0) / 1024 / 1024).toFixed(0)} MB</Descriptions.Item>
                  <Descriptions.Item label={t('metrics.memoryTotal')}>{((metrics.memory_total || 0) / 1024 / 1024).toFixed(0)} MB</Descriptions.Item>
                  <Descriptions.Item label={t('metrics.uptime')}>{(() => {
  const s = metrics?.uptime_seconds || 0;
  if (!s) return '0d';
  const td = Math.floor(s / 86400);
  const y = Math.floor(td / 365);
  const r = td % 365;
  const mo = Math.floor(r / 30);
  const d = r % 30;
  const p: string[] = [];
  if (y > 0) p.push(y + 'y');
  if (mo > 0) p.push(mo + 'm');
  if (d > 0 || p.length === 0) p.push(d + 'd');
  return p.join(' ');
})()}</Descriptions.Item>
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
          label: t('terminal.title'),
          children: (
            <Card>
              <SshTerminal serverId={id!} />
            </Card>
          ),
        },
        {
          key: 'notes',
          label: t('server.notes'),
          children: (
            <Card>
              <Input.TextArea
                rows={12}
                value={notes}
                onChange={(e) => { setNotes(e.target.value); setNotesChanged(true); }}
                placeholder={t('server.notesPlaceholder')}
              />
              <div style={{ marginTop: 16, textAlign: 'right' }}>
                <Button
                  type="primary"
                  icon={<SaveOutlined />}
                  disabled={!notesChanged}
                  onClick={handleSaveNotes}
                >
                  {t('common.save')}
                </Button>
              </div>
            </Card>
          ),
        },
      ]} />

      <Modal
        title={t('server.edit')}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={() => form.submit()}
        width={520}
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
          <Form.Item label={t('server.credential')}>
            <CredentialSelect value={selectedCredential} onChange={setSelectedCredential} />
          </Form.Item>
          {!selectedCredential && (
            <>
              <Form.Item name="ssh_username" label={t('server.sshUsername')} rules={[{ required: true }]}>
                <Input placeholder={t('server.sshUsernamePlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_password" label={t('server.sshPassword')}>
                <Input.Password placeholder={t('server.sshKeyEditPlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_key" label={t('server.sshKey')}>
                <Input.TextArea rows={4} placeholder={t('server.sshKeyEditPlaceholder')} />
              </Form.Item>
            </>
          )}
          <Form.Item name="ssh_host_key" label={t('server.sshHostKey')}>
            <Input.TextArea rows={2} placeholder={t('server.sshHostKeyPlaceholder')} />
          </Form.Item>
          <Form.Item name="expires_at" label={t('server.expiresAt')}>
            <DatePicker showTime style={{ width: '100%' }} placeholder={t('server.expiresAtPlaceholder')} />
          </Form.Item>
          <Form.Item label={t('server.tags')}>
            <TagSelect value={tagValues} onChange={setTagValues} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
