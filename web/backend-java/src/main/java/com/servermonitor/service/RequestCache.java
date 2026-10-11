package com.servermonitor.service;

import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Semaphore;

/**
 * Short-lived, request-coalescing cache mirroring services/request_cache.go:
 * concurrent callers for one key share a single load, successful results live
 * for the TTL, failures are not cached, and the number of simultaneous loads is
 * bounded. Entries map is capped (512) so a flood of distinct keys can't grow
 * it without limit.
 */
public class RequestCache<V> {

    private static final int MAX_ENTRIES = 512;

    private final class Entry {
        final CompletableFuture<V> future = new CompletableFuture<>();
        volatile Instant expires;
    }

    private final Map<String, Entry> entries = new HashMap<>();
    private final Semaphore slots;

    public RequestCache(int concurrency) {
        this.slots = new Semaphore(concurrency);
    }

    public void forget(String key) {
        synchronized (entries) {
            entries.remove(key);
        }
    }

    /** A loader that may fail; failures are shared with concurrent waiters and never cached. */
    @FunctionalInterface
    public interface Loader<V> {
        V load() throws Exception;
    }

    /**
     * Gets the (possibly cached) value for key, loading it once when absent or
     * expired. A load longer than 30s is abandoned server-side.
     */
    public V get(String key, Duration ttl, Loader<V> load) throws Exception {
        Entry entry;
        boolean creator = false;
        synchronized (entries) {
            Instant now = Instant.now();
            entries.entrySet().removeIf(e -> e.getValue().expires != null && now.isAfter(e.getValue().expires));
            entry = entries.get(key);
            if (entry == null) {
                if (entries.size() >= MAX_ENTRIES) {
                    throw new IllegalStateException("too many pending requests");
                }
                entry = new Entry();
                entries.put(key, entry);
                creator = true;
            }
        }
        if (creator) {
            final Entry created = entry;
            Thread worker = new Thread(() -> {
                try {
                    if (!slots.tryAcquire(30, java.util.concurrent.TimeUnit.SECONDS)) {
                        throw new IllegalStateException("request timed out waiting for a slot");
                    }
                    try {
                        V value = load.load();
                        synchronized (entries) {
                            if (entries.get(key) == created) {
                                created.expires = Instant.now().plus(ttl);
                            }
                        }
                        created.future.complete(value);
                    } finally {
                        slots.release();
                    }
                } catch (Throwable t) {
                    synchronized (entries) {
                        if (entries.get(key) == created) {
                            entries.remove(key);
                        }
                    }
                    created.future.completeExceptionally(t);
                }
            }, "request-cache-load");
            worker.setDaemon(true);
            worker.start();
        }
        try {
            return entry.future.get();
        } catch (ExecutionException e) {
            Throwable cause = e.getCause();
            if (cause instanceof Exception ex) {
                throw ex;
            }
            throw new RuntimeException(cause);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new RuntimeException("interrupted", e);
        }
    }
}
