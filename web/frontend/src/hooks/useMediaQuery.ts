import { useSyncExternalStore } from 'react';

/** antd's `xs` range: the layout is a single column and tables are narrower than their rows. */
export const PHONE_QUERY = '(max-width: 575px)';

function subscribe(query: string, onChange: () => void) {
  const list = window.matchMedia(query);
  list.addEventListener('change', onChange);
  return () => list.removeEventListener('change', onChange);
}

/**
 * True while the viewport matches `query`. Reads synchronously on the first
 * render, so a table decides its column pinning before it paints instead of
 * flashing the desktop layout on a phone.
 */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => subscribe(query, onChange),
    () => window.matchMedia(query).matches,
    () => false,
  );
}
