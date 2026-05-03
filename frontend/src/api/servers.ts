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
  cpu_cores: number;
  memory_total: number;
  disk_total: number;
  has_docker: boolean;
  docker_version: string;
  last_seen_at: string | null;
  created_at: string;
  tags: Tag[];
  latest_metrics: LatestMetrics | null;
}

export interface LatestMetrics {
  cpu_percent: number;
  memory_used: number;
  memory_total: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  disk_rx_bytes: number;
  disk_tx_bytes: number;
  uptime_seconds: number;
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

export interface MetricPoint {
  cpu_percent: number;
  memory_used: number;
  memory_total: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  disk_rx_bytes: number;
  disk_tx_bytes: number;
  uptime_seconds: number;
  recorded_at: string;
}

export const serversApi = {
  list: () => client.get<Server[]>('/servers'),

  create: (data: {
    name: string;
    host: string;
    port?: number;
    ssh_username?: string;
    ssh_password?: string;
    ssh_key?: string;
    ssh_host_key?: string;
    credential_id?: string;
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
  }) => client.put<Server>(`/servers/${id}`, data),

  delete: (id: string) => client.delete(`/servers/${id}`),

  setTags: (id: string, tag_ids: string[]) =>
    client.put(`/servers/${id}/tags`, { tag_ids }),

  getLatestMetrics: (id: string) =>
    client.get<MetricPoint>(`/servers/${id}/metrics/latest`),

  getMetricsHistory: (id: string, since?: string, until?: string) =>
    client.get<MetricPoint[]>(`/servers/${id}/metrics`, { params: { since, until } }),

  checkDocker: (id: string) =>
    client.get<{ installed: boolean; version?: string }>(`/servers/${id}/docker/check`),

  getContainers: (id: string) =>
    client.get<DockerContainer[]>(`/servers/${id}/docker/containers`),

  containerAction: (id: string, containerId: string, action: 'start' | 'stop' | 'restart') =>
    client.post(`/servers/${id}/docker/containers/${containerId}/${action}`),

  getContainerLogs: (id: string, containerId: string, tail?: number) =>
    client.get<{ logs: string }>(`/servers/${id}/docker/containers/${containerId}/logs`, { params: { tail } }),
};

export const tagsApi = {
  list: () => client.get<Tag[]>('/tags'),
  create: (name: string, color?: string) =>
    client.post<Tag>('/tags', { name, color }),
  delete: (id: string) => client.delete(`/tags/${id}`),
};
