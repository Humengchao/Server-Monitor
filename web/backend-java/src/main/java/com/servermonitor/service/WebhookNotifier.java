package com.servermonitor.service;

import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.servermonitor.config.MonitorConfig;
import com.servermonitor.repo.Alerts;
import com.servermonitor.repo.Alerts.AlertRule;
import com.servermonitor.repo.Alerts.AlertSnapshot;

/**
 * Delivers alert transitions to a user-configured URL, mirroring
 * services/alerts.go's WebhookNotifier. Unless ALLOW_PRIVATE_WEBHOOKS is set the
 * target must resolve to publicly routable addresses only — an authenticated
 * user must not be able to aim the alerting pipeline into the server's private
 * network. Redirects are followed and re-validated hop by hop.
 */
@Service
public class WebhookNotifier {

    private static final Logger log = LoggerFactory.getLogger(WebhookNotifier.class);
    private static final ObjectMapper JSON = new ObjectMapper();

    private static final Duration TIMEOUT = Duration.ofSeconds(6);
    private static final int MAX_URL_LENGTH = 4096;
    private static final int MAX_REDIRECTS = 5;

    private final boolean allowPrivate;
    private final HttpClient client;
    private final ExecutorService posts = Executors.newCachedThreadPool(r -> {
        Thread t = new Thread(r, "webhook-post");
        t.setDaemon(true);
        return t;
    });

    public WebhookNotifier(MonitorConfig cfg) {
        this.allowPrivate = cfg.isAllowPrivateWebhooks();
        this.client = HttpClient.newBuilder()
            .connectTimeout(TIMEOUT)
            .followRedirects(HttpClient.Redirect.NEVER)
            .build();
    }

    /** Posts the transition asynchronously; delivery failures are logged and never block the evaluation loop. */
    public void send(AlertRule rule, AlertSnapshot snap, String status, double value, Instant at) {
        String target = rule.webhookUrl() == null ? "" : rule.webhookUrl().trim();
        if (target.isEmpty()) {
            return;
        }
        String message = "resolved".equals(status)
            ? formatRecoveryMessage(rule, snap, value)
            : formatAlertMessage(rule, snap, value);
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("status", status);
        payload.put("rule", rule.name());
        payload.put("server", snap.name());
        payload.put("metric", rule.metric());
        payload.put("value", value);
        payload.put("threshold", rule.threshold());
        payload.put("comparator", rule.comparator());
        payload.put("message", message);
        payload.put("timestamp", at.toString());
        posts.submit(() -> {
            try {
                post(target, payload);
            } catch (Exception e) {
                log.warn("alerts: webhook for \"{}\" failed: {}", rule.name(), e.getMessage());
            }
        });
    }

    /** POSTs the payload, following redirects only onto targets that themselves validate. */
    public void post(String target, Object payload) throws IOException, InterruptedException {
        byte[] body;
        try {
            body = JSON.writeValueAsBytes(payload);
        } catch (Exception e) {
            throw new IOException("marshal webhook payload: " + e.getMessage(), e);
        }
        String current = target;
        for (int hop = 0; ; hop++) {
            validateTarget(current);
            HttpRequest request = HttpRequest.newBuilder()
                .uri(URI.create(current))
                .timeout(TIMEOUT)
                .header("Content-Type", "application/json")
                .header("User-Agent", "server-monitor-alerts/1")
                .POST(HttpRequest.BodyPublishers.ofByteArray(body))
                .build();
            HttpResponse<byte[]> response = client.send(request, HttpResponse.BodyHandlers.ofByteArray());
            int status = response.statusCode();
            if (status >= 300 && status < 400 && response.headers().firstValue("Location").isPresent()) {
                if (hop >= MAX_REDIRECTS) {
                    throw new IOException("webhook responded " + status + " after " + MAX_REDIRECTS + " redirects");
                }
                current = response.uri().resolve(response.headers().firstValue("Location").get()).toString();
                continue;
            }
            if (status >= 300) {
                throw new IOException("webhook responded " + status + " " + new String(response.body(), StandardCharsets.UTF_8).lines().findFirst().orElse(""));
            }
            return;
        }
    }

    /** Exposes target validation to handlers so a bad URL is rejected at save time. */
    public void validateWebhookURL(String target) {
        if (target == null || target.trim().isEmpty()) {
            return;
        }
        if (target.length() > MAX_URL_LENGTH) {
            throw new IllegalArgumentException("webhook URL is too long");
        }
        try {
            validateTarget(target.trim());
        } catch (IOException e) {
            throw new IllegalArgumentException(e.getMessage(), e);
        }
    }

