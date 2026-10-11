package com.servermonitor.repo;

import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.dao.EmptyResultDataAccessException;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.RowMapper;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

import com.servermonitor.crypto.FieldCipher;

/** servers + server_tags + tag reads/writes, mirroring internal/models/server.go. */
@Repository
public class Servers {

    private static final Logger log = LoggerFactory.getLogger(Servers.class);

    private final JdbcTemplate db;
    private final TransactionTemplate tx;
    private final FieldCipher cipher;

    public Servers(JdbcTemplate db, TransactionTemplate tx, FieldCipher cipher) {
        this.db = db;
        this.tx = tx;
        this.cipher = cipher;
    }

    // --- domain types -------------------------------------------------

    public record Tag(UUID id, UUID userId, String name, String color) {
    }

    public record LatestMetrics(double cpuPercent, double load1, double load5, double load15,
        long memoryUsed, long memoryTotal, long diskUsed, long networkRxBytes, long networkTxBytes,
        long networkRxTotal, long networkTxTotal, long diskRxBytes, long diskTxBytes,
        long uptimeSeconds, int latencyMs, Instant recordedAt) {
    }

    /**
     * A full server row with decrypted, credential-resolved SSH settings. Only
     * constructed via {@link #getServerByIdAndUser} / {@link #getAllServers} —
     * never serialized to a client (SSHPassword/SSHKey must not leak).
     */
    public static class ServerSecrets {
        public UUID id;
        public UUID userId;
        public String name = "";
        public String host = "";
        public int port;
        public String sshUsername = "";
        public String sshPassword = "";
        public String sshKey = "";
        public String sshHostKey = "";
        public UUID credentialId;
        public String serverType = "linux";
        public Instant createdAt;
        public int cpuCores;
        public long memoryTotal;
        public long diskTotal;
        public boolean hasDocker;
        public String dockerVersion = "";
    }

    /** The API-facing view (no secrets), joined with credential name and latest metrics. */
    public static class ServerSummary {
        public UUID id;
        public UUID userId;
        public String name = "";
        public String host = "";
        public int port;
        public String sshUsername = "";
        public String sshHostKey = "";
        public UUID credentialId;
        public String credentialName = "";
        public int cpuCores;
        public long memoryTotal;
        public long diskTotal;
        public boolean hasDocker;
        public String dockerVersion = "";
        public Instant expiresAt;
        public String serverType = "linux";
        public String notes = "";
        public double billingPrice;
        public String billingCurrency = "CNY";
        public String billingCycle = "year";
        public long trafficLimit;
        public String publicLocation = "";
        public Instant createdAt;
        public String lastErrorKind = "";
        public String lastError = "";
        public Instant lastErrorAt;
        public LatestMetrics latestMetrics;
        public List<Tag> tags;
    }

    private static final String SUMMARY_SELECT = """
        SELECT s.id, s.user_id, s.name, s.host, s.port, s.ssh_username, s.created_at,
         COALESCE(s.ssh_host_key, ''),
         COALESCE(s.credential_id::text, ''),
         COALESCE(c.name, ''),
         COALESCE(s.cpu_cores, 0), COALESCE(s.memory_total_bytes, 0), COALESCE(s.disk_total_bytes, 0),
         COALESCE(s.has_docker, FALSE), COALESCE(s.docker_version, ''),
         s.expires_at, COALESCE(s.server_type, 'linux'), COALESCE(s.notes, ''),
         COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'), COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0), COALESCE(s.public_location, ''),
         COALESCE(s.last_error_kind, ''), COALESCE(s.last_error, ''), s.last_error_at,
         COALESCE(sm.cpu_percent, 0), COALESCE(sm.load_1, 0), COALESCE(sm.load_5, 0), COALESCE(sm.load_15, 0),
         COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0), COALESCE(sm.disk_used_bytes, 0),
         COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0),
         COALESCE(sm.network_rx_total_bytes, 0), COALESCE(sm.network_tx_total_bytes, 0),
         COALESCE(sm.disk_rx_bytes, 0), COALESCE(sm.disk_tx_bytes, 0),
         COALESCE(sm.uptime_seconds, 0), COALESCE(sm.latency_ms, 0), sm.recorded_at
         FROM servers s
         LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id
         LEFT JOIN server_latest_metrics sm ON sm.server_id = s.id
        """;

