import client from './client';

export interface FileTarget { serverId: string; containerId?: string }
export interface RemoteFileEntry {
  name: string;
  path: string;
  size: number;
  modified_at: string;
  mode: string;
  is_dir: boolean;
  is_file: boolean;
  is_symlink: boolean;
}
export interface RemoteFileList { path: string; parent: string; entries: RemoteFileEntry[]; truncated: boolean }
export interface RemoteText { path: string; content: string; revision: string }
const endpoint = (target: FileTarget) => '/servers/' + encodeURIComponent(target.serverId) + '/files';
const params = (target: FileTarget, path: string) => ({ path, container: target.containerId || undefined });

export const filesApi = {
  list: (target: FileTarget, path: string, signal: AbortSignal) =>
    client.get<RemoteFileList>(endpoint(target), { params: params(target, path), signal, timeout: 120000 }),
  read: (target: FileTarget, path: string, signal: AbortSignal) =>
    client.get<RemoteText>(endpoint(target) + '/text', { params: params(target, path), signal, timeout: 120000 }),
  save: (target: FileTarget, path: string, content: string, revision: string, signal: AbortSignal) =>
    client.put<{ revision: string }>(endpoint(target) + '/text', { content, revision }, { params: params(target, path), signal, timeout: 120000 }),
  download: (target: FileTarget, path: string, signal: AbortSignal, progress: (percent: number) => void) =>
    client.get<Blob>(endpoint(target) + '/download', {
      params: params(target, path), signal, timeout: 120000, responseType: 'blob',
      onDownloadProgress: (event) => { if (event.total) progress(Math.round(event.loaded / event.total * 100)); },
    }),
  upload: (target: FileTarget, path: string, file: File, overwrite: boolean, signal: AbortSignal, progress: (percent: number) => void) => {
    const body = new FormData();
    body.append('file', file);
    return client.post(endpoint(target) + '/upload', body, {
      params: { ...params(target, path), overwrite: overwrite ? '1' : undefined }, signal, timeout: 120000,
      onUploadProgress: (event) => { if (event.total) progress(Math.round(event.loaded / event.total * 100)); },
    });
  },
};

export async function fileErrorCode(error: unknown): Promise<string> {
  const failure = error as { code?: string; response?: { data?: unknown; status?: number } };
  if (failure.response?.status === 429) return 'rate_limited';
  if (failure.response?.status === 413) return 'file_too_large';
  if (failure.code === 'ECONNABORTED' || failure.code === 'ETIMEDOUT') return 'timeout';
  let data = failure.response?.data;
  if (data instanceof Blob) { try { data = JSON.parse(await data.text()); } catch { return 'remote_failed'; } }
  return typeof data === 'object' && data !== null && 'code' in data && typeof data.code === 'string' ? data.code : 'remote_failed';
}
