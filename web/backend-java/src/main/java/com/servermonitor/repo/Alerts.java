package com.servermonitor.repo;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

/** alert_rules + alert_events, mirroring models/alert.go. */
@Repository
public class Alerts {

    private final JdbcTemplate db;

    public Alerts(JdbcTemplate db) {
        this.db = db;
    }

    // Metric vocabulary shared with the Go backend.
    public static final String METRIC_CPU = "cpu";
    public static final String METRIC_MEMORY = "memory";
    public static final String METRIC_DISK = "disk";
    public static final String METRIC_LOAD1 = "load1";
    public static final String METRIC_LATENCY = "latency";
    public static final String METRIC_OFFLINE = "offline";

    public static final Map<String, String> METRIC_UNITS = Map.of(
        METRIC_CPU, "%", METRIC_MEMORY, "%", METRIC_DISK, "%",
        METRIC_LOAD1, "", METRIC_LATENCY, "ms", METRIC_OFFLINE, "");

    public static boolean validAlertMetric(String metric) {
        return METRIC_UNITS.containsKey(metric);
    }

    public record AlertRule(UUID id, UUID userId, String name, UUID serverId, String serverName,
        String metric, String comparator, double threshold, int durationSeconds, boolean enabled,
        String webhookUrl, Instant createdAt, int firingCount) {

        public AlertRule withIdCreatedAt(UUID newId, Instant at) {
            return new AlertRule(newId, userId, name, serverId, serverName, metric, comparator,
                threshold, durationSeconds, enabled, webhookUrl, at, firingCount);
        }
    }

    public record AlertEvent(long id, UUID ruleId, String ruleName, String metric, UUID serverId,
        String serverName, double value, String message, String comparator, double threshold,
        int durationSeconds, Instant startedAt, Instant resolvedAt) {
    }

    /** One server's current state as the engine sees it; never carries secrets. */
    public record AlertSnapshot(UUID serverId, UUID userId, String name, long diskTotal,
        boolean hasMetrics, double cpuPercent, long memoryUsed, long memTotal, long diskUsed,
        double load1, int latencyMs, Instant recordedAt) {
    }

    public boolean createAlertRule(AlertRule r) {
        List<Instant> created = db.query("""
            INSERT INTO alert_rules (id, user_id, name, server_id, metric, comparator, threshold, duration_seconds, enabled, webhook_url)
             SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
             WHERE CAST(? AS uuid) IS NULL OR EXISTS (SELECT 1 FROM servers WHERE id = CAST(? AS uuid) AND user_id = ?)
             RETURNING created_at
            """, (rs, n) -> rs.getTimestamp(1).toInstant(),
            r.id(), r.userId(), r.name(), r.serverId(), r.metric(), r.comparator(), r.threshold(),
            r.durationSeconds(), r.enabled(), r.webhookUrl(),
            r.serverId(), r.serverId(), r.userId());
        return !created.isEmpty();
    }

    public boolean updateAlertRule(AlertRule r) {
        int rows = db.update("""
            UPDATE alert_rules SET name=?, server_id=?, metric=?, comparator=?, threshold=?,
             duration_seconds=?, enabled=?, webhook_url=?
             WHERE id=? AND user_id=?
             AND (CAST(? AS uuid) IS NULL OR EXISTS (SELECT 1 FROM servers WHERE id = CAST(? AS uuid) AND user_id = ?))
            """,
            r.name(), r.serverId(), r.metric(), r.comparator(), r.threshold(), r.durationSeconds(),
            r.enabled(), r.webhookUrl(), r.id(), r.userId(), r.serverId(), r.serverId(), r.userId());
        return rows > 0;
    }

    public void deleteAlertRule(UUID id, UUID userId) {
        db.update("DELETE FROM alert_rules WHERE id=? AND user_id=?", id, userId);
    }

    public List<AlertRule> getAlertRulesByUserId(UUID userId) {
        return db.query("""
            SELECT r.id, r.user_id, r.name, r.server_id, COALESCE(s.name, ''), r.metric, r.comparator,
             r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at,
             (SELECT COUNT(*) FROM alert_events e WHERE e.rule_id = r.id AND e.resolved_at IS NULL)
             FROM alert_rules r
             LEFT JOIN servers s ON s.id = r.server_id
             WHERE r.user_id = ?
             ORDER BY r.created_at DESC
            """, Alerts::mapRule, userId);
    }

    public List<AlertRule> getEnabledAlertRules() {
        return db.query("""
            SELECT r.id, r.user_id, r.name, r.server_id, '', r.metric, r.comparator,
             r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at, 0
             FROM alert_rules r WHERE r.enabled ORDER BY r.created_at
            """, Alerts::mapRule);
    }