    private static final RowMapper<ServerSummary> SUMMARY_MAPPER = (ResultSet rs, int rowNum) -> {
        ServerSummary s = new ServerSummary();
        int col = 1;
        s.id = UUID.fromString(rs.getString(col++));
        s.userId = UUID.fromString(rs.getString(col++));
        s.name = rs.getString(col++);
        s.host = rs.getString(col++);
        s.port = rs.getInt(col++);
        s.sshUsername = rs.getString(col++);
        s.createdAt = rs.getTimestamp(col++).toInstant();
        s.sshHostKey = rs.getString(col++);
        String credId = rs.getString(col++);
        if (!credId.isEmpty()) {
            try {
                s.credentialId = UUID.fromString(credId);
            } catch (IllegalArgumentException ignored) { }
        }
        s.credentialName = rs.getString(col++);
        s.cpuCores = rs.getInt(col++);
        s.memoryTotal = rs.getLong(col++);
        s.diskTotal = rs.getLong(col++);
        s.hasDocker = rs.getBoolean(col++);
        s.dockerVersion = rs.getString(col++);
        Timestamp expires = rs.getTimestamp(col++);
        if (expires != null) {
            s.expiresAt = expires.toInstant();
        }
        s.serverType = rs.getString(col++);
        s.notes = rs.getString(col++);
        s.billingPrice = rs.getDouble(col++);
        s.billingCurrency = rs.getString(col++);
        s.billingCycle = rs.getString(col++);
        s.trafficLimit = rs.getLong(col++);
        s.publicLocation = rs.getString(col++);
        s.lastErrorKind = rs.getString(col++);
        s.lastError = rs.getString(col++);
        Timestamp lastErr = rs.getTimestamp(col++);
        if (lastErr != null) {
            s.lastErrorAt = lastErr.toInstant();
        }
        // 15 metric columns precede sm.recorded_at (the 42nd in SUMMARY_SELECT).
        LatestMetrics m = readMetricPrefix(rs, col);
        col += 15;
        Timestamp recorded = rs.getTimestamp(col);
        if (recorded != null) {
            s.latestMetrics = new LatestMetrics(m.cpuPercent, m.load1, m.load5, m.load15,
                m.memoryUsed, m.memoryTotal, m.diskUsed, m.networkRxBytes, m.networkTxBytes,
                m.networkRxTotal, m.networkTxTotal, m.diskRxBytes, m.diskTxBytes,
                m.uptimeSeconds, m.latencyMs, recorded.toInstant());
        }
        return s;
    };

    private static LatestMetrics readMetricPrefix(ResultSet rs, int col) throws SQLException {
        int i = col;
        double cpu = rs.getDouble(i++);
        double l1 = rs.getDouble(i++);
        double l5 = rs.getDouble(i++);
        double l15 = rs.getDouble(i++);
        long memUsed = rs.getLong(i++);
        long memTotal = rs.getLong(i++);
        long diskUsed = rs.getLong(i++);
        long rx = rs.getLong(i++);
        long tx = rs.getLong(i++);
        long rxTotal = rs.getLong(i++);
        long txTotal = rs.getLong(i++);
        long drx = rs.getLong(i++);
        long dtx = rs.getLong(i++);
        long uptime = rs.getLong(i++);
        int latency = rs.getInt(i);
        return new LatestMetrics(cpu, l1, l5, l15, memUsed, memTotal, diskUsed, rx, tx, rxTotal, txTotal,
            drx, dtx, uptime, latency, null);
    }

    // --- summaries ----------------------------------------------------

    public List<ServerSummary> getServersByUserId(UUID userId) {
        return db.query(SUMMARY_SELECT + " WHERE s.user_id = ? ORDER BY s.created_at DESC",
            SUMMARY_MAPPER, userId);
    }

    public Optional<ServerSummary> getServerSummary(UUID id, UUID userId) {
        List<ServerSummary> list = db.query(SUMMARY_SELECT + " WHERE s.id = ? AND s.user_id = ?",
            SUMMARY_MAPPER, id, userId);
        return list.isEmpty() ? Optional.empty() : Optional.of(list.get(0));
    }

