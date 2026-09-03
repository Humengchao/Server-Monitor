import { PollErrorKind, Server } from '../api/servers';

/**
 * Every kind this bundle knows how to explain. Also the guard list: a backend
 * newer than the frontend can send a kind that has no copy here, and falling
 * back to `other` shows the host's raw error rather than an untranslated key.
 */
export const POLL_ERROR_KINDS: readonly PollErrorKind[] = [
  'auth', 'host_key', 'unreachable', 'timeout', 'command', 'storage', 'other',
];

/** The server's last failure kind, or null when it last polled successfully. */
export function pollErrorKind(server: Pick<Server, 'last_error_kind'>): PollErrorKind | null {
  const kind = server.last_error_kind;
  if (!kind) return null;
  return POLL_ERROR_KINDS.includes(kind) ? kind : 'other';
}
