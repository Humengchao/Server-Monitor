import React, { useRef, useEffect, useState } from 'react';
import { Terminal } from 'xterm';
import type { ITheme } from 'xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Button, Space, App } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import 'xterm/css/xterm.css';

interface Props {
  serverId: string;
}

const darkTheme: ITheme = {
  background: '#1e1e2e',
  foreground: '#cdd6f4',
};

const lightTheme: ITheme = {
  background: '#ffffff',
  foreground: '#333333',
  cursor: '#333333',
  cursorAccent: '#ffffff',
  selectionBackground: '#b3d4fc',
};

// Control messages are sent prefixed with \x01 so the backend can tell them
// apart from raw keystrokes (see services/ssh.go PumpStdin).
function sendResize(ws: WebSocket, terminal: Terminal) {
  if (ws.readyState === WebSocket.OPEN) {
    ws.send('\x01' + JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
  }
}

export default function SshTerminal({ serverId }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const termRef = useRef<HTMLDivElement>(null);
  const [connected, setConnected] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const cleanupRef = useRef<(() => void) | null>(null);

  const connect = () => {
    // Tear down any previous terminal/socket before creating a new one
    cleanupRef.current?.();

    const darkMode = localStorage.getItem('theme') === 'dark';
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: darkMode ? darkTheme : lightTheme,
    });

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();
    terminal.loadAddon(fitAddon);
    terminal.loadAddon(webLinksAddon);

    terminal.open(termRef.current!);
    fitAddon.fit();

    // ResizeObserver keeps terminal filling the container on any layout change
    const ro = new ResizeObserver(() => {
      try { fitAddon.fit(); } catch {}
    });
    ro.observe(termRef.current!);

    const token = localStorage.getItem('token');
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${window.location.host}/api/ssh/${serverId}?token=${token}`);
    wsRef.current = ws;

    ws.onopen = () => {
      setConnected(true);
      sendResize(ws, terminal); // sync PTY size with the fitted terminal
      terminal.write(t('terminal.connected') + '\r\n');
    };

    ws.onmessage = (ev) => {
      terminal.write(ev.data);
    };

    ws.onclose = () => {
      setConnected(false);
      terminal.write('\r\n' + t('terminal.disconnected') + '\r\n');
    };

    ws.onerror = () => {
      message.error(t('terminal.connFailed'));
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    // Whenever the fit addon changes the terminal dimensions, tell the backend
    terminal.onResize(() => sendResize(ws, terminal));

    const handleResize = () => fitAddon.fit();
    window.addEventListener('resize', handleResize);

    const cleanup = () => {
      ro.disconnect();
      window.removeEventListener('resize', handleResize);
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
      terminal.dispose();
    };
    cleanupRef.current = cleanup;
    return cleanup;
  };

  useEffect(() => {
    connect();
    return () => {
      cleanupRef.current?.();
      cleanupRef.current = null;
    };
  }, [serverId]);

  const handleReconnect = () => {
    connect();
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <Space style={{ marginBottom: 8 }}>
        <span style={{ color: connected ? '#52c41a' : '#ff4d4f' }}>
          ● {connected ? t('common.connected') : t('common.disconnected')}
        </span>
        <Button size="small" icon={<ReloadOutlined />} onClick={handleReconnect}>
          {t('terminal.reconnect')}
        </Button>
      </Space>
      <div
        ref={termRef}
        style={{ flex: 1, minHeight: 0, borderRadius: 8, overflow: 'hidden' }}
      />
    </div>
  );
}
