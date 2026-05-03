import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Modal, Form, Input, Popconfirm, Space, Typography, ColorPicker, App } from 'antd';
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons';
import { tagsApi, Tag } from '../api/servers';

const { Title } = Typography;

export default function Settings() {
  const { message } = App.useApp();
  const [tags, setTags] = useState<Tag[]>([]);
  const [loading, setLoading] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm();

  const loadTags = async () => {
    setLoading(true);
    try {
      const res = await tagsApi.list();
      setTags(res.data || []);
    } catch {
      message.error('Failed to load tags');
    }
    setLoading(false);
  };

  useEffect(() => {
    loadTags();
  }, []);

  const handleCreate = async (values: { name: string; color: string }) => {
    try {
      await tagsApi.create(values.name, values.color || '#1890ff');
      message.success('Tag created');
      setModalOpen(false);
      form.resetFields();
      loadTags();
    } catch {
      message.error('Failed to create tag');
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await tagsApi.delete(id);
      message.success('Tag deleted');
      loadTags();
    } catch {
      message.error('Failed to delete tag');
    }
  };

  const columns = [
    { title: 'Name', dataIndex: 'name', key: 'name' },
    {
      title: 'Color',
      dataIndex: 'color',
      key: 'color',
      render: (color: string) => (
        <Space>
          <span style={{ color, fontSize: 18 }}>●</span>
          {color}
        </Space>
      ),
    },
    {
      title: 'Actions',
      key: 'actions',
      render: (_: any, record: Tag) => (
        <Popconfirm title="Delete this tag?" onConfirm={() => handleDelete(record.id)}>
          <Button type="text" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 24, width: '100%', justifyContent: 'space-between' }}>
        <Title level={4} style={{ margin: 0 }}>Tag Management</Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setModalOpen(true)}>
          Create Tag
        </Button>
      </Space>

      <Card>
        <Table dataSource={tags} columns={columns} rowKey="id" loading={loading} pagination={false} />
      </Card>

      <Modal
        title="Create Tag"
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={() => form.submit()}
      >
        <Form form={form} layout="vertical" onFinish={handleCreate}>
          <Form.Item name="name" label="Tag Name" rules={[{ required: true }]}>
            <Input placeholder="Production" />
          </Form.Item>
          <Form.Item name="color" label="Color" initialValue="#1890ff">
            <ColorPicker format="hex" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
