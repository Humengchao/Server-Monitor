import { useState, useEffect, useCallback } from 'react';
import { serversApi, MetricPoint } from '../api/servers';

export function useMetrics(serverId: string, interval = 10000) {
  const [metrics, setMetrics] = useState<MetricPoint | null>(null);
  const [history, setHistory] = useState<MetricPoint[]>([]);
  const [loading, setLoading] = useState(true);

  const fetchLatest = useCallback(async () => {
    try {
      const res = await serversApi.getLatestMetrics(serverId);
      setMetrics(res.data);
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [serverId]);

  const fetchHistory = useCallback(async () => {
    try {
      const since = new Date(Date.now() - 3600 * 1000).toISOString();
      const res = await serversApi.getMetricsHistory(serverId, since);
      setHistory(res.data || []);
    } catch {
      // ignore
    }
  }, [serverId]);

  useEffect(() => {
    fetchLatest();
    fetchHistory();
    const timer = setInterval(fetchLatest, interval);
    return () => clearInterval(timer);
  }, [fetchLatest, fetchHistory, interval]);

  return { metrics, history, loading, refetch: fetchLatest };
}