    public boolean serverOwnedByUser(UUID id, UUID userId) {
        Boolean owned = db.queryForObject(
            "SELECT EXISTS(SELECT 1 FROM servers WHERE id=? AND user_id=?)", Boolean.class, id, userId);
        return Boolean.TRUE.equals(owned);
    }

    public Instant getServerCreatedAt(UUID id, UUID userId) {
        try {
            Timestamp ts = db.queryForObject("SELECT created_at FROM servers WHERE id=? AND user_id=?",
                Timestamp.class, id, userId);
            return ts.toInstant();
        } catch (EmptyResultDataAccessException e) {
            return null;
        }
    }

    // --- creation / update -------------------------------------------

    /** Input for create: every field except the ones the database fills in. */
    public record ServerUpsert(UUID userId, String name, String host, int port, String sshUsername,
        String sshPassword, String sshKey, String sshHostKey, UUID credentialId, String serverType,
        Instant expiresAt, String notes, double billingPrice, String billingCurrency,
        String billingCycle, long trafficLimit, String publicLocation) {
    }

    public ServerSummary createServer(UUID id, ServerUpsert in) {
        String encPassword = cipher.encrypt(in.sshPassword() == null ? "" : in.sshPassword());
        String encKey = cipher.encrypt(in.sshKey() == null ? "" : in.sshKey());
        db.update("""
            INSERT INTO servers (id, user_id, name, host, port, ssh_username, ssh_password, ssh_key, ssh_host_key, credential_id,
             expires_at, notes, server_type, billing_price, billing_currency, billing_cycle, traffic_limit_bytes, public_location)
             VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
            """,
            id, in.userId(), in.name(), in.host(), in.port(), in.sshUsername(), encPassword, encKey,
            valueOrEmpty(in.sshHostKey()), in.credentialId(),
            in.expiresAt() == null ? null : Timestamp.from(in.expiresAt()), in.notes(), in.serverType(),
            in.billingPrice(), in.billingCurrency(), in.billingCycle(), in.trafficLimit(), in.publicLocation());
        return getServerSummary(id, in.userId()).orElseThrow();
    }

    /**
     * Full update in a transaction; updates secrets only when non-empty. Returns
     * false when the row belongs to someone else (or does not exist).
     */
    public boolean updateServer(UUID id, UUID userId, ServerUpsert in) {
        Boolean updated = tx.execute(status -> {
            int rows = db.update("""
                UPDATE servers SET name=?, host=?, port=?, ssh_username=?, ssh_host_key=?, credential_id=?,
                 expires_at=?, notes=?, server_type=?, billing_price=?, billing_currency=?, billing_cycle=?, traffic_limit_bytes=?, public_location=?
                 WHERE id=? AND user_id=?
                """,
                in.name(), in.host(), in.port(), in.sshUsername(), valueOrEmpty(in.sshHostKey()), in.credentialId(),
                in.expiresAt() == null ? null : Timestamp.from(in.expiresAt()), in.notes(), in.serverType(),
                in.billingPrice(), in.billingCurrency(), in.billingCycle(), in.trafficLimit(), in.publicLocation(),
                id, userId);
            if (rows == 0) {
                return false;
            }
            if (in.sshPassword() != null && !in.sshPassword().isEmpty()) {
                db.update("UPDATE servers SET ssh_password=? WHERE id=? AND user_id=?",
                    cipher.encrypt(in.sshPassword()), id, userId);
            }
            if ("windows".equals(in.serverType())) {
                db.update("UPDATE servers SET ssh_key='', ssh_host_key='' WHERE id=? AND user_id=?", id, userId);
            } else if (in.sshKey() != null && !in.sshKey().isEmpty()) {
                db.update("UPDATE servers SET ssh_key=? WHERE id=? AND user_id=?",
                    cipher.encrypt(in.sshKey()), id, userId);
            }
            return true;
        });
        return Boolean.TRUE.equals(updated);
    }

    private static String valueOrEmpty(String v) {
        return v == null ? "" : v;
    }

    public boolean serverHasDirectSSHAuth(UUID id, UUID userId) {
        try {
            Boolean has = db.queryForObject(
                "SELECT COALESCE(ssh_password, '') <> '' OR COALESCE(ssh_key, '') <> ''"
                    + " FROM servers WHERE id=? AND user_id=?",
                Boolean.class, id, userId);
            return Boolean.TRUE.equals(has);
        } catch (EmptyResultDataAccessException e) {
            throw e;
        }
    }

