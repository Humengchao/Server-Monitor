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

export interface AlertEventPage {
  events: AlertEvent[];
  /** Rows matching the current filters, before the page window. */
  total: number;
}

export interface AlertEventQuery {
  activeOnly?: boolean;
  serverId?: string;
  limit?: number;
  offset?: number;
}

export const alertsApi = {
  listRules: () => client.get<AlertRule[]>('/alerts/rules'),
  createRule: (data: AlertRulePayload) => client.post<AlertRule>('/alerts/rules', data),
  updateRule: (id: string, data: AlertRulePayload) => client.put<AlertRule>(`/alerts/rules/${id}`, data),
  deleteRule: (id: string) => client.delete(`/alerts/rules/${id}`),
  listEvents: (query: AlertEventQuery = {}) =>
    client.get<AlertEventPage>('/alerts/events', {
      params: {
        active: query.activeOnly ? 1 : undefined,
        limit: query.limit,
        offset: query.offset || undefined,
        server_id: query.serverId || undefined,
      },
    }),
  summary: () => client.get<{ active: number }>('/alerts/summary'),
  testWebhook: (webhook_url: string) => client.post('/alerts/test', { webhook_url }),
};
