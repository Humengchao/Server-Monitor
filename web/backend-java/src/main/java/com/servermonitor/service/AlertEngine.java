package com.servermonitor.service;

import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.context.SmartLifecycle;
import org.springframework.stereotype.Service;

import com.servermonitor.config.MonitorConfig;
import com.servermonitor.repo.Alerts;
import com.servermonitor.repo.Alerts.AlertRule;
import com.servermonitor.repo.Alerts.AlertSnapshot;

import jakarta.annotation.PreDestroy;

/**
 * Periodically evaluates threshold rules against the latest metric sample of
 * every server and opens/resolves alert events, mirroring services/alerts.go.
 * Open episodes are rows in alert_events, so a restart neither re-notifies for
 * alerts already firing nor loses a pending resolve.
 */
@Service
public class AlertEngine implements SmartLifecycle {

    private static final Logger log = LoggerFactory.getLogger(AlertEngine.class);

    /** Matches the dashboard's online/offline threshold. */
    static final Duration OFFLINE_FLOOR = Duration.ofMinutes(2);
    /** How long a threshold rule tolerates missing samples before resolving an open alert. */
    static final Duration STALE_FLOOR = Duration.ofMinutes(10);
    private static final Duration EVENT_RETENTION = Duration.ofDays(30);
    private static final Duration PRUNE_INTERVAL = Duration.ofHours(6);

    private final Alerts alerts;
    private final WebhookNotifier notifier;
    private final long intervalMillis;

    private record Key(UUID ruleId, UUID serverId) {
    }

    private final Object mu = new Object();
    private final Map<Key, Instant> pending = new HashMap<>();
    private final Map<Key, Boolean> firing = new HashMap<>();

    private final AtomicBoolean running = new AtomicBoolean();
    private Thread loop;

    public AlertEngine(Alerts alerts, WebhookNotifier notifier, MonitorConfig cfg) {
        this.alerts = alerts;
        this.notifier = notifier;
        this.intervalMillis = Math.max(1, cfg.getAlertIntervalSeconds()) * 1000L;
    }

    @Override
    public void start() {
        if (!running.compareAndSet(false, true)) {
            return;
        }
        restoreFiringState();
        loop = new Thread(() -> {
            long lastPrune = 0;
            while (running.get()) {
                try {
                    Thread.sleep(intervalMillis);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                }
                evaluateOnce(Instant.now());
                if (System.currentTimeMillis() - lastPrune >= PRUNE_INTERVAL.toMillis()) {
                    lastPrune = System.currentTimeMillis();
                    try {
                        alerts.pruneAlertEvents(Instant.now().minus(EVENT_RETENTION));
                    } catch (Exception e) {
                        log.warn("alerts: prune events: {}", e.toString());
                    }
                }
            }
        }, "alert-engine");
        loop.setDaemon(true);
        loop.start();
    }

    @Override
    public void stop() {
        running.set(false);
        if (loop != null) {
            loop.interrupt();
        }
    }

    @PreDestroy
    void destroy() {
        stop();
    }

    @Override
    public boolean isRunning() {
        return running.get();
    }

    /** Reloads open events so restarts neither re-notify nor double-resolve. */
    private void restoreFiringState() {
        try {
            var open = alerts.getOpenAlertKeys();
            synchronized (mu) {
                open.forEach((ruleId, serverIds) -> {
                    for (UUID serverId : serverIds) {
                        firing.put(new Key(ruleId, serverId), Boolean.TRUE);
                    }
                });
            }
        } catch (Exception e) {
            log.warn("alerts: restore firing state: {}", e.toString());
        }
    }

    void evaluateOnce(Instant now) {
        java.util.List<AlertRule> rules;
        try {
            rules = alerts.getEnabledAlertRules();
        } catch (Exception e) {
            log.warn("alerts: load rules: {}", e.toString());
            return;
        }
        if (rules.isEmpty()) {
            releaseStale(java.util.Set.of(), now);
            return;
        }
        java.util.List<AlertSnapshot> snapshots;
        try {
            snapshots = alerts.getAlertSnapshots();
        } catch (Exception e) {
            log.warn("alerts: load snapshots: {}", e.toString());
            return;
        }
        java.util.Set<Key> live = new java.util.HashSet<>();
        for (AlertRule rule : rules) {
            for (AlertSnapshot snap : snapshots) {
                if (!snap.userId().equals(rule.userId())) {
                    continue;
                }
                if (rule.serverId() != null && !rule.serverId().equals(snap.serverId())) {
                    continue;
                }
                live.add(new Key(rule.id(), snap.serverId()));
                applyRule(rule, snap, now);
            }
        }
        releaseStale(live, now);
    }

    private enum State {CLEAR, BREACHED, UNKNOWN}

    private record Evaluation(double value, State state) {
    }

