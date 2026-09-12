import React, { useEffect, useState, useCallback, useRef } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { Collapse, Table, Tag, Button, Space, Typography, Spin, Empty, Drawer, App, Card, Tooltip, Result, Progress } from 'antd';
import {
  ReloadOutlined, CaretRightOutlined, PauseOutlined, SyncOutlined, ArrowRightOutlined, FileTextOutlined, CodeOutlined,
ContainerOutlined, CloudServerOutlined, CheckCircleOutlined, QuestionCircleOutlined, SearchOutlined, FolderOpenOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useTranslation } from 'react-i18next';
import { serversApi, Server, DockerContainer } from '../api/servers';
import { formatBytes, severityColor } from '../utils/format';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { usePolling } from '../hooks/usePolling';
import { createRequestQueue } from '../utils/requestQueue';
const queueStats = createRequestQueue(4);
function mergeStats(containers: DockerContainer[], stats: DockerContainer[]) {
  const readings = new Map(stats.map(item => [item.id, item]));
  return containers.map(item => { const reading = readings.get(item.id); return reading && reading.state === item.state ? { ...reading, id: item.id, name: item.name, image: item.image, state: item.state, status: item.status, ports: item.ports, created: item.created } : item; });
}
import '@xterm/xterm/css/xterm.css';

const { Title, Text } = Typography;

interface ServerDocker {
  server: Server;
  version: string;
  containers: DockerContainer[];
  loading: boolean;
  loaded: boolean;
  error: boolean;
  statsLoading?: boolean;
  statsError?: boolean;
}

const stateColor: Record<string, string> = {
  running: 'green',
  exited: 'red',
  paused: 'orange',
  restarting: 'blue',
  created: 'default',
  removing: 'warning',
  dead: 'error',
};

type Translate = (key: string, options?: Record<string, unknown>) => string;

