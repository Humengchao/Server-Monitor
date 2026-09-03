import { useCallback, useState } from 'react';
import { alertsApi, AlertEvent } from '../api/alerts';
import { usePolling } from './usePolling';

// The alert engine evaluates on its own cadence (ALERT_INTERVAL, 30s by
// default), so polling faster than that only costs requests.
const REFRESH_MS = 20000;

export interface FiringAlerts {
  /** Currently-firing events keyed by server ID; a server may have several. */
  byServer: Map<string, AlertEvent[]>;
  loading: boolean;
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
  const [byServer, setByServer] = useState<Map<string, AlertEvent[]>>(new Map());
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    try {
      // active=1 so the server does the filtering: on a fleet with a long
      // alert history, pulling everything just to discard the resolved ones
      // wastes the round trip.
      const res = await alertsApi.listEvents(true, 200);
      const next = new Map<string, AlertEvent[]>();
      for (const event of res.data || []) {
        if (event.resolved_at || !event.server_id) continue;
        const list = next.get(event.server_id);
        if (list) list.push(event);
        else next.set(event.server_id, [event]);
      }
      setByServer(next);
    } catch {
      // Alert state is supplementary here; a failure must not disturb the list.
    } finally {
      setLoading(false);
    }
  }, []);

  usePolling(() => load(), REFRESH_MS);

  return { byServer, loading };
}