    private void applyRule(AlertRule rule, AlertSnapshot snap, Instant now) {
        Key key = new Key(rule.id(), snap.serverId());

        Evaluation eval = switch (1) {
            default -> {
                Duration age = Duration.between(snap.recordedAt(), now);
                if (!snap.hasMetrics()) {
                    age = STALE_FLOOR.plus(OFFLINE_FLOOR);
                }
                if (Alerts.METRIC_OFFLINE.equals(rule.metric())) {
                    Duration window = max(Duration.ofSeconds(rule.durationSeconds()), OFFLINE_FLOOR);
                    yield age.compareTo(window) >= 0
                        ? new Evaluation(age.toSeconds(), State.BREACHED)
                        : new Evaluation(age.toSeconds(), State.CLEAR);
                }
                if (age.compareTo(STALE_FLOOR) >= 0) {
                    // The host stopped reporting; settle an open threshold alert
                    // rather than leaving it stuck forever.
                    yield new Evaluation(0, State.CLEAR);
                }
                Optional<Double> value = sampleMetric(rule.metric(), snap);
                if (value.isEmpty()) {
                    yield new Evaluation(0, State.UNKNOWN);
                }
                boolean breached = "<".equals(rule.comparator())
                    ? value.get() < rule.threshold() : value.get() > rule.threshold();
                yield new Evaluation(value.get(), breached ? State.BREACHED : State.CLEAR);
            }
        };

        switch (eval.state()) {
            case UNKNOWN -> {
                synchronized (mu) {
                    pending.remove(key);
                }
                return;
            }
            case CLEAR -> {
                boolean wasFiring;
                synchronized (mu) {
                    pending.remove(key);
                    wasFiring = firing.containsKey(key);
                }
                if (!wasFiring) {
                    return;
                }
                boolean resolved;
                try {
                    resolved = alerts.resolveAlertEvent(rule.id(), snap.serverId(), now);
                } catch (Exception e) {
                    // Keep the key tracked so the next tick retries the close.
                    log.warn("alerts: resolve {} on {}: {}", rule.name(), snap.name(), e.toString());
                    return;
                }
                synchronized (mu) {
                    firing.remove(key);
                }
                if (resolved) {
                    notifier.send(rule, snap, "resolved", eval.value(), now);
                }
                return;
            }
            case BREACHED -> {
                Instant since;
                synchronized (mu) {
                    if (firing.containsKey(key)) {
                        return;
                    }
                    since = pending.get(key);
                    if (since == null) {
                        since = now;
                        pending.put(key, since);
                    }
                }
                // "offline" fires as soon as the condition is observed: its own
                // duration lives in the staleness check, not the debounce.
                if (!Alerts.METRIC_OFFLINE.equals(rule.metric())) {
                    if (Duration.between(since, now).toSeconds() < rule.durationSeconds()) {
                        return;
                    }
                }
                String message = WebhookNotifier.formatAlertMessage(rule, snap, eval.value());
                Optional<Long> id;
                try {
                    id = alerts.openAlertEvent(rule.id(), snap.serverId(), rule.userId(),
                        eval.value(), message, now);
                } catch (Exception e) {
                    log.warn("alerts: open {} on {}: {}", rule.name(), snap.name(), e.toString());
                    return;
                }
                synchronized (mu) {
                    firing.put(key, Boolean.TRUE);
                    pending.remove(key);
                }
                if (id.isPresent()) {
                    log.info("alerts: firing \"{}\" on {} ({})", rule.name(), snap.name(), message);
                    notifier.send(rule, snap, "firing", eval.value(), now);
                }
            }
        }
    }

    /**
     * Closes open events for rule/server pairs the current rule set no longer
     * covers — a disabled rule, a narrowed scope, a deleted server — so pausing
     * a firing rule cannot leave it counted as active forever.
     */
    private void releaseStale(java.util.Set<Key> live, Instant now) {
        java.util.List<Key> orphaned = new java.util.ArrayList<>();
        synchronized (mu) {
            pending.keySet().removeIf(k -> !live.contains(k));
            for (Key key : firing.keySet()) {
                if (!live.contains(key)) {
                    orphaned.add(key);
                }
            }
        }
        for (Key key : orphaned) {
            boolean closed;
            try {
                closed = alerts.resolveAlertEvent(key.ruleId(), key.serverId(), now);
            } catch (Exception e) {
                log.warn("alerts: release {}/{}: {}", key.ruleId(), key.serverId(), e.toString());
                continue;
            }
            if (closed || true) {
                synchronized (mu) {
                    firing.remove(key);
                }
            }
        }
    }

    private static Optional<Double> sampleMetric(String metric, AlertSnapshot snap) {
        return switch (metric) {
            case Alerts.METRIC_CPU -> Optional.of(snap.cpuPercent());
            case Alerts.METRIC_MEMORY -> snap.memTotal() <= 0
                ? Optional.empty() : Optional.of((double) snap.memoryUsed() / snap.memTotal() * 100);
            case Alerts.METRIC_DISK -> snap.diskTotal() <= 0
                ? Optional.empty() : Optional.of((double) snap.diskUsed() / snap.diskTotal() * 100);
            case Alerts.METRIC_LOAD1 -> Optional.of(snap.load1());
            // A zero sample means the ping probe produced no answer, not 0 ms.
            case Alerts.METRIC_LATENCY -> snap.latencyMs() <= 0
                ? Optional.empty() : Optional.of((double) snap.latencyMs());
            default -> Optional.empty();
        };
    }

    static Duration max(Duration a, Duration b) {
        return a.compareTo(b) >= 0 ? a : b;
    }
}
