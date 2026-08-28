import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Empty, Input, Result, Segmented, Space, Table, Tag, Tooltip, Typography } from 'antd';
import { ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { hostOpsApi, ListeningPort, PortExposure, exposureColor } from '../api/hostops';

const { Text } = Typography;

// Listening sockets change rarely; this is a page you read, not a monitor you
// watch. Refreshing on demand plus a slow tick is enough.
const REFRESH_MS = 30000;

interface Props {
  serverId: string;
}

type ExposureFilter = 'all' | PortExposure;

/** Well-known services, so a port number reads as something recognisable. */
const WELL_KNOWN: Record<number, string> = {
  21: 'ftp', 22: 'ssh', 23: 'telnet', 25: 'smtp', 53: 'dns', 80: 'http',
  110: 'pop3', 111: 'rpcbind', 123: 'ntp', 143: 'imap', 161: 'snmp',
  389: 'ldap', 443: 'https', 445: 'smb', 465: 'smtps', 587: 'smtp',
  993: 'imaps', 995: 'pop3s', 1433: 'mssql', 1521: 'oracle', 2049: 'nfs',
  2375: 'docker', 2376: 'docker-tls', 3000: 'grafana', 3306: 'mysql',
  3389: 'rdp', 5432: 'postgres', 5601: 'kibana', 5672: 'amqp', 6379: 'redis',
  8080: 'http-alt', 8443: 'https-alt', 9000: 'php-fpm', 9090: 'prometheus',
  9200: 'elasticsearch', 11211: 'memcached', 27017: 'mongodb',
};

export default function PortTable({ serverId }: Props) {
  const { t } = useTranslation();
  const [ports, setPorts] = useState<ListeningPort[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<ExposureFilter>('all');
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async (showLoading = true) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    if (showLoading) setLoading(true);
    try {
      const res = await hostOpsApi.ports(serverId, controller.signal);
      setPorts(res.data.ports || []);
      setTotal(res.data.total || 0);
      setError(null);
    } catch (err: unknown) {
      if ((err as { code?: string })?.code === 'ERR_CANCELED') return;
      const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      setError(detail || t('port.loadFailed'));
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [serverId, t]);

  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    const timer = window.setInterval(() => load(false), REFRESH_MS);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
      abortRef.current?.abort();
    };
  }, [load]);

  const counts = useMemo(() => ({
    public: ports.filter((p) => p.exposure === 'public').length,
    private: ports.filter((p) => p.exposure === 'private').length,
    loopback: ports.filter((p) => p.exposure === 'loopback').length,
  }), [ports]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return ports.filter((p) => {
      if (filter !== 'all' && p.exposure !== filter) return false;
      if (!needle) return true;
      return String(p.port).includes(needle)
        || p.process.toLowerCase().includes(needle)
        || p.address.toLowerCase().includes(needle)
        || (WELL_KNOWN[p.port] || '').includes(needle);
    });
  }, [ports, query, filter]);

  const columns = [
    {
      title: t('port.port'),
      dataIndex: 'port',
      key: 'port',
      width: 132,
      defaultSortOrder: 'ascend' as const,
      sorter: (a: ListeningPort, b: ListeningPort) => a.port - b.port,
      render: (port: number) => (
        <span className="port-cell">
          <Text className="mono-cell">{port}</Text>
          {WELL_KNOWN[port] && <small>{WELL_KNOWN[port]}</small>}
        </span>
      ),
    },
    {
      title: t('port.protocol'),
      dataIndex: 'protocol',
      key: 'protocol',
      width: 92,
      sorter: (a: ListeningPort, b: ListeningPort) => a.protocol.localeCompare(b.protocol),
      render: (protocol: string) => <Text type="secondary" className="mono-cell">{protocol}</Text>,
    },
    {
      title: <Tooltip title={t('port.exposureHint')}><span className="col-hint">{t('port.exposure')}</span></Tooltip>,
      dataIndex: 'exposure',
      key: 'exposure',
      width: 124,
      render: (exposure: PortExposure) => (
        <Tag color={exposureColor(exposure)}>{t(`port.exposureValue.${exposure}`, { defaultValue: exposure })}</Tag>
      ),
    },
    {
      title: t('port.address'),
      dataIndex: 'address',
      key: 'address',
      width: 168,
      render: (address: string) => <Text className="mono-cell">{address}</Text>,
    },
    {
      title: t('port.process'),
      key: 'process',
      render: (_: unknown, entry: ListeningPort) => (entry.process
        ? <span className="command-cell">{entry.process}{entry.pid ? ` (${entry.pid})` : ''}</span>
        // Blank attribution means the SSH user may not see the owner, not that
        // the socket has none.
        : <Tooltip title={t('port.ownerHidden')}><Text type="secondary">—</Text></Tooltip>),
    },
  ];

  if (error && ports.length === 0) {
    return (
      <Result
        status="warning"
        title={t('port.unavailable')}
        subTitle={error}
        extra={<Button type="primary" icon={<ReloadOutlined />} onClick={() => load()}>{t('common.refresh')}</Button>}
      />
    );
  }

  return (
    <div className="process-panel">
      <div className="process-toolbar">
        <Space size={16} wrap className="process-summary">
          <span><Text type="secondary">{t('port.count')}</Text><strong>{total}</strong></span>
          <span>
            <Text type="secondary">{t('port.exposureValue.public')}</Text>
            <strong style={counts.public ? { color: exposureColor('public') } : undefined}>{counts.public}</strong>
          </span>
          <span><Text type="secondary">{t('port.exposureValue.private')}</Text><strong>{counts.private}</strong></span>
          <span><Text type="secondary">{t('port.exposureValue.loopback')}</Text><strong>{counts.loopback}</strong></span>
        </Space>
        <Space size={8} wrap>
          <Segmented
            value={filter}
            onChange={(value) => setFilter(value as ExposureFilter)}
            options={[
              { value: 'all', label: t('port.filterAll') },
              { value: 'public', label: t('port.exposureValue.public') },
              { value: 'private', label: t('port.exposureValue.private') },
              { value: 'loopback', label: t('port.exposureValue.loopback') },
            ]}
          />
          <Input
            allowClear
            className="process-search"
            prefix={<SearchOutlined />}
            placeholder={t('port.searchPlaceholder')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <Button icon={<ReloadOutlined />} onClick={() => load()} loading={loading} />
        </Space>
      </div>

      {/* A wildcard bind is the finding worth acting on: it accepts traffic on
          every interface, including whichever one faces the internet. */}
      {counts.public > 0 && (
        <Alert
          className="service-privilege-note"
          type="warning"
          showIcon
          title={t('port.publicWarnTitle', { count: counts.public })}
          description={t('port.publicWarnNote')}
        />
      )}

      {total > ports.length && (
        <Text type="secondary" className="process-truncated">
          {t('port.truncated', { shown: ports.length, total })}
        </Text>
      )}

      <Table
        className="server-table process-table"
        dataSource={filtered}
        columns={columns}
        rowKey={(entry) => `${entry.protocol}-${entry.address}-${entry.port}`}
        size="small"
        loading={loading && ports.length === 0}
        pagination={false}
        scroll={{ x: 760, y: 460 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('port.noMatches')} /> }}
      />
    </div>
  );
}
