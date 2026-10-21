import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Empty, Skeleton, Tooltip, Typography } from 'antd';
import { AlertOutlined, CheckCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  availabilityColor, uptimeApi, Outage, ServerUptimeDetail, UptimeWindow,
} from '../api/uptime';
import { usePolling } from '../hooks/usePolling';
import { formatDateTime } from '../utils/format';

const { Text } = Typography;

interface Props {
  serverId: string;
}

interface AvailabilityResult {
  serverId: string;
  detail: ServerUptimeDetail | null;
  windows: UptimeWindow[] | undefined;
  failed: boolean;
}

function formatDuration(seconds: number, t: (key: string, opts?: Record<string, unknown>) => string): string {
  if (seconds < 60) return t('uptime.durationSeconds', { count: seconds });
  if (seconds < 3600) return t('uptime.durationMinutes', { count: Math.round(seconds / 60) });
  if (seconds < 86400) {
    const hours = Math.floor(seconds / 3600);
    return `${t('uptime.durationHours', { count: hours })} ${t('uptime.durationMinutes', { count: Math.round((seconds % 3600) / 60) })}`;
  }
  return t('uptime.durationDays', { count: Math.round((seconds / 86400) * 10) / 10 });
}

function OutageRow({ outage, t, lang }: { outage: Outage; t: (k: string, o?: Record<string, unknown>) => string; lang: string }) {
  return (
    <li className={outage.ongoing ? 'ongoing' : undefined}>
      <span className="outage-dot" />
      <span className="outage-when">
        {formatDateTime(outage.started_at, lang)}
      </span>
      <span className="outage-length">
        {outage.ongoing
          ? t('uptime.ongoingFor', { duration: formatDuration(outage.seconds, t) })
          : formatDuration(outage.seconds, t)}
      </span>
    </li>
  );
}

/**
 * Observed availability for one server: the three window figures, a 30-day
 * daily strip, and the outage episodes behind them.
 */