function finiteNumber(value: number | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function ResourceBar({
  value,
  label,
  hue,
  available = true,
  unavailableText,
}: {
  value: number | undefined;
  label: string;
  hue: 'blue' | 'green';
  available?: boolean;
  unavailableText?: string;
}) {
  const numeric = finiteNumber(value);
  if (!available || numeric === null) return <span className="docker-metric-unavailable" title={unavailableText}>—</span>;
  const display = `${numeric.toFixed(1)}%`;
  // CPU can exceed 100% on a multicore host. Keep the label truthful while
  // clamping only the visual track to the progress component's range.
  const track = Math.min(100, Math.max(0, numeric));
  return (
    <div className="docker-resource-bar" title={`${label}: ${display}`}>
      <Progress percent={track} showInfo={false} size="small" strokeColor={severityColor(track, hue)} railColor="rgba(128, 140, 170, .16)" />
      <span>{display}</span>
    </div>
  );
}

function MemoryMetric({ container, t }: { container: DockerContainer; t: Translate }) {
  if (!container.stats_available) return <span className="docker-metric-unavailable" title={t('docker.statsUnavailable')}>—</span>;
  const usage = finiteNumber(container.memory_usage);
  const limit = finiteNumber(container.memory_limit);
  const percent = finiteNumber(container.memory_percent);
  if (usage === null && percent === null) return <span className="docker-metric-unavailable" title={t('docker.statsUnavailable')}>—</span>;
  const detail = usage !== null
    ? (limit !== null && limit > 0 ? `${formatBytes(usage, 1)} / ${formatBytes(limit, 1)}` : formatBytes(usage, 1))
    : '—';
  return (
    <div className="docker-resource-detail" title={t('docker.memoryHint')}>
      <strong>{detail}</strong>
      {percent !== null && <small>{percent.toFixed(1)}%</small>}
    </div>
  );
}

function DiskMetric({ container, t }: { container: DockerContainer; t: Translate }) {
  // Older backends did not send disk_available. Treat a present disk_usage as
  // usable for compatibility, while newer responses can explicitly mark it
  // unavailable when `docker ps --size` was not supported.
  const usage = finiteNumber(container.disk_usage);
  if (usage === null || container.disk_available === false) return <span className="docker-metric-unavailable" title={t('docker.diskUnavailable')}>—</span>;
  const virtual = finiteNumber(container.disk_virtual_usage);
  const read = finiteNumber(container.disk_read_bytes);
  const write = finiteNumber(container.disk_write_bytes);
  return (
    <div className="docker-resource-detail docker-disk-detail" title={t('docker.diskHint')}>
      <strong>{formatBytes(usage, 1)}</strong>
      {virtual !== null && virtual > usage && <small>≈ {formatBytes(virtual, 1)}</small>}
      {container.block_io_available && (read !== null || write !== null) && (
        <small>{t('docker.blockIO')}: {formatBytes(read || 0, 1)} ↓ / {formatBytes(write || 0, 1)} ↑</small>
      )}
    </div>
  );
}

function containerResourceColumns(t: Translate): ColumnsType<DockerContainer> {
  return [
    {
      title: <Tooltip title={t('docker.cpuHint')}><span className="col-hint">{t('docker.cpu')}</span></Tooltip>,
      key: 'cpu',
      width: 138,
      sorter: (a: DockerContainer, b: DockerContainer) => (a.cpu_percent || 0) - (b.cpu_percent || 0),
      render: (_: unknown, record: DockerContainer) => (
        <ResourceBar value={record.cpu_percent} label={t('docker.cpu')} hue="blue" available={record.stats_available} unavailableText={t('docker.statsUnavailable')} />
      ),
    },
    {
      title: <Tooltip title={t('docker.memoryHint')}><span className="col-hint">{t('docker.memory')}</span></Tooltip>,
      key: 'memory',
      width: 180,
      sorter: (a: DockerContainer, b: DockerContainer) => (a.memory_usage || 0) - (b.memory_usage || 0),
      render: (_: unknown, record: DockerContainer) => <MemoryMetric container={record} t={t} />,
    },
    {
      title: <Tooltip title={t('docker.diskHint')}><span className="col-hint">{t('docker.disk')}</span></Tooltip>,
      key: 'disk',
      width: 150,
      sorter: (a: DockerContainer, b: DockerContainer) => (a.disk_usage || 0) - (b.disk_usage || 0),
      render: (_: unknown, record: DockerContainer) => <DiskMetric container={record} t={t} />,
    },
  ];
}

function LogsModal({ serverId, containerId, containerName, onClose }: {
  serverId: string;
  containerId: string;
  containerName: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [logs, setLogs] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const ac = new AbortController();
    const timer = window.setTimeout(() => {
      setLoading(true);
      setLogs('');
      void serversApi.getContainerLogs(serverId, containerId, 500, ac.signal)
        .then((r) => setLogs(r.data.logs || t('docker.empty')))
        .catch(() => {
          if (!ac.signal.aborted) setLogs(t('docker.loadLogsFailed'));
        })
        .finally(() => {
          if (!ac.signal.aborted) setLoading(false);
        });
    }, 0);
    return () => { window.clearTimeout(timer); ac.abort(); };
  }, [serverId, containerId, t]);

  return (
    <Drawer
      title={t('docker.logsTitle', { name: containerName })}
      open
      onClose={onClose}
      maskClosable={false}
      placement="right"
      rootStyle={{ position: 'fixed' }}
      styles={{ body: { padding: 0, background: '#1e1e2e' }, wrapper: { width: '80vw' } }}
    >
      {loading ? (
        <div style={{ textAlign: 'center', padding: 40 }}><Spin /></div>
      ) : (
        <pre style={{
          color: '#cdd6f4',
          padding: 16,
          height: '100%',
          overflow: 'auto',
          fontSize: 13,
          fontFamily: 'Menlo, Monaco, "Courier New", monospace',
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
          margin: 0,
        }}>
          {logs}
        </pre>
      )}
    </Drawer>
  );
}