    private void validateTarget(String target) throws IOException {
        URI parsed;
        try {
            parsed = URI.create(target);
        } catch (IllegalArgumentException e) {
            throw new IOException("invalid webhook URL: " + e.getMessage(), e);
        }
        String scheme = parsed.getScheme();
        if (scheme == null || !(scheme.equalsIgnoreCase("http") || scheme.equalsIgnoreCase("https"))) {
            throw new IOException("webhook URL must use http or https");
        }
        String host = parsed.getHost();
        if (host == null || host.isEmpty()) {
            throw new IOException("webhook URL has no host");
        }
        if (allowPrivate) {
            return;
        }
        InetAddress[] ips;
        try {
            ips = InetAddress.getAllByName(host);
        } catch (java.net.UnknownHostException e) {
            throw new IOException("resolve webhook host: " + e.getMessage(), e);
        }
        for (InetAddress ip : ips) {
            if (isRestricted(ip)) {
                throw new IOException("webhook host " + host
                    + " resolves to a non-public address (set ALLOW_PRIVATE_WEBHOOKS=true to permit)");
            }
        }
    }

    private static boolean isRestricted(InetAddress ip) {
        if (ip.isLoopbackAddress() || ip.isAnyLocalAddress() || ip.isLinkLocalAddress()
            || ip.isSiteLocalAddress() || ip.isMulticastAddress()) {
            return true;
        }
        byte[] b = ip.getAddress();
        if (b.length == 4) {
            int first = b[0] & 0xff;
            int second = b[1] & 0xff;
            if (first == 100 && second >= 64 && second <= 127) {
                // CGNAT 100.64.0.0/10 is not RFC1918 but equally unsuitable.
                return true;
            }
            if (first == 169 && second == 254) {
                return true;
            }
        }
        return false;
    }

    // --- message formatting (shared with the engine) ---------------------

    /** The one-line summary stored with the event and sent to webhooks when an alert opens. */
    public static String formatAlertMessage(AlertRule rule, AlertSnapshot snap, double value) {
        if (Alerts.METRIC_OFFLINE.equals(rule.metric())) {
            return snap.name() + " has not reported for "
                + formatDuration(Duration.ofSeconds((long) value));
        }
        String unit = Alerts.METRIC_UNITS.getOrDefault(rule.metric(), "");
        return String.format(Locale.ROOT, "%s %s is %s%s (%s %s%s for %s)",
            snap.name(), rule.metric(), formatFloat1(value), unit,
            rule.comparator(), formatFloat(rule.threshold()), unit,
            formatDuration(Duration.ofSeconds(rule.durationSeconds())));
    }

    /** Describes the return to normal; reusing the firing wording here would contradict itself. */
    public static String formatRecoveryMessage(AlertRule rule, AlertSnapshot snap, double value) {
        if (Alerts.METRIC_OFFLINE.equals(rule.metric())) {
            return snap.name() + " is reporting again";
        }
        String unit = Alerts.METRIC_UNITS.getOrDefault(rule.metric(), "");
        return String.format(Locale.ROOT, "%s %s recovered to %s%s (threshold %s %s%s)",
            snap.name(), rule.metric(), formatFloat1(value), unit,
            rule.comparator(), formatFloat(rule.threshold()), unit);
    }

    /** strconv.FormatFloat(v, 'f', 1, 64). */
    static String formatFloat1(double v) {
        return String.format(Locale.ROOT, "%.1f", v);
    }

    /** strconv.FormatFloat(v, 'f', -1, 64) — shortest decimal that round-trips. */
    static String formatFloat(double v) {
        if (v == Math.floor(v) && !Double.isInfinite(v) && Math.abs(v) < 1e15) {
            return Long.toString((long) v);
        }
        String repr = Double.toString(v);
        return repr.endsWith(".0") ? repr.substring(0, repr.length() - 2) : repr;
    }

    static String formatDuration(Duration d) {
        long seconds = d.toSeconds();
        if (seconds < 60) {
            return seconds + "s";
        }
        if (seconds < 3600) {
            return (seconds / 60) + "m";
        }
        return (seconds / 3600) + "h" + ((seconds / 60) % 60) + "m";
    }
}
