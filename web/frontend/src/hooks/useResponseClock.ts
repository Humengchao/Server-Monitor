import { useState } from 'react';
import { usePolling } from './usePolling';

export interface ResponseClock {
  serverTime: number;
  receivedAt: number;
}

/** Anchor elapsed time to the API clock, even when the browser clock is wrong. */
export function responseClock(dateHeader: unknown): ResponseClock {
  const serverTime = typeof dateHeader === 'string' ? Date.parse(dateHeader) : NaN;
  return {
    serverTime: Number.isFinite(serverTime) ? serverTime : Date.now(),
    receivedAt: performance.now(),
  };
}

/** Keep samples aging while requests fail or wait for a response. */
export function useResponseClock(clock: ResponseClock | null): number {
  const [elapsedAt, setElapsedAt] = useState(() => performance.now());
  usePolling(() => { setElapsedAt(performance.now()); }, 1000, { enabled: clock !== null });
  return clock ? clock.serverTime + Math.max(0, elapsedAt - clock.receivedAt) : 0;
}
