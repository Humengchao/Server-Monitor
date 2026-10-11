package com.servermonitor.api;

import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Metrics;
import com.servermonitor.repo.Servers;

import jakarta.servlet.http.HttpServletRequest;

/** /api/servers/{id}/metrics — latest point and the tiered history series. */
@RestController
@RequestMapping("/api/servers")
public class MetricsController {

    private final Servers servers;
    private final Metrics metrics;

    public MetricsController(Servers servers, Metrics metrics) {
        this.servers = servers;
        this.metrics = metrics;
    }

    private UUID owned(String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        if (!servers.serverOwnedByUser(serverId, userId)) {
            throw ApiException.notFound("server not found");
        }
        return serverId;
    }

    @GetMapping("/{id}/metrics/latest")
    public ResponseEntity<Object> latest(@PathVariable String id, HttpServletRequest req) {
        UUID serverId = owned(id, req);
        var m = metrics.getLatestMetric(serverId);
        // No sample yet answers Go's literal "null" body.
        return m.<ResponseEntity<Object>>map(point -> ResponseEntity.ok(Jsons.metricPoint(point)))
            .orElseGet(() -> ResponseEntity.ok(null));
    }

    @GetMapping("/{id}/metrics")
    public List<Map<String, Object>> history(@PathVariable String id, HttpServletRequest req,
        @RequestParam(required = false) String since, @RequestParam(required = false) String until) {
        UUID serverId = owned(id, req);
        Instant sinceT = Instant.now().minus(1, ChronoUnit.HOURS);
        if (since != null && !since.isEmpty()) {
            try {
                sinceT = parseRfc3339(since);
            } catch (DateTimeParseException ignored) { }
        }
        Instant untilT = Instant.now();
        if (until != null && !until.isEmpty()) {
            try {
                untilT = parseRfc3339(until);
            } catch (DateTimeParseException ignored) { }
        }
        return metrics.getMetrics(serverId, sinceT, untilT).stream().map(Jsons::metricPoint).toList();
    }

    private static Instant parseRfc3339(String raw) {
        String value = raw.trim();
        try {
            return Instant.parse(value);
        } catch (DateTimeParseException e) {
            return java.time.OffsetDateTime.parse(value).toInstant();
        }
    }
}
