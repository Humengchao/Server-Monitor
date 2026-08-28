import { useCallback, useEffect, useState } from 'react';
import { uptimeApi, UptimeWindow, UptimeWindowKey } from '../api/uptime';

const REFRESH_MS = 60000;

export interface FleetUptimeState {
  /** Availability windows keyed by server ID. */
  byServer: Map<string, UptimeWindow[]>;
  loading: boolean;
}

/**
 * Availability for every server, refreshed once a minute.
 *
 * Deliberately separate from the dashboard's 3-second server poll: the census
 * scans a month of rollup buckets, and the figure cannot move meaningfully
 * inside a minute. The backend caches it per user for the same reason.
 */
export function useFleetUptime(): FleetUptimeState {
  const [byServer, setByServer] = useState<Map<string, UptimeWindow[]>>(new Map());
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    try {
      const res = await uptimeApi.fleet();
      const next = new Map<string, UptimeWindow[]>();
      for (const entry of res.data.servers || []) {
        next.set(entry.server_id, entry.windows);
      }
      setByServer(next);
    } catch {
      // Availability is supplementary; a failure must not disturb the page.
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => load(), 0);
    const timer = window.setInterval(() => load(), REFRESH_MS);
    return () => { window.clearTimeout(initial); window.clearInterval(timer); };
  }, [load]);

  return { byServer, loading };
}

/**
 * Picks one window's percentage, or undefined when it has not loaded yet — or
 * when the server is too new for the window to mean anything. A server added a
 * minute ago reports 0/0 buckets, and rendering that as 0% would read as a total
 * outage instead of "not yet".
 */
export function windowPercent(
  windows: UptimeWindow[] | undefined,
  key: UptimeWindowKey,
): number | undefined {
  const match = windows?.find((w) => w.window === key);
  if (!match || match.no_data) return undefined;
  return match.percent;
}
