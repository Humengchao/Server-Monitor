import React, { useEffect, useState, useCallback } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Typography, Descriptions, Tag, Space, Button, Card, Tabs, Spin, Modal, Form, Input, InputNumber, Select, App, Row, Col, Empty } from 'antd';
import { ArrowLeftOutlined, EditOutlined, DeleteOutlined, DockerOutlined, KeyOutlined, SaveOutlined, WindowsOutlined, AppleOutlined, CopyOutlined } from '@ant-design/icons';
import { DatePicker } from 'antd';
import dayjs, { Dayjs } from 'dayjs';
import { useTranslation } from 'react-i18next';
import { serversApi, Server } from '../api/servers';
import { useMetrics, TimeRange } from '../hooks/useMetrics';
import MetricsChart from '../components/MetricsChart';
import SshTerminal from '../components/SshTerminal';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';
import { ServerDockerPanel } from './Docker';

const { Title } = Typography;
const { RangePicker } = DatePicker;

type PresetKey = '1h' | 'today' | 'yesterday' | '7d' | '30d';

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** unitIndex).toFixed(unitIndex > 2 ? 1 : 2)} ${units[unitIndex]}`;
}

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

async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Fall back for browsers that expose the API but deny clipboard access.
    }
  }

  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    if (!document.execCommand('copy')) {
      throw new Error('Copy command failed');
    }
  } finally {
    textarea.remove();
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
  const [activeTab, setActiveTab] = useState('metrics');
  const [activePreset, setActivePreset] = useState<PresetKey | null>('1h');
  const [timeRange, setTimeRange] = useState<TimeRange>(() => getPresetRange('1h'));

  const { metrics, history, loading: metricsLoading } = useMetrics(id!, timeRange);

  // Presets whose window ends "now" keep sliding: recompute the range every
  // 30s so the chart stays live instead of freezing at the moment of the
  // click. Fixed windows (yesterday, custom ranges) stay as picked.
  useEffect(() => {
    if (!activePreset || activePreset === 'yesterday') return;
    const timer = window.setInterval(() => {
      setTimeRange(getPresetRange(activePreset));
    }, 30000);
    return () => window.clearInterval(timer);
  }, [activePreset]);

  const presets: { key: PresetKey; label: string }[] = [
    { key: '1h', label: t('preset.1h') },
    { key: 'today', label: t('preset.today') },
    { key: 'yesterday', label: t('preset.yesterday') },
    { key: '7d', label: t('preset.7d') },
    { key: '30d', label: t('preset.30d') },
  ];

  const loadServer = useCallback(async () => {
    try {
      const res = await serversApi.get(id!);
      const found = res.data;
      setServer(found);
      setDockerInstalled(found.has_docker);
      setNotes(found.notes || '');
      setNotesChanged(false);
    } catch (err: any) {
      if (err?.response?.status === 404) {
        setServer(null);
      } else {
        message.error(t('server.loadFailed'));
      }
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
      setActivePreset(null);
      setTimeRange({
        since: dates[0].toISOString(),
        until: dates[1].toISOString(),
      });
    }
  };

  const handleEdit = () => {
    if (!server) return;
    setSelectedCredential(server.credential_id || undefined);
    // Drop any password/key typed in a previously cancelled edit; empty
    // secret fields mean "keep current" on the backend.
    form.resetFields();
    form.setFieldsValue({
      name: server.name,
      host: server.host,
      port: server.port,
      ssh_username: server.ssh_username,
      ssh_host_key: server.ssh_host_key || '',
      expires_at: server.expires_at ? dayjs(server.expires_at) : null,
      server_type: server.server_type || 'linux',
      billing_price: server.billing_price || 0,
      billing_currency: server.billing_currency || 'CNY',
      billing_cycle: server.billing_cycle || 'year',
      traffic_limit_gb: Number(((server.traffic_limit_bytes || 0) / 1024 / 1024 / 1024).toFixed(2)),
      public_location: server.public_location || '',
    });
    setTagValues(server.tags?.map((t) => t.id) || []);
    setModalOpen(true);
  };

  const handleSubmit = async (values: any) => {
    if (!server) return;
    try {
      const payload = {
        ...values,
        name: typeof values.name === 'string' ? values.name.trim() : values.name,
        host: typeof values.host === 'string' ? values.host.trim() : values.host,
        ssh_username: typeof values.ssh_username === 'string' ? values.ssh_username.trim() : values.ssh_username,
        credential_id: selectedCredential || null,
        server_type: values.server_type || 'linux',
        expires_at: values.expires_at ? values.expires_at.toISOString() : null,
        traffic_limit_bytes: Math.round((values.traffic_limit_gb || 0) * 1024 * 1024 * 1024),
        notes: server.notes || '',
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
        ssh_username: server.ssh_username,
        ssh_host_key: server.ssh_host_key || '',
        credential_id: server.credential_id,
        server_type: server.server_type || 'linux',
        expires_at: server.expires_at || null,
        billing_price: server.billing_price || 0,
        billing_currency: server.billing_currency || 'CNY',
        billing_cycle: server.billing_cycle || 'year',
        traffic_limit_bytes: server.traffic_limit_bytes || 0,
        public_location: server.public_location || '',
        notes,
      });
      message.success(t('server.notesSaved'));
      setServer((current) => (current ? { ...current, notes } : current));
      setNotesChanged(false);
    } catch {
      message.error(t('server.notesSaveFailed'));
    }
  };

  const handleCopyHost = async () => {
    if (!server?.host) return;
    try {
      await copyToClipboard(server.host);
      message.success(t('server.hostCopied'));
    } catch {
      message.error(t('server.hostCopyFailed'));
    }
  };

  if (loading) return <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', minHeight: '60vh' }}><Spin size="large" /></div>;
  if (!server) return <div>{t('server.notFound')}</div>;

  return (
    <div className={`server-detail-page${activeTab === 'terminal' ? ' server-detail-page--terminal' : ''}`}>
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
        <Descriptions.Item label={t('server.hostLabel')}>
          <button
            type="button"
            className="copyable-host"
            title={t('server.copyHost')}
            aria-label={t('server.copyHost')}
            onClick={handleCopyHost}
          >
            {server.host}<CopyOutlined aria-hidden="true" />
          </button>
        </Descriptions.Item>
        <Descriptions.Item label={t('server.sshPortLabel')}>{server.port}</Descriptions.Item>
        <Descriptions.Item label={t('server.type')}>{server.server_type === 'windows' ? <><WindowsOutlined /> Windows</> : <><AppleOutlined /> Linux</>}</Descriptions.Item>
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

      <Tabs className="server-detail-tabs" activeKey={activeTab} onChange={setActiveTab} items={[
        {
          key: 'metrics',
          label: t('metrics.title'),
          children: (
            <Card loading={metricsLoading}>
              {metrics && (
                <Descriptions bordered size="small" column={{ xs: 1, sm: 2, lg: 3 }} style={{ marginBottom: 16 }}>
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
                  <Descriptions.Item label={t('metrics.totalUpload')}>{formatBytes(metrics.network_tx_total_bytes || 0)}</Descriptions.Item>
                  <Descriptions.Item label={t('metrics.totalDownload')}>{formatBytes(metrics.network_rx_total_bytes || 0)}</Descriptions.Item>
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
            <div className="server-detail-terminal">
              <SshTerminal serverId={id!} />
            </div>
          ),
        },
        {
          key: 'docker',
          label: t('docker.title'),
          children: dockerInstalled === true ? (
            <ServerDockerPanel serverId={id!} version={server.docker_version} />
          ) : (
            <Empty description={t('docker.noServers')} />
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
        width={680}
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
          <Form.Item name="server_type" label={t('server.type')} initialValue="linux">
            <Select>
              <Select.Option value="linux"><AppleOutlined /> Linux</Select.Option>
              <Select.Option value="windows"><WindowsOutlined /> Windows</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item label={t('server.credential')}>
            <CredentialSelect value={selectedCredential} onChange={setSelectedCredential} serverType={form.getFieldValue('server_type') || 'linux'} />
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
          <Form.Item name="public_location" label={t('server.publicLocation')}>
            <Input placeholder={t('server.publicLocationPlaceholder')} maxLength={128} />
          </Form.Item>
          <Row gutter={12}>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_price" label={t('server.billingPrice')}>
                <InputNumber min={0} precision={2} style={{ width: '100%' }} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_currency" label={t('server.billingCurrency')}>
                <Select options={[{ value: 'CNY', label: '¥ CNY' }, { value: 'USD', label: '$ USD' }, { value: 'EUR', label: '€ EUR' }]} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={8}>
              <Form.Item name="billing_cycle" label={t('server.billingCycle')}>
                <Select options={[
                  { value: 'month', label: t('server.cycleMonth') },
                  { value: 'quarter', label: t('server.cycleQuarter') },
                  { value: 'half_year', label: t('server.cycleHalfYear') },
                  { value: 'year', label: t('server.cycleYear') },
                ]} />
              </Form.Item>
            </Col>
          </Row>
          <Form.Item name="traffic_limit_gb" label={t('server.trafficLimit')}>
            <InputNumber min={0} precision={2} addonAfter="GB" style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item label={t('server.tags')}>
            <TagSelect value={tagValues} onChange={setTagValues} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
