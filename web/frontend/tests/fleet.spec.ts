import { test, expect } from '@playwright/test';
import { compareServers, highResourceUsage, needsRenewal, resourcePercent, serverStatus, summarizeFleet } from '../src/utils/fleet';
import { getExpirationInfo } from '../src/utils/format';
import { GB, host, observedAt, sampleFleet } from './fixtures/fleet';

test('collection status distinguishes missing samples and uses the response clock at the two-minute boundary', () => {
  const server = host('host');
  expect(serverStatus(server, observedAt)).toBe('online');
  server.latest_metrics!.recorded_at = new Date(observedAt - 119999).toISOString();
  expect(serverStatus(server, observedAt)).toBe('online');
  server.latest_metrics!.recorded_at = new Date(observedAt - 120000).toISOString();
  expect(serverStatus(server, observedAt)).toBe('offline');
  server.latest_metrics!.recorded_at = 'invalid';
  expect(serverStatus(server, observedAt)).toBe('pending');
  expect(serverStatus(host('new', { latest_metrics: null }), observedAt)).toBe('pending');
});

test('fleet totals exclude stale samples and use capacity sums while preserving zero usage', () => {
  const fleet = summarizeFleet(sampleFleet(), observedAt);
  expect(fleet).toMatchObject({ total: 4, online: 2, offline: 1, pending: 1, avgCPU: 46, avgMemory: 56.25 });
  expect(fleet.capacity.memory).toEqual({ used: 30 * GB, total: 40 * GB, count: 2 });
  expect(fleet.capacity.disk).toEqual({ used: 40 * GB, total: 200 * GB, count: 2 });
  expect(fleet.capacity.network).toEqual({ rx: 2048, tx: 4096, count: 2 });
  const missing = host('missing');
  missing.latest_metrics!.memory_total = 0;
  expect(resourcePercent(missing, 'memory')).toBeNull();
  expect(summarizeFleet([missing], observedAt).avgMemory).toBeNull();
  expect(summarizeFleet([missing], observedAt).capacity.memory.count).toBe(0);
  expect(summarizeFleet([], observedAt).avgCPU).toBeNull();
});

test('attention uses unrounded live values and renewal dates including already expired hosts', () => {
  const fleet = sampleFleet();
  expect(fleet.filter(server => highResourceUsage(server, observedAt)).map(s => s.id)).toEqual(['busy-host']);
  fleet[1].latest_metrics!.cpu_percent = 89.9;
  expect(highResourceUsage(fleet[1], observedAt)).toBe(false);
  expect(fleet.filter(server => needsRenewal(server, observedAt)).map(s => s.id)).toEqual(['busy-host', 'offline-host']);
  expect(needsRenewal(host('no-expiry'), observedAt)).toBe(false);
  expect(needsRenewal(host('invalid', { expires_at: 'invalid' }), observedAt)).toBe(false);
  expect(needsRenewal(host('boundary', { expires_at: new Date(observedAt + 30 * 86400000).toISOString() }), observedAt)).toBe(true);
  expect(needsRenewal(host('later', { expires_at: new Date(observedAt + 30 * 86400000 + 1).toISOString() }), observedAt)).toBe(false);
  expect(getExpirationInfo(fleet[1].expires_at, 'en', observedAt)?.daysLeft).toBe(5);
});

test('resource sorting puts a real zero before missing and historical values, expiry puts unset dates last', () => {
  const fleet = sampleFleet();
  expect([...fleet].sort((a, b) => compareServers(a, b, 'cpu', observedAt)).map(s => s.id))
    .toEqual(['busy-host', 'idle-host', 'offline-host', 'pending-host']);
  expect([...fleet].sort((a, b) => compareServers(a, b, 'expiry', observedAt)).map(s => s.id))
    .toEqual(['offline-host', 'busy-host', 'idle-host', 'pending-host']);
});
