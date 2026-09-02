import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, App, Button, Empty, Input, Result, Segmented, Space, Table, Tag, Tooltip, Typography } from 'antd';
import {
  CaretRightOutlined, PauseOutlined, ReloadOutlined, SearchOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  hostOpsApi, ServiceAction, ServiceManager, ServiceUnit, serviceStateColor,
} from '../api/hostops';
import { usePolling } from '../hooks/usePolling';

const { Text } = Typography;

// Services change far more slowly than processes, and each read costs two SSH
// sessions on a systemd host. Refresh accordingly.
const REFRESH_MS = 20000;

interface Props {
  serverId: string;
  serverType: string;
}

type StateFilter = 'all' | 'failed' | 'active' | 'inactive';

export default function ServiceTable({ serverId, serverType }: Props) {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const [units, setUnits] = useState<ServiceUnit[]>([]);
  const [manager, setManager] = useState<ServiceManager | undefined>(undefined);
  const [unsupported, setUnsupported] = useState<{ code: string; detail: string } | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [stateFilter, setStateFilter] = useState<StateFilter>('all');
  const [live, setLive] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async (showLoading = true) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    if (showLoading) setLoading(true);
    try {
      const res = await hostOpsApi.services(serverId, controller.signal);
      setUnits(res.data.services || []);
      setManager(res.data.manager);
      setTotal(res.data.total || 0);
      setUnsupported(res.data.supported === false
        ? { code: res.data.reason_code || 'absent', detail: res.data.reason || '' }
        : null);
      setError(null);
    } catch (err: unknown) {
      if ((err as { code?: string })?.code === 'ERR_CANCELED') return;
      const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      setError(detail || t('service.loadFailed'));
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [serverId, t]);

  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    return () => { window.clearTimeout(initial); abortRef.current?.abort(); };
  }, [load]);

  // Nothing to poll for on a host with no service manager.
  usePolling(() => load(false), REFRESH_MS, { leading: false, enabled: live && !unsupported });

  const counts = useMemo(() => ({
    failed: units.filter((u) => u.active === 'failed' || u.sub === 'failed').length,
    active: units.filter((u) => u.active === 'active').length,
  }), [units]);

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return units.filter((u) => {
      if (stateFilter === 'failed' && u.active !== 'failed' && u.sub !== 'failed') return false;
      if (stateFilter === 'active' && u.active !== 'active') return false;
      if (stateFilter === 'inactive' && u.active === 'active') return false;
      if (!needle) return true;
      return u.name.toLowerCase().includes(needle) || u.description.toLowerCase().includes(needle);
    });
  }, [units, query, stateFilter]);

  const runAction = (unit: ServiceUnit, action: ServiceAction) => {
    const confirmAction = async () => {
      setBusy(`${unit.name}:${action}`);
      try {
        await hostOpsApi.controlService(serverId, unit.name, action);
        message.success(t('service.actionSent', { action: t(`service.action.${action}`), name: unit.name }));
        // systemd returns before the unit has finished transitioning; re-read
        // once it has had a moment to settle, or the row still shows the old state.
        window.setTimeout(() => load(false), 1200);
      } catch (err: unknown) {
        const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
        // The host's own words ("Access denied", "Unit not found") say far more
        // than any message this component could compose.
        message.error(detail || t('service.actionFailed'));
      } finally {
        setBusy(null);
      }
    };

    // start is the only verb that cannot interrupt something already serving
    // traffic, so it is the only one that does not ask first.
    if (action === 'start') {
      confirmAction();
      return;
    }
    modal.confirm({
      title: t('service.confirmTitle', { action: t(`service.action.${action}`) }),
      content: (
        <div className="kill-confirm">
          <Text>{t('service.confirmBody')}</Text>
          <code>{unit.name}</code>
        </div>
      ),
      okText: t(`service.action.${action}`),
      okType: action === 'stop' ? 'danger' : 'primary',
      onOk: confirmAction,
    });
  };

  const columns = [
    {
      title: t('service.name'),
      dataIndex: 'name',
      key: 'name',
      width: 260,
      sorter: (a: ServiceUnit, b: ServiceUnit) => a.name.localeCompare(b.name),
      render: (name: string) => <Text className="mono-cell">{name}</Text>,
    },
    {
      title: t('service.state'),
      dataIndex: 'active',
      key: 'active',
      width: 132,
      render: (active: string, unit: ServiceUnit) => (
        <Tag color={serviceStateColor(active)} className="service-state-tag">
          {t(`service.state.${active}`, { defaultValue: active || '—' })}
          {/* The sub-state is what separates a one-shot that finished from a
              daemon that died; both read "inactive" without it. */}
          {unit.sub && unit.sub !== active && <span className="service-substate">{unit.sub}</span>}
        </Tag>
      ),
    },
    // sysv init scripts report no boot disposition at all, so the column is
    // hidden rather than shown empty for every row.
    ...(manager === 'sysv' ? [] : [{
      title: <Tooltip title={t('service.enabledHint')}><span className="col-hint">{t('service.enabled')}</span></Tooltip>,
      dataIndex: 'enabled',
      key: 'enabled',
      width: 118,
      render: (enabled: string) => (enabled
        ? <Text type={enabled === 'enabled' ? undefined : 'secondary'}>
            {t(`service.boot.${enabled}`, { defaultValue: enabled })}
          </Text>
        : <Text type="secondary">—</Text>),
    }]),
    ...(manager === 'sysv' ? [] : [{
      title: t('service.description'),
      dataIndex: 'description',
      key: 'description',
      render: (description: string) => (
        <Tooltip title={description} placement="topLeft">
          <span className="command-cell">{description || '—'}</span>
        </Tooltip>
      ),
    }]),
    {
      title: t('common.actions'),
      key: 'actions',
      width: serverType === 'windows' ? 176 : 240,
      fixed: 'right' as const,
      // Spelled out rather than iconised. Restart and reload are both "circular
      // arrow" glyphs at this size, and picking the wrong one is not a harmless
      // mistake: a restart drops every connection the service is holding.
      render: (_: unknown, unit: ServiceUnit) => (
        <Space size={2} className="service-actions">
          <Button type="text" size="small"
            loading={busy === `${unit.name}:start`}
            disabled={unit.active === 'active'}
            onClick={() => runAction(unit, 'start')}>{t('service.action.start')}</Button>
          <Button type="text" size="small"
            loading={busy === `${unit.name}:restart`}
            onClick={() => runAction(unit, 'restart')}>{t('service.action.restart')}</Button>
          {/* Windows has no reload verb, and mapping it onto a restart would
              drop connections the operator chose reload to preserve. */}
          {serverType !== 'windows' && (
            <Tooltip title={t('service.reloadHint')}>
              <Button type="text" size="small"
                loading={busy === `${unit.name}:reload`}
                onClick={() => runAction(unit, 'reload')}>{t('service.action.reload')}</Button>
            </Tooltip>
          )}
          <Button type="text" size="small" danger
            loading={busy === `${unit.name}:stop`}
            disabled={unit.active !== 'active'}
            onClick={() => runAction(unit, 'stop')}>{t('service.action.stop')}</Button>
        </Space>
      ),
    },
  ];

  if (error && units.length === 0) {
    return (
      <Result
        status="warning"
        title={t('service.unavailable')}
        subTitle={error}
        extra={<Button type="primary" icon={<ReloadOutlined />} onClick={() => load()}>{t('common.refresh')}</Button>}
      />
    );
  }

  if (unsupported) {
    // The backend's own sentence is English-only, so the reader gets the
    // localized explanation and the raw text is kept as a tooltip for whoever
    // is debugging the host.
    return (
      <Result
        status="info"
        title={t(`service.absent.${unsupported.code}Title`, { defaultValue: t('service.unsupported') })}
        subTitle={
          <Tooltip title={unsupported.detail || undefined}>
            <span>{t(`service.absent.${unsupported.code}Note`, { defaultValue: unsupported.detail })}</span>
          </Tooltip>
        }
        extra={<Button icon={<ReloadOutlined />} onClick={() => load()}>{t('common.refresh')}</Button>}
      />
    );
  }

  return (
    <div className="process-panel">
      <div className="process-toolbar">
        <Space size={16} wrap className="process-summary">
          <span><Text type="secondary">{t('service.count')}</Text><strong>{total}</strong></span>
          <span><Text type="secondary">{t('service.running')}</Text><strong>{counts.active}</strong></span>
          <span>
            <Text type="secondary">{t('service.failedCount')}</Text>
            <strong style={counts.failed ? { color: serviceStateColor('failed') } : undefined}>{counts.failed}</strong>
          </span>
        </Space>
        <Space size={8} wrap>
          <Segmented
            value={stateFilter}
            onChange={(value) => setStateFilter(value as StateFilter)}
            options={[
              { value: 'all', label: t('service.filterAll') },
              { value: 'failed', label: t('service.filterFailed') },
              { value: 'active', label: t('service.filterActive') },
              { value: 'inactive', label: t('service.filterInactive') },
            ]}
          />
          <Input
            allowClear
            className="process-search"
            prefix={<SearchOutlined />}
            placeholder={t('service.searchPlaceholder')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
          <Segmented
            value={live ? 'live' : 'paused'}
            onChange={(value) => setLive(value === 'live')}
            options={[
              { value: 'live', icon: <Tooltip title={t('process.autoRefresh')}><CaretRightOutlined /></Tooltip> },
              { value: 'paused', icon: <Tooltip title={t('process.pause')}><PauseOutlined /></Tooltip> },
            ]}
          />
          <Button icon={<ReloadOutlined />} onClick={() => load()} loading={loading} />
        </Space>
      </div>

      {/* Nothing here escalates privilege, so an unprivileged SSH user will be
          refused by systemd itself. Saying so up front beats an error per click. */}
      <Alert
        className="service-privilege-note"
        type="info"
        showIcon
        title={t('service.privilegeTitle')}
        description={t('service.privilegeNote')}
      />

      {total > units.length && (
        <Text type="secondary" className="process-truncated">
          {t('service.truncated', { shown: units.length, total })}
        </Text>
      )}

      <Table
        className="server-table process-table"
        dataSource={filtered}
        columns={columns}
        rowKey="name"
        size="small"
        loading={loading && units.length === 0}
        pagination={false}
        scroll={{ x: 820, y: 460 }}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('service.noMatches')} /> }}
      />
    </div>
  );
}