    public void deleteServer(UUID id, UUID userId) {
        db.update("DELETE FROM servers WHERE id=? AND user_id=?", id, userId);
    }

    public long deleteServers(List<UUID> ids, UUID userId) {
        if (ids.isEmpty()) {
            return 0;
        }
        return db.update("DELETE FROM servers WHERE id = ANY(?) AND user_id = ?",
            Uuids.array(db, ids), userId);
    }

    public int countOwnedServers(List<UUID> ids, UUID userId) {
        if (ids.isEmpty()) {
            return 0;
        }
        Integer count = db.queryForObject("SELECT COUNT(*) FROM servers WHERE id = ANY(?) AND user_id = ?",
            Integer.class, Uuids.array(db, ids), userId);
        return count == null ? 0 : count;
    }

    // --- secrets (for SSH work) --------------------------------------

    /** Full row with decrypted secrets and linked-credential resolution. */
    public Optional<ServerSecrets> getServerByIdAndUser(UUID id, UUID userId) {
        List<ServerSecrets> list = db.query("""
            SELECT id, user_id, name, host, port, ssh_username, COALESCE(ssh_password, ''), COALESCE(ssh_key, ''),
             COALESCE(ssh_host_key, ''), credential_id, created_at, COALESCE(server_type, 'linux'),
             COALESCE(cpu_cores, 0), COALESCE(memory_total_bytes, 0), COALESCE(disk_total_bytes, 0),
             COALESCE(has_docker, FALSE), COALESCE(docker_version, '')
             FROM servers WHERE id=? AND user_id=?
            """, (rs, n) -> {
            ServerSecrets s = new ServerSecrets();
            s.id = UUID.fromString(rs.getString("id"));
            s.userId = UUID.fromString(rs.getString("user_id"));
            s.name = rs.getString("name");
            s.host = rs.getString("host");
            s.port = rs.getInt("port");
            s.sshUsername = rs.getString(6);
            String encPassword = rs.getString(7);
            String encKey = rs.getString(8);
            s.sshHostKey = rs.getString(9);
            String cred = rs.getString(10);
            if (cred != null) {
                s.credentialId = UUID.fromString(cred);
            }
            s.createdAt = rs.getTimestamp(11).toInstant();
            s.serverType = rs.getString(12);
            s.cpuCores = rs.getInt(13);
            s.memoryTotal = rs.getLong(14);
            s.diskTotal = rs.getLong(15);
            s.hasDocker = rs.getBoolean(16);
            s.dockerVersion = rs.getString(17);
            s.sshPassword = cipher.decrypt(encPassword);
            s.sshKey = cipher.decrypt(encKey);
            return s;
        }, id, userId);
        if (list.isEmpty()) {
            return Optional.empty();
        }
        ServerSecrets s = list.get(0);
        if (s.credentialId != null) {
            // Resolve the linked credential, scoped to the same owner. A dangling
            // or foreign link must never fall back to the inline credentials.
            var cred = db.query(
                "SELECT ssh_username, COALESCE(ssh_password, ''), COALESCE(ssh_key, '') FROM credentials WHERE id=? AND user_id=?",
                (rs, n) -> new String[]{rs.getString(1), rs.getString(2), rs.getString(3)},
                s.credentialId, s.userId);
            if (cred.isEmpty()) {
                throw new IllegalStateException("resolve credentials for server " + s.name + ": no rows");
            }
            s.sshUsername = cred.get(0)[0];
            s.sshPassword = cipher.decrypt(cred.get(0)[1]);
            s.sshKey = cipher.decrypt(cred.get(0)[2]);
        }
        return Optional.of(s);
    }

