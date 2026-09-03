import React from 'react';
import { Button, Card, Checkbox, Tag, Progress, Typography, Space, Tooltip } from 'antd';
import {
  ArrowUpOutlined,
  ArrowDownOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  HddOutlined,
  ClockCircleOutlined,
  CalendarOutlined,
  ThunderboltOutlined,
  EnvironmentOutlined,
  EditOutlined,
  DeleteOutlined,
  RiseOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { Server } from '../api/servers';
import PlatformIcon from './PlatformIcon';
import { platformClass } from '../utils/platform';
import { AlertEvent } from '../api/alerts';
import { availabilityColor } from '../api/uptime';
import { useNavigate } from 'react-router-dom';
import { formatBytes, formatGB, formatUptime, getExpirationInfo, percentOf, severityColor } from '../utils/format';

const { Text } = Typography;

interface Props {
  server: Server;
  observedAt: number;
  onEdit?: (server: Server) => void;
  onDelete?: (server: Server) => void;
  /** While selecting, a click toggles selection instead of opening the host. */
  selectable?: boolean;
  selected?: boolean;
  onToggleSelect?: (server: Server) => void;
  /** Observed 24h availability; undefined until the census loads. */
  availability?: number;
  /** Rules currently firing on this host. */
  firing?: AlertEvent[];
}

function ServerCard({
  server, observedAt, onEdit, onDelete, selectable, selected, onToggleSelect, availability, firing,
}: Props) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const m = server.latest_metrics;
  const cpuPercent = m ? Math.round(m.cpu_percent) : 0;
  const memPercent = m ? percentOf(m.memory_used, m.memory_total) : 0;
  const diskPercent = m ? percentOf(m.disk_used, server.disk_total) : 0;

  const isOnline = observedAt > 0 && !!m?.recorded_at && observedAt - new Date(m.recorded_at).getTime() < 120000;
  const lang = i18n.language?.startsWith('zh') ? 'zh' : 'en';
  const expInfo = getExpirationInfo(server.expires_at, lang);
  const activate = () => {
    if (selectable) {
      onToggleSelect?.(server);
      return;
    }
    navigate(`/servers/${server.id}`);
  };

  return (
    <Card
      hoverable
      className={`server-card${isOnline ? '' : ' is-offline'}${selectable ? ' is-selectable' : ''}${selected ? ' is-selected' : ''}`}
      onClick={activate}
      tabIndex={0}
      role={selectable ? 'checkbox' : undefined}
      aria-checked={selectable ? !!selected : undefined}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || (selectable && event.key === ' ')) {
          event.preventDefault();
          activate();
        }
      }}
    >
      {selectable && (
        <div className="server-card-select">
          <Checkbox checked={!!selected} />
        </div>
      )}
      <div className="server-card-header">
        <div className={`server-platform ${platformClass(server.server_type)}`}>
          <PlatformIcon serverType={server.server_type} />
          {!!firing?.length && (
            <Tooltip title={firing.map((e) => e.rule_name).filter(Boolean).join('\n') || undefined}>
              <span className="alert-pill">{firing.length}</span>
            </Tooltip>
          )}
        </div>
        <div className="server-identity">
          <Text strong ellipsis title={server.name}>{server.name}</Text>
          <Text type="secondary" ellipsis title={server.host}>{server.host}</Text>
        </div>
        <div className={`status-pill ${isOnline ? 'online' : 'offline'}`}>
          <span />{isOnline ? t('dashboard.online') : t('dashboard.offline')}
        </div>
      </div>

      {!selectable && (onEdit || onDelete) && (
        // Revealed on hover/focus so the resting card stays uncluttered. The
        // click must not bubble, or it would also open the detail page.
        <div className="server-card-actions" onClick={(event) => event.stopPropagation()}>
          {onEdit && (
            <Tooltip title={t('common.edit')}>
              <Button size="small" type="text" icon={<EditOutlined />} onClick={() => onEdit(server)} />
            </Tooltip>
          )}
          {onDelete && (
            <Tooltip title={t('common.delete')}>
              <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => onDelete(server)} />
            </Tooltip>
          )}
        </div>
      )}

      {/* Rendered only when there is something to show: an empty tag row still
          took 24px plus 25px of margin, which is where the odd band of white
          under the header on an untagged card came from. */}
      {(server.public_location || !!server.tags?.length) && (
        <div className="server-tags">
          {server.public_location && (
            <Tag variant="filled" className="location-tag" icon={<EnvironmentOutlined />} title={server.public_location}>
              {server.public_location}
            </Tag>
          )}
          {server.tags?.map((tag) => (
            <Tag key={tag.id} color={tag.color}>{tag.name}</Tag>
          ))}
        </div>
      )}

      {/* Hardware facts are omitted rather than zeroed when the collector has
          never reached the host: a row of "0 核 · 0 GB · 0 GB" described a
          machine with no CPU and no disk instead of one we have not measured.
          The row itself survives because availability and expiry are known
          without ever connecting. */}
      <div className="server-specs">
        {!!server.cpu_cores && (
          <Space size={5}><DashboardOutlined /><Text type="secondary">{server.cpu_cores} {t('card.core')}</Text></Space>
        )}
        {server.memory_total > 0 && (
          <Space size={5}><DatabaseOutlined /><Text type="secondary">{formatGB(server.memory_total)}</Text></Space>
        )}
        {server.disk_total > 0 && (
          <Space size={5}><HddOutlined /><Text type="secondary">{formatGB(server.disk_total)}</Text></Space>
        )}
        {!!m?.uptime_seconds && (
          <Space size={5}><ClockCircleOutlined /><Text type="secondary">{formatUptime(m.uptime_seconds)}</Text></Space>
        )}
        {isOnline && !!m?.latency_ms && (
          <Tooltip title={t('card.latencyHint')}>
            <Space size={5}><ThunderboltOutlined /><Text type="secondary">{m.latency_ms} ms</Text></Space>
          </Tooltip>
        )}
        {typeof availability === 'number' && (
          <Tooltip title={t('uptime.badgeHint')}>
            <Space size={5}>
              <RiseOutlined style={{ color: availabilityColor(availability) }} />
              <Text style={{ color: availabilityColor(availability) }}>{availability.toFixed(2)}%</Text>
            </Space>
          </Tooltip>
        )}
        {expInfo && (
          <Space size={5}><CalendarOutlined style={{ color: expInfo.color }} /><Text style={{ color: expInfo.color }}>{expInfo.text}</Text></Space>
        )}
      </div>

      {m ? (
        <div className="server-metrics">
          <div className="metric-progress">
            <div><Text type="secondary">{t('card.cpu')}</Text><strong>{cpuPercent}%</strong></div>
            <Progress percent={cpuPercent} showInfo={false} strokeColor={severityColor(cpuPercent, 'blue')} railColor="rgba(128, 140, 170, .14)" />
          </div>
          <div className="metric-progress">
            <div><Text type="secondary">{t('card.memory')}</Text><strong>{memPercent}%</strong></div>
            <Progress percent={memPercent} showInfo={false} strokeColor={severityColor(memPercent, 'green')} railColor="rgba(128, 140, 170, .14)" />
          </div>
          <div className="metric-progress">
            <div><Text type="secondary">{t('card.disk')}</Text><strong>{diskPercent}%</strong></div>
            <Progress percent={diskPercent} showInfo={false} strokeColor={severityColor(diskPercent, 'violet')} railColor="rgba(128, 140, 170, .14)" />
          </div>
          <div className="throughput-grid">
            <div>
            <Text type="secondary">{t('card.network')}</Text>
            <Space size={4}>
              <ArrowDownOutlined className="rx" />
              <Text>{formatBytes(m.network_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined className="tx" />
              <Text>{formatBytes(m.network_tx_bytes)}/s</Text>
            </Space>
            </div>
            <div>
            <Text type="secondary">{t('card.disk')}</Text>
            <Space size={4}>
              <ArrowDownOutlined className="rx" />
              <Text>{formatBytes(m.disk_rx_bytes)}/s</Text>
            </Space>
            <Space size={4}>
              <ArrowUpOutlined className="tx" />
              <Text>{formatBytes(m.disk_tx_bytes)}/s</Text>
            </Space>
            </div>
          </div>
        </div>
      ) : (
        <div className="metrics-unavailable">
          <span className="metrics-unavailable-dot" />
          <Text type="secondary">{t('metrics.noData')}</Text>
        </div>
      )}
    </Card>
  );
}

function isOnlineAt(s: Server, observedAt: number): boolean {
  const at = s.latest_metrics?.recorded_at;
  return observedAt > 0 && !!at && observedAt - new Date(at).getTime() < 120000;
}

// Memoized: the dashboard replaces the whole servers array every poll, so we
// only re-render a card when its own displayed data (or online state) changes.
export default React.memo(ServerCard, (prev, next) => {
  const a = prev.server;
  const b = next.server;
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.host === b.host &&
    a.server_type === b.server_type &&
    a.expires_at === b.expires_at &&
    a.cpu_cores === b.cpu_cores &&
    a.memory_total === b.memory_total &&
    a.disk_total === b.disk_total &&
    a.public_location === b.public_location &&
    prev.selectable === next.selectable &&
    prev.selected === next.selected &&
    prev.availability === next.availability &&
    isOnlineAt(a, prev.observedAt) === isOnlineAt(b, next.observedAt) &&
    JSON.stringify(a.tags || []) === JSON.stringify(b.tags || []) &&
    JSON.stringify(a.latest_metrics) === JSON.stringify(b.latest_metrics)
  );
});