export default function AvailabilityPanel({ serverId }: Props) {
  const { t, i18n } = useTranslation();
  const [result, setResult] = useState<AvailabilityResult | null>(null);
  const requestRef = useRef<AbortController | null>(null);
  const current = result?.serverId === serverId ? result : null;
  const detail = current?.detail ?? null;
  const windows = current?.windows;
  const loading = current === null;
  const failed = current?.failed ?? false;

  const load = useCallback(async () => {
    if (requestRef.current) return;
    const controller = new AbortController();
    requestRef.current = controller;
    try {
      // The fleet census is cached server-side for a minute, so asking it for
      // one server's window figures is cheaper than a second bespoke query.
      const [detailRes, fleetRes] = await Promise.all([
        uptimeApi.detail(serverId, 30, controller.signal),
        uptimeApi.fleet(controller.signal),
      ]);
      if (controller.signal.aborted || requestRef.current !== controller) return;
      setResult({ serverId, detail: detailRes.data, windows: fleetRes.data.servers.find((s) => s.server_id === serverId)?.windows, failed: false });
    } catch {
      if (controller.signal.aborted || requestRef.current !== controller) return;
      // Stop the sibling request when either half fails. The panel requires
      // both snapshots, so letting the remaining query finish serves no view.
      controller.abort();
      setResult(previous => ({ serverId, detail: previous?.serverId === serverId ? previous.detail : null,
        windows: previous?.serverId === serverId ? previous.windows : undefined, failed: true }));
    } finally {
      if (requestRef.current === controller) requestRef.current = null;
    }
  }, [serverId]);

  // Availability moves slowly; a 5-minute refresh is plenty.
  usePolling(() => load(), 300000, { leading: false });

  // Keyed on `load` so switching to another server refetches: this panel is
  // reused across servers without remounting.
  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    return () => {
      window.clearTimeout(initial);
      requestRef.current?.abort();
      requestRef.current = null;
    };
  }, [load]);

  const totals = useMemo(() => {
    if (!detail) return null;
    const measured = detail.days.filter((d) => !d.no_data);
    const observed = measured.reduce((sum, d) => sum + d.observed_buckets, 0);
    const expected = measured.reduce((sum, d) => sum + d.expected_buckets, 0);
    const downtime = detail.outages.reduce((sum, o) => sum + o.seconds, 0);
    return { observed, expected, downtime, days: measured.length };
  }, [detail]);

  // A server with no scored day yet has nothing to plot and, more importantly,
  // nothing to say about outages: the backend cannot bound an episode without a
  // sample to anchor it, so an empty list here would claim a clean record for a
  // host that has never actually been reached.
  const hasHistory = !!totals && totals.days > 0;

  if (loading) return <Skeleton active paragraph={{ rows: 5 }} />;
  if (failed) return <Alert type="warning" showIcon title={t('uptime.loadFailed')} />;

  return (
    <div className="availability-panel">
      <Alert
        className="availability-basis"
        type="info"
        showIcon
        title={t('uptime.basisTitle')}
        description={t('uptime.basisNote')}
      />

      {!!windows?.length && (
        <div className="availability-windows">
          {windows.map((w) => (
            <div key={w.window} className="availability-window">
              <small>{t(`uptime.window.${w.window}`)}</small>
              {w.no_data ? (
                <strong className="availability-pending">—</strong>
              ) : (
                <strong style={{ color: availabilityColor(w.percent) }}>{w.percent.toFixed(2)}%</strong>
              )}
              <span>
                {w.no_data && t('uptime.tooNew')}
                {!w.no_data && (w.partial
                  ? t('uptime.partialWindow')
                  : t('uptime.observedOf', { observed: w.observed_buckets, expected: w.expected_buckets }))}
              </span>
            </div>
          ))}
        </div>
      )}

      {!hasHistory && (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('uptime.noHistory')} />
      )}

      {hasHistory && (
      <div>
        <Text type="secondary" className="availability-label">{t('uptime.dailyStrip')}</Text>
        <div className="availability-strip">
          {detail?.days.map((day) => (
            <Tooltip
              key={day.day}
              // hover alone leaves keyboard users out; focus opens the same tip.
              trigger={['hover', 'focus']}
              title={day.no_data
                ? `${day.day} · ${t('uptime.noData')}`
                : `${day.day} · ${day.percent.toFixed(2)}% (${day.observed_buckets}/${day.expected_buckets})`}
            >
              {/*
                Every bar is full height and carries its value in colour alone.
                Scaling height by availability would make a fully-down day a
                zero-height bar, i.e. invisible — exactly the day that most
                needs to be seen.
              */}
              <i
                tabIndex={0}
                className={day.no_data ? 'no-data' : undefined}
                style={day.no_data ? undefined : { background: availabilityColor(day.percent) }}
              />
            </Tooltip>
          ))}
        </div>
        {totals && (
          <Text type="secondary" className="availability-footnote">
            {t('uptime.stripFootnote', { days: totals.days })}
          </Text>
        )}
      </div>
      )}

      {hasHistory && (
      <div>
        <Text type="secondary" className="availability-label">
          {t('uptime.outages', { count: detail?.outages.length || 0 })}
          {totals && totals.downtime > 0 && ` · ${t('uptime.totalDowntime', { duration: formatDuration(totals.downtime, t) })}`}
        </Text>
        {detail?.outages.length ? (
          <ul className="outage-list">
            {detail.outages.map((outage) => (
              <OutageRow key={`${outage.started_at}-${outage.seconds}`} outage={outage} t={t} lang={i18n.language} />
            ))}
          </ul>
        ) : (
          <div className="availability-clean">
            <CheckCircleOutlined />
            <span>{t('uptime.noOutages')}</span>
          </div>
        )}
        {detail?.outages.length === 50 && (
          <Text type="secondary" className="availability-footnote">
            <AlertOutlined /> {t('uptime.outagesCapped')}
          </Text>
        )}
      </div>
      )}
    </div>
  );
}
