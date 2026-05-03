import React, { useEffect, useState, useCallback, useRef } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { Collapse, Table, Tag, Button, Space, Typography, Spin, message, Empty, Drawer } from 'antd';
import { ReloadOutlined, CaretRightOutlined, PauseOutlined, SyncOutlined, ArrowRightOutlined, FileTextOutlined, CodeOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { serversApi, Server, DockerContainer } from '../api/servers';
import { Terminal } from 'xterm';
import { FitAddon } from '@xterm/addon-fit';
import 'xterm/css/xterm.css';

const { Title, Text } = Typography;

interface ServerDocker {
  server: Server;
  version: string;
  containers: DockerContainer[];
  loading: boolean;
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

function LogsModal({ serverId, containerId, containerName, onClose }: {
  serverId: string;
  containerId: string;
  containerName: string;
  onClose: () => void;
}) {
  const [logs, setLogs] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    serversApi.getContainerLogs(serverId, containerId, 500)
      .then((r) => setLogs(r.data.logs || '(empty)'))
      .catch(() => setLogs('Failed to load logs'))
      .finally(() => setLoading(false));
  }, [serverId, containerId]);

  return (
    <Drawer
      title={`Logs: ${containerName}`}
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
  const termRef = useRef<HTMLDivElement>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const termRef2 = useRef<Terminal | null>(null);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    console.log('ExecDrawer effect:', { open, containerId, hasTerm: !!termRef.current });
    if (!open || !containerId || !termRef.current) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: { background: '#1e1e2e', foreground: '#cdd6f4' },
      scrollback: 5000,
    });
    termRef2.current = terminal;

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(termRef.current);

    const fitTimer = setTimeout(() => {
      try { fitAddon.fit(); } catch {}
    }, 300);

    const token = localStorage.getItem('token');
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${wsProtocol}//${window.location.host}/api/ws/servers/${serverId}/docker/containers/${containerId}/exec?token=${token}`;
    console.log('Exec WS connecting:', wsUrl);

    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.onopen = () => {
      console.log('Exec WS connected');
      setConnected(true);
      terminal.focus();
    };

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        terminal.write(ev.data);
      } else if (ev.data instanceof Blob) {
        ev.data.text().then((text) => terminal.write(text));
      }
    };

    ws.onclose = (ev) => {
      console.log('Exec WS closed', ev.code, ev.reason);
      setConnected(false);
      terminal.write('\r\n\x1b[31mDisconnected\x1b[0m\r\n');
    };

    ws.onerror = (ev) => {
      console.error('Exec WS error', ev);
      terminal.write('\r\n\x1b[31mConnection error\x1b[0m\r\n');
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    const handleResize = () => { try { fitAddon.fit(); } catch {} };
    window.addEventListener('resize', handleResize);

    return () => {
      console.log('ExecDrawer cleanup');
      clearTimeout(fitTimer);
      window.removeEventListener('resize', handleResize);
      ws.close();
      terminal.dispose();
    };
  }, [open, serverId, containerId]);

  const handleClose = () => {
    if (wsRef.current) wsRef.current.close();
    if (termRef2.current) termRef2.current.dispose();
    onClose();
  };

  return (
    <Drawer
      title={
        <Space>
          <CodeOutlined />
          <span>Exec: {containerName}</span>
          <Tag color={connected ? 'green' : 'red'}>{connected ? 'Connected' : 'Disconnected'}</Tag>
        </Space>
      }
      open={open}
      onClose={handleClose}
      maskClosable={false}
      placement="right"
      rootStyle={{ position: 'fixed' }}
      styles={{ body: { padding: 0, background: '#1e1e2e' }, wrapper: { width: '80vw' } }}
    >
      <div ref={termRef} style={{ width: '100%', height: 'calc(100vh - 110px)' }} />
    </Drawer>
  );
}

