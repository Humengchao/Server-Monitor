package com.servermonitor.repo;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

/** Uptime censuses against the metrics rollup tiers, mirroring models/uptime.go. */
@Repository
public class Uptime {

    /** The daily strip and the 30-day window both read the fifteen-minute tier. */
    public static final int DAILY_STRIP_BUCKET_SECONDS = 15 * 60;

    private final JdbcTemplate db;

    public Uptime(JdbcTemplate db) {
        this.db = db;
    }

    /** Raw bucket census for one server, before percentages. */
    public record UptimeCounts(UUID serverId, Instant createdAt, int minute24h, int minute7d, int quarter30d) {
    }

    /** Half-open ranges each tier is counted over, each snapped to its own bucket width. */
    public record UptimeCensusRange(Instant since24h, Instant since7d, Instant until1m,
        Instant since30d, Instant until15m) {
    }

    public List<UptimeCounts> getFleetUptimeCounts(UUID userId, UptimeCensusRange r) {
        Map<UUID, UptimeCounts> byId = new LinkedHashMap<>();
        db.query("""
            SELECT s.id, s.created_at,
                COUNT(m.recorded_at) FILTER (WHERE m.recorded_at >= GREATEST(?, s.created_at)) AS minute_24h,
                COUNT(m.recorded_at) AS minute_7d
             FROM servers s
             LEFT JOIN server_metrics_1m m
               ON m.server_id = s.id AND m.recorded_at >= GREATEST(?, s.created_at) AND m.recorded_at < ?
             WHERE s.user_id = ?
             GROUP BY s.id, s.created_at
            """, rs -> {
            UUID id = UUID.fromString(rs.getString(1));
            byId.put(id, new UptimeCounts(id, rs.getTimestamp(2).toInstant(), rs.getInt(3), rs.getInt(4), 0));
        }, Timestamp.from(r.since24h()), Timestamp.from(r.since7d()), Timestamp.from(r.until1m()), userId);

        db.query("""
            SELECT s.id, COUNT(m.recorded_at)
             FROM servers s
             LEFT JOIN server_metrics_15m m
               ON m.server_id = s.id AND m.recorded_at >= GREATEST(?, s.created_at) AND m.recorded_at < ?
             WHERE s.user_id = ?
             GROUP BY s.id
            """, rs -> {
            UUID id = UUID.fromString(rs.getString(1));
            UptimeCounts existing = byId.get(id);
            if (existing != null) {
                byId.put(id, new UptimeCounts(id, existing.createdAt(), existing.minute24h(),
                    existing.minute7d(), rs.getInt(2)));
            }
        }, Timestamp.from(r.since30d()), Timestamp.from(r.until15m()), userId);

        return new ArrayList<>(byId.values());
    }

    /** One calendar day's bucket census; days with no samples at all are absent (the caller fills them). */
    public record DailyAvailability(Instant day, int observed) {
    }

    public List<DailyAvailability> getDailyAvailability(UUID serverId, Instant since, Instant until) {
        return db.query("""
            SELECT date_trunc('day', recorded_at) AS day, COUNT(*)
             FROM server_metrics_15m
             WHERE server_id=? AND recorded_at >= ? AND recorded_at < ?
             GROUP BY day ORDER BY day
            """, (rs, n) -> new DailyAvailability(rs.getTimestamp(1).toInstant(), rs.getInt(2)),
            serverId, Timestamp.from(since), Timestamp.from(until));
    }

    public record MetricGap(Instant before, Instant after) {
    }

    public List<MetricGap> getMetricGaps(UUID serverId, Instant since, Instant until, int gapSeconds, int limit) {
        return db.query("""
            SELECT prev_at, recorded_at FROM (
                SELECT recorded_at, LAG(recorded_at) OVER (ORDER BY recorded_at) AS prev_at
                FROM server_metrics_1m
                WHERE server_id=? AND recorded_at >= ? AND recorded_at < ?
             ) g
             WHERE prev_at IS NOT NULL
               AND recorded_at - prev_at > make_interval(secs => ?::int)
             ORDER BY prev_at DESC
             LIMIT ?
            """, (rs, n) -> new MetricGap(rs.getTimestamp(1).toInstant(), rs.getTimestamp(2).toInstant()),
            serverId, Timestamp.from(since), Timestamp.from(until), gapSeconds, limit);
    }

    /** Newest minute bucket for a server, or null when it has never reported. */
    public Instant getLastMetricBucket(UUID serverId, Instant until) {
        Timestamp ts = db.queryForObject(
            "SELECT MAX(recorded_at) FROM server_metrics_1m WHERE server_id=? AND recorded_at < ?",
            Timestamp.class, serverId, Timestamp.from(until));
        return ts == null ? null : ts.toInstant();
    }
}
