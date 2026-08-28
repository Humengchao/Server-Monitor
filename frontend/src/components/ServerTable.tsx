import React from 'react';
import { Button, Progress, Space, Table, Tag, Tooltip, Typography } from 'antd';
import {
  ArrowDownOutlined, ArrowUpOutlined, CloudServerOutlined, DeleteOutlined, EditOutlined, WindowsOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Server } from '../api/servers';
import { availabilityColor } from '../api/uptime';
import { formatBytes, formatUptime, getExpirationInfo, percentOf, severityColor } from '../utils/format';

const { Text } = Typography;

interface Props {
  servers: Server[];
  observedAt: number;
  loading?: boolean;
  onEdit?: (server: Server) => void;
  onDelete?: (server: Server) => void;
  /** Non-null enables row selection. */
  selectedIds?: string[];
  onSelectionChange?: (ids: string[]) => void;
  /** Observed 24h availability keyed by server ID. */
  availability?: Map<string, number | undefined>;
}

function MiniBar({ percent, hue, label }: { percent: number; hue: 'blue' | 'green' | 'violet'; label?: string }) {
  return (
    <Tooltip title={label}>
      <div className="mini-bar">
        <Progress
          percent={percent}
          showInfo={false}
          size="small"
          strokeColor={severityColor(percent, hue)}
          railColor="rgba(128, 140, 170, .16)"
        />
        <span>{percent}%</span>
      </div>
    </Tooltip>
  );
}

/**
 * Dense alternative to the card grid. Same data, one row per host, so a large
 * fleet fits on a single screen.
 */
export default function ServerTable({
  servers, observedAt, loading, onEdit, onDelete, selectedIds, onSelectionChange, availability,
}: Props) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';

  const isOnline = (server: Server) => {
    const at = server.latest_metrics?.recorded_at;
    return observedAt > 0 && !!at && observedAt - new Date(at).getTime() < 120000;
  };

  const columns = [
    {
      title: t('server.serverName'),
      key: 'name',
      fixed: 'left' as const,
      width: 240,
      render: (_: unknown, server: Server) => (
        <div className="table-identity">
          <span className={`table-platform ${server.server_type === 'windows' ? 'windows' : 'linux'}`}>
            {server.server_type === 'windows' ? <WindowsOutlined /> : <CloudServerOutlined />}
          </span>
          <div>
            <strong>{server.name}</strong>
            <Text type="secondary">{server.host}</Text>
          </div>
        </div>
      ),
    },
    {
      title: t('common.status'),
      key: 'status',
      width: 104,
      render: (_: unknown, server: Server) => (
        <div className={`status-pill ${isOnline(server) ? 'online' : 'offline'}`}>
          <span />{isOnline(server) ? t('dashboard.online') : t('dashboard.offline')}
        </div>
      ),
    },
    {
      title: t('card.cpu'),
      key: 'cpu',
      width: 130,
      render: (_: unknown, server: Server) => (
        <MiniBar percent={Math.round(server.latest_metrics?.cpu_percent || 0)} hue="blue" />
      ),
    },
    {
      title: t('card.memory'),
      key: 'memory',
      width: 130,
      render: (_: unknown, server: Server) => {
        const m = server.latest_metrics;
        return (
          <MiniBar
            percent={m ? percentOf(m.memory_used, m.memory_total) : 0}
            hue="green"
            label={m ? `${formatBytes(m.memory_used)} / ${formatBytes(m.memory_total)}` : undefined}
          />
        );
      },
    },
    {
      title: t('card.disk'),
      key: 'disk',
      width: 130,
      render: (_: unknown, server: Server) => {
        const m = server.latest_metrics;
        return (
          <MiniBar
            percent={m ? percentOf(m.disk_used, server.disk_total) : 0}
            hue="violet"
            label={m ? `${formatBytes(m.disk_used)} / ${formatBytes(server.disk_total)}` : undefined}
          />
        );
      },
    },
    {
      title: t('card.network'),
      key: 'network',
      width: 160,
      render: (_: unknown, server: Server) => {
        const m = server.latest_metrics;
        if (!m) return <Text type="secondary">—</Text>;
        return (
          <Space size={10} className="table-throughput">
            <span><ArrowDownOutlined className="rx" />{formatBytes(m.network_rx_bytes, 1)}/s</span>
            <span><ArrowUpOutlined className="tx" />{formatBytes(m.network_tx_bytes, 1)}/s</span>
          </Space>
        );
      },
    },
    {
      title: t('metrics.uptime'),
      key: 'uptime',
      width: 100,
      render: (_: unknown, server: Server) => (
        <Text type="secondary">{formatUptime(server.latest_metrics?.uptime_seconds || 0)}</Text>
      ),
    },
    ...(availability ? [{
      title: <Tooltip title={t('uptime.badgeHint')}><span className="col-hint">{t('uptime.columnTitle')}</span></Tooltip>,
      key: 'availability',
      width: 104,
      render: (_: unknown, server: Server) => {
        const value = availability.get(server.id);
        if (typeof value !== 'number') return <Text type="secondary">—</Text>;
        return <Text style={{ color: availabilityColor(value) }} className="mono-cell">{value.toFixed(2)}%</Text>;
      },
    }] : []),
    {
      title: t('server.expiresAt'),
      key: 'expires',
      width: 130,
      render: (_: unknown, server: Server) => {
        const info = getExpirationInfo(server.expires_at, lang);
        return info ? <Text style={{ color: info.color }}>{info.text}</Text> : <Text type="secondary">—</Text>;
      },
    },
    {
      title: t('common.tags'),
      key: 'tags',
      width: 180,
      render: (_: unknown, server: Server) => (
        server.tags?.length
          ? <>{server.tags.map((tag) => <Tag key={tag.id} color={tag.color}>{tag.name}</Tag>)}</>
          : <Text type="secondary">—</Text>
      ),
    },
    ...(onEdit || onDelete ? [{
      title: t('common.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right' as const,
      // Row clicks navigate to the detail page, so the buttons must swallow
      // their own click.
      render: (_: unknown, server: Server) => (
        <Space size={0} onClick={(event) => event.stopPropagation()}>
          {onEdit && <Button type="text" size="small" icon={<EditOutlined />} onClick={() => onEdit(server)} />}
          {onDelete && <Button type="text" size="small" danger icon={<DeleteOutlined />} onClick={() => onDelete(server)} />}
        </Space>
      ),
    }] : []),
  ];

  const selecting = !!selectedIds;

  return (
    <Table
      className="server-table"
      dataSource={servers}
      columns={columns}
      rowKey="id"
      loading={loading}
      pagination={false}
      scroll={{ x: selecting ? 1240 : 1180 }}
      rowSelection={selecting ? {
        selectedRowKeys: selectedIds,
        onChange: (keys) => onSelectionChange?.(keys as string[]),
        columnWidth: 46,
        fixed: true,
      } : undefined}
      onRow={(server) => ({
        // While selecting, a row click toggles the checkbox instead of
        // navigating away mid-selection.
        onClick: () => {
          if (!selecting) {
            navigate(`/servers/${server.id}`);
            return;
          }
          const next = selectedIds!.includes(server.id)
            ? selectedIds!.filter((id) => id !== server.id)
            : [...selectedIds!, server.id];
          onSelectionChange?.(next);
        },
        style: { cursor: 'pointer' },
      })}
    />
  );
}
