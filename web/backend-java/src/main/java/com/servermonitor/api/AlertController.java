package com.servermonitor.api;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Alerts;
import com.servermonitor.repo.Alerts.AlertRule;
import com.servermonitor.service.WebhookNotifier;

import jakarta.servlet.http.HttpServletRequest;

/** /api/alerts/* — threshold rules, the event timeline, the header badge, and the webhook probe. */
@RestController
@RequestMapping("/api/alerts")
public class AlertController {

    private static final int ALERT_MIN_DURATION = 30;
    private static final int ALERT_MAX_DURATION = 24 * 60 * 60;

    private final Alerts alerts;
    private final WebhookNotifier notifier;

    public AlertController(Alerts alerts, WebhookNotifier notifier) {
        this.alerts = alerts;
        this.notifier = notifier;
    }

    public record RuleBody(String name, UUID server_id, String metric, String comparator,
        Double threshold, Integer duration_seconds, Boolean enabled, String webhook_url) {
    }

    private static Map<String, Object> ruleView(AlertRule r) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", r.id());
        out.put("user_id", r.userId());
        out.put("name", r.name());
        out.put("server_id", r.serverId());
        if (r.serverName() != null && !r.serverName().isEmpty()) {
            out.put("server_name", r.serverName());
        }
        out.put("metric", r.metric());
        out.put("comparator", r.comparator());
        out.put("threshold", r.threshold());
        out.put("duration_seconds", r.durationSeconds());
        out.put("enabled", r.enabled());
        out.put("webhook_url", r.webhookUrl());
        out.put("created_at", r.createdAt() == null ? null : Jsons.timestamp(r.createdAt()));
        out.put("firing_count", r.firingCount());
        return out;
    }

    /** Validates user input and applies the defaults the engine expects. */
    private AlertRule normalize(RuleBody req, UUID ruleId, UUID userId) {
        if (req == null || req.name() == null || req.name().trim().isEmpty()) {
            throw ApiException.badRequest("rule name is required");
        }
        String name = req.name().trim();
        if (name.length() > 128) {
            throw ApiException.badRequest("rule name is too long");
        }
        String metric = req.metric() == null ? "" : req.metric().trim().toLowerCase();
        if (!Alerts.validAlertMetric(metric)) {
            throw ApiException.badRequest("unsupported metric \"" + metric + "\"");
        }
        String comparator = req.comparator() == null || req.comparator().trim().isEmpty()
            ? ">" : req.comparator().trim();
        if (!comparator.equals(">") && !comparator.equals("<")) {
            throw ApiException.badRequest("comparator must be > or <");
        }
        double threshold = req.threshold() == null ? 0 : req.threshold();
        switch (metric) {
            case Alerts.METRIC_CPU, Alerts.METRIC_MEMORY, Alerts.METRIC_DISK -> {
                if (threshold < 0 || threshold > 100) {
                    throw ApiException.badRequest("threshold for " + metric + " must be between 0 and 100");
                }
            }
            case Alerts.METRIC_OFFLINE -> {
                threshold = 0;
                comparator = ">";
            }
            default -> {
                if (threshold < 0) {
                    throw ApiException.badRequest("threshold must not be negative");
                }
            }
        }
        int duration = req.duration_seconds() == null || req.duration_seconds() == 0
            ? 300 : req.duration_seconds();
        if (duration < ALERT_MIN_DURATION || duration > ALERT_MAX_DURATION) {
            throw ApiException.badRequest(
                "duration must be between " + ALERT_MIN_DURATION + " and " + ALERT_MAX_DURATION + " seconds");
        }
        String webhookUrl = req.webhook_url() == null ? "" : req.webhook_url().trim();
        try {
            notifier.validateWebhookURL(webhookUrl);
        } catch (IllegalArgumentException e) {
            throw ApiException.badRequest(e.getMessage());
        }
        return new AlertRule(ruleId, userId, name, req.server_id(), "", metric, comparator, threshold,
            duration, req.enabled() == null || req.enabled(), webhookUrl, Instant.now(), 0);
    }

    @GetMapping("/rules")
    public List<Map<String, Object>> listRules(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        return alerts.getAlertRulesByUserId(userId).stream().map(AlertController::ruleView).toList();
    }

    @PostMapping("/rules")
    public ResponseEntity<Map<String, Object>> createRule(@RequestBody RuleBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        AlertRule rule = normalize(body, UUID.randomUUID(), userId);
        boolean inserted = alerts.createAlertRule(rule);
        if (!inserted) {
            throw ApiException.badRequest("server not found");
        }
        return ResponseEntity.status(HttpStatus.CREATED).body(ruleView(rule));
    }

    @PutMapping("/rules/{id}")
    public Map<String, Object> updateRule(@PathVariable String id, @RequestBody RuleBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID ruleId = UuidParser.parse(id);
        AlertRule rule = normalize(body, ruleId, userId);
        if (!alerts.updateAlertRule(rule)) {
            throw ApiException.notFound("alert rule not found");
        }
        return ruleView(rule);
    }

    @DeleteMapping("/rules/{id}")
    public Map<String, Object> deleteRule(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        alerts.deleteAlertRule(UuidParser.parse(id), userId);
        return Map.of("message", "deleted");
    }

    @GetMapping("/events")
    public List<Map<String, Object>> listEvents(HttpServletRequest req,
        @RequestParam(required = false) String active, @RequestParam(required = false) Integer limit) {
        UUID userId = AuthSupport.userId(req);
        boolean activeOnly = "1".equals(active) || "true".equals(active);
        var events = alerts.getAlertEventsByUserId(userId, activeOnly, limit == null ? 0 : limit);
        return events.stream().map(AlertController::eventView).toList();
    }

    private static Map<String, Object> eventView(Alerts.AlertEvent e) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", e.id());
        out.put("rule_id", e.ruleId());
        out.put("rule_name", e.ruleName());
        out.put("metric", e.metric());
        out.put("server_id", e.serverId());
        out.put("server_name", e.serverName());
        out.put("value", e.value());
        out.put("message", e.message());
        out.put("comparator", e.comparator());
        out.put("threshold", e.threshold());
        out.put("duration_seconds", e.durationSeconds());
        out.put("started_at", Jsons.timestamp(e.startedAt()));
        out.put("resolved_at", e.resolvedAt() == null ? null : Jsons.timestamp(e.resolvedAt()));
        return out;
    }

    @GetMapping("/summary")
    public Map<String, Object> summary(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        return Map.of("active", alerts.countActiveAlerts(userId));
    }

    public record TestBody(String webhook_url) {
    }

    /** Delivers a sample payload so a user can verify their endpoint before a real breach. */
    @PostMapping("/test")
    public Map<String, Object> testWebhook(@RequestBody TestBody body) {
        if (body == null || body.webhook_url() == null || body.webhook_url().trim().isEmpty()) {
            throw ApiException.badRequest("webhook_url is required");
        }
        String url = body.webhook_url().trim();
        try {
            notifier.validateWebhookURL(url);
        } catch (IllegalArgumentException e) {
            throw ApiException.badRequest(e.getMessage());
        }
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("status", "test");
        payload.put("rule", "Webhook test");
        payload.put("server", "example-host");
        payload.put("metric", "cpu");
        payload.put("value", 93.4);
        payload.put("threshold", 90);
        payload.put("message", "This is a test notification from Server Monitor.");
        payload.put("timestamp", Instant.now().toString());
        try {
            notifier.post(url, payload);
        } catch (Exception e) {
            throw ApiException.of(502, e.getMessage());
        }
        return Map.of("message", "sent");
    }
}
