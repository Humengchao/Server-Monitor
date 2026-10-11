package com.servermonitor.api;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.PublicStatusRepo;
import com.servermonitor.repo.Uptime;
import com.servermonitor.service.UptimeService;

/**
 * /api/public/status — the anonymized probe page's feed. Only the anonymous
 * alias the handler assigns is published; names, hosts, ports, credentials,
 * user ids, notes and database ids never leave this layer.
 */
@RestController
public class PublicStatusController {

    private static final Logger log = LoggerFactory.getLogger(PublicStatusController.class);

    /** Metrics only change once per poll interval; the census result is shared briefly. */
    private static final Duration CACHE_TTL = Duration.ofSeconds(2);
    /** Four hours of fifteen-minute buckets; under that an availability figure is noise. */
    private static final int MIN_AVAILABILITY_BUCKETS = 16;

    private final PublicStatusRepo repo;

    private Map<String, Object> cached;
    private Instant cacheExpires = Instant.EPOCH;

    public PublicStatusController(PublicStatusRepo repo) {
        this.repo = repo;
    }

    @GetMapping("/api/public/status")
    public synchronized Map<String, Object> get() {
        if (cached != null && Instant.now().isBefore(cacheExpires)) {
            return cached;
        }
        Instant now = Instant.now();
        Instant availabilityEnd = UptimeService.tierEnd(now, Uptime.DAILY_STRIP_BUCKET_SECONDS);
        List<PublicStatusRepo.PublicServerMetric> metrics;
        try {
            metrics = repo.getPublicServerMetrics(availabilityEnd.minus(Duration.ofDays(30)), availabilityEnd);
        } catch (Exception e) {
            log.warn("public status error: {}", e.toString());
            throw ApiException.of(500, "status temporarily unavailable");
        }

        List<Map<String, Object>> nodes = new ArrayList<>();
        int online = 0, degraded = 0, offline = 0;
        long memoryUsedSum = 0, memoryTotalSum = 0, diskUsedSum = 0, diskTotalSum = 0,
            trafficSum = 0, rxSum = 0, txSum = 0;
        double remainingValueSum = 0;

        int index = 0;
        for (PublicStatusRepo.PublicServerMetric metric : metrics) {
            index++;
            int memoryPercent = percent(metric.memoryUsed(), metric.memoryTotal());
            int diskPercent = percent(metric.diskUsed(), metric.diskTotal());
            long trafficTotal = metric.networkRxTotal() + metric.networkTxTotal();
            int trafficPercent = percent(trafficTotal, metric.trafficLimit());
            int cpuPercent = clampPercent(metric.cpuPercent());
            String status = "offline";
            if (metric.recordedAt() != null && Duration.between(metric.recordedAt(), now).compareTo(Duration.ofMinutes(2)) < 0) {
                status = "online";
                if (cpuPercent >= 85 || memoryPercent >= 90 || diskPercent >= 90) {
                    status = "degraded";
                }
            }
            switch (status) {
                case "online" -> online++;
                case "degraded" -> degraded++;
                default -> offline++;
            }

            double[] remaining = calculateRemaining(now, metric.expiresAt(), metric.billingPrice(), metric.billingCycle());
            int packetLoss = "offline".equals(status) ? 100 : 0;

            // Below a few hours of history the number says more about when the
            // node was added than about its reliability, so publish nothing.
            Double availability = null;
            if (metric.availability30dExpected() >= MIN_AVAILABILITY_BUCKETS) {
                availability = UptimeService.availabilityPercent(metric.availability30dObserved(),
                    metric.availability30dExpected());
            }

            List<Map<String, Object>> tags = new ArrayList<>();
            for (PublicStatusRepo.PublicTag t : metric.tags()) {
                tags.add(Map.of("name", t.name(), "color", t.color()));
            }

            Map<String, Object> node = new LinkedHashMap<>();
            node.put("alias", String.format("NODE %02d", index));
            node.put("location", metric.publicLocation());
            node.put("tags", tags);
            node.put("server_type", metric.serverType());
            node.put("status", status);
            node.put("cpu_cores", metric.cpuCores());
            node.put("cpu_percent", cpuPercent);
            node.put("load_1", metric.load1());
            node.put("load_5", metric.load5());
            node.put("load_15", metric.load15());
            node.put("memory_used", metric.memoryUsed());
            node.put("memory_total", metric.memoryTotal());
            node.put("memory_percent", memoryPercent);
            node.put("disk_used", metric.diskUsed());
            node.put("disk_total", metric.diskTotal());
            node.put("disk_percent", diskPercent);
            node.put("network_rx_bytes", metric.networkRxBytes());
            node.put("network_tx_bytes", metric.networkTxBytes());
            node.put("network_rx_total_bytes", metric.networkRxTotal());
            node.put("network_tx_total_bytes", metric.networkTxTotal());
            node.put("traffic_limit_bytes", metric.trafficLimit());
            node.put("traffic_percent", trafficPercent);
            node.put("uptime_seconds", metric.uptimeSeconds());
            node.put("expires_at", metric.expiresAt() == null ? null : Jsons.timestamp(metric.expiresAt()));
            node.put("remaining_days", (int) remaining[0]);
            node.put("billing_price", metric.billingPrice());
            node.put("billing_currency", metric.billingCurrency());
            node.put("billing_cycle", metric.billingCycle());
            node.put("remaining_value", remaining[1]);
            node.put("latency_ms", metric.latencyMs());
            node.put("packet_loss_percent", packetLoss);
            node.put("availability_30d", availability);
            nodes.add(node);

            memoryUsedSum += metric.memoryUsed();
            memoryTotalSum += metric.memoryTotal();
            diskUsedSum += metric.diskUsed();
            diskTotalSum += metric.diskTotal();
            trafficSum += trafficTotal;
            rxSum += metric.networkRxBytes();
            txSum += metric.networkTxBytes();
            remainingValueSum += remaining[1];
        }

        String overall = "operational";
        if (!metrics.isEmpty() && online == 0 && degraded == 0) {
            overall = "outage";
        } else if (degraded > 0 || offline > 0) {
            overall = "degraded";
        }

        Map<String, Object> summary = new LinkedHashMap<>();
        summary.put("total", metrics.size());
        summary.put("online", online);
        summary.put("degraded", degraded);
        summary.put("offline", offline);
        summary.put("memory_used", memoryUsedSum);
        summary.put("memory_total", memoryTotalSum);
        summary.put("disk_used", diskUsedSum);
        summary.put("disk_total", diskTotalSum);
        summary.put("traffic_total_bytes", trafficSum);
        summary.put("network_rx_bytes", rxSum);
        summary.put("network_tx_bytes", txSum);
        summary.put("remaining_value", remainingValueSum);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("overall", overall);
        response.put("generated_at", Jsons.timestamp(now));
        response.put("summary", summary);
        response.put("nodes", nodes);
        response.put("privacy", Map.of(
            "anonymized", true,
            "hidden_fields", List.of("server_name", "hostname", "ip_address", "port",
                "ssh_user", "credentials", "notes", "database_id")));

        cached = response;
        cacheExpires = Instant.now().plus(CACHE_TTL);
        return response;
    }

    private static int percent(long used, long total) {
        if (total <= 0) {
            return 0;
        }
        return clampPercent((double) used / total * 100);
    }

    private static int clampPercent(double value) {
        return Math.max(0, Math.min(100, (int) Math.round(value)));
    }

    /** Remaining days and the not-yet-consumed share of the billing period's price. */
    private static double[] calculateRemaining(Instant now, Instant expiresAt, double price, String cycle) {
        if (expiresAt == null || !expiresAt.isAfter(now)) {
            return new double[]{0, 0};
        }
        int days = (int) Math.ceil(Duration.between(now, expiresAt).toHours() / 24.0);
        Double cycleDays = switch (cycle) {
            case "month" -> 30.0;
            case "quarter" -> 91.0;
            case "half_year" -> 182.0;
            case "year" -> 365.0;
            default -> null;
        };
        if (cycleDays == null || price <= 0) {
            return new double[]{days, 0};
        }
        return new double[]{days, Math.round(price * days / cycleDays * 100) / 100.0};
    }
}
