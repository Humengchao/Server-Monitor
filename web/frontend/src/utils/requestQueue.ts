export function createRequestQueue(concurrency: number) {
  let active = 0;
  const pending: Array<() => void> = [];
  return <Value,>(task: () => Promise<Value>): Promise<Value> => new Promise((resolve, reject) => {
    const run = () => { active++; task().then(resolve, reject).finally(() => { active--; pending.shift()?.(); }); };
    if (active < concurrency) run(); else pending.push(run);
  });
}
