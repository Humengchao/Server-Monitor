export function createRequestQueue(concurrency: number) {
  if (!Number.isInteger(concurrency) || concurrency < 1) throw new RangeError('Concurrency must be a positive integer');
  let active = 0;
  const pending: Array<() => void> = [];
  return <Value,>(task: () => Promise<Value>, signal?: AbortSignal): Promise<Value> => new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    // Remove canceled work before it reaches the front of a busy queue. Active
    // tasks retain their slot until they settle and cancel their own transport.
    const cancel = () => {
      const index = pending.indexOf(run);
      if (index !== -1) pending.splice(index, 1);
      reject(signal?.reason);
    };
    const run = () => {
      signal?.removeEventListener('abort', cancel);
      active++;
      void Promise.resolve().then(() => {
        signal?.throwIfAborted();
        return task();
      }).then(resolve, reject).finally(() => {
        active--;
        pending.shift()?.();
      });
    };
    signal?.addEventListener('abort', cancel, { once: true });
    if (active < concurrency) run(); else pending.push(run);
  });
}
