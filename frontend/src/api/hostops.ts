import client from './client';

/** Which mechanism answered. sysv reports strictly less than systemd. */
export type ServiceManager = 'systemd' | 'sysv' | 'windows';

export interface ServiceUnit {
  name: string;
  /** "loaded" / "not-found" / "masked"; empty on hosts that don't report it. */
  load: string;
  /** "active" / "inactive" / "failed" / "activating". */
  active: string;
  /** Type-specific detail: "running", "exited", "dead". */
  sub: string;
  /** Boot-time disposition. Empty means the host didn't say, not "disabled". */
  enabled: string;
  description: string;
}

export interface ServiceListResponse {
  services: ServiceUnit[];
  manager?: ServiceManager;
  total: number;
  returned: number;
  /** false when the host cannot be asked — no manager, or one that refused. */
  supported: boolean;
  /** Stable token to localize against; `reason` is the English sentence. */
  reason_code?: 'absent' | 'unreachable';
  reason?: string;
}

export type ServiceAction = 'start' | 'stop' | 'restart' | 'reload';

/** Who can reach a listening socket, derived from its bind address. */
export type PortExposure = 'public' | 'private' | 'loopback' | 'unknown';

export interface ListeningPort {
  protocol: string;
  address: string;
  port: number;
  /** Blank when the SSH user may not see the owning process. */
  process: string;
  pid: number;
  exposure: PortExposure;
}

export interface PortListResponse {
  ports: ListeningPort[];
  total: number;
  returned: number;
}

/** Colour per exposure class, shared by the tag and the summary counts. */
export function exposureColor(exposure: PortExposure): string {
  switch (exposure) {
    case 'public': return '#e2545f';
    case 'private': return '#cf8f2c';
    case 'loopback': return '#17a082';
    default: return '#778199';
  }
}

/** Colour for a unit's high-level state. */
export function serviceStateColor(active: string): string {
  switch (active) {
    case 'active': return '#17a082';
    case 'failed': return '#e2545f';
    case 'activating':
    case 'deactivating': return '#cf8f2c';
    default: return '#778199';
  }
}

export const hostOpsApi = {
  services: (id: string, signal?: AbortSignal) =>
    client.get<ServiceListResponse>(`/servers/${id}/services`, { signal }),
  controlService: (id: string, name: string, action: ServiceAction) =>
    client.post<{ message: string }>(`/servers/${id}/services/control`, { name, action },
      // The host's own refusal ("Access denied") is the useful part of a 502
      // here, so it must reach the caller rather than trigger a redirect.
      { skipAuthRedirect: true }),
  ports: (id: string, signal?: AbortSignal) =>
    client.get<PortListResponse>(`/servers/${id}/ports`, { signal }),
};
