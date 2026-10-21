/** Read a public API error without letting malformed response data reach React. */
export function apiError(error: unknown, fallback: string): string {
  if (!error || typeof error !== 'object' || !('response' in error)) return fallback;
  const response = error.response;
  if (!response || typeof response !== 'object' || !('data' in response)) return fallback;
  const data = response.data;
  if (!data || typeof data !== 'object' || !('error' in data)) return fallback;
  return typeof data.error === 'string' && data.error ? data.error : fallback;
}