export default function Docker() {
  const [servers, setServers] = useState<ServerDocker[]>([]);
  const [initialLoading, setInitialLoading] = useState(true);
  const [activeKeys, setActiveKeys] = useState<string[]>([]);
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [logsTarget, setLogsTarget] = useState<{ serverId: string; containerId: string; containerName: string } | null>(null);
  const [execTarget, setExecTarget] = useState<{ serverId: string; containerId: string; containerName: string } | null>(null);

  const expandServerId = searchParams.get('server');

  const loadServers = useCallback(async () => {
    setInitialLoading(true);
    try {
      const res = await serversApi.list();
      const allServers = res.data || [];

      const withDocker: ServerDocker[] = allServers
        .filter((s) => s.has_docker)
        .map((s) => ({
          server: s,
          version: s.docker_version || '',
          containers: [],
          loading: false,
        }));

      setServers(withDocker);

      if (expandServerId) {
        const match = withDocker.find((s) => s.server.id === expandServerId);
        if (match) {
          setActiveKeys([expandServerId]);
          loadContainers(expandServerId);
        }
      }
    } catch {
      // ignore
    }
    setInitialLoading(false);
  }, [expandServerId]);

  const loadContainers = async (serverId: string) => {
    setServers((prev) => prev.map((s) => (s.server.id === serverId ? { ...s, loading: true } : s)));
    try {
      const res = await serversApi.getContainers(serverId);
      setServers((prev) => prev.map((s) => (s.server.id === serverId ? { ...s, containers: res.data || [], loading: false } : s)));
    } catch {
      message.error('Failed to load containers');
      setServers((prev) => prev.map((s) => (s.server.id === serverId ? { ...s, loading: false } : s)));
    }
  };

  const handleAction = async (serverId: string, containerId: string, action: 'start' | 'stop' | 'restart') => {
    try {
      await serversApi.containerAction(serverId, containerId, action);
      message.success(`Container ${action} success`);
      loadContainers(serverId);
    } catch {
      message.error(`Failed to ${action} container`);
    }
  };

  useEffect(() => {
    loadServers();
  }, [loadServers]);

  const getColumns = (serverId: string): ColumnsType<DockerContainer> => [
    {
      title: 'Name',
      dataIndex: 'name',
      key: 'name',
      render: (v: string) => <Text strong>{v}</Text>,
    },
    {
      title: 'Image',
      dataIndex: 'image',
      key: 'image',
      ellipsis: true,
    },
    {
      title: 'State',
      dataIndex: 'state',
      key: 'state',
      width: 110,
      render: (v: string) => <Tag color={stateColor[v] || 'default'}>{v}</Tag>,
    },
    {
      title: 'Status',
      dataIndex: 'status',
      key: 'status',
      ellipsis: true,
    },
    {
      title: 'Ports',
      dataIndex: 'ports',
      key: 'ports',
      ellipsis: true,
      width: 200,
    },
    {
      title: 'Actions',
      key: 'actions',
      width: 320,
      render: (_, record) => (
        <Space size="small" wrap>
          {record.state !== 'running' ? (
            <Button size="small" type="primary" icon={<CaretRightOutlined />} onClick={() => handleAction(serverId, record.id, 'start')}>Start</Button>
          ) : (
            <>
              <Button size="small" icon={<PauseOutlined />} onClick={() => handleAction(serverId, record.id, 'stop')}>Stop</Button>
              <Button size="small" icon={<SyncOutlined />} onClick={() => handleAction(serverId, record.id, 'restart')}>Restart</Button>
            </>
          )}
          <Button size="small" icon={<FileTextOutlined />} onClick={() => setLogsTarget({ serverId, containerId: record.id, containerName: record.name })}>Logs</Button>
          <Button size="small" icon={<CodeOutlined />} onClick={() => setExecTarget({ serverId, containerId: record.id, containerName: record.name })}>Exec</Button>
        </Space>
      ),
    },
  ];

  const collapseItems = servers.map((sd) => ({
    key: sd.server.id,
    label: (
      <Space>
        <Text strong>{sd.server.name}</Text>
        <Text type="secondary">({sd.server.host})</Text>
        <Tag color="blue">Docker {sd.version}</Tag>
        <Text type="secondary">{sd.containers.length} containers</Text>
      </Space>
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
        Server Detail
      </Button>
    ),
    children: (
      <Table
        rowKey="id"
        columns={getColumns(sd.server.id)}
        dataSource={sd.containers}
        loading={sd.loading}
        pagination={false}
        size="small"
        locale={{ emptyText: <Empty description="No containers" /> }}
      />
    ),
  }));

  const handleCollapseChange = (keys: string | string[]) => {
    const keyArr = Array.isArray(keys) ? keys : [keys];
    setActiveKeys(keyArr);
    for (const key of keyArr) {
      const sd = servers.find((s) => s.server.id === key);
      if (sd && sd.containers.length === 0 && !sd.loading) {
        loadContainers(key);
      }
    }
  };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Title level={4} style={{ margin: 0 }}>Docker</Title>
        <Button icon={<ReloadOutlined />} onClick={loadServers}>Refresh</Button>
      </div>

      {initialLoading ? (
        <div style={{ textAlign: 'center', padding: 80 }}><Spin size="large" /></div>
      ) : servers.length === 0 ? (
        <Empty description="No servers with Docker installed" />
      ) : (
        <Collapse
          activeKey={activeKeys}
          onChange={handleCollapseChange}
          items={collapseItems}
        />
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
