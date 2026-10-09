import React, { useRef, useEffect, useState, useContext } from 'react';
import { Terminal } from '@xterm/xterm';
import type { ITheme } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { Button, Space, App } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { DarkModeContext } from '../contexts/DarkModeContext';
import '@xterm/xterm/css/xterm.css';

interface Props {
  serverId: string;
}

type ConnectionState = 'connecting' | 'connected' | 'disconnected';

const darkTheme: ITheme = {
  background: '#1e1e2e',
  foreground: '#cdd6f4',
};

// Dimmed gray instead of pure white so the terminal stands out from the page.
const lightTheme: ITheme = {
  background: '#dde3ea',
  foreground: '#1f2329',
  cursor: '#1f2329',
  cursorAccent: '#dde3ea',
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
  const darkMode = useContext(DarkModeContext);
  const darkModeRef = useRef(darkMode);
  const tRef = useRef(t);
  const messageRef = useRef(message);
  const termRef = useRef<HTMLDivElement>(null);
  const [attempt, setAttempt] = useState(0);
  const session = `${serverId}/${attempt}`;
  const [status, setStatus] = useState<{ session: string; state: ConnectionState } | null>(null);
  const state = status?.session === session ? status.state : 'connecting';
  const terminalRef = useRef<Terminal | null>(null);

  // Retheme the live terminal instantly when the app theme toggles
  useEffect(() => {
    darkModeRef.current = darkMode;
    if (terminalRef.current) {
      terminalRef.current.options.theme = darkMode ? darkTheme : lightTheme;
    }
  }, [darkMode]);

  useEffect(() => {
    tRef.current = t;
    messageRef.current = message;
  }, [message, t]);

  useEffect(() => {
    const host = termRef.current;
    if (!host) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, "Courier New", monospace',
      theme: darkModeRef.current ? darkTheme : lightTheme,
    });
    terminalRef.current = terminal;

    const fitAddon = new FitAddon();
    const webLinksAddon = new WebLinksAddon();
    terminal.loadAddon(fitAddon);
    terminal.loadAddon(webLinksAddon);

    terminal.open(host);
    const fit = () => {
      try {
        fitAddon.fit();
      } catch {
        // The terminal may already be disposed during a concurrent resize.
      }
    };
    fit();

    // ResizeObserver keeps terminal filling the container on any layout change.
    const ro = new ResizeObserver(fit);
    ro.observe(host);

    const token = localStorage.getItem('token');
    const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    // The JWT rides in the subprotocol list (backend echoes "bearer") instead
    // of the query string, so it never lands in gin/nginx access logs.
    const ws = new WebSocket(
      `${wsProtocol}//${window.location.host}/api/ssh/${serverId}`,
      token ? ['bearer', token] : undefined,
    );
    // Keep SSH output as bytes: xterm preserves UTF-8 sequences split across
    // frames, and no asynchronous Blob conversion can outlive this session.
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      setStatus({ session, state: 'connected' });
      sendResize(ws, terminal); // sync PTY size with the fitted terminal
      terminal.write(tRef.current('terminal.connected') + '\r\n');
    };

    ws.onmessage = (ev) => {
      terminal.write(typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data));
    };

    ws.onclose = () => {
      setStatus({ session, state: 'disconnected' });
      terminal.write('\r\n' + tRef.current('terminal.disconnected') + '\r\n');
    };

    ws.onerror = () => {
      messageRef.current.error(tRef.current('terminal.connFailed'));
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    // Whenever the fit addon changes the terminal dimensions, tell the backend
    terminal.onResize(() => sendResize(ws, terminal));

    window.addEventListener('resize', fit);

    return () => {
      ro.disconnect();
      window.removeEventListener('resize', fit);
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close();
      terminal.dispose();
      if (terminalRef.current === terminal) terminalRef.current = null;
    };
  }, [serverId, session]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <Space style={{ marginBottom: 8 }}>
        <span className={`terminal-state ${state}`} role="status">
          ● {t(`common.${state}`)}
        </span>
        <Button size="small" icon={<ReloadOutlined />} aria-label={t('terminal.reconnect')} onClick={() => setAttempt((value) => value + 1)}>
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
