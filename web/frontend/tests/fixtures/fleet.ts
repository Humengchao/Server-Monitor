import type { Server } from '../../src/api/servers';

export const observedAt = Date.parse('2030-06-15T12:00:00Z');
export const GB = 1024 ** 3;
export function host(id: string, overrides: Partial<Server> = {}): Server {
  return {
    id, name: id, host: '192.0.2.1', port: 22, ssh_username: 'monitor',
    server_type: 'linux', cpu_cores: 2, memory_total: 8 * GB, disk_total: 100 * GB,
    has_docker: false, docker_version: '', billing_price: 0, billing_currency: 'CNY',
    billing_cycle: 'month', traffic_limit_bytes: 0, public_location: '',
    created_at: '2030-01-01T00:00:00Z', tags: [],
    latest_metrics: {
      recorded_at: new Date(observedAt - 10000).toISOString(), cpu_percent: 0,
      memory_used: 2 * GB, memory_total: 8 * GB, disk_used: 20 * GB,
      network_rx_bytes: 1024, network_tx_bytes: 2048, network_rx_total_bytes: 0, network_tx_total_bytes: 0,
      disk_rx_bytes: 0, disk_tx_bytes: 0, load_1: 0, load_5: 0, load_15: 0, uptime_seconds: 86400, latency_ms: 5,
    }, ...overrides,
  };
}
export function sampleFleet(): Server[] {
  const busy = host('busy-host');
  busy.latest_metrics!.cpu_percent = 92;
  busy.latest_metrics!.memory_used = 28 * GB;
  busy.latest_metrics!.memory_total = 32 * GB;
  busy.memory_total = 32 * GB;
  busy.expires_at = '2030-06-20T12:00:00Z';
  busy.tags = [{ id: 'production', user_id: 'test', name: 'Production', color: '#5d7df7' }];
  const offline = host('offline-host', { expires_at: '2030-06-01T12:00:00Z' });
  offline.latest_metrics!.recorded_at = new Date(observedAt - 600000).toISOString();
  offline.latest_metrics!.cpu_percent = 99;
  offline.latest_metrics!.network_rx_bytes = 50 * GB;
  return [host('idle-host'), busy, offline, host('pending-host', { latest_metrics: null, cpu_cores: 0, memory_total: 0, disk_total: 0 })];
}