    /**
     * Every server with decrypted, credential-resolved SSH settings for the
     * collector. Mirrors GetAllServers: one join query, skip broken rows with a
     * log line rather than failing the whole fleet.
     */
    public List<ServerSecrets> getAllServers() {
        return db.query("""
            SELECT s.id, s.user_id, s.name, s.host, s.port,
             s.ssh_username, COALESCE(s.ssh_password, ''), COALESCE(s.ssh_key, ''),
             COALESCE(s.ssh_host_key, ''), s.credential_id, COALESCE(s.server_type, 'linux'),
             c.ssh_username AS cu, COALESCE(c.ssh_password, '') AS cp, COALESCE(c.ssh_key, '') AS ck
             FROM servers s
             LEFT JOIN credentials c ON c.id = s.credential_id AND c.user_id = s.user_id
            """, (rs, n) -> {
            String cred = rs.getString("credential_id");
            String credUser = rs.getString("cu");
            if (cred != null && credUser == null) {
                log.warn("collector: resolve credentials for {}: no rows", rs.getString("name"));
                return null;
            }
            ServerSecrets s = new ServerSecrets();
            s.id = UUID.fromString(rs.getString("id"));
            s.userId = UUID.fromString(rs.getString("user_id"));
            s.name = rs.getString("name");
            s.host = rs.getString("host");
            s.port = rs.getInt("port");
            s.serverType = rs.getString(11);
            s.sshHostKey = rs.getString(9);
            String encPassword;
            String encKey;
            if (cred != null) {
                s.credentialId = UUID.fromString(cred);
                s.sshUsername = credUser;
                encPassword = rs.getString("cp");
                encKey = rs.getString("ck");
            } else {
                s.sshUsername = rs.getString(6);
                encPassword = rs.getString(7);
                encKey = rs.getString(8);
            }
            try {
                s.sshPassword = cipher.decrypt(encPassword);
                s.sshKey = cipher.decrypt(encKey);
            } catch (RuntimeException e) {
                log.warn("collector: decrypt credentials for {} failed: {}", s.name, e.getMessage());
                return null;
            }
            return s;
        }).stream().filter(java.util.Objects::nonNull).toList();
    }

    // --- host facts written by the collector --------------------------

    public void updateServerSystemInfo(UUID id, int cpuCores, long memTotal, long diskTotal) {
        db.update("UPDATE servers SET cpu_cores=?, memory_total_bytes=?, disk_total_bytes=? WHERE id=?",
            cpuCores, memTotal, diskTotal, id);
    }

    public void updateDockerInfo(UUID id, boolean hasDocker, String version) {
        db.update("UPDATE servers SET has_docker=?, docker_version=? WHERE id=?", hasDocker, version, id);
    }

    public record DockerInfo(UUID serverId, boolean hasDocker, String version) {
    }

    public List<DockerInfo> getAllDockerInfo() {
        return db.query("SELECT id, COALESCE(has_docker, FALSE), COALESCE(docker_version, '') FROM servers",
            (rs, n) -> new DockerInfo(UUID.fromString(rs.getString(1)), rs.getBoolean(2), rs.getString(3)));
    }

    public Optional<DockerInfo> getServerDockerInfo(UUID id, UUID userId) {
        try {
            return Optional.ofNullable(db.queryForObject(
                "SELECT id, COALESCE(has_docker, FALSE), COALESCE(docker_version, '') FROM servers WHERE id=? AND user_id=?",
                (rs, n) -> new DockerInfo(UUID.fromString(rs.getString(1)), rs.getBoolean(2), rs.getString(3)),
                id, userId));
        } catch (EmptyResultDataAccessException e) {
            return Optional.empty();
        }
    }

    // --- poll error diagnostics --------------------------------------

    /** Stores a stable reason only when it changes; a stuck host must not cause a write per retry. */
    public void recordServerPollError(UUID id, String kind, String detail, Instant at) {
        db.update("""
            UPDATE servers SET last_error_kind=?, last_error=?, last_error_at=?
             WHERE id=? AND (last_error_kind IS DISTINCT FROM ? OR last_error IS DISTINCT FROM ? OR last_error_at IS NULL)
            """, kind, detail, Timestamp.from(at), id, kind, detail);
    }

    public void clearServerPollError(UUID id) {
        db.update("""
            UPDATE servers SET last_error_kind='', last_error='', last_error_at=NULL
             WHERE id=? AND (last_error_kind IS DISTINCT FROM '' OR last_error IS DISTINCT FROM '' OR last_error_at IS NOT NULL)
            """, id);
    }

    // --- tags ---------------------------------------------------------

    public void createTag(Tag t) {
        db.update("INSERT INTO tags (id, user_id, name, color) VALUES (?,?,?,?)",
            t.id(), t.userId(), t.name(), t.color());
    }

