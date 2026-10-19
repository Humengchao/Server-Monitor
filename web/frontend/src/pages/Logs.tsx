import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Card, Empty, Input, Select, Space, Spin, Tag, Typography } from 'antd';
import { useLocation, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { serversApi } from '../api/servers';
import type { Server } from '../api/servers';
import { logsApi } from '../api/logs';
import type { LogEntry, LogsPage, LogsQuery } from '../api/logs';
import { usePolling } from '../hooks/usePolling';
import '../i18n/networkLogs';

function localTime(value: string) {
  const date = new Date(value); if (!Number.isFinite(date.getTime())) return '';
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19);
}
export default function Logs() {
  const { t } = useTranslation('networkLogs');
  const [params, setParams] = useSearchParams();
  const { key: navigation } = useLocation();
  const [servers, setServers] = useState<Server[]>([]);
  const [defaultSince] = useState(() => new Date(Date.now() - 3600000).toISOString());
  const query = useMemo(() => ({ navigation, server: params.get('server') || '', container: params.get('container') || undefined,
    params: { since: params.get('since') || defaultSince, until: params.get('until') || undefined,
      unit: params.get('unit') || undefined, priority: params.get('priority') || undefined, keyword: params.get('keyword') || undefined, limit: 200 } satisfies LogsQuery,
  }), [params, navigation, defaultSince]);
  // Draft filters belong to the displayed URL. A browser navigation immediately
  // selects its own source, before the next request or reset effect completes.
  const initialDraft = { query, server: query.server, container: query.container || '', since: localTime(query.params.since),
    until: localTime(query.params.until || ''), unit: query.params.unit || '', priority: query.params.priority, keyword: query.params.keyword || '' };
  const [draft, setDraft] = useState(initialDraft);
  const filters = draft.query === query ? draft : initialDraft;
  const { server, container, since, until, unit, priority, keyword } = filters;
  const changeFilter = (change: Partial<Omit<typeof filters, 'query'>>) => setDraft({ ...filters, ...change });
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [result, setResult] = useState<LogsPage | null>(null);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(false);
  const [following, setFollowing] = useState<typeof query | null>(null);
  const follow = following === query;
  const [trimmed, setTrimmed] = useState(false);
  const [displayQuery, setDisplayQuery] = useState(query);
  const visibleEntries = displayQuery === query ? entries : [];
  const visibleResult = displayQuery === query ? result : null;
  const cursor = useRef('');
  const request = useRef<AbortController | null>(null);
  const scope = useRef<object | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    void serversApi.list(controller.signal).then(res => { if (!controller.signal.aborted) setServers(res.data || []); }).catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => controller.abort();
  }, []);
  const load = useCallback(async (reset = false) => {
    const active = scope.current;
    if (!active || !query.server || request.current) return;
    const controller = new AbortController(); request.current = controller;
    setLoading(true);
    try {
      const response = await logsApi.read(query.server, query.container, { ...query.params, cursor: reset ? undefined : cursor.current || undefined }, controller.signal);
      if (scope.current !== active || controller.signal.aborted) return;
      const page = response.data; setResult(page); setError(false);
      setDisplayQuery(query);
      cursor.current = page.next_cursor || (reset ? '' : cursor.current);
      setEntries(current => {
        const next = reset ? page.entries : [...current, ...page.entries.filter(entry => !current.some(old => old.id === entry.id))];
        if (next.length > 2000) setTrimmed(true);
        return next.slice(-2000);
      });
      if (page.reason === 'cursor_unavailable' || page.reason === 'unsupported' || page.reason === 'access_denied' || page.partial && !page.next_cursor) setFollowing(null);
    } catch { if (scope.current === active && !controller.signal.aborted) setError(true); }
    finally { if (request.current === controller) { request.current = null; if (scope.current === active) setLoading(false); } }
  }, [query]);
  useEffect(() => {
    scope.current = {};
    const timer = window.setTimeout(() => { cursor.current = ''; setEntries([]); setResult(null); setTrimmed(false); setError(false); void load(true); }, 0);
    return () => { scope.current = null; window.clearTimeout(timer); request.current?.abort(); request.current = null; };
  }, [load]);
  usePolling(() => load(), 3000, { leading: false, enabled: follow && !query.params.until });
  const apply = () => {
    const start = new Date(since), end = until ? new Date(until) : null;
    if (!Number.isFinite(start.getTime()) || end && !Number.isFinite(end.getTime())) { setError(true); return; }
    setFollowing(null);
    const next = { server, container: container.trim() || undefined, params: { since: start.toISOString(), until: end?.toISOString(), unit: container ? undefined : unit.trim() || undefined, priority: container ? undefined : priority, keyword: keyword || undefined, limit: 200 } };
    const search = new URLSearchParams({ server, since: next.params.since });
    if (next.container) search.set('container', next.container); if (next.params.until) search.set('until', next.params.until); if (next.params.unit) search.set('unit', next.params.unit);
    if (next.params.priority) search.set('priority', next.params.priority); if (next.params.keyword) search.set('keyword', next.params.keyword);
    setParams(search, { replace: true });
  };
  const download = () => {
    const text = visibleEntries.map(entry => `${entry.time} [${entry.stream}] ${entry.unit || ''} ${entry.message}`).join('\n');
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
    const link = document.createElement('a'); link.href = url; link.download = 'host-logs.txt'; link.click(); window.setTimeout(() => URL.revokeObjectURL(url), 5000);
  };
  return <div>
    <div className="page-heading"><div><Typography.Title level={2}>{t('logs')}</Typography.Title><Typography.Text type="secondary">{t('logsHint')}</Typography.Text></div></div>
    <Card className="panel-card"><Space wrap align="start" style={{ marginBottom: 16 }}>
      <label>{t('server')}<Select aria-label={t('server')} value={server || undefined} style={{ width: 220, display: 'block' }} showSearch optionFilterProp="label" onChange={server => changeFilter({ server })} options={servers.map(host => ({ value: host.id, label: host.name }))} /></label>
      <label>{t('container')}<Input aria-label={t('container')} value={container} onChange={event => changeFilter({ container: event.target.value })} /></label>
      <label>{t('since')}<Input type="datetime-local" aria-label={t('since')} value={since} onChange={event => changeFilter({ since: event.target.value })} /></label>
      <label>{t('until')}<Input type="datetime-local" aria-label={t('until')} value={until} onChange={event => changeFilter({ until: event.target.value })} /></label>
      <label>{t('unit')}<Input aria-label={t('unit')} disabled={!!container} value={unit} onChange={event => changeFilter({ unit: event.target.value })} /></label>
      <label>{t('priority')}<Select aria-label={t('priority')} value={priority} onChange={priority => changeFilter({ priority })} allowClear disabled={!!container} placeholder={t('all')} style={{ width: 150, display: 'block' }} options={['emerg', 'alert', 'crit', 'err', 'warning', 'notice', 'info', 'debug'].map((label, value) => ({ value: String(value), label }))} /></label>
      <label>{t('keyword')}<Input aria-label={t('keyword')} value={keyword} onChange={event => changeFilter({ keyword: event.target.value })} onPressEnter={apply} maxLength={512} /></label>
      <Button type="primary" onClick={apply} disabled={!server}>{t('apply')}</Button>
    </Space>
      <Space wrap style={{ marginBottom: 16 }}><Button onClick={() => { void load(true); }} disabled={!query.server} loading={loading}>{t('refresh')}</Button><Button onClick={() => { void load(); }} disabled={loading || !visibleResult?.next_cursor}>{t('next')}</Button><Button onClick={() => setFollowing(current => current === query ? null : query)} disabled={!query.server || !!query.params.until}>{t(follow ? 'stop' : 'follow')}</Button><Button disabled={!visibleEntries.length} onClick={download}>{t('export')}</Button><Typography.Text>{t('rows')}: {visibleEntries.length} · {t('scanned')}: {visibleResult?.scanned ?? 0}</Typography.Text></Space>
      {error && <Alert type="error" showIcon title={t('failed')} />}
      {visibleResult?.truncated && <Alert type="warning" showIcon title={t('truncated')} />}
      {visibleResult?.partial && <Alert type="warning" showIcon title={t(visibleResult.reason || 'command_failed', { defaultValue: visibleResult.reason })} />}
      {trimmed && <Alert type="info" title={t('bufferLimit')} />}
      {!query.server ? <Empty description={t('noServer')} /> : !visibleEntries.length && !loading ? <Empty description={t('noLogs')} /> : <div aria-busy={loading} style={{ maxHeight: '65vh', overflow: 'auto', marginTop: 12 }}>
        {visibleEntries.map(entry => <div key={entry.id} className="host-log-entry" style={{ borderBottom: '1px solid var(--border)', padding: '8px 0' }}><Space wrap><time>{new Date(entry.time).toLocaleString()}</time><Tag color={entry.stream === 'stderr' ? 'orange' : undefined}>{entry.stream}</Tag>{entry.unit && <Typography.Text code>{entry.unit}</Typography.Text>}{entry.priority && <Tag>{entry.priority}</Tag>}</Space><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', margin: '4px 0' }}>{entry.message}</pre></div>)}
      </div>}
      {loading && <Spin />}
    </Card>
  </div>;
}
