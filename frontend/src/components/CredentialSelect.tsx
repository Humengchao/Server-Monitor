import React, { useEffect, useState } from 'react';
import { Select, Space, Button, Input, App } from 'antd';
import { PlusOutlined, KeyOutlined } from '@ant-design/icons';
import { credentialsApi, Credential } from '../api/credentials';

interface Props {
  value?: string;
  onChange?: (id: string | undefined) => void;
}

export default function CredentialSelect({ value, onChange }: Props) {
  const { message } = App.useApp();
  const [creds, setCreds] = useState<Credential[]>([]);
  const [loading, setLoading] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [newName, setNewName] = useState('');
  const [newUsername, setNewUsername] = useState('root');
  const [newPassword, setNewPassword] = useState('');
  const [newKey, setNewKey] = useState('');

  const loadCreds = async () => {
    setLoading(true);
    try {
      const res = await credentialsApi.list();
      setCreds(res.data || []);
    } catch {
      // ignore
    }
    setLoading(false);
  };

  useEffect(() => {
    loadCreds();
  }, []);

  const handleCreate = async () => {
    if (!newName.trim()) return;
    try {
      const res = await credentialsApi.create({
        name: newName.trim(),
        ssh_username: newUsername || 'root',
        ssh_password: newPassword,
        ssh_key: newKey,
      });
      setCreds([res.data, ...creds]);
      onChange?.(res.data.id);
      setNewName('');
      setNewUsername('root');
      setNewPassword('');
      setNewKey('');
      setShowNew(false);
      message.success('Credential created');
    } catch {
      message.error('Failed to create credential');
    }
  };

  return (
    <Select
      allowClear
      placeholder="Select credential (optional)"
      value={value || undefined}
      onChange={(v) => onChange?.(v || undefined)}
      loading={loading}
      style={{ width: '100%' }}
      notFoundContent={loading ? 'Loading...' : 'No credentials yet'}
      popupRender={(menu) => (
        <>
          {menu}
          <div style={{ padding: 8, borderTop: '1px solid #f0f0f0' }}>
            {showNew ? (
              <Space orientation="vertical" style={{ width: '100%' }} size={4}>
                <Input
                  size="small"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="Credential name"
                />
                <Input
                  size="small"
                  value={newUsername}
                  onChange={(e) => setNewUsername(e.target.value)}
                  placeholder="SSH username"
                />
                <Input.Password
                  size="small"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder="SSH password (optional)"
                />
                <Input.TextArea
                  size="small"
                  rows={2}
                  value={newKey}
                  onChange={(e) => setNewKey(e.target.value)}
                  placeholder="SSH private key (optional)"
                  style={{ fontSize: 12 }}
                />
                <Space>
                  <Button size="small" type="primary" onClick={handleCreate}>Create</Button>
                  <Button size="small" onClick={() => setShowNew(false)}>Cancel</Button>
                </Space>
              </Space>
            ) : (
              <Button type="dashed" size="small" icon={<PlusOutlined />} block onClick={() => setShowNew(true)}>
                Quick Create
              </Button>
            )}
          </div>
        </>
      )}
    >
      {creds.map((c) => (
        <Select.Option key={c.id} value={c.id}>
          <Space>
            <KeyOutlined />
            <span>{c.name}</span>
            <span style={{ color: '#999', fontSize: 12 }}>({c.ssh_username})</span>
          </Space>
        </Select.Option>
      ))}
    </Select>
  );
}
