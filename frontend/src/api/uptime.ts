import client from './client';

export type UptimeWindowKey = '24h' | '7d' | '30d';

export interface UptimeWindow {
  window: UptimeWindowKey;
  percent: number;
  observed_buckets: number;
  expected_buckets: number;
  /** The window is longer than the server has existed. */
  partial: boolean;
  /** Nothing was expected yet — too new to score. Distinct from 0%. */
  no_data: boolean;
}

export interface ServerUptime {
  server_id: string;
  windows: UptimeWindow[];
}

export interface FleetUptime {
  servers: ServerUptime[];
  /** Always "observed" — see basis_note. */
  basis: string;
  basis_note: string;
  generated_at: string;
}

export interface UptimeDay {
  day: string;
  percent: number;
  /** Quarter-hour buckets — the strip reads the same tier as the 30-day figure. */
  observed_buckets: number;
  expected_buckets: number;
  /** The server did not exist for any of this day — distinct from 0%. */
  no_data: boolean;
}

export interface Outage {
  started_at: string;
  ended_at: string;
  seconds: number;
  /** Had not recovered when the window closed. */
  ongoing: boolean;
}

export interface ServerUptimeDetail {
  server_id: string;
  since: string;
  until: string;
  days: UptimeDay[];
  outages: Outage[];
  basis: string;
  generated_at: string;
}

/** Colour thresholds shared by the badge, the list column and the daily strip. */
export function availabilityColor(percent: number): string {
  if (percent >= 99.5) return '#17a082';
  if (percent >= 99) return '#4f9a4f';
  if (percent >= 95) return '#cf8f2c';
  return '#e2545f';
}

export const uptimeApi = {
  fleet: () => client.get<FleetUptime>('/servers/uptime'),
  detail: (id: string, days = 30) =>
    client.get<ServerUptimeDetail>(`/servers/${id}/uptime`, { params: { days } }),
};
