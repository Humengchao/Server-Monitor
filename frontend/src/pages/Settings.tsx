import React, { useEffect, useState } from 'react';
import { Card, Table, Button, Modal, Form, Input, Popconfirm, Space, Typography, ColorPicker, App } from 'antd';
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { tagsApi, Tag } from '../api/servers';

const { Title } = Typography;

export default function Settings() {
  const { t } = useTranslation();
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
      message.error(t('settings.loadTagsFailed'));
    }
    setLoading(false);
  };

  useEffect(() => {
    loadTags();
  }, []);

  const handleCreate = async (values: { name: string; color: string }) => {
    try {
      await tagsApi.create(values.name, values.color || '#1890ff');
      message.success(t('settings.tagCreated'));
      setModalOpen(false);
      form.resetFields();
      loadTags();
    } catch {
      message.error(t('settings.tagCreateFailed'));
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

  const columns = [
    { title: t('common.name'), dataIndex: 'name', key: 'name' },
    {
      title: t('common.color'),
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
      title: t('common.actions'),
      key: 'actions',
      render: (_: any, record: Tag) => (
        <Popconfirm title={t('settings.deleteTagConfirm')} onConfirm={() => handleDelete(record.id)}>
          <Button type="text" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 24, width: '100%', justifyContent: 'space-between' }}>
        <Title level={4} style={{ margin: 0 }}>{t('settings.tagManagement')}</Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setModalOpen(true)}>
          {t('settings.createTag')}
        </Button>
      </Space>

      <Card>
        <Table dataSource={tags} columns={columns} rowKey="id" loading={loading} pagination={false} />
      </Card>

      <Modal
        title={t('settings.createTag')}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={() => form.submit()}
      >
        <Form form={form} layout="vertical" onFinish={handleCreate}>
          <Form.Item name="name" label={t('settings.tagName')} rules={[{ required: true }]}>
            <Input placeholder={t('settings.tagNamePlaceholder')} />
          </Form.Item>
          <Form.Item name="color" label={t('settings.color')} initialValue="#1890ff">
            <ColorPicker format="hex" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
