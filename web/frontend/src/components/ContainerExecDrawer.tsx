import { useEffect, useRef, useState } from 'react';
import { Button, Drawer, Space, Tag, Tooltip } from 'antd';
import { CodeOutlined, ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { PHONE_QUERY, useMediaQuery } from '../hooks/useMediaQuery';

type ConnectionState = 'connecting' | 'connected' | 'disconnected';

const STATE_COLOR: Record<ConnectionState, string> = {
  connecting: 'processing',
  connected: 'success',
  disconnected: 'error',
};

interface Props {
  serverId: string;
  containerId: string;
  containerName: string;
  open: boolean;
  onClose: () => void;
}

/**
 * Interactive shell inside a running container.
 *
 * The terminal is created from a callback ref, not a mount effect: antd renders
 * the drawer body one commit after `open` flips, so an effect keyed on `open`
 * found no element, returned early and never ran again. The shell silently
 * never started.
 */
export default function ContainerExecDrawer({ serverId, containerId, containerName, open, onClose }: Props) {
  const { t } = useTranslation();
  const phone = useMediaQuery(PHONE_QUERY);
  const [host, setHost] = useState<HTMLDivElement | null>(null);
  const [attempt, setAttempt] = useState(0);
  // Status is keyed by session so a reconnect or a different container starts
  // out as "connecting" without an effect having to reset it.
  const session = `${serverId}/${containerId}/${attempt}`;
  const [status, setStatus] = useState<{ session: string; state: ConnectionState } | null>(null);
  const state: ConnectionState = status?.session === session ? status.state : 'connecting';
  // Socket callbacks read t through a ref so a language switch doesn't tear
  // down the shell just to retranslate its messages.
  const tRef = useRef(t);
  useEffect(() => { tRef.current = t; }, [t]);

  useEffect(() => {
    if (!open || !containerId || !host) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: { background: '#1e1e2e', foreground: '#cdd6f4' },
      scrollback: 5000,
    });
    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(host);
    const fit = () => { try { fitAddon.fit(); } catch { /* disposed during a resize */ } };
    fit();
    const observer = new ResizeObserver(fit);
    observer.observe(host);

    const token = localStorage.getItem('token');
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    // The JWT travels as a subprotocol ("bearer, <jwt>") so it stays out of access logs.
    const ws = new WebSocket(
      `${protocol}//${window.location.host}/api/ws/servers/${serverId}/docker/containers/${containerId}/exec`,
      token ? ['bearer', token] : undefined,
    );
    // xterm decodes a byte stream across frame boundaries. Blob.text() would
    // decode each frame separately and could finish after the session closes.
    ws.binaryType = 'arraybuffer';
    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send('\x01' + JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
      }
    };

    ws.onopen = () => {
      setStatus({ session, state: 'connected' });
      sendResize();
      terminal.focus();
    };
    ws.onmessage = (event) => {
      terminal.write(typeof event.data === 'string' ? event.data : new Uint8Array(event.data));
    };
    ws.onclose = () => {
      setStatus({ session, state: 'disconnected' });
      terminal.write(`\r\n\x1b[31m${tRef.current('docker.execDisconnected')}\x1b[0m\r\n`);
    };
    ws.onerror = () => {
      terminal.write(`\r\n\x1b[31m${tRef.current('docker.execConnError')}\x1b[0m\r\n`);
    };
    terminal.onData((data) => { if (ws.readyState === WebSocket.OPEN) ws.send(data); });
    terminal.onResize(sendResize);
    window.addEventListener('resize', fit);

    return () => {
      observer.disconnect();
      window.removeEventListener('resize', fit);
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
      terminal.dispose();
    };
  }, [open, serverId, containerId, host, session]);

  return (
    <Drawer
      title={
        <Space>
          <CodeOutlined />
          <span>{t('docker.execTitle', { name: containerName })}</span>
          <Tag color={STATE_COLOR[state]} role="status">{t(`common.${state}`)}</Tag>
        </Space>
      }
      extra={
        <Tooltip title={t('terminal.reconnect')}>
          <Button icon={<ReloadOutlined />} aria-label={t('terminal.reconnect')} onClick={() => setAttempt((n) => n + 1)} />
        </Tooltip>
      }
      open={open}
      onClose={onClose}
      maskClosable={false}
      placement="right"
      rootStyle={{ position: 'fixed' }}
      styles={{
        body: { padding: 0, background: '#1e1e2e', display: 'flex', flexDirection: 'column' },
        wrapper: { width: phone ? '100vw' : 'min(1100px, 80vw)' },
      }}
    >
      <div ref={setHost} className="container-exec-host" />
    </Drawer>
  );
}