    public List<Tag> getTagsByUserId(UUID userId) {
        return db.query("SELECT id, user_id, name, color FROM tags WHERE user_id=? ORDER BY name",
            (rs, n) -> new Tag(UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)),
                rs.getString(3), rs.getString(4)), userId);
    }

    public void deleteTag(UUID id, UUID userId) {
        db.update("DELETE FROM tags WHERE id=? AND user_id=?", id, userId);
    }

    public List<Tag> getServerTags(UUID serverId) {
        return db.query("""
            SELECT t.id, t.user_id, t.name, t.color FROM tags t
             JOIN server_tags st ON t.id = st.tag_id WHERE st.server_id = ?
            """, (rs, n) -> new Tag(UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)),
            rs.getString(3), rs.getString(4)), serverId);
    }

    /** One query for the tags of many servers, keyed by server id. */
    public Map<UUID, List<Tag>> getTagsForServers(List<UUID> serverIds) {
        Map<UUID, List<Tag>> out = new HashMap<>();
        if (serverIds.isEmpty()) {
            return out;
        }
        db.query("""
            SELECT st.server_id, t.id, t.user_id, t.name, t.color FROM tags t
             JOIN server_tags st ON t.id = st.tag_id
             WHERE st.server_id = ANY(?) ORDER BY t.name
            """, rs -> {
            UUID serverId = UUID.fromString(rs.getString(1));
            out.computeIfAbsent(serverId, k -> new ArrayList<>())
                .add(new Tag(UUID.fromString(rs.getString(2)), UUID.fromString(rs.getString(3)),
                    rs.getString(4), rs.getString(5)));
        }, Uuids.array(db, serverIds));
        return out;
    }

    /** Replaces a server's tag set; returns false when the server is not owned. */
    public boolean setServerTags(UUID serverId, UUID userId, List<UUID> tagIds) {
        Boolean ok = tx.execute(status -> {
            Boolean owned = db.queryForObject("SELECT EXISTS(SELECT 1 FROM servers WHERE id=? AND user_id=?)",
                Boolean.class, serverId, userId);
            if (!Boolean.TRUE.equals(owned)) {
                return false;
            }
            db.update("DELETE FROM server_tags WHERE server_id=?", serverId);
            if (!tagIds.isEmpty()) {
                db.update("""
                    INSERT INTO server_tags (server_id, tag_id)
                     SELECT ?, id FROM tags WHERE id = ANY(?) AND user_id = ?
                    """, serverId, Uuids.array(db, tagIds), userId);
            }
            return true;
        });
        return Boolean.TRUE.equals(ok);
    }

    public void addTagsToServers(List<UUID> serverIds, List<UUID> tagIds, UUID userId) {
        if (serverIds.isEmpty() || tagIds.isEmpty()) {
            return;
        }
        db.update("""
            INSERT INTO server_tags (server_id, tag_id)
             SELECT s.id, t.id FROM servers s CROSS JOIN tags t
             WHERE s.id = ANY(?) AND s.user_id = ?
               AND t.id = ANY(?) AND t.user_id = ?
             ON CONFLICT (server_id, tag_id) DO NOTHING
            """, Uuids.array(db, serverIds), Uuids.array(db, tagIds), userId, Uuids.array(db, tagIds), userId);
    }

    public void removeTagsFromServers(List<UUID> serverIds, List<UUID> tagIds, UUID userId) {
        if (serverIds.isEmpty() || tagIds.isEmpty()) {
            return;
        }
        db.update("""
            DELETE FROM server_tags st
             USING servers s
             WHERE st.server_id = s.id AND s.user_id = ?
               AND st.server_id = ANY(?) AND st.tag_id = ANY(?)
            """, userId, Uuids.array(db, serverIds), Uuids.array(db, tagIds));
    }

    // --- fleet helpers ------------------------------------------------

    /** Tag query preserving insertion order for the public status page. */
    public Map<UUID, List<Tag>> getTagsForServersOrdered(List<UUID> serverIds) {
        Map<UUID, List<Tag>> raw = getTagsForServers(serverIds);
        Map<UUID, List<Tag>> out = new LinkedHashMap<>();
        for (UUID id : serverIds) {
            out.computeIfAbsent(id, raw::get);
        }
        return new HashMap<>(out);
    }
}
