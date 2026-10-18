import client from './client';
export interface NetworkSample {
  recorded_at: string; status: 'ok' | 'loss' | 'down' | 'error' | 'unknown'; sent: number | null; received: number | null;
  loss_percent: number | null; avg_ms: number | null; min_ms: number | null; max_ms: number | null; mdev_ms: number | null;
  error_code?: string; threshold_exceeded: boolean;
}
export interface NetworkCheckInput { name: string; source_server_id: string | null; target_server_id: string; packets: number; enabled: boolean; loss_threshold: number; latency_threshold_ms: number | null }
export interface NetworkCheck extends NetworkCheckInput { id: string; created_at: string; latest: NetworkSample | null }
export interface NetworkHistory { samples: NetworkSample[]; truncated: boolean; summary: { sent: number; received: number; loss_percent: number | null; avg_ms: number | null; known_samples: number; unknown_samples: number } }
export const networkApi = {
  list: (signal?: AbortSignal) => client.get<NetworkCheck[]>('/network/checks', { signal }),
  create: (value: NetworkCheckInput) => client.post<NetworkCheck>('/network/checks', value),
  update: (id: string, value: NetworkCheckInput) => client.put<NetworkCheck>(`/network/checks/${id}`, value),
  delete: (id: string) => client.delete(`/network/checks/${id}`),
  run: (id: string, signal?: AbortSignal) => client.post<NetworkSample>(`/network/checks/${id}/run`, undefined, { signal, timeout: 30000 }),
  history: (id: string, since: string, until: string, signal?: AbortSignal) => client.get<NetworkHistory>(`/network/checks/${id}/history`, { params: { since, until, limit: 2000 }, signal }),
};
