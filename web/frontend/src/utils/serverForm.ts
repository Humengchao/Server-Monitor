import dayjs from 'dayjs';
import type { Dayjs } from 'dayjs';
import type { Server } from '../api/servers';

const GB = 1024 * 1024 * 1024;

export interface ServerFormValues {
  name: string;
  host: string;
  port?: number;
  ssh_username?: string;
  ssh_password?: string;
  ssh_key?: string;
  ssh_host_key?: string;
  server_type?: string;
  expires_at?: Dayjs | null;
  billing_price?: number;
  billing_currency?: string;
  billing_cycle?: string;
  traffic_limit_gb?: number;
  public_location?: string;
}

export interface ServerPayload {
  name: string;
  host: string;
  port?: number;
  ssh_username?: string;
  ssh_password?: string;
  ssh_key?: string;
  ssh_host_key?: string;
  credential_id?: string | null;
  expires_at?: string | null;
  billing_price?: number;
  billing_currency?: string;
  billing_cycle?: string;
  traffic_limit_bytes?: number;
  public_location?: string;
  notes?: string;
  server_type?: string;
}

const trim = (value: unknown) => (typeof value === 'string' ? value.trim() : value);

/** Seeds the form from a saved server. Secrets are never echoed back; empty means "keep". */
export function serverFormValues(server: Server): ServerFormValues {
  return {
    name: server.name,
    host: server.host,
    port: server.port,
    ssh_username: server.ssh_username,
    ssh_host_key: server.ssh_host_key || '',
    expires_at: server.expires_at ? dayjs(server.expires_at) : null,
    server_type: server.server_type || 'linux',
    billing_price: server.billing_price || 0,
    billing_currency: server.billing_currency || 'CNY',
    billing_cycle: server.billing_cycle || 'year',
    traffic_limit_gb: Number(((server.traffic_limit_bytes || 0) / GB).toFixed(2)),
    public_location: server.public_location || '',
  };
}

/**
 * Turns submitted values into the API payload. Fields the form hid for the
 * chosen type or auth mode are dropped rather than sent stale: antd keeps the
 * values of unmounted fields, so a password typed for another server would
 * otherwise ride along and silently overwrite this one's credentials.
 */
export function buildServerPayload(values: ServerFormValues, options: { credentialId?: string; notes?: string } = {}): ServerPayload {
  const serverType = values.server_type || 'linux';
  const windows = serverType === 'windows';
  const shared = !!options.credentialId;
  return {
    ...values,
    name: trim(values.name) as string,
    host: trim(values.host) as string,
    ssh_username: trim(values.ssh_username) as string | undefined,
    ssh_password: shared ? undefined : values.ssh_password,
    ssh_key: windows || shared ? undefined : values.ssh_key,
    ssh_host_key: windows ? undefined : values.ssh_host_key,
    port: windows ? undefined : values.port,
    credential_id: options.credentialId || null,
    server_type: serverType,
    expires_at: values.expires_at ? values.expires_at.toISOString() : null,
    traffic_limit_bytes: Math.round((values.traffic_limit_gb || 0) * GB),
    notes: options.notes || '',
  };
}
