import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

type Reader<T> = (serverId: string, signal: AbortSignal) => Promise<{ data: T }>;

interface RequestScope {
  key: { serverId: string };
  request: { controller: AbortController; promise: Promise<void> } | null;
  refreshTimer: number | undefined;
}

interface PanelState<T> {
  scope: RequestScope['key'];
  data: T | null;
  loading: boolean;
  // An empty message means the caller should use its localized fallback.
  error: string | null;
}

/** Share expensive SSH reads between the initial load, refresh button and poll. */
export function useHostPanel<T>(serverId: string, read: Reader<T>) {
  const scopeKey = useMemo(() => ({ serverId }), [serverId]);
  const scopeRef = useRef<RequestScope | null>(null);
  const [state, setState] = useState<PanelState<T>>({ scope: scopeKey, data: null, loading: true, error: null });

  const isActive = useCallback(() => scopeRef.current?.key === scopeKey, [scopeKey]);

  const load = useCallback((showLoading = true): Promise<void> => {
    const scope = scopeRef.current;
    if (!scope || scope.key !== scopeKey) return Promise.resolve();
    if (showLoading) {
      setState(current => current.scope === scopeKey
        ? { ...current, loading: true }
        : { scope: scopeKey, data: null, loading: true, error: null });
    }
    if (scope.request) return scope.request.promise;

    const controller = new AbortController();
    const promise = Promise.resolve().then(async () => {
      if (scopeRef.current !== scope || controller.signal.aborted) return;
      try {
        const response = await read(scopeKey.serverId, controller.signal);
        if (scopeRef.current !== scope || controller.signal.aborted) return;
        setState({ scope: scopeKey, data: response.data, error: null, loading: false });
      } catch (error: unknown) {
        if (scopeRef.current !== scope || controller.signal.aborted) return;
        if ((error as { code?: string })?.code === 'ERR_CANCELED') return;
        const detail = (error as { response?: { data?: { error?: string } } })?.response?.data?.error;
        setState(current => ({
          scope: scopeKey, data: current.scope === scopeKey ? current.data : null, error: detail || '', loading: false,
        }));
      } finally {
        if (scope.request?.controller === controller) scope.request = null;
      }
    });
    scope.request = { controller, promise };
    return promise;
  }, [scopeKey, read]);

  const scheduleReload = useCallback((delayMs: number) => {
    const scope = scopeRef.current;
    if (!scope || scope.key !== scopeKey) return;
    window.clearTimeout(scope.refreshTimer);
    scope.refreshTimer = window.setTimeout(async () => {
      scope.refreshTimer = undefined;
      // An action needs a new snapshot, even if a read started before it settled.
      // Wait for that read instead of repeatedly aborting a slow SSH command.
      await scope.request?.promise;
      await load(false);
    }, delayMs);
  }, [scopeKey, load]);

  useEffect(() => {
    const scope: RequestScope = { key: scopeKey, request: null, refreshTimer: undefined };
    scopeRef.current = scope;
    const initial = window.setTimeout(() => { void load(); }, 0);
    return () => {
      scopeRef.current = null;
      window.clearTimeout(initial);
      window.clearTimeout(scope.refreshTimer);
      scope.request?.controller.abort();
      scope.request = null;
    };
  }, [scopeKey, load]);

  // Never expose the previous host's rows, including the render before cleanup.
  return {
    data: state.scope === scopeKey ? state.data : null,
    loading: state.scope === scopeKey ? state.loading : true,
    error: state.scope === scopeKey ? state.error : null,
    load, scheduleReload, isActive,
  };
}
