import React, { useEffect, useState } from 'react';
import {
  App, Alert, Button, Card, ColorPicker, Col, Form, Input, Modal, Popconfirm, Row, Space, Table, Tag as AntTag,
  Tooltip, Typography,
} from 'antd';
import {
  BellOutlined, DeleteOutlined, EditOutlined, ExperimentOutlined, LockOutlined, PlusOutlined,
  SafetyCertificateOutlined, TagsOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { tagsApi, Tag } from '../api/servers';
import { alertsApi } from '../api/alerts';
import { authApi } from '../api/auth';
import { settingsApi } from '../api/settings';
import { useAuthStore } from '../store/authStore';

const { Title, Text } = Typography;

interface PasswordFormValues {
  current_password: string;
  new_password: string;
  confirm_password: string;
}

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

export default function Settings() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { user, token, setAuth, logout } = useAuthStore();
  const [tags, setTags] = useState<Tag[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  // null while creating; the tag being renamed otherwise.
  const [editingTag, setEditingTag] = useState<Tag | null>(null);
  const [form] = Form.useForm();
  const [passwordForm] = Form.useForm<PasswordFormValues>();
  const [savingPassword, setSavingPassword] = useState(false);
  const [webhook, setWebhook] = useState('');
  const [savedWebhook, setSavedWebhook] = useState('');
  const [savingWebhook, setSavingWebhook] = useState(false);
  const [testingWebhook, setTestingWebhook] = useState(false);

  const loadTags = async () => {
    setLoading(true);
    try {
      const res = await tagsApi.list();
      setTags(res.data || []);
    } catch {
      message.error(t('settings.loadTagsFailed'));
    }
    setLoading(false);
  };

  const loadSettings = async () => {
    try {
      const res = await settingsApi.get();
      setWebhook(res.data.default_webhook_url || '');
      setSavedWebhook(res.data.default_webhook_url || '');
    } catch {
      message.error(t('settings.loadSettingsFailed'));
    }
  };

  useEffect(() => {
    // Deferred so the first fetch doesn't set state synchronously in the effect.
    const initial = window.setTimeout(() => { loadTags(); loadSettings(); }, 0);
    return () => window.clearTimeout(initial);
  }, []);

  const handleSaveWebhook = async () => {
    setSavingWebhook(true);
    try {
      const res = await settingsApi.update(webhook.trim());
      setSavedWebhook(res.data.default_webhook_url || '');
      setWebhook(res.data.default_webhook_url || '');
      message.success(res.data.default_webhook_url
        ? t('settings.webhookSaved')
        : t('settings.webhookCleared'));
    } catch (err: unknown) {
      // The server's own words name the reason — an unreachable scheme, a
      // private address — and beat any message this page could compose.
      message.error(apiError(err, t('settings.webhookSaveFailed')));
    }
    setSavingWebhook(false);
  };

  const handleTestWebhook = async () => {
    setTestingWebhook(true);
    try {
      await alertsApi.testWebhook(webhook.trim());
      message.success(t('settings.webhookTestSent'));
    } catch (err: unknown) {
      message.error(apiError(err, t('settings.webhookTestFailed')));
    }
    setTestingWebhook(false);
  };

  // antd's ColorPicker hands the form an AggregationColor object (not a hex
  // string) once the user picks a color; sending it raw makes the backend's
  // string binding reject the request.
  const toHexColor = (value: unknown): string => {
    if (typeof value === 'string' && value) return value;
    if (value && typeof (value as { toHexString?: unknown }).toHexString === 'function') {
      return (value as { toHexString: () => string }).toHexString();
    }
    return '#1890ff';
  };

  const openCreate = () => {
    setEditingTag(null);
    // destroyOnHidden discards the fields, but the create form must not inherit
    // whatever colour the previous edit left behind.
    form.setFieldsValue({ name: '', color: '#1890ff' });
    setModalOpen(true);
  };

  const openEdit = (tag: Tag) => {
    setEditingTag(tag);
    form.setFieldsValue({ name: tag.name, color: tag.color });
    setModalOpen(true);
  };

  const handleSubmit = async (values: { name: string; color: unknown }) => {
    const color = toHexColor(values.color);
    try {
      if (editingTag) {
        await tagsApi.update(editingTag.id, values.name, color);
        message.success(t('settings.tagUpdated'));
      } else {
        await tagsApi.create(values.name, color);
        message.success(t('settings.tagCreated'));
      }
      setModalOpen(false);
      setEditingTag(null);
      form.resetFields();
      loadTags();
    } catch (err: unknown) {
      // A duplicate name comes back as 409 and needs its own wording: the
      // generic failure message sent people looking for a server fault.
      const status = (err as { response?: { status?: number } })?.response?.status;
      if (status === 409) {
        message.error(t('settings.tagNameTaken'));
        return;
      }
      message.error(editingTag ? t('settings.tagUpdateFailed') : t('settings.tagCreateFailed'));
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await tagsApi.delete(id);
      message.success(t('settings.tagDeleted'));
      loadTags();
    } catch {
      message.error(t('settings.tagDeleteFailed'));
    }
  };

  const handleChangePassword = async (values: PasswordFormValues) => {
    setSavingPassword(true);
    try {
      const res = await authApi.changePassword(values.current_password, values.new_password);
      passwordForm.resetFields();
      // The server revoked every token issued before this moment. It hands back
      // a replacement so this tab stays signed in; without one we have to bounce
      // the user to the login page rather than leave them with a dead token.
      if (res.data.token && user) {
        setAuth(res.data.token, user);
        message.success(t('settings.passwordChanged'));
      } else {
        message.success(t('settings.passwordChangedReauth'));
        window.setTimeout(() => { logout(); window.location.href = '/login'; }, 1500);
      }
    } catch (err: unknown) {
      message.error(apiError(err, t('settings.passwordChangeFailed')));
    }
    setSavingPassword(false);
  };

  const columns = [
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      render: (name: string, record: Tag) => <AntTag color={record.color}>{name}</AntTag>,
    },
    {
      title: t('common.color'),
      dataIndex: 'color',
      key: 'color',
      render: (color: string) => (
        <Space>
          <span className="color-dot" style={{ background: color }} />
          <Text className="mono-cell">{color}</Text>
        </Space>
      ),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 96,
      render: (_: unknown, record: Tag) => (
        <Space size={0}>
          <Tooltip title={t('common.edit')}>
            <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(record)} />
          </Tooltip>
          {/* The confirm spells out the cascade: deleting a tag removes it from
              every server at once, and there is no undo. */}
          <Popconfirm
            title={t('settings.deleteTagConfirm')}
            description={t('settings.deleteTagWarning')}
            okButtonProps={{ danger: true }}
            onConfirm={() => handleDelete(record.id)}
          >
            <Tooltip title={t('common.delete')}>
              <Button type="text" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div className="settings-page">
      <div className="page-heading">
        <div>
          <Text className="eyebrow">{t('settings.eyebrow')}</Text>
          <Title level={2}>{t('nav.settings')}</Title>
          <Text type="secondary">{t('settings.subtitle')}</Text>
        </div>
      </div>

      <Row gutter={[18, 18]}>
        <Col xs={24} xl={13}>
          <Card
            className="panel-card"
            title={<Space><TagsOutlined />{t('settings.tagManagement')}</Space>}
            extra={
              <Button type="primary" size="small" icon={<PlusOutlined />} onClick={openCreate}>
                {t('settings.createTag')}
              </Button>
            }
          >
            <Text type="secondary" className="card-hint">{t('settings.tagHint')}</Text>
            <Table
              className="server-table"
              dataSource={tags}
              columns={columns}
              rowKey="id"
              size="small"
              loading={loading}
              pagination={false}
            />
          </Card>

          {/* Alert rules each carried their own webhook, so a fleet with ten
              rules meant pasting the same URL ten times and editing all ten to
              change it. A rule that leaves its own field blank now inherits
              this. */}
          <Card
            className="panel-card settings-notify-card"
            title={<Space><BellOutlined />{t('settings.notifications')}</Space>}
          >
            <Text type="secondary" className="card-hint">{t('settings.webhookHint')}</Text>
            <Input
              value={webhook}
              onChange={(event) => setWebhook(event.target.value)}
              placeholder="https://hooks.example.com/alerts"
              allowClear
              onPressEnter={handleSaveWebhook}
            />
            <Space className="settings-notify-actions" wrap>
              <Button
                type="primary"
                loading={savingWebhook}
                disabled={webhook.trim() === savedWebhook}
                onClick={handleSaveWebhook}
              >
                {t('common.save')}
              </Button>
              <Button
                icon={<ExperimentOutlined />}
                loading={testingWebhook}
                // Testing an unsaved URL is the point — you check it works
                // before committing to it.
                disabled={!webhook.trim()}
                onClick={handleTestWebhook}
              >
                {t('alerts.testWebhook')}
              </Button>
              {!savedWebhook && (
                <Text type="secondary">{t('settings.webhookUnset')}</Text>
              )}
            </Space>
          </Card>
        </Col>

        <Col xs={24} xl={11}>
          <Card className="panel-card" title={<Space><SafetyCertificateOutlined />{t('settings.accountSecurity')}</Space>}>
            <div className="account-identity">
              <span>{user?.username?.trim().charAt(0).toUpperCase() || 'U'}</span>
              <div>
                <strong>{user?.username}</strong>
                <Text type="secondary">{t('settings.signedIn')}</Text>
              </div>
            </div>

            <Alert
              className="settings-alert"
              type="info"
              showIcon
              title={t('settings.revokeNotice')}
            />

            <Form
              form={passwordForm}
              layout="vertical"
              onFinish={handleChangePassword}
              requiredMark={false}
              disabled={!token}
            >
              <Form.Item
                name="current_password"
                label={t('settings.currentPassword')}
                rules={[{ required: true, message: t('settings.currentPasswordRequired') }]}
              >
                <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
              </Form.Item>
              <Form.Item
                name="new_password"
                label={t('settings.newPassword')}
                rules={[
                  { required: true, message: t('settings.newPasswordRequired') },
                  { min: 6, message: t('register.passwordMin') },
                ]}
              >
                <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
              </Form.Item>
              <Form.Item
                name="confirm_password"
                label={t('settings.confirmPassword')}
                dependencies={['new_password']}
                rules={[
                  { required: true, message: t('settings.confirmPasswordRequired') },
                  ({ getFieldValue }) => ({
                    validator: (_, value) =>
                      !value || getFieldValue('new_password') === value
                        ? Promise.resolve()
                        : Promise.reject(new Error(t('settings.passwordMismatch'))),
                  }),
                ]}
              >
                <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={savingPassword} block>
                {t('settings.updatePassword')}
              </Button>
            </Form>
          </Card>
        </Col>
      </Row>

      <Modal
        title={editingTag ? t('settings.editTag') : t('settings.createTag')}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditingTag(null); }}
        onOk={() => form.submit()}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item
            name="name"
            label={t('settings.tagName')}
            // 64 matches the column; trimmed because a name of spaces passes
            // `required` but is rejected by the server.
            normalize={(value: string) => value?.replace(/^\s+/, '')}
            rules={[{ required: true }, { max: 64, message: t('settings.tagNameTooLong') }]}
          >
            <Input placeholder={t('settings.tagNamePlaceholder')} maxLength={64} showCount />
          </Form.Item>
          <Form.Item name="color" label={t('settings.color')} initialValue="#1890ff">
            <ColorPicker format="hex" />
          </Form.Item>
          {editingTag && (
            <Text type="secondary">{t('settings.editTagHint')}</Text>
          )}
        </Form>
      </Modal>
    </div>
  );
}
