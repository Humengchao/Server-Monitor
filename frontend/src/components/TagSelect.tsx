import React, { useEffect, useState } from 'react';
import { Select, Space, Button, Input, ColorPicker, App } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { tagsApi, Tag } from '../api/servers';

interface Props {
  value?: string[];
  onChange?: (tagIds: string[]) => void;
}

export default function TagSelect({ value = [], onChange }: Props) {
  const { message } = App.useApp();
  const [tags, setTags] = useState<Tag[]>([]);
  const [loading, setLoading] = useState(false);
  const [newName, setNewName] = useState('');
  const [newColor, setNewColor] = useState('#1890ff');
  const [showNew, setShowNew] = useState(false);

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

  const handleCreate = async () => {
    if (!newName.trim()) return;
    try {
      const res = await tagsApi.create(newName.trim(), newColor);
      setTags([...tags, res.data]);
      setNewName('');
      setNewColor('#1890ff');
      setShowNew(false);
    } catch {
      message.error('Failed to create tag');
    }
  };

  return (
    <Space orientation="vertical" style={{ width: '100%' }}>
      <Select
        mode="multiple"
        placeholder="Select tags"
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
                    placeholder="Tag name"
                    onPressEnter={handleCreate}
                  />
                  <input
                    type="color"
                    value={newColor}
                    onChange={(e) => setNewColor(e.target.value)}
                    style={{ width: 28, height: 28, border: 'none', cursor: 'pointer' }}
                  />
                  <Button size="small" type="primary" onClick={handleCreate}>Add</Button>
                </Space>
              ) : (
                <Button type="dashed" size="small" icon={<PlusOutlined />} onClick={() => setShowNew(true)}>
                  Create Tag
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
