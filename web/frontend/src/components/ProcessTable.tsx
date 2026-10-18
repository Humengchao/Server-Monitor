import React, { useMemo, useState } from 'react';
import { App, Button, Empty, Input, Progress, Result, Segmented, Space, Table, Tooltip, Typography } from 'antd';
import {
  CloseCircleOutlined, ReloadOutlined, SearchOutlined, PauseCircleOutlined, PlayCircleOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { PHONE_QUERY, useMediaQuery } from '../hooks/useMediaQuery';
import { serversApi, ProcessInfo } from '../api/servers';
import { formatBytes, severityColor } from '../utils/format';
import { usePolling } from '../hooks/usePolling';
import { useHostPanel } from '../hooks/useHostPanel';

const { Text } = Typography;

const REFRESH_MS = 5000;
const NO_PROCESSES: ProcessInfo[] = [];

interface Props {
  serverId: string;
  serverType: string;
}

/** "3d 4h", "12m", "45s" — compact enough for a table cell. */
function formatElapsed(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—';
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m`;
  return `${Math.floor(seconds)}s`;
}

function UsageCell({ percent, hue }: { percent: number; hue: 'blue' | 'green' }) {
  const value = Math.min(100, Math.max(0, percent));
  return (
    <div className="mini-bar">
      <Progress percent={value} showInfo={false} size="small" strokeColor={severityColor(value, hue)} railColor="rgba(128, 140, 170, .16)" />
      <span>{percent.toFixed(1)}%</span>
    </div>
  );
}

export default function ProcessTable({ serverId, serverType }: Props) {
  const { t } = useTranslation();
  const phone = useMediaQuery(PHONE_QUERY);
  const { message, modal } = App.useApp();
  const { data, loading, error: loadError, load, scheduleReload, isActive } = useHostPanel(serverId, serversApi.getProcesses);
  const processes = data?.processes || NO_PROCESSES;
  const total = data?.total || 0;
  const error = loadError === null ? null : loadError || t('process.loadFailed');
  const [query, setQuery] = useState('');
  const [live, setLive] = useState(true);
  usePolling(() => load(false), REFRESH_MS, { leading: false, enabled: live });

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return processes;
    return processes.filter((p) =>
      p.command.toLowerCase().includes(needle) ||
      p.user.toLowerCase().includes(needle) ||
      String(p.pid).includes(needle));
  }, [processes, query]);

  const summary = useMemo(() => ({
    cpu: processes.reduce((sum, p) => sum + p.cpu_percent, 0),
    rss: processes.reduce((sum, p) => sum + p.rss_bytes, 0),
  }), [processes]);

  const handleKill = (proc: ProcessInfo, force: boolean) => {
    modal.confirm({
      title: force ? t('process.killTitle') : t('process.terminateTitle'),
      content: (
        <div className="kill-confirm">
          <Text>{force ? t('process.killConfirm') : t('process.terminateConfirm')}</Text>
          <code>{`PID ${proc.pid} · ${proc.command}`}</code>
        </div>
      ),
      okText: force ? t('process.kill') : t('process.terminate'),
      okType: 'danger',
      onOk: async () => {
        if (!isActive()) return;
        try {
          await serversApi.killProcess(serverId, proc.pid, force);
          if (!isActive()) return;
          message.success(t('process.signalSent', { pid: proc.pid }));
          // The host needs a moment to reap the process before it disappears.
          scheduleReload(700);
        } catch (err: unknown) {
          if (!isActive()) return;
          const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
          message.error(detail || t('process.signalFailed'));
        }
      },
    });
  };

  const columns = [
    {
      title: 'PID',
      dataIndex: 'pid',
      key: 'pid',
      width: 84,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.pid - b.pid,
      render: (pid: number) => <Text className="mono-cell">{pid}</Text>,
    },
    ...(serverType === 'windows' ? [] : [{
      title: t('process.user'),
      dataIndex: 'user',
      key: 'user',
      width: 120,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.user.localeCompare(b.user),
      render: (user: string) => <Text type="secondary">{user || '—'}</Text>,
    }]),
    {
      // ps reports CPU averaged over the process's whole lifetime, not an
      // instantaneous reading — a detail worth surfacing, because a process
      // that was busy at startup keeps a high number long after it went idle.
      title: <Tooltip title={t('process.cpuHint')}><span className="col-hint">CPU %</span></Tooltip>,
      dataIndex: 'cpu_percent',
      key: 'cpu',
      width: 128,
      defaultSortOrder: 'descend' as const,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.cpu_percent - b.cpu_percent,
      render: (value: number) => <UsageCell percent={value} hue="blue" />,
    },
    {
      title: t('process.memory'),
      dataIndex: 'mem_percent',
      key: 'mem',
      width: 128,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.mem_percent - b.mem_percent,
      render: (value: number) => <UsageCell percent={value} hue="green" />,
    },
    {
      title: 'RSS',
      dataIndex: 'rss_bytes',
      key: 'rss',
      width: 100,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.rss_bytes - b.rss_bytes,
      render: (value: number) => <Text className="mono-cell">{formatBytes(value, 1)}</Text>,
    },
    ...(serverType === 'windows' ? [] : [{
      title: t('process.state'),
      dataIndex: 'state',
      key: 'state',
      width: 82,
      render: (state: string) => (
        <Tooltip title={t(`process.stateHint.${state.charAt(0)}`, { defaultValue: '' })}>
          <span className="state-chip">{state}</span>
        </Tooltip>
      ),
    }]),
    {
      title: t('process.elapsed'),
      dataIndex: 'elapsed_seconds',
      key: 'elapsed',
      width: 96,
      sorter: (a: ProcessInfo, b: ProcessInfo) => a.elapsed_seconds - b.elapsed_seconds,
      render: (value: number) => <Text type="secondary">{formatElapsed(value)}</Text>,
    },
    {
      title: t('process.command'),
      dataIndex: 'command',
      key: 'command',
      render: (command: string) => (
        <Tooltip title={command} placement="topLeft">
          <span className="command-cell">{command}</span>
        </Tooltip>
      ),
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 96,
      fixed: phone ? undefined : ('right' as const),
      render: (_: unknown, proc: ProcessInfo) => (
        <Space size={0}>
          <Tooltip title={t('process.terminate')}>
            <Button type="text" size="small" aria-label={`${t('process.terminate')} ${proc.command || proc.pid}`} icon={<CloseCircleOutlined />} onClick={() => handleKill(proc, false)} />
          </Tooltip>
          {serverType !== 'windows' && (
            <Tooltip title={t('process.kill')}>
              <Button type="text" size="small" danger aria-label={`${t('process.kill')} ${proc.command || proc.pid}`} onClick={() => handleKill(proc, true)}>-9</Button>
            </Tooltip>
          )}
        </Space>
      ),
    },
  ];

  if (error && processes.length === 0) {
    return (
      <Result
        status="warning"
        title={t('process.unavailable')}
        subTitle={error}
        extra={<Button type="primary" icon={<ReloadOutlined />} onClick={() => load()}>{t('common.refresh')}</Button>}
      />
    );
  }

  return (
    <div className="process-panel">
      <div className="process-toolbar">
        <Space size={16} wrap className="process-summary">
          <span><Text type="secondary">{t('process.count')}</Text><strong>{total}</strong></span>
          <span><Text type="secondary">{t('process.totalCpu')}</Text><strong>{summary.cpu.toFixed(1)}%</strong></span>
          <span><Text type="secondary">{t('process.totalMemory')}</Text><strong>{formatBytes(summary.rss, 1)}</strong></span>
        </Space>
        <Space size={8} wrap>
          <Input
            allowClear
            className="process-search"
            prefix={<SearchOutlined />}
            placeholder={t('process.searchPlaceholder')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <Segmented
            value={live ? 'live' : 'paused'}
            onChange={(value) => setLive(value === 'live')}
            options={[
              { value: 'live', icon: <Tooltip title={t('process.autoRefresh')}><PlayCircleOutlined /></Tooltip> },
              { value: 'paused', icon: <Tooltip title={t('process.pause')}><PauseCircleOutlined /></Tooltip> },
            ]}
          />
          <Button aria-label={t('common.refresh')} icon={<ReloadOutlined />} onClick={() => load()} loading={loading} />
        </Space>
      </div>

      {total > processes.length && (
        <Text type="secondary" className="process-truncated">
          {t('process.truncated', { shown: processes.length, total })}
        </Text>
      )}

      <Table
        className="server-table process-table"
        dataSource={filtered}
        columns={columns}
        rowKey="pid"
        size="small"
        loading={loading && processes.length === 0}
        pagination={false}
        scroll={{ x: 900, y: 460 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('process.noMatches')} /> }}
      />
    </div>
  );
}
