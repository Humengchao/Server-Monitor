import React, { useRef, useEffect, useState } from 'react';
import { Terminal } from 'xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Button, Space, App } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import 'xterm/css/xterm.css';

interface Props {
  serverId: string;
}

export default function SshTerminal({ serverId }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const termRef = useRef<HTMLDivElement>(null);
  const [connected, setConnected] = useState(false);
  const [term, setTerm] = useState<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);

  const connect = () => {
    if (wsRef.current) {
      wsRef.current.close();
    }

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: { background: '#1e1e2e', foreground: '#cdd6f4' },
    });

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();
    terminal.loadAddon(fitAddon);
    terminal.loadAddon(webLinksAddon);
    fitAddonRef.current = fitAddon;

    terminal.open(termRef.current!);
    fitAddon.fit();

    const token = localStorage.getItem('token');
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${window.location.host}/api/ssh/${serverId}?token=${token}`);
    wsRef.current = ws;

    ws.onopen = () => {
      setConnected(true);
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

    terminal.onResize(() => {
      setTerm(terminal);
    });

    setTerm(terminal);

    const handleResize = () => fitAddon.fit();
    window.addEventListener('resize', handleResize);

    return () => {
      window.removeEventListener('resize', handleResize);
      ws.close();
      terminal.dispose();
    };
  };

  useEffect(() => {
    const cleanup = connect();
    return () => cleanup?.();
  }, [serverId]);

  const handleReconnect = () => {
    if (wsRef.current) wsRef.current.close();
    if (term) term.dispose();
    connect();
  };

  return (
    <div>
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
        style={{ width: '100%', height: 400, borderRadius: 8, overflow: 'hidden' }}
      />
    </div>
  );
}
