package com.servermonitor.repo;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.RowMapper;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

/** server_metrics tiers + rollups + retention, mirroring models/tag.go's MetricPoint block. */
@Repository
public class Metrics {

    private static final Logger log = LoggerFactory.getLogger(Metrics.class);

    /** Charts cap at 500 points; bucket width is derived from the range. */
    public static final int MAX_METRICS_POINTS = 500;

    private final JdbcTemplate db;
    private final TransactionTemplate tx;

    public Metrics(JdbcTemplate db, TransactionTemplate tx) {
        this.db = db;
        this.tx = tx;
    }

    public record MetricPoint(double cpuPercent, double load1, double load5, double load15,
        long memoryUsed, long memoryTotal, long diskUsed, long networkRxBytes, long networkTxBytes,
        long networkRxTotal, long networkTxTotal, long diskRxBytes, long diskTxBytes,
        long uptimeSeconds, int latencyMs, Instant recordedAt) {

        public static MetricPoint recorded(Instant at) {
            return new MetricPoint(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, at);
        }
    }

    private static final RowMapper<MetricPoint> MAPPER = (rs, n) -> new MetricPoint(
        rs.getDouble(1), rs.getDouble(2), rs.getDouble(3), rs.getDouble(4),
        rs.getLong(5), rs.getLong(6), rs.getLong(7), rs.getLong(8), rs.getLong(9),
        rs.getLong(10), rs.getLong(11), rs.getLong(12), rs.getLong(13),
        rs.getLong(14), rs.getInt(15), rs.getTimestamp(16).toInstant());

    /** Upserts the one-row latest table and appends the raw sample, in one transaction. */
    public void saveMetric(UUID serverId, MetricPoint m) {
        Instant recordedAt = m.recordedAt() == null ? Instant.now() : m.recordedAt();
        tx.executeWithoutResult(status -> {
            db.update("""
                INSERT INTO server_latest_metrics (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
                 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
                 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at)
                 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
                 ON CONFLICT (server_id) DO UPDATE SET
                 cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5, load_15=EXCLUDED.load_15,
                 memory_used=EXCLUDED.memory_used, memory_total=EXCLUDED.memory_total, disk_used_bytes=EXCLUDED.disk_used_bytes,
                 network_rx_bytes=EXCLUDED.network_rx_bytes, network_tx_bytes=EXCLUDED.network_tx_bytes,
                 network_rx_total_bytes=EXCLUDED.network_rx_total_bytes, network_tx_total_bytes=EXCLUDED.network_tx_total_bytes,
                 disk_rx_bytes=EXCLUDED.disk_rx_bytes, disk_tx_bytes=EXCLUDED.disk_tx_bytes,
                 uptime_seconds=EXCLUDED.uptime_seconds, latency_ms=EXCLUDED.latency_ms, recorded_at=EXCLUDED.recorded_at
                """,
                serverId, m.cpuPercent(), m.load1(), m.load5(), m.load15(), m.memoryUsed(), m.memoryTotal(),
                m.diskUsed(), m.networkRxBytes(), m.networkTxBytes(), m.networkRxTotal(), m.networkTxTotal(),
                m.diskRxBytes(), m.diskTxBytes(), m.uptimeSeconds(), m.latencyMs(), Timestamp.from(recordedAt));
            db.update("""
                INSERT INTO server_metrics (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
                 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
                 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at)
                 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
                """,
                serverId, m.cpuPercent(), m.load1(), m.load5(), m.load15(), m.memoryUsed(), m.memoryTotal(),
                m.diskUsed(), m.networkRxBytes(), m.networkTxBytes(), m.networkRxTotal(), m.networkTxTotal(),
                m.diskRxBytes(), m.diskTxBytes(), m.uptimeSeconds(), m.latencyMs(), Timestamp.from(recordedAt));
        });
    }

