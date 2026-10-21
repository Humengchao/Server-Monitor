/** Resolve an API root once so HTTP and WebSockets preserve the same prefix. */
export function apiBaseURL(configured: string | undefined, origin: string): string {
  return new URL((configured || '/api').replace(/\/+$/, '') + '/', origin).href;
}

export function websocketURL(path: string, base: string): string {
  const url = new URL(path.replace(/^\/+/, ''), base);
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.href;
}