    private static AlertRule mapRule(java.sql.ResultSet rs, int n) throws java.sql.SQLException {
        String serverId = rs.getString(4);
        return new AlertRule(
            UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)), rs.getString(3),
            serverId == null ? null : UUID.fromString(serverId), rs.getString(5),
            rs.getString(6), rs.getString(7), rs.getDouble(8), rs.getInt(9), rs.getBoolean(10),
            rs.getString(11), rs.getTimestamp(12).toInstant(), rs.getInt(13));
    }

    public List<AlertSnapshot> getAlertSnapshots() {
        return db.query("""
            SELECT s.id, s.user_id, s.name, COALESCE(s.disk_total_bytes, 0),
             lm.server_id IS NOT NULL,
             COALESCE(lm.cpu_percent, 0), COALESCE(lm.memory_used, 0), COALESCE(lm.memory_total, 0),
             COALESCE(lm.disk_used_bytes, 0), COALESCE(lm.load_1, 0), COALESCE(lm.latency_ms, 0),
             COALESCE(lm.recorded_at, TIMESTAMPTZ 'epoch')
             FROM servers s
             LEFT JOIN server_latest_metrics lm ON lm.server_id = s.id
            """, (rs, n) -> new AlertSnapshot(
            UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)), rs.getString(3),
            rs.getLong(4), rs.getBoolean(5), rs.getDouble(6), rs.getLong(7), rs.getLong(8),
            rs.getLong(9), rs.getDouble(10), rs.getInt(11), rs.getTimestamp(12).toInstant()));
    }

    /**
     * Inserts a firing episode, or returns empty when one is already open for
     * this rule/server pair (the partial unique index makes this race-free).
     */
    public Optional<Long> openAlertEvent(UUID ruleId, UUID serverId, UUID userId, double value,
        String message, Instant startedAt) {
        List<Long> ids = db.query("""
            INSERT INTO alert_events (rule_id, server_id, user_id, value, message, started_at)
             VALUES (?,?,?,?,?,?)
             ON CONFLICT (rule_id, server_id) WHERE resolved_at IS NULL DO NOTHING
             RETURNING id
            """, (rs, n) -> rs.getLong(1), ruleId, serverId, userId, value, message, Timestamp.from(startedAt));
        return ids.isEmpty() ? Optional.empty() : Optional.of(ids.get(0));
    }

    public boolean resolveAlertEvent(UUID ruleId, UUID serverId, Instant at) {
        return db.update(
            "UPDATE alert_events SET resolved_at=? WHERE rule_id=? AND server_id=? AND resolved_at IS NULL",
            Timestamp.from(at), ruleId, serverId) > 0;
    }

    /** Open rule/server pairs, so the engine rebuilds its in-memory view after a restart. */
    public Map<UUID, Set<UUID>> getOpenAlertKeys() {
        Map<UUID, Set<UUID>> open = new HashMap<>();
        db.query("SELECT rule_id, server_id FROM alert_events WHERE resolved_at IS NULL", rs -> {
            open.computeIfAbsent(UUID.fromString(rs.getString(1)), k -> new HashSet<>())
                .add(UUID.fromString(rs.getString(2)));
        });
        return open;
    }

    public List<AlertEvent> getAlertEventsByUserId(UUID userId, boolean activeOnly, int limit) {
        if (limit < 1 || limit > 500) {
            limit = 100;
        }
        String filter = activeOnly ? " AND e.resolved_at IS NULL" : "";
        return db.query(String.format("""
            SELECT e.id, e.rule_id, COALESCE(r.name, ''), COALESCE(r.metric, ''), e.server_id, COALESCE(s.name, ''),
             e.value, e.message, COALESCE(r.comparator, '>'), COALESCE(r.threshold, 0), COALESCE(r.duration_seconds, 0),
             e.started_at, e.resolved_at
             FROM alert_events e
             LEFT JOIN alert_rules r ON r.id = e.rule_id
             LEFT JOIN servers s ON s.id = e.server_id
             WHERE e.user_id = ?%s
             ORDER BY e.resolved_at IS NULL DESC, e.started_at DESC
             LIMIT ?
            """, filter), (rs, n) -> {
            Timestamp resolved = rs.getTimestamp(13);
            return new AlertEvent(
                rs.getLong(1), UUID.fromString(rs.getString(2)), rs.getString(3), rs.getString(4),
                UUID.fromString(rs.getString(5)), rs.getString(6), rs.getDouble(7), rs.getString(8),
                rs.getString(9), rs.getDouble(10), rs.getInt(11), rs.getTimestamp(12).toInstant(),
                resolved == null ? null : resolved.toInstant());
        }, userId, limit);
    }

    public int countActiveAlerts(UUID userId) {
        Integer count = db.queryForObject(
            "SELECT COUNT(*) FROM alert_events WHERE user_id=? AND resolved_at IS NULL",
            Integer.class, userId);
        return count == null ? 0 : count;
    }

    public void pruneAlertEvents(Instant before) {
        db.update("DELETE FROM alert_events WHERE resolved_at IS NOT NULL AND resolved_at < ?",
            Timestamp.from(before));
    }
}
