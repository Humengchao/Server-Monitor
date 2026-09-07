import React, { useCallback, useEffect, useState } from 'react';
import { Select, Space, Button, Input, App } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { tagsApi, Tag } from '../api/servers';

interface Props {
  value?: string[];
  onChange?: (tagIds: string[]) => void;
}

export default function TagSelect({ value = [], onChange }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [tags, setTags] = useState<Tag[]>([]);
  const [loading, setLoading] = useState(false);
  const [newName, setNewName] = useState('');
  const [newColor, setNewColor] = useState('#1890ff');
  const [showNew, setShowNew] = useState(false);
  const [creating, setCreating] = useState(false);

  const loadTags = useCallback(async () => {
    setLoading(true);
    try {
      const res = await tagsApi.list();
      setTags(res.data || []);
    } catch {
      message.error(t('settings.loadTagsFailed'));
    }
    setLoading(false);
  }, [message, t]);

  useEffect(() => {
    // Defer the initial request so the effect itself does not synchronously
    // trigger state updates during the commit phase.
    const timer = window.setTimeout(() => { void loadTags(); }, 0);
    return () => window.clearTimeout(timer);
  }, [loadTags]);

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setCreating(true);
    try {
      const res = await tagsApi.create(newName.trim(), newColor);
      setTags((prev) => [...prev, res.data]);
      setNewName('');
      setNewColor('#1890ff');
      setShowNew(false);
    } catch {
      message.error(t('settings.tagCreateFailed'));
    } finally {
      setCreating(false);
    }
  };

  return (
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Select
        mode="multiple"
        placeholder={t('common.tags')}
        value={value}
        onChange={onChange}
        loading={loading}
        style={{ width: '100%' }}
        popupRender={(menu) => (
          <>
            {menu}
            <div style={{ padding: 8, borderTop: '1px solid #f0f0f0' }}>
              {showNew ? (
                <Space>
                  <Input
                    size="small"
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                    placeholder={t('settings.tagNamePlaceholder')}
                    onPressEnter={handleCreate}
                  />
                  <input
                    type="color"
                    value={newColor}
                    onChange={(e) => setNewColor(e.target.value)}
                    style={{ width: 28, height: 28, border: 'none', cursor: 'pointer' }}
                  />
                  <Button size="small" type="primary" loading={creating} disabled={!newName.trim()} onClick={handleCreate}>{t('common.add')}</Button>
                </Space>
              ) : (
                <Button type="dashed" size="small" icon={<PlusOutlined />} onClick={() => setShowNew(true)}>
                  {t('settings.createTag')}
                </Button>
              )}
            </div>
          </>
        )}
      >
        {tags.map((tag) => (
          <Select.Option key={tag.id} value={tag.id}>
            <span style={{ color: tag.color }}>●</span> {tag.name}
          </Select.Option>
        ))}
      </Select>
    </Space>
  );
}
