package com.servermonitor.service;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

import org.springframework.stereotype.Service;

import com.servermonitor.repo.Uptime;

/**
 * Observed-availability math, mirroring services/uptime.go. Availability is
 * derived from how many metric buckets actually landed in a window versus how
 * many the collector should have written: panel downtime counts against every
 * server, and the numbers measure "did we see this host", not the host's own
 * uptime counter.
 */
@Service
public class UptimeService {

    /** Excludes the trailing stretch the rollup has not consolidated yet. */
    public static final Duration UPTIME_ROLLUP_LAG = Duration.ofMinutes(2);
    /** Smallest gap between consecutive buckets counted as an outage. */
    public static final Duration UPTIME_OUTAGE_GAP = Duration.ofMinutes(2);
    /** Bounds the episode list returned per server. */
    public static final int UPTIME_MAX_OUTAGES = 50;

    public record UptimeWindowSpec(String key, Duration span, int bucketSeconds) {
    }

    /** The three windows the API reports; 30d reads the fifteen-minute tier. */
    public static final List<UptimeWindowSpec> UPTIME_WINDOWS = List.of(
        new UptimeWindowSpec("24h", Duration.ofHours(24), 60),
        new UptimeWindowSpec("7d", Duration.ofDays(7), 60),
        new UptimeWindowSpec("30d", Duration.ofDays(30), 15 * 60));

    /** observed/expected as a percentage rounded to two decimals, clamped to [0,100]. */
    public static double availabilityPercent(int observed, int expected) {
        if (expected <= 0) {
            return 0;
        }
        double ratio = (double) observed / expected * 100;
        if (ratio > 100) {
            ratio = 100;
        }
        if (ratio < 0) {
            ratio = 0;
        }
        return Math.round(ratio * 100) / 100.0;
    }

    /**
     * Counts the rollup buckets that should exist in [start, end). Buckets are
     * wall-clock aligned, so this counts alignment boundaries inside the range
     * rather than dividing the duration — the distinction shows up at every
     * edge that is not itself aligned (a server created mid-bucket, a partial
     * day), where dividing undercounts by one.
     */
    public static int expectedBuckets(Instant start, Instant end, int bucketSeconds) {
        if (bucketSeconds <= 0 || !end.isAfter(start)) {
            return 0;
        }
        Instant first = ceilBucket(start, bucketSeconds);
        Instant last = ceilBucket(end, bucketSeconds);
        if (!last.isAfter(first)) {
            return 0;
        }
        return (int) (Duration.between(first, last).toSeconds() / bucketSeconds);
    }

    private static Instant ceilBucket(Instant t, int bucketSeconds) {
        long epoch = t.getEpochSecond();
        long remainder = Math.floorMod(epoch, bucketSeconds);
        if (remainder == 0 && t.getNano() == 0) {
            return t;
        }
        return Instant.ofEpochSecond(epoch - remainder + bucketSeconds);
    }

    /** Newest instant a census of the tier may cover: lag back, then snap to the bucket boundary. */
    public static Instant tierEnd(Instant now, int bucketSeconds) {
        if (bucketSeconds <= 0) {
            return now;
        }
        Instant lagged = now.minus(UPTIME_ROLLUP_LAG);
        long epoch = lagged.getEpochSecond();
        return Instant.ofEpochSecond(epoch - Math.floorMod(epoch, bucketSeconds));
    }

    public record WindowStart(Instant start, boolean partial) {
    }

    /** Clamps a window's start to when the server was created. */
    public static WindowStart windowStart(Instant now, Duration span, Instant createdAt) {
        Instant start = now.minus(span);
        if (createdAt.isAfter(start)) {
            return new WindowStart(createdAt, true);
        }
        return new WindowStart(start, false);
    }

    /** One contiguous stretch with no metric samples. */
    public record Outage(Instant startedAt, Instant endedAt, long seconds, boolean ongoing) {
    }

    /**
     * Turns bucket gaps into outage episodes, appending a trailing ongoing
     * episode when the last sample is older than the gap threshold — a host
     * down *right now* has no "after" bucket to pair with.
     */
    public static List<Outage> buildOutages(List<Uptime.MetricGap> gaps, Instant lastSample,
        Instant windowEnd) {
        List<Outage> outages = new ArrayList<>(gaps.size() + 1);
        for (Uptime.MetricGap gap : gaps) {
            outages.add(new Outage(gap.before(), gap.after(),
                Duration.between(gap.before(), gap.after()).toSeconds(), false));
        }
        if (lastSample != null && Duration.between(lastSample, windowEnd).compareTo(UPTIME_OUTAGE_GAP) > 0) {
            outages.add(new Outage(lastSample, windowEnd,
                Duration.between(lastSample, windowEnd).toSeconds(), true));
        }
        return outages;
    }
}
