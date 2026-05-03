import React, { useEffect, useState } from 'react';
import { Select, Space, Button, Input, App } from 'antd';
import { PlusOutlined, KeyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { credentialsApi, Credential } from '../api/credentials';

interface Props {
  value?: string;
  onChange?: (id: string | undefined) => void;
}

export default function CredentialSelect({ value, onChange }: Props) {
  const { t } = useTranslation();
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
      message.success(t('credential.created'));
    } catch {
      message.error(t('credential.createFailed'));
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
                  <Button size="small" type="primary" onClick={handleCreate}>{t('common.create')}</Button>
                  <Button size="small" onClick={() => setShowNew(false)}>{t('common.cancel')}</Button>
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
