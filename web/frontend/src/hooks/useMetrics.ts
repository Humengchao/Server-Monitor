import { useState, useEffect, useCallback, useRef } from 'react';
import { serversApi, MetricPoint } from '../api/servers';
import { usePolling } from './usePolling';
import { responseClock, ResponseClock } from './useResponseClock';
import { MAX_FUTURE_SAMPLE_SKEW_MS } from '../utils/fleet';

export interface TimeRange {
  since: string;
  until: string;
}

export const METRIC_ONLINE_WINDOW_MS = 120000;

/**
 * Judge freshness against the API clock rather than the browser's wall clock.
 * Keeping this in one place avoids subtly different online checks across
 * pages and treats malformed/future timestamps conservatively.
 */
export function isMetricFresh(metrics: MetricPoint | null, observedAt: number, windowMs = METRIC_ONLINE_WINDOW_MS): boolean {
  if (!metrics?.recorded_at || !Number.isFinite(observedAt) || observedAt <= 0) return false;
  const recordedAt = Date.parse(metrics.recorded_at);
  if (!Number.isFinite(recordedAt)) return false;
  const age = observedAt - recordedAt;
  // A small amount of clock skew is fine, but a wildly future timestamp is
  // not evidence that a host is alive.
  return age >= -MAX_FUTURE_SAMPLE_SKEW_MS && age < windowMs;
}

interface LatestResult {
  serverId: string;
  metrics: MetricPoint | null;
  clock: ResponseClock | null;
  error: boolean;
}

interface HistoryResult {
  key: string;
  data: MetricPoint[];
  loading: boolean;
  error: boolean;
}

const EMPTY_HISTORY: MetricPoint[] = [];

export function useMetrics(serverId: string, timeRange: TimeRange, options: { interval?: number; historyEnabled?: boolean } = {}) {
  const { interval = 3000, historyEnabled = true } = options;
  const [latest, setLatest] = useState<LatestResult | null>(null);
  const [history, setHistory] = useState<HistoryResult | null>(null);
  const latestAbortRef = useRef<AbortController | null>(null);
  const historyAbortRef = useRef<AbortController | null>(null);
  const completedHistoryKeyRef = useRef<string | null>(null);
  const { since, until } = timeRange;
  const historyKey = JSON.stringify([serverId, since, until]);

  const fetchLatest = useCallback(async () => {
    // The initial read is outside usePolling's own serialization. Give it the
    // full request deadline instead of restarting it on the first 3s tick.
    if (latestAbortRef.current && !latestAbortRef.current.signal.aborted) return;
    const controller = new AbortController();
    latestAbortRef.current = controller;
    try {
      const res = await serversApi.getLatestMetrics(serverId, controller.signal);
      if (controller.signal.aborted || latestAbortRef.current !== controller) return;
      setLatest({ serverId, metrics: res.data ?? null, clock: responseClock(res.headers?.date), error: false });
    } catch {
      if (controller.signal.aborted || latestAbortRef.current !== controller) return;
      // A failed refresh must not reset the original sample's clock.
      setLatest((current) => current?.serverId === serverId
        ? { ...current, error: true }
        : { serverId, metrics: null, clock: null, error: true });
    } finally {
      if (latestAbortRef.current === controller) latestAbortRef.current = null;
    }
  }, [serverId]);

  const fetchHistory = useCallback(async () => {
    if (!historyEnabled) return;
    historyAbortRef.current?.abort();
    const controller = new AbortController();
    historyAbortRef.current = controller;
    completedHistoryKeyRef.current = null;
    setHistory({ key: historyKey, data: EMPTY_HISTORY, loading: true, error: false });
    try {
      const res = await serversApi.getMetricsHistory(serverId, since, until, controller.signal);
      if (controller.signal.aborted || historyAbortRef.current !== controller) return;
      completedHistoryKeyRef.current = historyKey;
      setHistory({ key: historyKey, data: res.data || EMPTY_HISTORY, loading: false, error: false });
    } catch {
      if (controller.signal.aborted || historyAbortRef.current !== controller) return;
      setHistory({ key: historyKey, data: EMPTY_HISTORY, loading: false, error: true });
    } finally {
      if (historyAbortRef.current === controller) historyAbortRef.current = null;
    }
  }, [serverId, since, until, historyKey, historyEnabled]);

  usePolling(fetchLatest, interval, { leading: false });

  useEffect(() => {
    const timer = window.setTimeout(() => { void fetchLatest(); }, 0);
    return () => {
      window.clearTimeout(timer);
      latestAbortRef.current?.abort();
    };
  }, [fetchLatest]);

  useEffect(() => {
    // A hidden chart has no consumer. Keep a completed fixed window in memory
    // so reopening it does not repeat the read; interrupted reads can retry.
    if (!historyEnabled || completedHistoryKeyRef.current === historyKey) return;
    const timer = window.setTimeout(() => { void fetchHistory(); }, 0);
    return () => {
      window.clearTimeout(timer);
      historyAbortRef.current?.abort();
    };
  }, [fetchHistory, historyEnabled, historyKey]);

  // Rendering can precede effect cleanup. Keying the results prevents a
  // previous host/window from being displayed or exported during that render.
  const currentLatest = latest?.serverId === serverId ? latest : null;
  const currentHistory = history?.key === historyKey ? history : null;
  return {
    metrics: currentLatest?.metrics ?? null,
    clock: currentLatest?.clock ?? null,
    loading: currentLatest === null,
    latestError: currentLatest?.error ?? false,
    history: currentHistory?.data ?? EMPTY_HISTORY,
    historyLoading: currentHistory?.loading ?? true,
    historyError: currentHistory?.error ?? false,
    refetchLatest: fetchLatest,
    refetchHistory: fetchHistory,
  };
}
