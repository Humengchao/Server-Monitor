package com.servermonitor.service;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Semaphore;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.stereotype.Service;

import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.ssh.SshConnectionCache;
import com.servermonitor.ssh.SshSupport;

/** Fans one command out over SSH across many servers, mirroring services/batch.go. */
@Service
public class BatchService {

    /** Bounds one request; fifty hosts at the concurrency below is already a few seconds. */
    public static final int BATCH_MAX_TARGETS = 50;
    /** Limits simultaneous SSH sessions so a wide batch cannot exhaust fds or MaxSessions. */
    public static final int BATCH_CONCURRENCY = 8;
    /** Per host, not for the batch as a whole. */
    public static final Duration BATCH_COMMAND_TIMEOUT = Duration.ofSeconds(60);
    /** Caps each host's captured output so a runaway command can't stream gigabytes into memory. */
    public static final int BATCH_OUTPUT_LIMIT = 64 * 1024;
    /** Keeps a pasted script from becoming the payload. */
    public static final int BATCH_MAX_COMMAND_LENGTH = 4096;

    /** One host's outcome; output holds combined stdout+stderr, truncated at the cap. */
    public record BatchResult(UUID serverId, String serverName, boolean ok, String output,
        String error, boolean truncated, long durationMs) {
    }

    private final SshConnectionCache cache;

    public BatchService(SshConnectionCache cache) {
        this.cache = cache;
    }

    /** Executes cmd on every server concurrently; individual failures never abort the batch. */
    public List<BatchResult> runBatchCommand(List<ServerSecrets> servers, String cmd) {
        BatchResult[] results = new BatchResult[servers.size()];
        Semaphore slots = new Semaphore(BATCH_CONCURRENCY);
        ExecutorService pool = Executors.newFixedThreadPool(Math.min(BATCH_CONCURRENCY, Math.max(1, servers.size())), r -> {
            Thread t = new Thread(r, "batch-exec");
            t.setDaemon(true);
            return t;
        });
        List<java.util.concurrent.Future<?>> futures = new ArrayList<>();
        for (int i = 0; i < servers.size(); i++) {
            final int index = i;
            final ServerSecrets s = servers.get(i);
            futures.add(pool.submit(() -> {
                try {
                    slots.acquire();
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    results[index] = new BatchResult(s.id, s.name, false, "", "interrupted", false, 0);
                    return;
                }
                long started = System.currentTimeMillis();
                try {
                    ClientSession client;
                    try {
                        client = cache.get(s);
                    } catch (Exception e) {
                        results[index] = new BatchResult(s.id, s.name, false, "",
                            "SSH connection failed: " + ProcessService.cleanCommandError(String.valueOf(e.getMessage())),
                            false, System.currentTimeMillis() - started);
                        return;
                    }
                    SshSupport.BoundedResult bounded = SshSupport.runCmdBounded(client, cmd,
                        BATCH_COMMAND_TIMEOUT, BATCH_OUTPUT_LIMIT);
                    long duration = System.currentTimeMillis() - started;
                    if (bounded.error() != null) {
                        results[index] = new BatchResult(s.id, s.name, false, bounded.output(),
                            bounded.error().getMessage(), bounded.truncated(), duration);
                    } else {
                        results[index] = new BatchResult(s.id, s.name, true, bounded.output(), "",
                            bounded.truncated(), duration);
                    }
                } finally {
                    slots.release();
                }
            }));
        }
        for (java.util.concurrent.Future<?> future : futures) {
            try {
                future.get();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                break;
            } catch (java.util.concurrent.ExecutionException ignored) { }
        }
        pool.shutdownNow();
        return List.of(results);
    }
}