    /**
     * Merges the three retention tiers into one bucketed series. The SQL is the
     * Go models.GetMetrics query verbatim.
     */
    public List<MetricPoint> getMetrics(UUID serverId, Instant since, Instant until) {
        int bucketSecs = (int) (until.getEpochSecond() - since.getEpochSecond()) / MAX_METRICS_POINTS;
        if (bucketSecs < 1) {
            bucketSecs = 1;
        }
        Instant now = Instant.now();
        Instant rawCutoff = now.minusSeconds(24 * 3600);
        Instant minuteCutoff = now.minusSeconds(30L * 24 * 3600);
        return db.query("""
            WITH source AS (
                SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
                    network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
                    disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
                FROM server_metrics_15m
                WHERE server_id=? AND recorded_at >= ? AND recorded_at <= ? AND recorded_at < ?
                UNION ALL
                SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
                    network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
                    disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
                FROM server_metrics_1m
                WHERE server_id=? AND recorded_at >= ? AND recorded_at <= ? AND recorded_at >= ? AND recorded_at < ?
                UNION ALL
                SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
                    network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
                    disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
                FROM server_metrics
                WHERE server_id=? AND recorded_at >= ? AND recorded_at <= ? AND recorded_at >= ?
            )
            SELECT AVG(cpu_percent)::DECIMAL(5,2), AVG(load_1)::DECIMAL(8,2), AVG(load_5)::DECIMAL(8,2), AVG(load_15)::DECIMAL(8,2),
                   ROUND(AVG(memory_used))::BIGINT, MAX(memory_total)::BIGINT, ROUND(AVG(disk_used_bytes))::BIGINT,
                   ROUND(AVG(network_rx_bytes))::BIGINT, ROUND(AVG(network_tx_bytes))::BIGINT,
                   MAX(network_rx_total_bytes)::BIGINT, MAX(network_tx_total_bytes)::BIGINT,
                   ROUND(AVG(disk_rx_bytes))::BIGINT, ROUND(AVG(disk_tx_bytes))::BIGINT,
                   MAX(uptime_seconds)::BIGINT, ROUND(AVG(latency_ms))::INT,
                   TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / ?) * ?) AS bucket_time
            FROM source
            GROUP BY bucket_time ORDER BY bucket_time ASC
            """, MAPPER,
            serverId, Timestamp.from(since), Timestamp.from(until), Timestamp.from(minuteCutoff),
            serverId, Timestamp.from(since), Timestamp.from(until), Timestamp.from(minuteCutoff), Timestamp.from(rawCutoff),
            serverId, Timestamp.from(since), Timestamp.from(until), Timestamp.from(rawCutoff),
            bucketSecs, bucketSecs);
    }

    public Optional<MetricPoint> getLatestMetric(UUID serverId) {
        List<MetricPoint> list = db.query("""
            SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
             network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
             disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
             FROM server_latest_metrics WHERE server_id=?
            """, MAPPER, serverId);
        return list.isEmpty() ? Optional.empty() : Optional.of(list.get(0));
    }

    // --- rollups & retention ------------------------------------------

    public boolean metricRollupBackfilled() {
        Boolean exists = db.queryForObject(
            "SELECT EXISTS (SELECT 1 FROM metric_maintenance_state WHERE name='tiered_metrics_v1')",
            Boolean.class);
        return Boolean.TRUE.equals(exists);
    }

    public void markMetricRollupBackfilled() {
        db.update("""
            INSERT INTO metric_maintenance_state (name, completed_at) VALUES ('tiered_metrics_v1', NOW())
             ON CONFLICT (name) DO UPDATE SET completed_at=EXCLUDED.completed_at
            """);
    }

