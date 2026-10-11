package com.servermonitor.service;

import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Semaphore;
import java.util.concurrent.TimeUnit;

import org.apache.sshd.client.session.ClientSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.context.SmartLifecycle;
import org.springframework.stereotype.Service;

import com.servermonitor.config.MonitorConfig;
import com.servermonitor.repo.Metrics;
import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.ssh.SshSupport;

import jakarta.annotation.PreDestroy;

/**
 * Periodically polls every registered server over SSH and stores the sample,
 * mirroring services/metrics.go's Collector. Each host is scheduled
 * independently (a slow host stays in-flight and is skipped next tick while
 * healthy hosts keep their cadence), failures back off exponentially, and
 * authentication failures back off harder (fail2ban risk).
 *
 * Metric history maintenance (rollups + retention pruning) runs on its own
 * loop so it never blocks live collection.
 */
@Service
public class Collector implements SmartLifecycle {

    private static final Logger log = LoggerFactory.getLogger(Collector.class);

    static final Duration SSH_COLLECTION_TIMEOUT = Duration.ofSeconds(20);
    private static final Duration MAINTENANCE_DELAY = Duration.ofSeconds(15);
    private static final Duration MAINTENANCE_INTERVAL = Duration.ofMinutes(1);
    private static final int METRIC_DELETE_BATCH = 5000;
    private static final int METRIC_DELETE_RUNS = 20;
    private static final Duration POLL_FAILURE_BASE = Duration.ofSeconds(15);
    private static final Duration POLL_FAILURE_MAX = Duration.ofMinutes(15);
    private static final Duration SECURITY_FAILURE_BASE = Duration.ofMinutes(10);
    private static final Duration SECURITY_FAILURE_MAX = Duration.ofHours(1);

    /** Which failure mode PollNow reports back to the operator. */
    public enum PollNowResult {
        OK, IN_FLIGHT, BUSY, STOPPED, FAILED
    }

    public static class PollNowException extends Exception {
        private final PollNowResult result;
        private final PollErrorClassifiers.Kind kind;
        private final String detail;

        public PollNowException(PollNowResult result, PollErrorClassifiers.Kind kind, String detail) {
            super(detail);
            this.result = result;
            this.kind = kind;
            this.detail = detail;
        }

        public PollNowResult getResult() {
            return result;
        }

        public PollErrorClassifiers.Kind getKind() {
            return kind;
        }

        public String getDetail() {
            return detail;
        }
    }

    private final Servers servers;
    private final Metrics metrics;
    private final long pollIntervalMillis;

    private final Object mu = new Object();
    /** Previous cumulative counters per server, for rate derivation. */
    private final Map<UUID, long[]> prev = new HashMap<>();
    private final Map<UUID, Instant> prevTime = new HashMap<>();
    private final Map<UUID, SysInfo> sysInfo = new HashMap<>();
    private final Map<UUID, DockerInfo> dockerInfo = new HashMap<>();

    private final Object pollMu = new Object();
    private final Map<UUID, FailureState> failures = new HashMap<>();
    private final Set<UUID> inFlight = new HashSet<>();

    private final Object clientMu = new Object();
    private final Map<UUID, ClientSession> clients = new HashMap<>();
    private final Map<UUID, byte[]> clientFingerprints = new HashMap<>();

    private final Semaphore pollSlots = new Semaphore(10);
    private final ExecutorService workers = Executors.newCachedThreadPool(r -> {
        Thread t = new Thread(r, "collector-poll");
        t.setDaemon(true);
        return t;
    });

    private volatile boolean running;
    private Thread pollThread;
    private Thread maintenanceThread;

    private record SysInfo(int cores, long memTotal, long diskTotal) {
    }

    private record DockerInfo(boolean hasDocker, String version) {
    }

    private static final class FailureState {
        int attempts;
        Instant retryAt;
        byte[] fingerprint;
        boolean securityFailure;
    }

    public Collector(Servers servers, Metrics metrics, MonitorConfig cfg) {
        this.servers = servers;
        this.metrics = metrics;
        this.pollIntervalMillis = Duration.ofSeconds(cfg.getPollIntervalSeconds()).toMillis();
    }

