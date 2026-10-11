package com.servermonitor.security;

import java.time.Duration;
import java.util.HashMap;
import java.util.Map;

/**
 * Token-bucket rate limiter keyed by client IP, mirroring the Gin middleware.
 * IPv6 clients are bucketed by their /64 prefix (rotating through a subscriber's
 * range must not reset the budget); IPv4 stays per address. The map is bounded:
 * at the cap stale entries are pruned inline, and new keys are rejected — which
 * is the limiter's job anyway.
 */
public class RateLimiter {

    private static final int MAX_BUCKETS = 10_000;

    private static final class Bucket {
        double tokens;
        long lastFillNanos;
        long lastAccessNanos;
    }

    private final int requests;
    private final double perSeconds;
    private final Map<String, Bucket> buckets = new HashMap<>();

    public RateLimiter(int requests, Duration per) {
        this.requests = requests;
        this.perSeconds = per.toNanos() / 1_000_000_000.0;
        Thread cleaner = new Thread(() -> {
            while (true) {
                try {
                    Thread.sleep(Duration.ofMinutes(5).toMillis());
                } catch (InterruptedException e) {
                    return;
                }
                synchronized (RateLimiter.this) {
                    pruneStale(System.nanoTime());
                }
            }
        }, "ratelimit-cleaner-" + requests + "-" + per.toSeconds());
        cleaner.setDaemon(true);
        cleaner.start();
    }

    /** Returns true when the request may proceed; false when it must be rejected with 429. */
    public synchronized boolean tryAcquire(String clientIp) {
        String key = rateLimitKey(clientIp);
        long now = System.nanoTime();
        Bucket b = buckets.get(key);
        if (b == null) {
            if (buckets.size() >= MAX_BUCKETS) {
                pruneStale(now);
            }
            if (buckets.size() >= MAX_BUCKETS) {
                return false;
            }
            b = new Bucket();
            b.tokens = requests;
            b.lastFillNanos = now;
            b.lastAccessNanos = now;
            buckets.put(key, b);
        }
        double elapsed = (now - b.lastFillNanos) / 1_000_000_000.0;
        int refill = (int) (requests * elapsed / perSeconds);
        if (refill > 0) {
            b.tokens = Math.min(requests, b.tokens + refill);
            b.lastFillNanos = now;
        }
        b.lastAccessNanos = now;
        if (b.tokens >= 1) {
            b.tokens -= 1;
            return true;
        }
        return false;
    }

    private void pruneStale(long nowNanos) {
        long cutoff = nowNanos - Duration.ofMinutes(10).toNanos();
        buckets.entrySet().removeIf(e -> e.getValue().lastAccessNanos < cutoff);
    }

    static String rateLimitKey(String ip) {
        if (ip == null || ip.isBlank()) {
            return "";
        }
        // IPv4 (and anything unparseable) buckets per literal address.
        if (!ip.contains(":")) {
            return ip;
        }
        // IPv6: collapse the first four hextets into a /64 prefix.
        String expanded = ip;
        int pct = expanded.indexOf('%');
        if (pct > 0) {
            expanded = expanded.substring(0, pct);
        }
        String[] parts = expanded.split(":");
        StringBuilder prefix = new StringBuilder();
        int count = 0;
        for (String part : parts) {
            if (count >= 4) {
                break;
            }
            if (count > 0) {
                prefix.append(':');
            }
            prefix.append(part.isEmpty() ? "0" : part);
            count++;
        }
        while (count < 4) {
            prefix.append(":0");
            count++;
        }
        return prefix + "::/64";
    }

    /** Visible for tests. */
    synchronized int bucketCount() {
        return buckets.size();
    }
}
