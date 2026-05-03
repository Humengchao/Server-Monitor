import React, { useEffect, useState, useCallback } from 'react';
import {
  Table, Button, Modal, Form, Input, Space, Typography, Popconfirm, App,
} from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined, KeyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { credentialsApi, Credential } from '../api/credentials';

const { Title } = Typography;

export default function Credentials() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [creds, setCreds] = useState<Credential[]>([]);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Credential | null>(null);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await credentialsApi.list();
      setCreds(res.data || []);
    } catch {
      message.error(t('credential.loadFailed'));
    }
    setLoading(false);
  }, [message, t]);

  useEffect(() => {
    load();
  }, [load]);

  const handleSubmit = async (values: any) => {
    try {
      if (editing) {
        await credentialsApi.update(editing.id, values);
        message.success(t('credential.updated'));
      } else {
        await credentialsApi.create(values);
        message.success(t('credential.created'));
      }
      setModalOpen(false);
      form.resetFields();
      setEditing(null);
      load();
    } catch (err: any) {
      message.error(err.response?.data?.error || t('server.operationFailed'));
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await credentialsApi.delete(id);
      message.success(t('credential.deleted'));
      load();
    } catch {
      message.error(t('credential.deleteFailed'));
    }
  };

  const handleEdit = (cred: Credential) => {
    setEditing(cred);
    form.setFieldsValue({
      name: cred.name,
      ssh_username: cred.ssh_username,
    });
    setModalOpen(true);
  };

  const columns = [
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      render: (name: string) => (
        <Space><KeyOutlined />{name}</Space>
      ),
    },
    {
      title: t('credential.sshUsername'),
      dataIndex: 'ssh_username',
      key: 'ssh_username',
    },
    {
      title: t('common.created'),
      dataIndex: 'created_at',
      key: 'created_at',
      render: (v: string) => new Date(v).toLocaleString(),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 120,
      render: (_: any, record: Credential) => (
        <Space>
          <Button type="text" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
          <Popconfirm
            title={t('credential.deleteConfirm')}
            description={t('credential.deleteDesc')}
            onConfirm={() => handleDelete(record.id)}
          >
            <Button type="text" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 24, width: '100%', justifyContent: 'space-between' }}>
        <Title level={4} style={{ margin: 0 }}>{t('credential.title')}</Title>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            form.resetFields();
            setModalOpen(true);
          }}
        >
          {t('credential.add')}
        </Button>
      </Space>

      <Table
        dataSource={creds}
        columns={columns}
        rowKey="id"
        loading={loading}
        pagination={false}
      />

      <Modal
        title={editing ? t('credential.edit') : t('credential.add')}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditing(null); }}
        onOk={() => form.submit()}
        width={480}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label={t('credential.name')} rules={[{ required: true }]}>
            <Input placeholder={t('credential.namePlaceholder')} />
          </Form.Item>
          <Form.Item name="ssh_username" label={t('credential.sshUsername')} rules={[{ required: true }]}>
            <Input placeholder={t('credential.sshUsernamePlaceholder')} />
          </Form.Item>
          <Form.Item name="ssh_password" label={t('credential.sshPassword')}>
            <Input.Password placeholder={t('credential.sshPasswordPlaceholder')} />
          </Form.Item>
          <Form.Item name="ssh_key" label={t('credential.sshKey')}>
            <Input.TextArea rows={4} placeholder={t('credential.sshKeyPlaceholder')} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
