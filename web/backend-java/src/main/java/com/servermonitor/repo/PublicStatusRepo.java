package com.servermonitor.repo;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.List;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;

/** The anonymized public status census. Deliberately selects nothing that could identify a host. */
@Repository
public class PublicStatusRepo {

    private static final ObjectMapper JSON = new ObjectMapper();

    private final JdbcTemplate db;

    public record PublicTag(String name, String color) {
    }

    public record PublicServerMetric(String publicLocation, List<PublicTag> tags, String serverType,
        int cpuCores, long diskTotal, Instant expiresAt, double billingPrice, String billingCurrency,
        String billingCycle, long trafficLimit, double cpuPercent, double load1, double load5, double load15,
        long memoryUsed, long memoryTotal, long diskUsed, long networkRxBytes, long networkTxBytes,
        long networkRxTotal, long networkTxTotal, long uptimeSeconds, int latencyMs, Instant recordedAt,
        int availability30dObserved, int availability30dExpected) {
    }

    public PublicStatusRepo(JdbcTemplate db) {
        this.db = db;
    }

    public List<PublicServerMetric> getPublicServerMetrics(Instant since30d, Instant until) {
        return db.query("""
            SELECT COALESCE(s.public_location, ''),
                COALESCE((
                    SELECT JSON_AGG(JSON_BUILD_OBJECT('name', t.name, 'color', t.color) ORDER BY t.name)::TEXT
                    FROM tags t JOIN server_tags st ON st.tag_id = t.id WHERE st.server_id = s.id
                ), '[]'),
                COALESCE(s.server_type, 'linux'), COALESCE(s.cpu_cores, 0), COALESCE(s.disk_total_bytes, 0),
                s.expires_at, COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'),
                COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0),
                COALESCE(sm.cpu_percent, 0), COALESCE(sm.load_1, 0), COALESCE(sm.load_5, 0), COALESCE(sm.load_15, 0),
                COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0), COALESCE(sm.disk_used_bytes, 0),
                COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0),
                COALESCE(sm.network_rx_total_bytes, 0), COALESCE(sm.network_tx_total_bytes, 0),
                COALESCE(sm.uptime_seconds, 0), COALESCE(sm.latency_ms, 0), sm.recorded_at,
                COALESCE((
                    SELECT COUNT(*) FROM server_metrics_15m q
                    WHERE q.server_id = s.id AND q.recorded_at >= GREATEST(?, s.created_at) AND q.recorded_at < ?
                ), 0),
                GREATEST(0, (CEIL(EXTRACT(EPOCH FROM CAST(? AS timestamptz)) / 900)
                    - CEIL(EXTRACT(EPOCH FROM GREATEST(CAST(? AS timestamptz), s.created_at)) / 900))::INT)
            FROM servers s
            LEFT JOIN server_latest_metrics sm ON sm.server_id = s.id
            ORDER BY s.created_at ASC, s.id ASC
            """, (rs, n) -> {
            List<PublicTag> tags;
            try {
                tags = JSON.readValue(rs.getString(2), new TypeReference<>() { });
            } catch (Exception e) {
                throw new IllegalStateException("parse public tags: " + e.getMessage(), e);
            }
            Timestamp expires = rs.getTimestamp(6);
            Timestamp recorded = rs.getTimestamp(24);
            return new PublicServerMetric(
                rs.getString(1), tags, rs.getString(3), rs.getInt(4), rs.getLong(5),
                expires == null ? null : expires.toInstant(),
                rs.getDouble(7), rs.getString(8), rs.getString(9), rs.getLong(10),
                rs.getDouble(11), rs.getDouble(12), rs.getDouble(13), rs.getDouble(14),
                rs.getLong(15), rs.getLong(16), rs.getLong(17), rs.getLong(18), rs.getLong(19),
                rs.getLong(20), rs.getLong(21), rs.getLong(22), rs.getInt(23),
                recorded == null ? null : recorded.toInstant(),
                rs.getInt(25), rs.getInt(26));
        }, Timestamp.from(since30d), Timestamp.from(until), Timestamp.from(until), Timestamp.from(since30d));
    }
}
