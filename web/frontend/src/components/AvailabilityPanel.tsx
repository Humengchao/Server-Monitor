import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Alert, Empty, Skeleton, Tooltip, Typography } from 'antd';
import { AlertOutlined, CheckCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import {
  availabilityColor, uptimeApi, Outage, ServerUptimeDetail, UptimeWindow,
} from '../api/uptime';
import { usePolling } from '../hooks/usePolling';

const { Text } = Typography;

interface Props {
  serverId: string;
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

function OutageRow({ outage, t }: { outage: Outage; t: (k: string, o?: Record<string, unknown>) => string }) {
  return (
    <li className={outage.ongoing ? 'ongoing' : undefined}>
      <span className="outage-dot" />
      <span className="outage-when">
        {new Date(outage.started_at).toLocaleString()}
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
  const { t } = useTranslation();
  const [detail, setDetail] = useState<ServerUptimeDetail | null>(null);
  const [windows, setWindows] = useState<UptimeWindow[] | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);

  const load = useCallback(async () => {
    try {
      // The fleet census is cached server-side for a minute, so asking it for
      // one server's window figures is cheaper than a second bespoke query.
      const [detailRes, fleetRes] = await Promise.all([
        uptimeApi.detail(serverId, 30),
        uptimeApi.fleet(),
      ]);
      setDetail(detailRes.data);
      setWindows(fleetRes.data.servers.find((s) => s.server_id === serverId)?.windows);
      setFailed(false);
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, [serverId]);

  // Availability moves slowly; a 5-minute refresh is plenty.
  usePolling(() => load(), 300000, { leading: false });

  // Keyed on `load` so switching to another server refetches: this panel is
  // reused across servers without remounting.
  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    return () => window.clearTimeout(initial);
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
              <OutageRow key={`${outage.started_at}-${outage.seconds}`} outage={outage} t={t} />
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
