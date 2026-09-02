import { useEffect, useRef } from 'react';

export interface PollingOptions {
  /**
   * Run once as soon as the hook mounts. Defaults to true.
   *
   * Only suitable when the callback does not depend on props: the leading call
   * fires on mount alone, so a loader keyed on something like `serverId` needs
   * its own effect to refetch when that value changes.
   */
  leading?: boolean;
  /** Stop polling without unmounting, e.g. a panel toggled out of live mode. */
  enabled?: boolean;
}

/**
 * Interval polling that stands down while the tab is in the background.
 *
 * Every page here refreshes on a timer, and some of those ticks are expensive
 * on the far side: the process, service and port panels each cost an SSH round
 * trip to the monitored host. Browsers only throttle background timers to about
 * once a minute rather than stopping them, so a forgotten tab kept paying for
 * data nobody could see. Ticks are skipped while `document.hidden`, and a
 * skipped tick is made up the moment the tab is looked at again, so returning
 * to a page shows current data instead of whatever was on screen a minute ago.
 *
 * The callback is read through a ref, so a caller may pass a fresh closure on
 * every render (a `t`-dependent loader, say) without restarting the interval or
 * firing an extra request.
 */
export function usePolling(callback: () => void, intervalMs: number, options: PollingOptions = {}) {
  const { leading = true, enabled = true } = options;
  const callbackRef = useRef(callback);

  // Synced in an effect rather than during render: writing a ref while
  // rendering is not allowed. Declared before the interval effect so the ref is
  // current by the time any tick can fire.
  useEffect(() => {
    callbackRef.current = callback;
  }, [callback]);

  useEffect(() => {
    if (!enabled || intervalMs <= 0) return;

    let missedTick = false;

    const tick = () => {
      if (document.hidden) {
        missedTick = true;
        return;
      }
      callbackRef.current();
    };

    const onVisibilityChange = () => {
      if (!document.hidden && missedTick) {
        missedTick = false;
        callbackRef.current();
      }
    };

    // Deferred so the first call never sets state synchronously inside an
    // effect body, which would cascade renders.
    const initial = leading ? window.setTimeout(() => callbackRef.current(), 0) : undefined;
    const timer = window.setInterval(tick, intervalMs);
    document.addEventListener('visibilitychange', onVisibilityChange);

    return () => {
      if (initial !== undefined) window.clearTimeout(initial);
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisibilityChange);
    };
  }, [enabled, leading, intervalMs]);
}
