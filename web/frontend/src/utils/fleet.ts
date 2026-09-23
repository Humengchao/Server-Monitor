import type { Server } from '../api/servers';

export type ServerStatus = 'online' | 'offline' | 'pending';
export type ResourceMetric = 'cpu' | 'memory' | 'disk';
export type FleetSort = 'default' | 'name' | ResourceMetric | 'uptime' | 'expiry';
export const ONLINE_WINDOW_MS = 120000;
// A host's clock can be slightly ahead of the API's response clock. Keep the
// tolerance the same on the dashboard and detail page so a future-dated sample
// is never shown as online in one view and offline in the other.
export const MAX_FUTURE_SAMPLE_SKEW_MS = 30000;
export const RESOURCE_WARNING_PERCENT = 90;
export const RENEWAL_WINDOW_DAYS = 30;

export function serverStatus(server: Server, observedAt: number): ServerStatus {
  const recordedAt = Date.parse(server.latest_metrics?.recorded_at || '');
  if (!Number.isFinite(recordedAt)) return 'pending';
  const age = observedAt - recordedAt;
  return observedAt > 0 && age >= -MAX_FUTURE_SAMPLE_SKEW_MS && age < ONLINE_WINDOW_MS ? 'online' : 'offline';
}

function nonnegative(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0;
}

/** Unknown capacity is not zero utilization. Keep precision until rendering. */
export function resourcePercent(server: Server, metric: ResourceMetric): number | null {
  const m = server.latest_metrics;
  if (!m) return null;
  if (metric === 'cpu') return nonnegative(m.cpu_percent) ? Math.min(100, m.cpu_percent) : null;
  const used = metric === 'memory' ? m.memory_used : m.disk_used;
  const total = metric === 'memory' ? m.memory_total : server.disk_total;
  return nonnegative(used) && nonnegative(total) && total > 0 ? Math.min(100, used / total * 100) : null;
}

export function highResourceUsage(server: Server, observedAt: number): boolean {
  return serverStatus(server, observedAt) === 'online'
    && (['cpu', 'memory', 'disk'] as const).some(metric => (resourcePercent(server, metric) ?? -1) >= RESOURCE_WARNING_PERCENT);
}

export function needsRenewal(server: Server, observedAt: number): boolean {
  const expiresAt = Date.parse(server.expires_at || '');
  return observedAt > 0 && Number.isFinite(expiresAt)
    && expiresAt <= observedAt + RENEWAL_WINDOW_DAYS * 86400000;
}

export function compareServers(a: Server, b: Server, sort: FleetSort, observedAt: number): number {
  if (sort === 'default') return 0;
  if (sort === 'name') return a.name.localeCompare(b.name);
  const value = (server: Server): number | null => {
    if (sort === 'expiry') {
      const date = Date.parse(server.expires_at || '');
      return Number.isFinite(date) ? date : null;
    }
    // Historical and missing samples must not outrank current readings.
    if (serverStatus(server, observedAt) !== 'online') return null;
    if (sort === 'uptime') {
      const uptime = server.latest_metrics?.uptime_seconds;
      return nonnegative(uptime) ? uptime : null;
    }
    return resourcePercent(server, sort);
  };
  const av = value(a), bv = value(b);
  if (av === null) return bv === null ? 0 : 1;
  if (bv === null) return -1;
  return sort === 'expiry' ? av - bv : bv - av;
}

export function summarizeFleet(servers: Server[], observedAt: number) {
  const counts = { online: 0, offline: 0, pending: 0 };
  const cpu: number[] = [], memory: number[] = [];
  const capacity = {
    memory: { used: 0, total: 0, count: 0 },
    disk: { used: 0, total: 0, count: 0 },
    network: { rx: 0, tx: 0, count: 0 },
  };
  for (const server of servers) {
    const status = serverStatus(server, observedAt);
    counts[status]++;
    if (status !== 'online' || !server.latest_metrics) continue;
    const m = server.latest_metrics;
    const cpuValue = resourcePercent(server, 'cpu');
    const memoryValue = resourcePercent(server, 'memory');
    if (cpuValue !== null) cpu.push(cpuValue);
    if (memoryValue !== null) memory.push(memoryValue);
    for (const key of ['memory', 'disk'] as const) {
      if (resourcePercent(server, key) === null) continue;
      capacity[key].used += key === 'memory' ? m.memory_used : m.disk_used;
      capacity[key].total += key === 'memory' ? m.memory_total : server.disk_total;
      capacity[key].count++;
    }
    if (nonnegative(m.network_rx_bytes) && nonnegative(m.network_tx_bytes)) {
      capacity.network.rx += m.network_rx_bytes;
      capacity.network.tx += m.network_tx_bytes;
      capacity.network.count++;
    }
  }
  const average = (values: number[]) => values.length ? values.reduce((sum, value) => sum + value, 0) / values.length : null;
  return { ...counts, total: servers.length, avgCPU: average(cpu), avgMemory: average(memory), capacity };
}