    @Override
    public void start() {
        if (running) {
            return;
        }
        running = true;
        seedDockerInfo();
        pollThread = new Thread(() -> {
            pollAll();
            while (running) {
                try {
                    Thread.sleep(pollIntervalMillis);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                }
                pollAll();
            }
        }, "collector-tick");
        pollThread.setDaemon(true);
        pollThread.start();

        maintenanceThread = new Thread(() -> {
            try {
                Thread.sleep(MAINTENANCE_DELAY.toMillis());
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
            while (running) {
                maintainMetricHistory();
                try {
                    Thread.sleep(MAINTENANCE_INTERVAL.toMillis());
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                }
            }
        }, "collector-maintenance");
        maintenanceThread.setDaemon(true);
        maintenanceThread.start();
    }

    @Override
    public void stop() {
        running = false;
        if (pollThread != null) {
            pollThread.interrupt();
        }
        if (maintenanceThread != null) {
            maintenanceThread.interrupt();
        }
        Map<UUID, ClientSession> stale;
        synchronized (clientMu) {
            stale = new HashMap<>(clients);
            clients.clear();
            clientFingerprints.clear();
        }
        stale.values().forEach(Collector::closeQuietly);
        workers.shutdownNow();
    }

    @PreDestroy
    void destroy() {
        stop();
    }

    @Override
    public boolean isRunning() {
        return running;
    }

    /** Primes the Docker change-detection cache so a restart's first poll doesn't blank flags. */
    private void seedDockerInfo() {
        try {
            List<Servers.DockerInfo> rows = servers.getAllDockerInfo();
            synchronized (mu) {
                for (Servers.DockerInfo row : rows) {
                    dockerInfo.put(row.serverId(), new DockerInfo(row.hasDocker(), row.version()));
                }
            }
        } catch (Exception e) {
            log.warn("collector: seed docker info: {}", e.toString());
        }
    }

    void maintainMetricHistory() {
        Instant now = Instant.now();
        try {
            boolean backfilled = metrics.metricRollupBackfilled();
            if (!backfilled) {
                metrics.rollupMetrics1m(now.minusSeconds(30L * 24 * 3600), now);
                metrics.rollupMetrics15m(now.minusSeconds(30L * 24 * 3600), now);
                metrics.markMetricRollupBackfilled();
                log.info("collector: tiered metric history backfill completed");
            } else {
                Instant oneMinuteStart = metrics.metricRollupStart("server_metrics_1m",
                    now.minusSeconds(30L * 24 * 3600), Duration.ofMinutes(5));
                metrics.rollupMetrics1m(oneMinuteStart, now);
                Instant fifteenMinuteStart = metrics.metricRollupStart("server_metrics_15m",
                    now.minusSeconds(30L * 24 * 3600), Duration.ofHours(1));
                metrics.rollupMetrics15m(fifteenMinuteStart, now);
            }
        } catch (Exception e) {
            log.warn("collector: metric rollup failed: {}", e.toString());
            return;
        }
        deleteMetricTier("server_metrics", now.minus(Duration.ofHours(24)));
        deleteMetricTier("server_metrics_1m", now.minusSeconds(30L * 24 * 3600));
        deleteMetricTier("server_metrics_15m", now.minus(Duration.ofDays(365)));
    }

    private void deleteMetricTier(String table, Instant before) {
        for (int i = 0; i < METRIC_DELETE_RUNS; i++) {
            long rows;
            try {
                rows = metrics.deleteMetricBatch(table, before, METRIC_DELETE_BATCH);
            } catch (Exception e) {
                log.warn("collector: prune {} failed: {}", table, e.toString());
                return;
            }
            if (rows < METRIC_DELETE_BATCH) {
                return;
            }
        }
    }

    void pollAll() {
        List<ServerSecrets> fleet;
        try {
            fleet = servers.getAllServers();
        } catch (Exception e) {
            log.warn("collector: failed to list servers: {}", e.toString());
            return;
        }
        Set<UUID> active = new HashSet<>();
        for (ServerSecrets s : fleet) {
            active.add(s.id);
        }
        pruneServerState(active);

        for (ServerSecrets s : fleet) {
            byte[] fingerprint = fingerprint(s);
            if (!beginPoll(s.id, fingerprint)) {
                continue;
            }
            workers.submit(() -> {
                try {
                    try {
                        pollSlots.acquire();
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                        return;
                    }
                    if (!running) {
                        pollSlots.release();
                        return;
                    }
                    try {
                        Metrics.MetricPoint m = collectOne(s, fingerprint);
                        try {
                            metrics.saveMetric(s.id, m);
                            clearPollFailure(s.id);
                            clearPersistedPollError(s.id);
                        } catch (Exception e) {
                            Duration delay = recordPollFailure(s.id, fingerprint, e, Instant.now());
                            log.warn("collector: save metric for {} failed: {}; retry in {}s",
                                s.name, e.toString(), delay.toSeconds());
                            if (failureAttempts(s.id) >= 2) {
                                persistPollError(s.id, new IOException("save metric: " + e.getMessage(), e));
                            }
                        }
                    } catch (Exception e) {
                        Duration delay = recordPollFailure(s.id, fingerprint, e, Instant.now());
                        log.warn("collector: poll {} failed: {}; retry in {}s", s.name, e.toString(), delay.toSeconds());
                        if (failureAttempts(s.id) >= 2) {
                            persistPollError(s.id, e);
                        }
                    } finally {
                        pollSlots.release();
                    }
                } finally {
                    endPoll(s.id);
                }
            });
        }
    }

    /**
     * Bypasses a host's retry backoff for an operator who has just fixed
     * credentials. Still serialized against the scheduled loop and bounded by
     * the same SSH concurrency — a busy pool answers 409 immediately rather
     * than holding the HTTP request open.
     */
    public void pollNow(ServerSecrets s) throws PollNowException {
        byte[] fingerprint = fingerprint(s);
        clearPollFailure(s.id);
        if (!beginPoll(s.id, fingerprint)) {
            throw new PollNowException(PollNowResult.IN_FLIGHT, PollErrorClassifiers.Kind.NONE,
                "a poll for this server is already running");
        }
        try {
            if (!running) {
                throw new PollNowException(PollNowResult.STOPPED, PollErrorClassifiers.Kind.NONE,
                    "collector is stopped");
            }
            if (!pollSlots.tryAcquire()) {
                throw new PollNowException(PollNowResult.BUSY, PollErrorClassifiers.Kind.NONE,
                    "collector is busy");
            }
            try {
                Metrics.MetricPoint m = collectOne(s, fingerprint);
                try {
                    metrics.saveMetric(s.id, m);
                } catch (Exception e) {
                    recordPollFailure(s.id, fingerprint, e, Instant.now());
                    persistPollError(s.id, new IOException("save metric: " + e.getMessage(), e));
                    throw new PollNowException(PollNowResult.FAILED, PollErrorClassifiers.Kind.STORAGE,
                        PollErrorClassifiers.trimPollErrorDetail(e));
                }
                clearPersistedPollError(s.id);
            } catch (PollNowException e) {
                throw e;
            } catch (Exception e) {
                recordPollFailure(s.id, fingerprint, e, Instant.now());
                persistPollError(s.id, e);
                throw new PollNowException(PollNowResult.FAILED, PollErrorClassifiers.classify(e),
                    PollErrorClassifiers.trimPollErrorDetail(e));
            } finally {
                pollSlots.release();
            }
        } finally {
            endPoll(s.id);
        }
    }

    private Metrics.MetricPoint collectOne(ServerSecrets s, byte[] fingerprint) throws Exception {
        final int[] pingLatency = {0};
        Thread ping = new Thread(() -> pingLatency[0] = collectPingLatency(s.host), "collector-ping");
        ping.setDaemon(true);
        ping.start();

        ClientSession client = getClient(s, fingerprint);
        try {
            java.util.concurrent.Callable<Metrics.MetricPoint> collection = () -> {
                Metrics.MetricPoint m = "windows".equals(s.serverType)
                    ? collectWindows(client, s)
                    : collectLinux(client, s);
                if (m != null) {
                    try {
                        ping.join(5000);
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                    }
                    m = new Metrics.MetricPoint(m.cpuPercent(), m.load1(), m.load5(), m.load15(),
                        m.memoryUsed(), m.memoryTotal(), m.diskUsed(), m.networkRxBytes(), m.networkTxBytes(),
                        m.networkRxTotal(), m.networkTxTotal(), m.diskRxBytes(), m.diskTxBytes(),
                        m.uptimeSeconds(), pingLatency[0], m.recordedAt());
                }
                return m;
            };
            // The whole host collection runs behind a hard deadline; on timeout
            // the SSH client is dropped so one broken host never blocks pollAll.
            return SshSupport.withTimeout(client, SSH_COLLECTION_TIMEOUT, collection);
        } catch (Exception e) {
            dropClient(s.id, client);
            if (isTimeout(e)) {
                throw new IOException("collection timed out after " + SSH_COLLECTION_TIMEOUT.toSeconds() + "s", e);
            }
            throw e;
        }
    }

    private static boolean isTimeout(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof java.util.concurrent.TimeoutException
                || (t.getMessage() != null && t.getMessage().contains("timed out"))) {
                return true;
            }
        }
        return false;
    }

    private Metrics.MetricPoint collectLinux(ClientSession client, ServerSecrets s) throws Exception {
        String out;
        try {
            out = SshSupport.runCmd(client, LinuxMetricsParsers.LINUX_METRICS_COMMAND);
        } catch (Exception e) {
            throw new IOException("linux metrics: " + e.getMessage(), e);
        }
        List<String> sections = LinuxMetricsParsers.splitLinuxSections(out, LinuxMetricsParsers.SECTION_COUNT);

        Instant now = Instant.now();
        double cpu = LinuxMetricsParsers.cpuPercentFromSamples(
            sections.get(LinuxMetricsParsers.SECTION_STAT_FIRST),
            sections.get(LinuxMetricsParsers.SECTION_STAT_SECOND));
        double[] load = LinuxMetricsParsers.parseLoadAvg(sections.get(LinuxMetricsParsers.SECTION_LOADAVG));
        long[] mem = LinuxMetricsParsers.parseMemInfo(sections.get(LinuxMetricsParsers.SECTION_MEMINFO));
        long[] net = LinuxMetricsParsers.parseNetDev(sections.get(LinuxMetricsParsers.SECTION_NETDEV));
        long[] disk = LinuxMetricsParsers.parseDiskStats(sections.get(LinuxMetricsParsers.SECTION_DISKSTATS));
        long uptime = LinuxMetricsParsers.parseUptime(sections.get(LinuxMetricsParsers.SECTION_UPTIME));
        long[] diskUsage = LinuxMetricsParsers.parseDiskUsage(sections.get(LinuxMetricsParsers.SECTION_DISKUSAGE));
        int cores = LinuxMetricsParsers.parseNproc(sections.get(LinuxMetricsParsers.SECTION_NPROC));
        var docker = LinuxMetricsParsers.parseDockerProbe(sections.get(LinuxMetricsParsers.SECTION_DOCKER));

        Metrics.MetricPoint m = new Metrics.MetricPoint(
            cpu, load[0], load[1], load[2], mem[0], mem[1], diskUsage[0],
            0, 0, net[0], net[1], 0, 0, uptime, 0, now);
        m = applyRates(s.id, m, net[0], net[1], disk[0], disk[1]);
        syncHostInfo(s.id, cores, mem[1], diskUsage[1], docker.presence(), docker.version());
        return m;
    }

    private Metrics.MetricPoint collectWindows(ClientSession client, ServerSecrets s) throws Exception {
        String out;
        try {
            out = SshSupport.runCmd(client, WindowsProbes.windowsMetricsCommand());
        } catch (Exception e) {
            throw new IOException("windows metrics: " + e.getMessage(), e);
        }
        var vals = WindowsProbes.parseWindowsMetrics(out);
        if (vals.values().isEmpty()) {
            throw new IOException("windows metrics: no data in output " + out.trim());
        }
        long memTotalKB = vals.get("memtotal");
        long memoryTotal = memTotalKB * 1024;
        long memoryUsed = (memTotalKB - vals.get("memfree")) * 1024;
        if (memoryUsed < 0) {
            memoryUsed = 0;
        }
        long diskTotal = vals.get("disktotal");
        long diskUsed = diskTotal - vals.get("diskfree");
        if (diskUsed < 0) {
            diskUsed = 0;
        }
        Instant now = Instant.now();
        Metrics.MetricPoint m = new Metrics.MetricPoint(
            vals.get("cpu"), vals.get("queue"), 0, 0, memoryUsed, memoryTotal, diskUsed,
            0, 0, vals.get("netrx"), vals.get("nettx"), 0, 0, vals.get("uptime"), 0, now);
        m = applyRates(s.id, m, vals.get("netrx"), vals.get("nettx"), vals.get("diskread"), vals.get("diskwrite"));
        var presence = WindowsProbes.windowsDockerPresence(vals.dockerPresent(), vals.dockerVersion());
        syncHostInfo(s.id, (int) vals.get("cores"), memoryTotal, diskTotal, presence, vals.dockerVersion());
        return m;
    }

    /** Converts cumulative counters into per-second rates against the previous sample. */
    private Metrics.MetricPoint applyRates(UUID serverId, Metrics.MetricPoint m,
        long netRx, long netTx, long diskRx, long diskTx) {
        Instant now = m.recordedAt();
        long rxRate = 0, txRate = 0, drxRate = 0, dtxRate = 0;
        synchronized (mu) {
            long[] previous = prev.get(serverId);
            Instant previousTime = prevTime.get(serverId);
            if (previous != null && previousTime != null && previous[0] <= netRx && previous[1] <= netTx) {
                long elapsedMillis = now.toEpochMilli() - previousTime.toEpochMilli();
                if (elapsedMillis > 0) {
                    rxRate = (long) ((netRx - previous[0]) * 1000.0 / elapsedMillis);
                    txRate = (long) ((netTx - previous[1]) * 1000.0 / elapsedMillis);
                    drxRate = (long) ((diskRx - previous[2]) * 1000.0 / elapsedMillis);
                    dtxRate = (long) ((diskTx - previous[3]) * 1000.0 / elapsedMillis);
                }
            }
            prev.put(serverId, new long[]{netRx, netTx, diskRx, diskTx});
            prevTime.put(serverId, now);
        }
        return new Metrics.MetricPoint(m.cpuPercent(), m.load1(), m.load5(), m.load15(),
            m.memoryUsed(), m.memoryTotal(), m.diskUsed(), rxRate, txRate, netRx, netTx,
            drxRate, dtxRate, m.uptimeSeconds(), m.latencyMs(), now);
    }

    /** Persists slow-changing host facts, writing only when they differ from the stored row. */
    private void syncHostInfo(UUID serverId, int cores, long memTotal, long diskTotal,
        LinuxMetricsParsers.DockerPresence presence, String dockerVersion) {
        if (cores > 0) {
            SysInfo info = new SysInfo(cores, memTotal, diskTotal);
            boolean write;
            synchronized (mu) {
                write = !info.equals(sysInfo.get(serverId));
            }
            if (write) {
                try {
                    servers.updateServerSystemInfo(serverId, cores, memTotal, diskTotal);
                    synchronized (mu) {
                        sysInfo.put(serverId, info);
                    }
                } catch (Exception e) {
                    log.warn("collector: update system info for {}: {}", serverId, e.toString());
                }
            }
        }
        DockerInfo next;
        boolean write;
        synchronized (mu) {
            DockerInfo prevDocker = dockerInfo.get(serverId);
            var resolved = resolveDockerState(prevDocker, presence, dockerVersion);
            next = resolved[0] == null ? null : (DockerInfo) resolved[0];
            write = (Boolean) resolved[1];
        }
        if (write && next != null) {
            try {
                servers.updateDockerInfo(serverId, next.hasDocker(), next.version());
                synchronized (mu) {
                    dockerInfo.put(serverId, next);
                }
            } catch (Exception e) {
                log.warn("collector: update docker info for {}: {}", serverId, e.toString());
            }
        }
    }

    /**
     * Decides what one poll's Docker probe should do to the persisted row:
     * unknown writes nothing (last answer stands); absent clears has_docker;
     * installed-without-version keeps the stored version; anything else writes
     * only on change.
     */
    static Object[] resolveDockerState(DockerInfo prev, LinuxMetricsParsers.DockerPresence presence,
        String version) {
        if (presence == LinuxMetricsParsers.DockerPresence.UNKNOWN) {
            return new Object[]{prev, false};
        }
        String nextVersion = version == null ? "" : version;
        boolean nextHasDocker = presence == LinuxMetricsParsers.DockerPresence.INSTALLED;
        if (prev != null && nextHasDocker && nextVersion.isEmpty() && prev.hasDocker()) {
            nextVersion = prev.version();
        }
        DockerInfo next = new DockerInfo(nextHasDocker, nextVersion);
        return new Object[]{next, !next.equals(prev)};
    }

    private int collectPingLatency(String host) {
        try {
            Process process = new ProcessBuilder("ping", "-n", "-c", "3", "-W", "1", host)
                .redirectErrorStream(true).start();
            String output = new String(process.getInputStream().readAllBytes());
            boolean finished = process.waitFor(4, java.util.concurrent.TimeUnit.SECONDS);
            if (!finished) {
                process.destroyForcibly();
                return 0;
            }
            return LinuxMetricsParsers.parsePingAverage(output);
        } catch (Exception e) {
            return 0;
        }
    }

    // --- client pooling -------------------------------------------------

    /** Pooled session per server, redialed when settings change. Polls are serialized per server. */
    private ClientSession getClient(ServerSecrets s, byte[] fingerprint) throws IOException {
        synchronized (clientMu) {
            ClientSession existing = clients.get(s.id);
            if (existing != null) {
                if (java.security.MessageDigest.isEqual(clientFingerprints.get(s.id), fingerprint)) {
                    return existing;
                }
                dropClient(s.id, existing);
            }
        }
        ClientSession client = SshSupport.dial(s.host, s.port, s.sshUsername, s.sshPassword,
            s.sshKey, s.sshHostKey);
        synchronized (clientMu) {
            clients.put(s.id, client);
            clientFingerprints.put(s.id, fingerprint);
        }
        return client;
    }

    private void dropClient(UUID serverId, ClientSession client) {
        if (client == null) {
            return;
        }
        synchronized (clientMu) {
            if (clients.get(serverId) == client) {
                clients.remove(serverId);
                clientFingerprints.remove(serverId);
            }
        }
        closeQuietly(client);
    }

    private static void closeQuietly(ClientSession session) {
        if (session == null) {
            return;
        }
        try {
            session.close(true);
        } catch (Exception ignored) { }
    }

    // --- failure bookkeeping --------------------------------------------

    static byte[] fingerprint(ServerSecrets s) {
        return com.servermonitor.ssh.SshConnectionCache.fingerprint(s);
    }

    private boolean beginPoll(UUID serverId, byte[] fingerprint) {
        synchronized (pollMu) {
            if (inFlight.contains(serverId)) {
                return false;
            }
            FailureState failure = failures.get(serverId);
            if (failure != null) {
                if (!java.security.MessageDigest.isEqual(failure.fingerprint, fingerprint)) {
                    failures.remove(serverId);
                } else if (Instant.now().isBefore(failure.retryAt)) {
                    return false;
                }
            }
            inFlight.add(serverId);
            return true;
        }
    }

    private void endPoll(UUID serverId) {
        synchronized (pollMu) {
            inFlight.remove(serverId);
        }
    }

    private int failureAttempts(UUID serverId) {
        synchronized (pollMu) {
            FailureState state = failures.get(serverId);
            return state == null ? 0 : state.attempts;
        }
    }

    private Duration recordPollFailure(UUID serverId, byte[] fingerprint, Throwable err, Instant now) {
        synchronized (pollMu) {
            FailureState state = failures.get(serverId);
            if (state == null || !java.security.MessageDigest.isEqual(state.fingerprint, fingerprint)) {
                state = new FailureState();
                state.fingerprint = fingerprint;
            }
            if (PollErrorClassifiers.isSSHAuthenticationFailure(err) && !state.securityFailure) {
                state.securityFailure = true;
                state.attempts = 0;
            }
            state.attempts++;
            Duration delay = state.securityFailure
                ? exponentialDelay(state.attempts, SECURITY_FAILURE_BASE, SECURITY_FAILURE_MAX)
                : exponentialDelay(state.attempts, POLL_FAILURE_BASE, POLL_FAILURE_MAX);
            state.retryAt = now.plus(delay);
            failures.put(serverId, state);
            return delay;
        }
    }

    static Duration exponentialDelay(int attempt, Duration base, Duration maximum) {
        if (attempt < 1) {
            attempt = 1;
        }
        long delay = base.toMillis();
        long max = maximum.toMillis();
        for (int i = 1; i < attempt && delay < max; i++) {
            if (delay > max / 2) {
                return maximum;
            }
            delay *= 2;
        }
        return Duration.ofMillis(Math.min(delay, max));
    }

    private void clearPollFailure(UUID serverId) {
        synchronized (pollMu) {
            failures.remove(serverId);
        }
    }

    private void persistPollError(UUID serverId, Throwable err) {
        try {
            servers.recordServerPollError(serverId, PollErrorClassifiers.classify(err).wire(),
                PollErrorClassifiers.trimPollErrorDetail(err), Instant.now());
        } catch (Exception e) {
            log.warn("collector: record poll error for {}: {}", serverId, e.toString());
        }
    }

    private void clearPersistedPollError(UUID serverId) {
        try {
            servers.clearServerPollError(serverId);
        } catch (Exception e) {
            log.warn("collector: clear poll error for {}: {}", serverId, e.toString());
        }
    }

    /** Drops pooled connections and bookkeeping for servers that no longer exist. */
    private void pruneServerState(Set<UUID> active) {
        java.util.List<ClientSession> stale = new java.util.ArrayList<>();
        synchronized (clientMu) {
            clients.entrySet().removeIf(e -> {
                if (!active.contains(e.getKey())) {
                    stale.add(e.getValue());
                    return true;
                }
                return false;
            });
            clientFingerprints.keySet().retainAll(active);
        }
        stale.forEach(Collector::closeQuietly);
        synchronized (pollMu) {
            failures.keySet().retainAll(active);
        }
        synchronized (mu) {
            prev.keySet().retainAll(active);
            prevTime.keySet().retainAll(active);
            sysInfo.keySet().retainAll(active);
            dockerInfo.keySet().retainAll(active);
        }
    }

    // --- Docker probes for the manual re-check endpoint -------------------

    public record DockerProbe(boolean known, boolean installed, String version) {
    }

    /** Uses exactly the collector's probe+parser so a manual re-check and the poll can never disagree. */
    public static DockerProbe probeDocker(ClientSession client, String serverType) {
        if ("windows".equals(serverType)) {
            String out;
            try {
                out = SshSupport.runCmd(client, WindowsProbes.windowsDockerProbeCommand());
            } catch (IOException e) {
                out = "";
            }
            var parsed = WindowsProbes.parseWindowsDockerProbe(out);
            return new DockerProbe(parsed.presence() != LinuxMetricsParsers.DockerPresence.UNKNOWN,
                parsed.presence() == LinuxMetricsParsers.DockerPresence.INSTALLED, parsed.version());
        }
        String out;
        try {
            out = SshSupport.runCmd(client, LinuxMetricsParsers.LINUX_DOCKER_PROBE);
        } catch (IOException e) {
            out = "";
        }
        var parsed = LinuxMetricsParsers.parseDockerProbe(out);
        return new DockerProbe(parsed.presence() != LinuxMetricsParsers.DockerPresence.UNKNOWN,
            parsed.presence() == LinuxMetricsParsers.DockerPresence.INSTALLED, parsed.version());
    }
}
