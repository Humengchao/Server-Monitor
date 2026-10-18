import { test, expect } from '@playwright/test';
import { createRequestQueue } from '../src/utils/requestQueue';

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

test('the request queue limits active work and starts waiting tasks in order', async () => {
  const queue = createRequestQueue(2);
  const first = deferred();
  const second = deferred();
  const started: number[] = [];
  const jobs = [
    queue(async () => { started.push(1); await first.promise; return 1; }),
    queue(async () => { started.push(2); await second.promise; return 2; }),
    queue(async () => { started.push(3); return 3; }),
    queue(async () => { started.push(4); return 4; }),
  ];
  await expect.poll(() => started).toEqual([1, 2]);
  first.resolve();
  await expect.poll(() => started).toEqual([1, 2, 3, 4]);
  second.resolve();
  expect(await Promise.all(jobs)).toEqual([1, 2, 3, 4]);
});

test('a canceled waiting request rejects promptly without running or delaying later work', async () => {
  const queue = createRequestQueue(1);
  const held = deferred();
  const first = queue(() => held.promise);
  const controller = new AbortController();
  let canceledTaskRan = false;
  const canceled = queue(async () => { canceledTaskRan = true; }, controller.signal);
  const rejected = expect(canceled).rejects.toMatchObject({ name: 'AbortError' });
  const next = queue(async () => 'next');
  controller.abort();
  // This must resolve before the active request releases its slot.
  await rejected;
  expect(canceledTaskRan).toBe(false);
  held.resolve();
  await first;
  expect(await next).toBe('next');
  expect(canceledTaskRan).toBe(false);
});

test('synchronous task failures and rejected requests release their queue slots', async () => {
  const queue = createRequestQueue(1);
  const failure = new Error('task failed before returning a promise');
  const syncFailure = queue(() => { throw failure; });
  const asyncFailure = queue(() => Promise.reject(new Error('request failed')));
  const next = queue(async () => 'recovered');
  const results = await Promise.allSettled([syncFailure, asyncFailure, next]);
  expect(results).toEqual([
    { status: 'rejected', reason: failure },
    { status: 'rejected', reason: new Error('request failed') },
    { status: 'fulfilled', value: 'recovered' },
  ]);
});

test('already canceled requests never run and invalid concurrency is rejected', async () => {
  const queue = createRequestQueue(1);
  const controller = new AbortController();
  controller.abort();
  let ran = false;
  await expect(queue(async () => { ran = true; }, controller.signal)).rejects.toMatchObject({ name: 'AbortError' });
  expect(ran).toBe(false);
  for (const concurrency of [0, -1, 1.5, NaN, Infinity]) {
    expect(() => createRequestQueue(concurrency)).toThrow(RangeError);
  }
});
