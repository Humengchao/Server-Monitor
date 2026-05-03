import React, { useEffect, useState, useCallback } from 'react';
import {
  Table, Button, Modal, Form, Input, Space, Typography, Popconfirm, App,
} from 'antd';
import { PlusOutlined, EditOutlined, DeleteOutlined, KeyOutlined } from '@ant-design/icons';
import { credentialsApi, Credential } from '../api/credentials';

const { Title } = Typography;

export default function Credentials() {
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
      message.error('Failed to load credentials');
    }
    setLoading(false);
  }, [message]);

  useEffect(() => {
    load();
  }, [load]);

  const handleSubmit = async (values: any) => {
    try {
      if (editing) {
        await credentialsApi.update(editing.id, values);
        message.success('Credential updated');
      } else {
        await credentialsApi.create(values);
        message.success('Credential created');
      }
      setModalOpen(false);
      form.resetFields();
      setEditing(null);
      load();
    } catch (err: any) {
      message.error(err.response?.data?.error || 'Operation failed');
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await credentialsApi.delete(id);
      message.success('Credential deleted');
      load();
    } catch {
      message.error('Failed to delete credential');
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
      title: 'Name',
      dataIndex: 'name',
      key: 'name',
      render: (name: string) => (
        <Space><KeyOutlined />{name}</Space>
      ),
    },
    {
      title: 'SSH Username',
      dataIndex: 'ssh_username',
      key: 'ssh_username',
    },
    {
      title: 'Created',
      dataIndex: 'created_at',
      key: 'created_at',
      render: (t: string) => new Date(t).toLocaleString(),
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 120,
      render: (_: any, record: Credential) => (
        <Space>
          <Button type="text" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
          <Popconfirm
            title="Delete this credential?"
            description="Servers using it will keep their current username."
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
        <Title level={4} style={{ margin: 0 }}>Credentials</Title>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            form.resetFields();
            setModalOpen(true);
          }}
        >
          Add Credential
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
        title={editing ? 'Edit Credential' : 'Add Credential'}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditing(null); }}
        onOk={() => form.submit()}
        width={480}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label="Credential Name" rules={[{ required: true }]}>
            <Input placeholder="My SSH Key" />
          </Form.Item>
          <Form.Item name="ssh_username" label="SSH Username" rules={[{ required: true }]}>
            <Input placeholder="root" />
          </Form.Item>
          <Form.Item name="ssh_password" label="SSH Password">
            <Input.Password placeholder="Leave blank if using key" />
          </Form.Item>
          <Form.Item name="ssh_key" label="SSH Private Key">
            <Input.TextArea rows={4} placeholder="Paste private key content" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
