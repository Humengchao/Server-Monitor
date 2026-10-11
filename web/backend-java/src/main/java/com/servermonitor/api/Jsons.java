package com.servermonitor.api;

import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import com.servermonitor.repo.LoginHistory.LoginRecord;
import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSummary;
import com.servermonitor.repo.Servers.Tag;

/** Maps repo/domain values onto the exact JSON shapes the Go backend emits (snake_case keys, RFC3339 times). */
public final class Jsons {

    private Jsons() {
    }

    private static final DateTimeFormatter TS = DateTimeFormatter.ISO_OFFSET_DATE_TIME.withZone(ZoneOffset.UTC);

    /** RFC3339 with up-to-nanos fraction; Go's time.Time marshals the same way. */
    public static String timestamp(Instant at) {
        if (at == null) {
            return null;
        }
        return TS.format(at);
    }

    public static Map<String, Object> tag(Tag t) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", t.id());
        out.put("user_id", t.userId());
        out.put("name", t.name());
        out.put("color", t.color());
        return out;
    }

    public static Map<String, Object> latestMetrics(Servers.LatestMetrics m) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("cpu_percent", m.cpuPercent());
        out.put("load_1", m.load1());
        out.put("load_5", m.load5());
        out.put("load_15", m.load15());
        out.put("memory_used", m.memoryUsed());
        out.put("memory_total", m.memoryTotal());
        out.put("disk_used", m.diskUsed());
        out.put("network_rx_bytes", m.networkRxBytes());
        out.put("network_tx_bytes", m.networkTxBytes());
        out.put("network_rx_total_bytes", m.networkRxTotal());
        out.put("network_tx_total_bytes", m.networkTxTotal());
        out.put("disk_rx_bytes", m.diskRxBytes());
        out.put("disk_tx_bytes", m.diskTxBytes());
        out.put("uptime_seconds", m.uptimeSeconds());
        out.put("latency_ms", m.latencyMs());
        out.put("recorded_at", timestamp(m.recordedAt()));
        return out;
    }

    public static Map<String, Object> metricPoint(com.servermonitor.repo.Metrics.MetricPoint m) {
        Map<String, Object> out = latestMetrics(new Servers.LatestMetrics(m.cpuPercent(), m.load1(), m.load5(),
            m.load15(), m.memoryUsed(), m.memoryTotal(), m.diskUsed(), m.networkRxBytes(), m.networkTxBytes(),
            m.networkRxTotal(), m.networkTxTotal(), m.diskRxBytes(), m.diskTxBytes(), m.uptimeSeconds(),
            m.latencyMs(), m.recordedAt()));
        return out;
    }

    public static Map<String, Object> serverSummary(ServerSummary s) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", s.id);
        out.put("user_id", s.userId);
        out.put("name", s.name);
        out.put("host", s.host);
        out.put("port", s.port);
        out.put("ssh_username", s.sshUsername);
        if (s.sshHostKey != null && !s.sshHostKey.isEmpty()) {
            out.put("ssh_host_key", s.sshHostKey);
        }
        out.put("credential_id", s.credentialId);
        if (s.credentialName != null && !s.credentialName.isEmpty()) {
            out.put("credential_name", s.credentialName);
        }
        out.put("server_type", s.serverType);
        out.put("cpu_cores", s.cpuCores);
        out.put("memory_total", s.memoryTotal);
        out.put("disk_total", s.diskTotal);
        out.put("has_docker", s.hasDocker);
        out.put("docker_version", s.dockerVersion);
        out.put("expires_at", s.expiresAt == null ? null : timestamp(s.expiresAt));
        out.put("billing_price", s.billingPrice);
        out.put("billing_currency", s.billingCurrency);
        out.put("billing_cycle", s.billingCycle);
        out.put("traffic_limit_bytes", s.trafficLimit);
        out.put("public_location", s.publicLocation);
        if (s.notes != null && !s.notes.isEmpty()) {
            out.put("notes", s.notes);
        }
        out.put("created_at", timestamp(s.createdAt));
        if (s.lastErrorKind != null && !s.lastErrorKind.isEmpty()) {
            out.put("last_error_kind", s.lastErrorKind);
        }
        if (s.lastError != null && !s.lastError.isEmpty()) {
            out.put("last_error", s.lastError);
        }
        if (s.lastErrorAt != null) {
            out.put("last_error_at", timestamp(s.lastErrorAt));
        }
        // Go marshals a nil slice as null but the frontend reads tags only via
        // optional chaining; emit [] to stay friendlier to strict consumers.
        if (s.tags != null) {
            List<Map<String, Object>> tags = new ArrayList<>();
            for (Tag t : s.tags) {
                tags.add(tag(t));
            }
            out.put("tags", tags);
        }
        out.put("latest_metrics", s.latestMetrics == null ? null : latestMetrics(s.latestMetrics));
        return out;
    }

    public static Map<String, Object> loginRecord(LoginRecord r) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", r.id());
        out.put("user_id", r.userId());
        out.put("ip", r.ip());
        out.put("user_agent", r.userAgent());
        out.put("success", r.success());
        out.put("logged_at", timestamp(r.loggedAt()));
        return out;
    }
}
