import { useEffect, useState } from 'react';
import type { Server } from '../api/servers';
import type { ResponseClock } from './useResponseClock';
import { MAX_FUTURE_SAMPLE_SKEW_MS, ONLINE_WINDOW_MS } from '../utils/fleet';

const DAY = 86400000;

/** Check at a fleet transition, with a daily ceiling to keep timers in range. */
export function nextFleetChange(servers: Server[], now: number): number {
  let next = Infinity;
  const consider = (at: number) => { if (at > now && at < next) next = at; };
  let hasExpiry = false;
  for (const server of servers) {
    const recorded = Date.parse(server.latest_metrics?.recorded_at || '');
    consider(recorded - MAX_FUTURE_SAMPLE_SKEW_MS);
    consider(recorded + ONLINE_WINDOW_MS);
    const expiry = Date.parse(server.expires_at || '');
    if (!Number.isFinite(expiry)) continue;
    hasExpiry = true;
    // Whole remaining days drive renewal filters and warning colors. Expired
    // uses a strict comparison, so schedule one millisecond after equality.
    consider(expiry + 1);
    consider(expiry - Math.floor((expiry - now) / DAY) * DAY + 1);
  }
  if (hasExpiry) {
    const tomorrow = new Date(now);
    tomorrow.setHours(24, 0, 0, 0);
    consider(tomorrow.getTime());
  }
  // Browsers overflow delays above 2^31-1 ms into a near-immediate timeout.
  // Clock skew or future-dated samples must not create a busy render loop.
  return Number.isFinite(next) ? Math.min(next, now + DAY) : next;
}

export function useFleetClock(clock: ResponseClock | null, servers: Server[]): number {
  const [elapsedAt, setElapsedAt] = useState(() => performance.now());
  const now = clock ? clock.serverTime + Math.max(0, elapsedAt - clock.receivedAt) : 0;
  useEffect(() => {
    if (!clock) return;
    const refresh = () => { if (!document.hidden) setElapsedAt(performance.now()); };
    const next = nextFleetChange(servers, now);
    const actual = clock.serverTime + Math.max(0, performance.now() - clock.receivedAt);
    const timer = !document.hidden && Number.isFinite(next)
      ? window.setTimeout(refresh, Math.max(1, next - actual)) : undefined;
    document.addEventListener('visibilitychange', refresh);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', refresh);
    };
  }, [clock, servers, now]);
  return now;
}
