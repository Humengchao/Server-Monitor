import { useState, useEffect, useCallback, useRef } from 'react';
import { serversApi, MetricPoint } from '../api/servers';
import { usePolling } from './usePolling';

export interface TimeRange {
  since: string;
  until: string;
}

function metricsChanged(a: MetricPoint | null, b: MetricPoint | null): boolean {
  if (!a && !b) return false;
  if (!a || !b) return true;
  return (
    a.cpu_percent !== b.cpu_percent ||
    a.load_1 !== b.load_1 ||
    a.load_5 !== b.load_5 ||
    a.load_15 !== b.load_15 ||
    a.memory_used !== b.memory_used ||
    a.memory_total !== b.memory_total ||
    a.disk_used !== b.disk_used ||
    a.network_rx_bytes !== b.network_rx_bytes ||
    a.network_tx_bytes !== b.network_tx_bytes ||
    a.network_rx_total_bytes !== b.network_rx_total_bytes ||
    a.network_tx_total_bytes !== b.network_tx_total_bytes ||
    a.disk_rx_bytes !== b.disk_rx_bytes ||
    a.disk_tx_bytes !== b.disk_tx_bytes ||
    a.uptime_seconds !== b.uptime_seconds ||
    a.latency_ms !== b.latency_ms
  );
}

export function useMetrics(serverId: string, timeRange: TimeRange, interval = 3000) {
  const [metrics, setMetrics] = useState<MetricPoint | null>(null);
  const [history, setHistory] = useState<MetricPoint[]>([]);
  const [loading, setLoading] = useState(true);
  // The API's Date header, used to judge liveness against the server's clock
  // rather than the browser's: local skew beyond the online window would
  // otherwise mislabel a healthy host.
  const [observedAt, setObservedAt] = useState(0);
  const latestAbortRef = useRef<AbortController | null>(null);
  const historyAbortRef = useRef<AbortController | null>(null);
  const timeRangeRef = useRef(timeRange);
  useEffect(() => {
    timeRangeRef.current = timeRange;
  }, [timeRange]);

  const fetchLatest = useCallback(async () => {
    latestAbortRef.current?.abort();
    const controller = new AbortController();
    latestAbortRef.current = controller;
    try {
      const res = await serversApi.getLatestMetrics(serverId, controller.signal);
      const newMetrics = res.data ?? null;
      setMetrics((prev) => (metricsChanged(prev, newMetrics) ? newMetrics : prev));
      const dateHeader = res.headers?.date;
      const serverNow = typeof dateHeader === 'string' ? Date.parse(dateHeader) : NaN;
      setObservedAt(Number.isNaN(serverNow) ? Date.now() : serverNow);
    } catch {
      // ignore
    } finally {
      if (latestAbortRef.current === controller) {
        latestAbortRef.current = null;
        setLoading(false);
      }
    }
  }, [serverId]);

  const fetchHistory = useCallback(async () => {
    historyAbortRef.current?.abort();
    const controller = new AbortController();
    historyAbortRef.current = controller;
    try {
      const range = timeRangeRef.current;
      const res = await serversApi.getMetricsHistory(serverId, range.since, range.until, controller.signal);
      setHistory(res.data || []);
    } catch {
      // ignore
    } finally {
      if (historyAbortRef.current === controller) historyAbortRef.current = null;
    }
  }, [serverId]);

  usePolling(fetchLatest, interval, { leading: false });

  useEffect(() => {
    // Defer the initial request so the effect itself does not synchronously
    // trigger state updates during the commit phase.
    const timer = window.setTimeout(() => { void fetchLatest(); }, 0);
    return () => window.clearTimeout(timer);
  }, [fetchLatest]);

  useEffect(() => {
    fetchHistory();
  }, [fetchHistory, timeRange.since, timeRange.until]);

  useEffect(() => () => {
    latestAbortRef.current?.abort();
    historyAbortRef.current?.abort();
  }, [serverId]);

  return { metrics, history, loading, observedAt, refetchLatest: fetchLatest, refetchHistory: fetchHistory };
}