    /** Start instant for an incremental rollup: last bucket minus the overlap, never before the fallback. */
    public Instant metricRollupStart(String table, Instant fallback, java.time.Duration overlap) {
        if (!"server_metrics_1m".equals(table) && !"server_metrics_15m".equals(table)) {
            throw new IllegalArgumentException("unsupported rollup table " + table);
        }
        Timestamp latest = db.queryForObject("SELECT MAX(recorded_at) FROM " + table, Timestamp.class);
        if (latest == null) {
            return fallback;
        }
        Instant start = latest.toInstant().minus(overlap);
        return start.isBefore(fallback) ? fallback : start;
    }

    public void rollupMetrics1m(Instant since, Instant until) {
        rollupMetrics("server_metrics", "server_metrics_1m", 60, "COUNT(*)::INT", since, until);
    }

    public void rollupMetrics15m(Instant since, Instant until) {
        rollupMetrics("server_metrics_1m", "server_metrics_15m", 15 * 60, "COALESCE(SUM(sample_count), 0)::INT", since, until);
    }

    private void rollupMetrics(String sourceTable, String targetTable, int bucketSeconds,
        String sampleExpression, Instant since, Instant until) {
        String query = String.format("""
            INSERT INTO %s (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
             disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
             disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, sample_count, recorded_at)
             SELECT server_id, AVG(cpu_percent)::DECIMAL(5,2), AVG(load_1)::DECIMAL(8,2), AVG(load_5)::DECIMAL(8,2), AVG(load_15)::DECIMAL(8,2),
             ROUND(AVG(memory_used))::BIGINT, MAX(memory_total)::BIGINT, ROUND(AVG(disk_used_bytes))::BIGINT,
             ROUND(AVG(network_rx_bytes))::BIGINT, ROUND(AVG(network_tx_bytes))::BIGINT,
             MAX(network_rx_total_bytes)::BIGINT, MAX(network_tx_total_bytes)::BIGINT,
             ROUND(AVG(disk_rx_bytes))::BIGINT, ROUND(AVG(disk_tx_bytes))::BIGINT,
             MAX(uptime_seconds)::BIGINT, ROUND(AVG(latency_ms))::INT, %s,
             TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / ?) * ?)
             FROM %s WHERE recorded_at >= ? AND recorded_at < ?
             GROUP BY server_id, FLOOR(EXTRACT(EPOCH FROM recorded_at) / ?::int)
             ON CONFLICT (server_id, recorded_at) DO UPDATE SET
             cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5, load_15=EXCLUDED.load_15,
             memory_used=EXCLUDED.memory_used, memory_total=EXCLUDED.memory_total, disk_used_bytes=EXCLUDED.disk_used_bytes,
             network_rx_bytes=EXCLUDED.network_rx_bytes, network_tx_bytes=EXCLUDED.network_tx_bytes,
             network_rx_total_bytes=EXCLUDED.network_rx_total_bytes, network_tx_total_bytes=EXCLUDED.network_tx_total_bytes,
             disk_rx_bytes=EXCLUDED.disk_rx_bytes, disk_tx_bytes=EXCLUDED.disk_tx_bytes,
             uptime_seconds=EXCLUDED.uptime_seconds, latency_ms=EXCLUDED.latency_ms, sample_count=EXCLUDED.sample_count
            """, targetTable, sampleExpression, sourceTable);
        // Positional order in the statement: see the SQL template above.
        db.update(query,
            bucketSeconds, bucketSeconds,
            Timestamp.from(since), Timestamp.from(until),
            bucketSeconds);
    }

    /** Bounded delete so retention cleanup never takes one giant lock. */
    public long deleteMetricBatch(String table, Instant before, int limit) {
        if (!"server_metrics".equals(table) && !"server_metrics_1m".equals(table)
            && !"server_metrics_15m".equals(table)) {
            throw new IllegalArgumentException("unsupported metric table " + table);
        }
        return db.update(String.format("""
            WITH doomed AS (
                SELECT ctid FROM %s WHERE recorded_at < ? LIMIT ?
            ) DELETE FROM %s WHERE ctid IN (SELECT ctid FROM doomed)
            """, table, table), Timestamp.from(before), limit);
    }
}
