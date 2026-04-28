import React, { useEffect, useState } from 'react';
import {
  Row, Col, Button, Modal, Form, Input, InputNumber, Select, message, Typography, Space
} from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import ServerCard from '../components/ServerCard';
import TagSelect from '../components/TagSelect';
import { serversApi, Server } from '../api/servers';

const { Title } = Typography;

export default function Dashboard() {
  const [servers, setServers] = useState<Server[]>([]);
  const [loading, setLoading] = useState(true);
  const [modalOpen, setModalOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<Server | null>(null);
  const [form] = Form.useForm();
  const [tagValues, setTagValues] = useState<string[]>([]);

  const loadServers = async () => {
    setLoading(true);
    try {
      const res = await serversApi.list();
      setServers(res.data || []);
    } catch {
      message.error('Failed to load servers');
    }
    setLoading(false);
  };

  useEffect(() => {
    loadServers();
  }, []);

  const handleSubmit = async (values: any) => {
    try {
      if (editingServer) {
        await serversApi.update(editingServer.id, values);
        await serversApi.setTags(editingServer.id, tagValues);
        message.success('Server updated');
      } else {
        const res = await serversApi.create(values);
        if (tagValues.length > 0) {
          await serversApi.setTags(res.data.id, tagValues);
        }
        message.success('Server added');
      }
      setModalOpen(false);
      form.resetFields();
      setTagValues([]);
      setEditingServer(null);
      loadServers();
    } catch (err: any) {
      message.error(err.response?.data?.error || 'Operation failed');
    }
  };

  const handleEdit = (server: Server) => {
    setEditingServer(server);
    form.setFieldsValue({
      name: server.name,
      host: server.host,
      port: server.port,
      ssh_username: server.ssh_username,
    });
    setTagValues(server.tags?.map((t) => t.id) || []);
    setModalOpen(true);
  };

  const handleDelete = async (server: Server) => {
    Modal.confirm({
      title: 'Delete Server',
      content: `Are you sure you want to delete "${server.name}"?`,
      onOk: async () => {
        try {
          await serversApi.delete(server.id);
          message.success('Server deleted');
          loadServers();
        } catch {
          message.error('Failed to delete server');
        }
      },
    });
  };

  return (
    <div>
      <Space style={{ marginBottom: 24, width: '100%', justifyContent: 'space-between' }}>
        <Title level={4} style={{ margin: 0 }}>My Servers</Title>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={loadServers}>Refresh</Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => {
            setEditingServer(null);
            form.resetFields();
            setTagValues([]);
            setModalOpen(true);
          }}>
            Add Server
          </Button>
        </Space>
      </Space>

      <Row gutter={[16, 16]}>
        {servers.map((s) => (
          <Col key={s.id} xs={24} sm={12} lg={8} xl={6}>
            <ServerCard server={s} />
          </Col>
        ))}
        {!loading && servers.length === 0 && (
          <Col span={24}>
            <div style={{ textAlign: 'center', padding: 64, color: '#999' }}>
              No servers yet. Click "Add Server" to get started.
            </div>
          </Col>
        )}
      </Row>

      <Modal
        title={editingServer ? 'Edit Server' : 'Add Server'}
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setEditingServer(null); }}
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
            <Input.Password placeholder="Leave blank to use key" />
          </Form.Item>
          <Form.Item name="ssh_key" label="SSH Private Key">
            <Input.TextArea rows={4} placeholder="Paste private key content" />
          </Form.Item>
          <Form.Item label="Tags">
            <TagSelect value={tagValues} onChange={setTagValues} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
