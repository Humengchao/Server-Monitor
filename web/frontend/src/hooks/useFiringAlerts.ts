import { useCallback, useEffect, useRef, useState } from 'react';
import { alertsApi, ServerAlertSummary } from '../api/alerts';
import { usePolling } from './usePolling';

// The alert engine evaluates on its own cadence (ALERT_INTERVAL, 30s by
// default), so polling faster than that only costs requests.
const REFRESH_MS = 20000;

export interface FiringAlerts {
  /** Active counts and rule names for every firing server. */
  byServer: Map<string, ServerAlertSummary>;
  loading: boolean;
  error: boolean;
}

/**
 * Which servers are currently firing an alert.
 *
 * The dashboard and the alert centre were entirely disconnected: a host could
 * have four rules firing and its card looked no different from a healthy one.
 * This is the shared source both list views read so the state is visible where
 * people actually look.
 */
export function useFiringAlerts(): FiringAlerts {
  const [byServer, setByServer] = useState<Map<string, ServerAlertSummary>>(new Map());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const requestRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    if (requestRef.current) return;
    const controller = new AbortController();
    requestRef.current = controller;
    try {
      // The event history is capped; an aggregate keeps older active alerts
      // visible even when newer incidents fill that entire page.
      const res = await alertsApi.serverSummary(controller.signal);
      if (controller.signal.aborted || requestRef.current !== controller) return;
      const next = new Map<string, ServerAlertSummary>();
      for (const server of res.data.servers || []) {
        if (server.active > 0) next.set(server.server_id, server);
      }
      setByServer(next);
      setError(false);
    } catch {
      if (controller.signal.aborted || requestRef.current !== controller) return;
      // Alert state is supplementary here; a failure must not disturb the list.
      setError(true);
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null;
        if (!controller.signal.aborted) setLoading(false);
      }
    }
  }, []);

  usePolling(() => load(), REFRESH_MS);

  useEffect(() => () => {
    requestRef.current?.abort();
    requestRef.current = null;
  }, []);

  return { byServer, loading, error };
}
