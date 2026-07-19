import React, { useEffect, useState, useCallback, useMemo } from 'react';
import {
  Row, Col, Button, Modal, Form, Input, InputNumber, Select, Typography, Space, App
} from 'antd';
import { DatePicker } from 'antd';
import dayjs from 'dayjs';
import { PlusOutlined, ReloadOutlined, FilterOutlined, SafetyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import ServerCard from '../components/ServerCard';
import TagSelect from '../components/TagSelect';
import CredentialSelect from '../components/CredentialSelect';
import { serversApi, Server, Tag } from '../api/servers';

const { Title, Text } = Typography;

async function lookupIP(ip: string): Promise<string> {
  try {
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), 3000);
    const res = await fetch(
      `http://ip-api.com/json/${ip}?fields=status,country,regionName,city,isp`,
      { signal: ac.signal },
    );
    clearTimeout(timer);
    const data = await res.json();
    if (data.status === 'success') {
      return `${data.isp} ${data.country} ${data.city}`;
    }
  } catch { /* timeout or network error */ }
  return '';
}

export default function Dashboard() {
  const { t } = useTranslation();
  const { message, notification } = App.useApp();
  const [servers, setServers] = useState<Server[]>([]);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);
  const [filterTagIds, setFilterTagIds] = useState<string[]>([]);
  const [selectedCredential, setSelectedCredential] = useState<string | undefined>(undefined);

  useEffect(() => {
    const raw = localStorage.getItem('last_login');
    if (!raw) return;
    localStorage.removeItem('last_login');

    let parsed: { ip: string; logged_at: string };
    try {
      parsed = JSON.parse(raw);
    } catch {
      return;
    }

    const { ip, logged_at } = parsed;
    const loginTime = new Date(logged_at).toLocaleString();

    // Helper to show/update the notification
    const showNotification = (currentDesc: string, lastDesc: string) => {
      notification.info({
        title: t('notification.loginSuccess'),
        description: (
          <div style={{ whiteSpace: 'pre-line' }}>
            {currentDesc ? (
              <>
                <div><Text strong>{t('notification.currentLogin')}</Text></div>
                <div>{currentDesc}</div>
                <div style={{ marginTop: 8 }}><Text strong>{t('notification.previousLogin')}</Text></div>
              </>
            ) : (
              <div><Text strong>{t('notification.previousLogin')}</Text></div>
            )}
            <div>{lastDesc}</div>
          </div>
        ),
        icon: <SafetyOutlined style={{ color: '#1890ff' }} />,
        placement: 'bottomRight',
        duration: 10,
      });
    };

    // Show basic notification immediately, then enrich with geolocation
    showNotification('', `IP: ${ip}  Time: ${loginTime}`);

    const ac = new AbortController();
    const ipTimer = setTimeout(() => ac.abort(), 4000);
    fetch('https://api.ipify.org?format=json', { signal: ac.signal })
      .then((r) => r.json())
      .then(async (data) => {
        clearTimeout(ipTimer);
        const currentIP = data.ip;
        const [currentLoc, lastLoc] = await Promise.all([
          lookupIP(currentIP),
          lookupIP(ip),
        ]);
        const currentDesc = currentLoc ? `IP: ${currentIP}\nLocation: ${currentLoc}` : `IP: ${currentIP}`;
        const lastDesc = lastLoc
          ? `IP: ${ip}\nLocation: ${lastLoc}\nTime: ${loginTime}`
          : `IP: ${ip}\nTime: ${loginTime}`;
        showNotification(currentDesc, lastDesc);
      })
      .catch(() => { clearTimeout(ipTimer); /* silently degrade */ });
  }, []);

  const loadServers = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    try {
      const res = await serversApi.list();
      setServers(res.data || []);
    } catch {
      if (showLoading) message.error(t('server.loadFailed'));
    }
    if (showLoading) setLoading(false);
  }, [message, t]);

  useEffect(() => {
    loadServers();
    const timer = setInterval(() => loadServers(false), 3000);
    return () => clearInterval(timer);
  }, [loadServers]);

  const handleSubmit = async (values: any) => {
    try {
      const payload = {
        ...values,
        credential_id: selectedCredential || null,
        expires_at: values.expires_at ? values.expires_at.toISOString() : null,
      };
      if (editingServer) {
        await serversApi.update(editingServer.id, payload);
        await serversApi.setTags(editingServer.id, tagValues);
        message.success(t('server.updated'));
      } else {
        const res = await serversApi.create(payload);
        if (tagValues.length > 0) {
          await serversApi.setTags(res.data.id, tagValues);
        }
        message.success(t('server.added'));
      }
      setModalOpen(false);
      form.resetFields();
      setTagValues([]);
      setSelectedCredential(undefined);
      setEditingServer(null);
      loadServers();
    } catch (err: any) {
      message.error(err.response?.data?.error || t('server.operationFailed'));
    }
  };

  const allTags = useMemo(() => {
    const map = new Map<string, Tag>();
    servers.forEach((s) => s.tags?.forEach((t) => map.set(t.id, t)));
    return Array.from(map.values());
  }, [servers]);

  const filteredServers = useMemo(() => {
    if (filterTagIds.length === 0) return servers;
    return servers.filter((s) => filterTagIds.some((id) => s.tags?.some((t) => t.id === id)));
  }, [servers, filterTagIds]);

  const handleEdit = (server: Server) => {
    setEditingServer(server);
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

  const handleDelete = async (server: Server) => {
    Modal.confirm({
      title: t('server.delete'),
      content: t('server.deleteConfirm', { name: server.name }),
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success(t('server.deleted'));
          loadServers();
        } catch {
          message.error(t('server.deleteFailed'));
        }
      },
    });
  };

  return (
    <div>
      <Space style={{ marginBottom: 24, width: '100%', justifyContent: 'space-between' }}>
        <Title level={4} style={{ margin: 0 }}>{t('server.title')}</Title>
        <Space>
          {allTags.length > 0 && (
            <Select
              mode="multiple"
              placeholder={<Space><FilterOutlined />{t('server.filterByTag')}</Space>}
              value={filterTagIds}
              onChange={setFilterTagIds}
              style={{ minWidth: 180 }}
              maxTagCount="responsive"
              allowClear
            >
              {allTags.map((tag) => (
                <Select.Option key={tag.id} value={tag.id}>
                  <span style={{ color: tag.color }}>●</span> {tag.name}
                </Select.Option>
              ))}
            </Select>
          )}
          <Button icon={<ReloadOutlined />} onClick={() => loadServers()}>{t('common.refresh')}</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => {
            setEditingServer(null);
            form.resetFields();
            setTagValues([]);
            setSelectedCredential(undefined);
            setModalOpen(true);
          }}>
            {t('server.add')}
          </Button>
        </Space>
      </Space>

      <Row gutter={[16, 16]}>
        {filteredServers.map((s) => (
          <Col key={s.id} xs={24} sm={12} lg={8} xl={6}>
            <ServerCard server={s} />
          </Col>
        ))}
        {!loading && filteredServers.length === 0 && (
          <Col span={24}>
            <div style={{ textAlign: 'center', padding: 64, color: '#999' }}>
              {t('server.empty')}
            </div>
          </Col>
        )}
      </Row>

      <Modal
        title={editingServer ? t('server.edit') : t('server.add')}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditingServer(null); }}
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
                <Input.Password placeholder={t('server.sshPasswordPlaceholder')} />
              </Form.Item>
              <Form.Item name="ssh_key" label={t('server.sshKey')}>
                <Input.TextArea rows={4} placeholder={t('server.sshKeyPlaceholder')} />
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
