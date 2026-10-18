/** Alert context includes time and host only; metric alerts do not identify a service. */
export function incidentLogsURL(server: string, at: string): string {
  const time = Date.parse(at);
  const query = new URLSearchParams({ server });
  if (Number.isFinite(time)) { query.set('since', new Date(time - 600000).toISOString()); query.set('until', new Date(time + 600000).toISOString()); }
  return `/logs?${query}`;
}
