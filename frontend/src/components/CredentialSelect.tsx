import React, { useEffect, useState } from 'react';
import { Select, Space, Button, Input, App, Tag } from 'antd';
import { PlusOutlined, KeyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { credentialsApi, Credential } from '../api/credentials';

interface Props {
  value?: string;
  onChange?: (id: string | undefined) => void;
  serverType?: string;
}

export default function CredentialSelect({ value, onChange, serverType }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [creds, setCreds] = useState<Credential[]>([]);
  const [loading, setLoading] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [newName, setNewName] = useState('');
  const [newUsername, setNewUsername] = useState('root');
  const [newPassword, setNewPassword] = useState('');
  const [newKey, setNewKey] = useState('');
  const [creating, setCreating] = useState(false);

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
    // Defer the initial request so the effect itself does not synchronously
    // trigger state updates during the commit phase.
    const timer = window.setTimeout(() => { void loadCreds(); }, 0);
    return () => window.clearTimeout(timer);
  }, []);

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setCreating(true);
    try {
      const res = await credentialsApi.create({
        name: newName.trim(),
        ssh_username: newUsername.trim() || 'root',
        ssh_password: newPassword,
        ssh_key: newKey,
        credential_type: serverType || 'linux',
      });
      setCreds((prev) => [res.data, ...prev]);
      onChange?.(res.data.id);
      setNewName('');
      setNewUsername('root');
      setNewPassword('');
      setNewKey('');
      setShowNew(false);
      message.success(t('credential.created'));
    } catch {
      message.error(t('credential.createFailed'));
    } finally {
      setCreating(false);
    }
  };

  return (
    <Select
      allowClear
      placeholder={t('credential.selectPlaceholder')}
      value={value || undefined}
      onChange={(v) => onChange?.(v || undefined)}
      loading={loading}
      style={{ width: '100%' }}
      notFoundContent={loading ? t('common.loading') : t('credential.noCredentials')}
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
                  placeholder={t('credential.credNamePlaceholder')}
                />
                <Input
                  size="small"
                  value={newUsername}
                  onChange={(e) => setNewUsername(e.target.value)}
                  placeholder={t('credential.sshUsernameInlinePlaceholder')}
                />
                <Input.Password
                  size="small"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder={t('credential.sshPasswordInlinePlaceholder')}
                />
                <Input.TextArea
                  size="small"
                  rows={2}
                  value={newKey}
                  onChange={(e) => setNewKey(e.target.value)}
                  placeholder={t('credential.sshKeyInlinePlaceholder')}
                  style={{ fontSize: 12 }}
                />
                <Space>
                  <Button size="small" type="primary" loading={creating} disabled={!newName.trim()} onClick={handleCreate}>{t('common.create')}</Button>
                  <Button size="small" disabled={creating} onClick={() => setShowNew(false)}>{t('common.cancel')}</Button>
                </Space>
              </Space>
            ) : (
              <Button type="dashed" size="small" icon={<PlusOutlined />} block onClick={() => setShowNew(true)}>
                {t('credential.quickCreate')}
              </Button>
            )}
          </div>
        </>
      )}
    >
      {creds.filter((c) => !serverType || c.credential_type === serverType).map((c) => (
        <Select.Option key={c.id} value={c.id}>
          <Space>
            <KeyOutlined />
            <span>{c.name}</span>
            <span style={{ color: '#999', fontSize: 12 }}>({c.ssh_username})</span>
            <Tag style={{ fontSize: 10, lineHeight: '16px' }}>{c.credential_type === 'windows' ? 'Win' : 'Linux'}</Tag>
          </Space>
        </Select.Option>
      ))}
    </Select>
  );
}
