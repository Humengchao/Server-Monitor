import client from './client';

export type AlertMetric = 'cpu' | 'memory' | 'disk' | 'load1' | 'latency' | 'offline';

export interface AlertRule {
  id: string;
  user_id: string;
  name: string;
  server_id: string | null;
  server_name?: string;
  metric: AlertMetric;
  comparator: '>' | '<';
  threshold: number;
  duration_seconds: number;
  enabled: boolean;
  webhook_url: string;
  created_at: string;
  firing_count: number;
}

export interface AlertEvent {
  id: number;
  rule_id: string;
  rule_name: string;
  metric: AlertMetric;
  server_id: string;
  server_name: string;
  value: number;
  /** English summary recorded by the engine; used when the rule is gone. */
  message: string;
  comparator: '>' | '<';
  threshold: number;
  duration_seconds: number;
  started_at: string;
  resolved_at: string | null;
}

export type AlertEventState = 'active' | 'resolved' | 'all';
export interface AlertEventPage { events: AlertEvent[]; next_cursor?: string }

export interface ServerAlertSummary {
  server_id: string;
  active: number;
  rule_names: string[];
}

export interface ActiveAlertSummary {
  active: number;
  servers: ServerAlertSummary[];
}

export interface AlertRulePayload {
  name: string;
  server_id: string | null;
  metric: AlertMetric;
  comparator: '>' | '<';
  threshold: number;
  duration_seconds: number;
  enabled: boolean;
  webhook_url: string;
}

// Metrics measured as a percentage of a known total; the UI renders them with a
// "%" suffix and clamps their threshold input to 0-100.
export const PERCENT_METRICS: AlertMetric[] = ['cpu', 'memory', 'disk'];

export const METRIC_UNITS: Record<AlertMetric, string> = {
  cpu: '%',
  memory: '%',
  disk: '%',
  load1: '',
  latency: 'ms',
  offline: '',
};

export const alertsApi = {
  listRules: (signal?: AbortSignal) => client.get<AlertRule[]>('/alerts/rules', { signal }),
  createRule: (data: AlertRulePayload) => client.post<AlertRule>('/alerts/rules', data),
  updateRule: (id: string, data: AlertRulePayload) => client.put<AlertRule>(`/alerts/rules/${id}`, data),
  deleteRule: (id: string) => client.delete(`/alerts/rules/${id}`),
  listEvents: (activeOnly = false, limit = 100, signal?: AbortSignal) =>
    client.get<AlertEvent[]>('/alerts/events', { params: { active: activeOnly ? 1 : undefined, limit }, signal }),
  eventPage: (state: AlertEventState, cursor = '', signal?: AbortSignal) =>
    client.get<AlertEventPage>('/alerts/events/page', { params: { state, limit: 100, cursor: cursor || undefined }, signal }),
  summary: (signal?: AbortSignal) => client.get<{ active: number }>('/alerts/summary', { signal }),
  serverSummary: (signal?: AbortSignal) =>
    client.get<ActiveAlertSummary>('/alerts/summary', { params: { by_server: 1 }, signal }),
  testWebhook: (webhook_url: string) => client.post('/alerts/test', { webhook_url }),
};
