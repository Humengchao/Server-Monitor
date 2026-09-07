import React, { useState } from 'react';
import { App, Button, Modal, Segmented, Space, Tag, Typography } from 'antd';
import {
  CloseOutlined, CodeOutlined, DeleteOutlined, DownloadOutlined, TagsOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { serversApi, Server } from '../api/servers';
import TagSelect from './TagSelect';
import BatchExecModal from './BatchExecModal';
import { formatBytes } from '../utils/format';
import { downloadCSV } from '../utils/csv';

const { Text } = Typography;

interface Props {
  selected: Server[];
  total: number;
  onSelectAll: () => void;
  onClear: () => void;
  /** Called after any mutation so the dashboard can refetch. */
  onChanged: () => void;
}

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

const INVENTORY_COLUMNS = [
  'name', 'host', 'port', 'server_type', 'ssh_username', 'cpu_cores',
  'memory_total', 'disk_total', 'public_location', 'expires_at',
  'billing_price', 'billing_currency', 'billing_cycle', 'tags',
] as const;

/**
 * Sticky bar that appears while servers are selected, hosting every batch
 * action. Kept out of Dashboard so the selection UI can grow without making
 * that page any longer.
 */
export default function BatchActionBar({ selected, total, onSelectAll, onClear, onChanged }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const [tagModalOpen, setTagModalOpen] = useState(false);
  const [execModalOpen, setExecModalOpen] = useState(false);
  const [tagIds, setTagIds] = useState<string[]>([]);
  const [tagAction, setTagAction] = useState<'add' | 'remove'>('add');
  const [savingTags, setSavingTags] = useState(false);

  const ids = selected.map((s) => s.id);

  const handleApplyTags = async () => {
    if (tagIds.length === 0) {
      message.warning(t('batch.pickTags'));
      return;
    }
    setSavingTags(true);
    try {
      await serversApi.bulkTags(ids, tagIds, tagAction);
      message.success(tagAction === 'add'
        ? t('batch.tagsAdded', { count: ids.length })
        : t('batch.tagsRemoved', { count: ids.length }));
      setTagModalOpen(false);
      setTagIds([]);
      onChanged();
    } catch (err: unknown) {
      message.error(apiError(err, t('batch.tagsFailed')));
    }
    setSavingTags(false);
  };

  const handleDelete = () => {
    modal.confirm({
      title: t('batch.deleteTitle', { count: selected.length }),
      width: 520,
      content: (
        <div className="batch-confirm">
          <Text type="secondary">{t('batch.deleteBody')}</Text>
          <div className="batch-confirm-targets">
            {selected.map((s) => <Tag key={s.id}>{s.name}</Tag>)}
          </div>
        </div>
      ),
      okText: t('common.delete'),
      okType: 'danger',
      onOk: async () => {
        try {
          const res = await serversApi.bulkDelete(ids);
          message.success(t('batch.deleted', { count: res.data.deleted }));
          onClear();
          onChanged();
        } catch (err: unknown) {
          message.error(apiError(err, t('batch.deleteFailed')));
        }
      },
    });
  };

  // Exports the selected hosts' inventory (no secrets — the API never returns
  // passwords or keys in the first place).
  const handleExport = () => {
    const rows: string[][] = [[...INVENTORY_COLUMNS]];
    for (const server of selected) {
      rows.push(INVENTORY_COLUMNS.map((column) => {
        if (column === 'tags') return (server.tags || []).map((tag) => tag.name).join(' ');
        if (column === 'memory_total' || column === 'disk_total') return formatBytes(server[column]);
        return String(server[column] ?? '');
      }));
    }
    downloadCSV(`servers-${selected.length}.csv`, rows);
    message.success(t('batch.exported', { count: selected.length }));
  };

  return (
    <>
      <div className="batch-bar" role="toolbar" aria-label={t('batch.barLabel')}>
        <div className="batch-bar-count">
          <strong>{selected.length}</strong>
          <span>{t('batch.selected')}</span>
        </div>
        <Space size={4} wrap className="batch-bar-actions">
          {selected.length < total && (
            <Button type="text" size="small" onClick={onSelectAll}>{t('batch.selectAll', { count: total })}</Button>
          )}
          <Button icon={<TagsOutlined />} onClick={() => setTagModalOpen(true)}>{t('batch.tags')}</Button>
          <Button icon={<DownloadOutlined />} onClick={handleExport}>{t('batch.export')}</Button>
          <Button icon={<CodeOutlined />} onClick={() => setExecModalOpen(true)}>{t('batch.exec')}</Button>
          <Button danger icon={<DeleteOutlined />} onClick={handleDelete}>{t('common.delete')}</Button>
          <Button type="text" icon={<CloseOutlined />} onClick={onClear} aria-label={t('batch.clear')} />
        </Space>
      </div>

      <Modal
        title={t('batch.tagsTitle')}
        open={tagModalOpen}
        onCancel={() => setTagModalOpen(false)}
        onOk={handleApplyTags}
        confirmLoading={savingTags}
        okText={tagAction === 'add' ? t('batch.applyAdd') : t('batch.applyRemove')}
        destroyOnHidden
      >
        <div className="batch-tag-body">
          <Text type="secondary">{t('batch.tagsBody', { count: selected.length })}</Text>
          <Segmented
            block
            value={tagAction}
            onChange={(value) => setTagAction(value as 'add' | 'remove')}
            options={[
              { label: t('batch.actionAdd'), value: 'add' },
              { label: t('batch.actionRemove'), value: 'remove' },
            ]}
          />
          <TagSelect value={tagIds} onChange={setTagIds} />
        </div>
      </Modal>

      <BatchExecModal open={execModalOpen} servers={selected} onClose={() => setExecModalOpen(false)} />
    </>
  );
}
