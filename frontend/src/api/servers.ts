import client from './client';

export interface Server {
  id: string;
  user_id: string;
  name: string;
  host: string;
  port: number;
  ssh_username: string;
  ssh_host_key?: string;
  credential_id?: string;
  credential_name?: string;
  server_type: string;
  cpu_cores: number;
  memory_total: number;
  disk_total: number;
  has_docker: boolean;
  docker_version: string;
  expires_at?: string | null;
  billing_price: number;
  billing_currency: string;
  billing_cycle: string;
  traffic_limit_bytes: number;
  public_location: string;
  notes?: string;
  created_at: string;
  tags: Tag[];
  latest_metrics: LatestMetrics | null;
}

export interface LatestMetrics {
  cpu_percent: number;
  load_1: number;
  load_5: number;
  load_15: number;
  memory_used: number;
  memory_total: number;
  disk_used: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  network_rx_total_bytes: number;
  network_tx_total_bytes: number;
  disk_rx_bytes: number;
  disk_tx_bytes: number;
  uptime_seconds: number;
  latency_ms: number;
  recorded_at: string;
}

export interface Tag {
  id: string;
  user_id: string;
  name: string;
  color: string;
}

export interface DockerContainer {
  id: string;
  name: string;
  image: string;
  status: string;
  state: string;
  ports: string;
  created: string;
}

export interface ProcessInfo {
  pid: number;
  user: string;
  cpu_percent: number;
  mem_percent: number;
  rss_bytes: number;
  state: string;
  /** 0 when the host's ps could not report an elapsed time. */
  elapsed_seconds: number;
  command: string;
}

export interface ProcessListResponse {
  processes: ProcessInfo[];
  /** Processes on the host, before the response cap. */
  total: number;
  returned: number;
}

export interface BatchResult {
  server_id: string;
  server_name: string;
  ok: boolean;
  /** Combined stdout+stderr, capped server-side. */
  output: string;
  error: string;
  truncated: boolean;
  duration_ms: number;
}

export interface BatchExecResponse {
  results: BatchResult[];
  succeeded: number;
  failed: number;
}

/** Mirrors services.BatchMaxTargets on the backend. */
export const BATCH_MAX_TARGETS = 50;

export interface MetricPoint {
  cpu_percent: number;
  load_1: number;
  load_5: number;
  load_15: number;
  memory_used: number;
  memory_total: number;
  disk_used: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  network_rx_total_bytes: number;
  network_tx_total_bytes: number;
  disk_rx_bytes: number;
  disk_tx_bytes: number;
  uptime_seconds: number;
  latency_ms: number;
  recorded_at: string;
}

export const serversApi = {
  list: () => client.get<Server[]>('/servers'),

  get: (id: string) => client.get<Server>(`/servers/${id}`),

  create: (data: {
    name: string;
    host: string;
    port?: number;
    ssh_username?: string;
    ssh_password?: string;
    ssh_key?: string;
    ssh_host_key?: string;
    credential_id?: string;
    expires_at?: string | null;
    billing_price?: number;
    billing_currency?: string;
    billing_cycle?: string;
    traffic_limit_bytes?: number;
    public_location?: string;
    notes?: string;
    server_type?: string;
  }) => client.post<Server>('/servers', data),

  update: (id: string, data: {
    name: string;
    host: string;
    port?: number;
    ssh_username?: string;
    ssh_password?: string;
    ssh_key?: string;
    ssh_host_key?: string;
    credential_id?: string;
    expires_at?: string | null;
    billing_price?: number;
    billing_currency?: string;
    billing_cycle?: string;
    traffic_limit_bytes?: number;
    public_location?: string;
    notes?: string;
    server_type?: string;
  }) => client.put<Server>(`/servers/${id}`, data),

  delete: (id: string) => client.delete(`/servers/${id}`),

  setTags: (id: string, tag_ids: string[]) =>
    client.put(`/servers/${id}/tags`, { tag_ids }),

  getLatestMetrics: (id: string) =>
    client.get<MetricPoint>(`/servers/${id}/metrics/latest`),

  getMetricsHistory: (id: string, since?: string, until?: string) =>
    client.get<MetricPoint[]>(`/servers/${id}/metrics`, { params: { since, until } }),

  bulkTags: (server_ids: string[], tag_ids: string[], action: 'add' | 'remove') =>
    client.post<{ message: string; servers: number }>('/servers/bulk/tags', { server_ids, tag_ids, action }),

  bulkDelete: (server_ids: string[]) =>
    client.post<{ message: string; deleted: number }>('/servers/bulk/delete', { server_ids }),

  // Fans a command out over SSH; allow well past the default axios timeout
  // because the backend waits up to 60s per host.
  bulkExec: (server_ids: string[], command: string) =>
    client.post<BatchExecResponse>('/servers/bulk/exec', { server_ids, command }, { timeout: 120000 }),

  getProcesses: (id: string, signal?: AbortSignal) =>
    client.get<ProcessListResponse>(`/servers/${id}/processes`, { signal, timeout: 20000 }),

  killProcess: (id: string, pid: number, force = false) =>
    client.delete(`/servers/${id}/processes/${pid}`, { params: force ? { force: 1 } : undefined }),

  checkDocker: (id: string) =>
    client.get<{ installed: boolean; version?: string }>(`/servers/${id}/docker/check`),
  /** Asks the host directly and updates the stored flag — the recovery path for a
   *  server whose Docker flag was lost to a transient probe failure. */
  redetectDocker: (id: string) =>
    client.get<{ installed: boolean; version?: string; refreshed: boolean }>(
      `/servers/${id}/docker/check`, { params: { refresh: 1 } }),

  getContainers: (id: string) =>
    client.get<DockerContainer[]>(`/servers/${id}/docker/containers`),

  containerAction: (id: string, containerId: string, action: 'start' | 'stop' | 'restart') =>
    client.post(`/servers/${id}/docker/containers/${containerId}/${action}`),

  getContainerLogs: (id: string, containerId: string, tail?: number, signal?: AbortSignal) =>
    client.get<{ logs: string }>(`/servers/${id}/docker/containers/${containerId}/logs`, { params: { tail }, signal }),
};

export const tagsApi = {
  list: () => client.get<Tag[]>('/tags'),
  create: (name: string, color?: string) =>
    client.post<Tag>('/tags', { name, color }),
  delete: (id: string) => client.delete(`/tags/${id}`),
};
