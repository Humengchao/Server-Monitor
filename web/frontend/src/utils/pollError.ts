import type { PollErrorKind, Server } from '../api/servers';

const KNOWN: readonly PollErrorKind[] = ['auth', 'host_key', 'unreachable', 'timeout', 'command', 'storage', 'other'];

export function pollErrorKind(server: Pick<Server, 'last_error_kind'>): PollErrorKind | null {
  if (!server.last_error_kind) return null;
  return KNOWN.includes(server.last_error_kind as PollErrorKind)
    ? server.last_error_kind as PollErrorKind
    : 'other';
}

export function hasPollError(server: Pick<Server, 'last_error_kind'>): boolean {
  return !!pollErrorKind(server);
}