function ExecDrawer({ serverId, containerId, containerName, open, onClose }: {
  serverId: string;
  containerId: string;
  containerName: string;
  open: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const termRef = useRef<HTMLDivElement>(null);
  const [connected, setConnected] = useState(false);
  // Read t through a ref inside socket callbacks so a language switch doesn't
  // tear down and restart the exec session just to retranslate messages.
  const tRef = useRef(t);
  useEffect(() => { tRef.current = t; }, [t]);

  useEffect(() => {
    if (!open || !containerId || !termRef.current) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: { background: '#1e1e2e', foreground: '#cdd6f4' },
      scrollback: 5000,
    });

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(termRef.current);

    // Use ResizeObserver for robust terminal sizing (replaces fragile 300ms timeout)
    const ro = new ResizeObserver(() => {
      try { fitAddon.fit(); } catch { /* terminal may be disposed during unmount */ }
    });
    ro.observe(termRef.current!);

    const token = localStorage.getItem('token');
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.host}/api/ws/servers/${serverId}/docker/containers/${containerId}/exec`;

    // Token travels as a subprotocol ("bearer, <jwt>") to stay out of logs.
    const ws = new WebSocket(wsUrl, token ? ['bearer', token] : undefined);

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send('\x01' + JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
      }
    };

    ws.onopen = () => {
      setConnected(true);
      sendResize(); // sync PTY size with the fitted terminal
      terminal.focus();
    };

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        terminal.write(ev.data);
      } else if (ev.data instanceof Blob) {
        ev.data.text().then((text) => terminal.write(text));
      }
    };

    ws.onclose = () => {
      setConnected(false);
      terminal.write(`\r\n\x1b[31m${tRef.current('docker.execDisconnected')}\x1b[0m\r\n`);
    };

    ws.onerror = () => {
      terminal.write(`\r\n\x1b[31m${tRef.current('docker.execConnError')}\x1b[0m\r\n`);
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    // Whenever the fit addon changes the terminal dimensions, tell the backend
    terminal.onResize(() => sendResize());

    const handleResize = () => { try { fitAddon.fit(); } catch { /* terminal may be disposed during unmount */ } };
    window.addEventListener('resize', handleResize);

    return () => {
      ro.disconnect();
      window.removeEventListener('resize', handleResize);
      ws.close();
      terminal.dispose();
    };
  }, [open, serverId, containerId]);

  return (
    <Drawer
      title={
        <Space>
          <CodeOutlined />
          <span>{t('docker.execTitle', { name: containerName })}</span>
          <Tag color={connected ? 'green' : 'red'}>{connected ? t('common.connected') : t('common.disconnected')}</Tag>
        </Space>
      }
      open={open}
      onClose={onClose}
      maskClosable={false}
      placement="right"
      rootStyle={{ position: 'fixed' }}
      styles={{ body: { padding: 0, background: '#1e1e2e' }, wrapper: { width: '80vw' } }}
    >
      <div ref={termRef} style={{ width: '100%', height: 'calc(100vh - 110px)' }} />
    </Drawer>
  );
}

export function ServerDockerPanel({ serverId, version }: { serverId: string; version?: string }) {
  const navigate = useNavigate();
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const [containers, setContainers] = useState<DockerContainer[]>([]);
  const [loading, setLoading] = useState(true);
  const inventoryRequest = useRef<AbortController | null>(null);
  const statsRequest = useRef<AbortController | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  const [statsError, setStatsError] = useState(false);
  const [logsTarget, setLogsTarget] = useState<{ containerId: string; containerName: string } | null>(null);
  const [execTarget, setExecTarget] = useState<{ containerId: string; containerName: string } | null>(null);
  // `${containerId}:${action}` of the action currently in flight, so the row's
  // own button spins and repeat clicks are swallowed.
  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const loadContainers = useCallback(async (showLoading = true) => {
    if (inventoryRequest.current) return;
    const controller = new AbortController();
    inventoryRequest.current = controller;
    if (showLoading) setLoading(true);
    try {
      const response = await serversApi.getContainers(serverId, controller.signal);
      if (controller.signal.aborted) return;
      setContainers(previous => mergeStats(response.data || [], previous));
      setStatsLoading(true);
      setStatsError(false);
      statsRequest.current?.abort();
      const statistics = new AbortController();
      statsRequest.current = statistics;
      void queueStats(() => serversApi.getContainerStats(serverId, statistics.signal)).then(response => {
        if (!statistics.signal.aborted) setContainers(current => mergeStats(current, response.data || []));
      }).catch(() => { if (!statistics.signal.aborted) setStatsError(true); })
        .finally(() => { if (statsRequest.current === statistics) { statsRequest.current = null; setStatsLoading(false); } });
    } catch {
      if (!controller.signal.aborted && showLoading) message.error(t('docker.loadFailed'));
    } finally {
      if (inventoryRequest.current === controller) { inventoryRequest.current = null; setLoading(false); }
    }
  }, [message, serverId, t]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setContainers([]);
      void loadContainers();
    }, 0);
    return () => {
      window.clearTimeout(timer);
      inventoryRequest.current?.abort();
      inventoryRequest.current = null;
      statsRequest.current?.abort();
      statsRequest.current = null;
    };
  }, [loadContainers]);

  // Container resource values are live readings. Refresh only while this
  // detail panel is mounted, so the hidden tabs do not create SSH traffic.
  usePolling(() => loadContainers(false), 15000, { leading: false });

  const runAction = async (containerId: string, action: 'start' | 'stop' | 'restart') => {
    setActionBusy(`${containerId}:${action}`);
    try {
      await serversApi.containerAction(serverId, containerId, action);
      message.success(t('docker.actionSuccess', { action: t(`docker.${action}`) }));
      inventoryRequest.current?.abort();
      inventoryRequest.current = null;
      statsRequest.current?.abort();
      await loadContainers();
    } catch {
      message.error(t('docker.actionFailed', { action: t(`docker.${action}`) }));
    } finally {
      setActionBusy(null);
    }
  };

  // start is the only verb that cannot interrupt something already serving
  // traffic, so it is the only one that does not ask first.
  const handleAction = (containerId: string, action: 'start' | 'stop' | 'restart') => {
    if (action === 'start') {
      void runAction(containerId, action);
      return;
    }
    modal.confirm({
      title: t('docker.confirmTitle', { action: t(`docker.${action}`) }),
      content: t('docker.confirmBody'),
      okText: t(`docker.${action}`),
      okType: 'danger',
      onOk: () => runAction(containerId, action),
    });
  };

  const columns: ColumnsType<DockerContainer> = [
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      render: (value: string) => <Text strong>{value}</Text>,
    },
    {
      title: t('docker.image'),
      dataIndex: 'image',
      key: 'image',
      ellipsis: true,
    },
    {
      title: t('docker.state'),
      dataIndex: 'state',
      key: 'state',
      width: 110,
      render: (value: string) => <Tag color={stateColor[value] || 'default'}>{t(`docker.stateValue.${value}`, { defaultValue: value })}</Tag>,
    },
    ...containerResourceColumns(t),
    {
      title: t('common.status'),
      dataIndex: 'status',
      key: 'status',
      ellipsis: true,
    },
    {
      title: t('docker.ports'),
      dataIndex: 'ports',
      key: 'ports',
      ellipsis: true,
      width: 200,
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 320,
      render: (_, record) => (
        <Space size="small" wrap>
          {record.state !== 'running' ? (
            <Button size="small" type="primary" icon={<CaretRightOutlined />} loading={actionBusy === `${record.id}:start`} onClick={() => handleAction(record.id, 'start')}>{t('docker.start')}</Button>
          ) : (
            <>
              <Button size="small" icon={<PauseOutlined />} loading={actionBusy === `${record.id}:stop`} onClick={() => handleAction(record.id, 'stop')}>{t('docker.stop')}</Button>
              <Button size="small" icon={<SyncOutlined />} loading={actionBusy === `${record.id}:restart`} onClick={() => handleAction(record.id, 'restart')}>{t('docker.restart')}</Button>
            </>
          )}
          <Button size="small" icon={<FileTextOutlined />} onClick={() => setLogsTarget({ containerId: record.id, containerName: record.name })}>{t('docker.logs')}</Button>
          <Button size="small" icon={<CodeOutlined />} onClick={() => setExecTarget({ containerId: record.id, containerName: record.name })}>{t('docker.exec')}</Button>
          <Button size="small" icon={<FolderOpenOutlined />} disabled={record.state !== 'running'} onClick={() => navigate('/files?server=' + serverId + '&container=' + record.id)}>{t('nav.files')}</Button>
        </Space>
      ),
    },
  ];

  return (
    <Card
      title={(
        <Space wrap>
          {version && <Tag color="blue">{t('docker.version', { version })}</Tag>}
          <Text type="secondary">{t('docker.containers', { count: containers.length })}</Text>
        </Space>
      )}
      extra={<Space>{statsLoading && <Tag>{t('docker.statsLoading')}</Tag>}{statsError && <Tag color="warning">{t('docker.statsError')}</Tag>}<Button icon={<ReloadOutlined />} onClick={() => { void loadContainers(); }}>{t('common.refresh')}</Button></Space>}
    >
      <Table
        className="server-table"
        rowKey="id"
        columns={columns}
        dataSource={containers}
        loading={loading}
        pagination={false}
        size="small"
        scroll={{ x: 1500 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('docker.noContainers')} /> }}
      />

      {logsTarget && (
        <LogsModal
          serverId={serverId}
          containerId={logsTarget.containerId}
          containerName={logsTarget.containerName}
          onClose={() => setLogsTarget(null)}
        />
      )}

      <ExecDrawer
        serverId={serverId}
        containerId={execTarget?.containerId || ''}
        containerName={execTarget?.containerName || ''}
        open={!!execTarget}
        onClose={() => setExecTarget(null)}
      />
    </Card>
  );
}

export default function Docker() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const [servers, setServers] = useState<ServerDocker[]>([]);
  // Servers whose stored flag says "no Docker". Kept rather than filtered out:
  // the flag can be lost to a transient probe failure, and a server that
  // silently vanishes from this page is exactly the symptom that hides the bug.
  const [undetected, setUndetected] = useState<Server[]>([]);
  const [redetecting, setRedetecting] = useState<string | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [activeKeys, setActiveKeys] = useState<string[]>([]);
  const activeKeysRef = useRef<string[]>([]);
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [logsTarget, setLogsTarget] = useState<{ serverId: string; containerId: string; containerName: string } | null>(null);
  const [execTarget, setExecTarget] = useState<{ serverId: string; containerId: string; containerName: string } | null>(null);
  // `${containerId}:${action}` of the action currently in flight, so the row's
  // own button spins and repeat clicks are swallowed.
  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const expandServerId = searchParams.get('server');

  const requestsRef = useRef(new Map<string, AbortController>());
  const loadGenerationRef = useRef(0);

  const loadContainers = useCallback(async (serverId: string, showLoading = true) => {
    if (requestsRef.current.has(serverId)) return;
    const controller = new AbortController();
    requestsRef.current.set(serverId, controller);
    const generation = loadGenerationRef.current;
    if (showLoading) {
      setServers((previous) => previous.map((item) => item.server.id === serverId ? { ...item, loading: true, error: false } : item));
    }
    try {
      const response = await serversApi.getContainers(serverId, controller.signal);
      if (generation !== loadGenerationRef.current || controller.signal.aborted) return;
      setServers((previous) => previous.map((item) => item.server.id === serverId ? { ...item, containers: mergeStats(response.data || [], item.containers), loading: false, loaded: true, error: false, statsLoading: true, statsError: false } : item));
      const statsController = new AbortController();
      requestsRef.current.get('stats:' + serverId)?.abort();
      requestsRef.current.set('stats:' + serverId, statsController);
      void queueStats(() => serversApi.getContainerStats(serverId, statsController.signal)).then((stats) => {
        if (generation !== loadGenerationRef.current || statsController.signal.aborted) return;
        setServers(previous => previous.map(item => item.server.id === serverId ? { ...item, containers: mergeStats(item.containers, stats.data || []), statsLoading: false } : item));
      }).catch(() => {
        if (generation !== loadGenerationRef.current || statsController.signal.aborted) return;
        setServers(previous => previous.map(item => item.server.id === serverId ? { ...item, statsLoading: false, statsError: true } : item));
      }).finally(() => { if (requestsRef.current.get('stats:' + serverId) === statsController) requestsRef.current.delete('stats:' + serverId); });
    } catch {
      if (generation !== loadGenerationRef.current || controller.signal.aborted) return;
      setServers((previous) => previous.map((item) => item.server.id === serverId ? { ...item, loading: false, error: true } : item));
    } finally {
      if (requestsRef.current.get(serverId) === controller) requestsRef.current.delete(serverId);
    }
  }, []);

  const loadServers = useCallback(async () => {
    const generation = ++loadGenerationRef.current;
    for (const controller of requestsRef.current.values()) controller.abort();
    requestsRef.current.clear();
    setInitialLoading(true);
    setLoadError(false);
    try {
      const response = await serversApi.list();
      if (generation !== loadGenerationRef.current) return;
      const allServers = response.data || [];
      const withDocker: ServerDocker[] = allServers.filter((server) => server.has_docker).map((server) => ({
        server, version: server.docker_version || '', containers: [], loading: true, loaded: false, error: false,
      }));
      setServers(withDocker);
      setUndetected(allServers.filter((server) => !server.has_docker));
      const validIDs = new Set(withDocker.map((item) => item.server.id));
      const keys = (expandServerId ? [expandServerId] : activeKeysRef.current).filter((key) => validIDs.has(key));
      activeKeysRef.current = keys;
      setActiveKeys(keys);
      const scheduled = [...withDocker].sort((first, second) => Number(keys.includes(second.server.id)) - Number(keys.includes(first.server.id)));
      let nextIndex = 0;
      void Promise.all(Array.from({ length: Math.min(4, withDocker.length) }, async () => {
        while (generation === loadGenerationRef.current && nextIndex < withDocker.length) {
          const server = scheduled[nextIndex++].server;
          await loadContainers(server.id);
        }
      }));
    } catch {
      if (generation === loadGenerationRef.current) {
        setLoadError(true);
        message.error(t('docker.loadFailed'));
      }
    } finally {
      if (generation === loadGenerationRef.current) setInitialLoading(false);
    }
  }, [expandServerId, loadContainers, message, t]);

  const runAction = async (serverId: string, containerId: string, action: 'start' | 'stop' | 'restart') => {
    setActionBusy(`${containerId}:${action}`);
    try {
      await serversApi.containerAction(serverId, containerId, action);
      message.success(t('docker.actionSuccess', { action: t(`docker.${action}`) }));
      requestsRef.current.get(serverId)?.abort();
      requestsRef.current.delete(serverId);
      requestsRef.current.get('stats:' + serverId)?.abort();
      void loadContainers(serverId);
    } catch {
      message.error(t('docker.actionFailed', { action: t(`docker.${action}`) }));
    } finally {
      setActionBusy(null);
    }
  };

  // start is the only verb that cannot interrupt something already serving
  // traffic, so it is the only one that does not ask first.
  const handleAction = (serverId: string, containerId: string, action: 'start' | 'stop' | 'restart') => {
    if (action === 'start') {
      void runAction(serverId, containerId, action);
      return;
    }
    modal.confirm({
      title: t('docker.confirmTitle', { action: t(`docker.${action}`) }),
      content: t('docker.confirmBody'),
      okText: t(`docker.${action}`),
      okType: 'danger',
      onOk: () => runAction(serverId, containerId, action),
    });
  };

  const cancelLoads = useCallback(() => {
    loadGenerationRef.current++;
    for (const controller of requestsRef.current.values()) controller.abort();
    requestsRef.current.clear();
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadServers(); }, 0);
    return () => { window.clearTimeout(timer); cancelLoads(); };
  }, [loadServers, cancelLoads]);

  // Refresh resource readings only for expanded hosts. The request guard in
  // loadContainers prevents a slow SSH call from overlapping the next tick.
  usePolling(
    () => Promise.all(activeKeys.map((key) => loadContainers(key, false))).then(() => undefined),
    15000,
    { leading: false, enabled: activeKeys.length > 0 && !servers.some((item) => item.loading) },
  );

  const getColumns = (serverId: string): ColumnsType<DockerContainer> => [
    {
      title: t('common.name'),
      dataIndex: 'name',
      key: 'name',
      render: (v: string) => <Text strong>{v}</Text>,
    },
    {
      title: t('docker.image'),
      dataIndex: 'image',
      key: 'image',
      ellipsis: true,
    },
    {
      title: t('docker.state'),
      dataIndex: 'state',
      key: 'state',
      width: 110,
      render: (v: string) => <Tag color={stateColor[v] || 'default'}>{t(`docker.stateValue.${v}`, { defaultValue: v })}</Tag>,
    },
    ...containerResourceColumns(t),
    {
      title: t('common.status'),
      dataIndex: 'status',
      key: 'status',
      ellipsis: true,
    },
    {
      title: t('docker.ports'),
      dataIndex: 'ports',
      key: 'ports',
      ellipsis: true,
      width: 200,
    },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 320,
      render: (_, record) => (
        <Space size="small" wrap>
          {record.state !== 'running' ? (
            <Button size="small" type="primary" icon={<CaretRightOutlined />} loading={actionBusy === `${record.id}:start`} onClick={() => handleAction(serverId, record.id, 'start')}>{t('docker.start')}</Button>
          ) : (
            <>
              <Button size="small" icon={<PauseOutlined />} loading={actionBusy === `${record.id}:stop`} onClick={() => handleAction(serverId, record.id, 'stop')}>{t('docker.stop')}</Button>
              <Button size="small" icon={<SyncOutlined />} loading={actionBusy === `${record.id}:restart`} onClick={() => handleAction(serverId, record.id, 'restart')}>{t('docker.restart')}</Button>
            </>
          )}
          <Button size="small" icon={<FileTextOutlined />} onClick={() => setLogsTarget({ serverId, containerId: record.id, containerName: record.name })}>{t('docker.logs')}</Button>
          <Button size="small" icon={<CodeOutlined />} onClick={() => setExecTarget({ serverId, containerId: record.id, containerName: record.name })}>{t('docker.exec')}</Button>
          <Button size="small" icon={<FolderOpenOutlined />} disabled={record.state !== 'running'} onClick={() => navigate('/files?server=' + serverId + '&container=' + record.id)}>{t('nav.files')}</Button>
        </Space>
      ),
    },
  ];

  const redetect = async (server: Server) => {
    setRedetecting(server.id);
    try {
      const res = await serversApi.redetectDocker(server.id);
      if (res.data.installed) {
        message.success(t('docker.redetectFound', { name: server.name, version: res.data.version || '' }));
        loadServers();
      } else if (res.data.refreshed) {
        message.info(t('docker.redetectAbsent', { name: server.name }));
      } else {
        // The host could not be asked at all; the stored answer was left alone.
        message.warning(t('docker.redetectUnknown', { name: server.name }));
      }
    } catch (err: unknown) {
      const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      message.error(detail || t('docker.redetectFailed'));
    } finally {
      setRedetecting(null);
    }
  };

  const totals = {
    containers: servers.reduce((sum, sd) => sum + sd.containers.length, 0),
    running: servers.reduce((sum, sd) => sum + sd.containers.filter((c) => c.state === 'running').length, 0),
  };
  const loadedHosts = servers.filter((sd) => sd.loaded).length;
  const allHostsLoaded = loadedHosts === servers.length;
  const summaryHint = allHostsLoaded ? undefined : t('docker.summaryPartial', { loaded: loadedHosts, total: servers.length });
  const summaryValue = (value: number) => loadedHosts > 0 ? String(value) : '—';

  const collapseItems = servers.map((sd) => ({
    key: sd.server.id,
    label: (
      <div className="docker-host-head">
        <span className="server-platform linux docker-host-icon"><ContainerOutlined /></span>
        <div className="docker-host-identity">
          <strong>{sd.server.name}</strong>
          <span>{sd.server.host}{sd.version ? ` · Docker ${sd.version}` : ''}</span>
        </div>
        <div className="docker-host-counts">
          {sd.error && <Tag color="error">{t('docker.loadFailed')}</Tag>}
          {sd.statsLoading && <Tag>{t('docker.statsLoading')}</Tag>}
          {sd.statsError && <Tag color="warning">{t('docker.statsFailed')}</Tag>}
          {sd.loaded ? (
            <>
              <span className="docker-count running">
                <i />{t('docker.runningCount', { count: sd.containers.filter((c) => c.state === 'running').length })}
              </span>
              {sd.containers.some((c) => c.state !== 'running') && (
                <span className="docker-count stopped">
                  <i />{t('docker.stoppedCount', { count: sd.containers.filter((c) => c.state !== 'running').length })}
                </span>
              )}
            </>
          ) : sd.loading ? <Spin size="small" /> : null}
        </div>
      </div>
    ),
    extra: (
      <Button
        size="small"
        icon={<ArrowRightOutlined />}
        onClick={(e) => {
          e.stopPropagation();
          navigate(`/servers/${sd.server.id}`);
        }}
      >
        {t('docker.serverDetail')}
      </Button>
    ),
    children: (
      <>
      {sd.error && <Result status="warning" title={t('docker.loadFailed')} extra={<Button onClick={() => { void loadContainers(sd.server.id); }}>{t('common.refresh')}</Button>} />}
      <Table
        className="server-table"
        rowKey="id"
        columns={getColumns(sd.server.id)}
        dataSource={sd.containers}
        loading={sd.loading}
        pagination={false}
        size="small"
        scroll={{ x: 1500 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('docker.noContainers')} /> }}
      />
      </>
    ),
  }));

  const handleCollapseChange = (keys: string | string[]) => {
    const keyArr = Array.isArray(keys) ? keys : [keys];
    activeKeysRef.current = keyArr;
    setActiveKeys(keyArr);
    for (const key of keyArr) {
      const sd = servers.find((s) => s.server.id === key);
      // loaded distinguishes "fetched, genuinely empty" from "never fetched",
      // so servers without containers aren't re-queried on every toggle.
      if (sd && !sd.loaded && !sd.loading) {
        loadContainers(key);
      }
    }
  };

  return (
    <div>
      <div className="page-heading">
        <div>
          <Text className="eyebrow">{t('docker.eyebrow')}</Text>
          <Title level={2}>{t('docker.title')}</Title>
          <Text type="secondary">{t('docker.subtitle')}</Text>
        </div>
        <Space className="page-actions">
          <Button icon={<ReloadOutlined />} onClick={loadServers} loading={initialLoading}>{t('common.refresh')}</Button>
        </Space>
      </div>

      <div className="overview-grid">
        <Card className="overview-card overview-card-primary" variant="borderless">
          <div className="overview-icon"><CloudServerOutlined /></div>
          <div><Text type="secondary">{t('docker.hostsWithDocker')}</Text><strong>{servers.length}</strong></div>
        </Card>
        <Card className="overview-card overview-card-accent" variant="borderless">
          <div className="overview-icon"><ContainerOutlined /></div>
          <div><Text type="secondary">{t('docker.totalContainers')}</Text><strong title={summaryHint}>{summaryValue(totals.containers)}</strong></div>
        </Card>
        <Card className="overview-card overview-card-success" variant="borderless">
          <div className="overview-icon"><CheckCircleOutlined /></div>
          <div><Text type="secondary">{t('docker.runningContainers')}</Text><strong title={summaryHint}>{summaryValue(totals.running)}</strong></div>
        </Card>
        <Card className={`overview-card ${undetected.length ? 'overview-card-amber' : 'overview-card-muted'}`} variant="borderless">
          <div className="overview-icon"><QuestionCircleOutlined /></div>
          <div><Text type="secondary">{t('docker.hostsWithout')}</Text><strong>{undetected.length}</strong></div>
        </Card>
      </div>

      {initialLoading ? (
        <div style={{ textAlign: 'center', padding: 80 }}><Spin size="large" /></div>
      ) : loadError ? (
        <Result
          status="error"
          title={t('docker.loadFailed')}
          extra={<Button type="primary" icon={<ReloadOutlined />} onClick={loadServers}>{t('common.refresh')}</Button>}
        />
      ) : (
        <>
          {servers.length === 0 ? (
            <Card className="panel-card">
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('docker.noServers')} />
            </Card>
          ) : (
            <Collapse
              className="docker-hosts"
              activeKey={activeKeys}
              onChange={handleCollapseChange}
              items={collapseItems}
            />
          )}

          {/* Servers the collector has not seen Docker on. Listed rather than
              hidden so a flag lost to a slow poll can be recovered here, on
              demand, instead of waiting for a background poll to happen to
              succeed. */}
          {undetected.length > 0 && (
            <div className="docker-undetected">
              <div className="section-heading">
                <div>
                  <Title level={4}>{t('docker.undetectedTitle')}</Title>
                  <Text type="secondary">{t('docker.undetectedHint')}</Text>
                </div>
              </div>
              <div className="docker-undetected-list">
                {undetected.map((server) => (
                  <div key={server.id} className="docker-undetected-row">
                    <span className={`server-platform ${server.server_type === 'windows' ? 'windows' : 'linux'}`}><CloudServerOutlined /></span>
                    <div className="docker-host-identity">
                      <strong>{server.name}</strong>
                      <span>{server.host}</span>
                    </div>
                    <Space size={6}>
                      <Tooltip title={t('docker.redetectHint')}>
                        <Button size="small" icon={<SearchOutlined />}
                          loading={redetecting === server.id}
                          onClick={() => redetect(server)}>
                          {t('docker.redetect')}
                        </Button>
                      </Tooltip>
                      <Button size="small" type="text" aria-label={t('docker.serverDetail')} icon={<ArrowRightOutlined />} onClick={() => navigate(`/servers/${server.id}`)} />
                    </Space>
                  </div>
                ))}
              </div>
            </div>
          )}
        </>
      )}

      {logsTarget && (
        <LogsModal
          serverId={logsTarget.serverId}
          containerId={logsTarget.containerId}
          containerName={logsTarget.containerName}
          onClose={() => setLogsTarget(null)}
        />
      )}

      <ExecDrawer
        serverId={execTarget?.serverId || ''}
        containerId={execTarget?.containerId || ''}
        containerName={execTarget?.containerName || ''}
        open={!!execTarget}
        onClose={() => setExecTarget(null)}
      />
    </div>
  );
}
