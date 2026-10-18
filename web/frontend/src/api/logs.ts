import client from './client';
export interface LogEntry { id: string; time: string; message: string; unit?: string; priority?: string; stream: 'journal' | 'stdout' | 'stderr' }
export interface LogsPage { entries: LogEntry[]; next_cursor?: string; truncated: boolean; partial: boolean; reason?: string; scanned: number }
export interface LogsQuery { since: string; until?: string; unit?: string; priority?: string; keyword?: string; cursor?: string; limit?: number }
export const logsApi = {
  read: (server: string, container: string | undefined, query: LogsQuery, signal: AbortSignal) => client.get<LogsPage>(
    container ? `/servers/${encodeURIComponent(server)}/docker/containers/${encodeURIComponent(container)}/logs/page` : `/servers/${encodeURIComponent(server)}/logs`,
    { params: query, signal, timeout: 15000 }),
};
