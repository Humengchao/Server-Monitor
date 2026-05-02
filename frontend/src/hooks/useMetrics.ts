import { useState, useEffect, useCallback, useRef } from 'react';
import { serversApi, MetricPoint } from '../api/servers';

export interface TimeRange {
  since: string;
  until: string;
}

function metricsChanged(a: MetricPoint | null, b: MetricPoint | null): boolean {
  if (!a && !b) return false;
  if (!a || !b) return true;
  return (
    a.cpu_percent !== b.cpu_percent ||
    a.memory_used !== b.memory_used ||
    a.network_rx_bytes !== b.network_rx_bytes ||
    a.network_tx_bytes !== b.network_tx_bytes ||
    a.disk_rx_bytes !== b.disk_rx_bytes ||
    a.disk_tx_bytes !== b.disk_tx_bytes ||
    a.uptime_seconds !== b.uptime_seconds
  );
}

export function useMetrics(serverId: string, timeRange: TimeRange, interval = 3000) {
  const [metrics, setMetrics] = useState<MetricPoint | null>(null);
  const [history, setHistory] = useState<MetricPoint[]>([]);
  const [loading, setLoading] = useState(true);
  const timeRangeRef = useRef(timeRange);
  timeRangeRef.current = timeRange;

  const fetchLatest = useCallback(async () => {
    try {
      const res = await serversApi.getLatestMetrics(serverId);
      const newMetrics = res.data ?? null;
      setMetrics((prev) => (metricsChanged(prev, newMetrics) ? newMetrics : prev));
    } catch {
      // ignore
    } finally {
      setLoading(false);
    }
  }, [serverId]);

  const fetchHistory = useCallback(async () => {
    try {
      const range = timeRangeRef.current;
      const res = await serversApi.getMetricsHistory(serverId, range.since, range.until);
      setHistory(res.data || []);
    } catch {
      // ignore
    }
  }, [serverId]);

  useEffect(() => {
    fetchLatest();
    const timer = setInterval(fetchLatest, interval);
    return () => clearInterval(timer);
  }, [fetchLatest, interval]);

  useEffect(() => {
    fetchHistory();
  }, [fetchHistory, timeRange.since, timeRange.until]);

  return { metrics, history, loading, refetchLatest: fetchLatest, refetchHistory: fetchHistory };
}
