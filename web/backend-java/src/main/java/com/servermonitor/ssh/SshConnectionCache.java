package com.servermonitor.ssh;

import java.io.IOException;
import java.security.MessageDigest;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;

import org.apache.sshd.client.session.ClientSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Component;

import com.servermonitor.repo.Servers.ServerSecrets;

/**
 * Reuses SSH connections across interactive API calls (Docker, files, processes),
 * so each request doesn't pay a full TCP + SSH handshake. Entries are keyed by
 * server id, validated against a fingerprint of the connection settings, and
 * closed after 5 idle minutes.
 *
 * Deliberately separate from the collector's own pooled sessions: the collector
 * arms collection deadlines and drops stalled transports, which would tear down
 * connections mid-use for API requests.
 */
@Component
public class SshConnectionCache implements AutoCloseable {

    private static final Logger log = LoggerFactory.getLogger(SshConnectionCache.class);

    private static final Duration IDLE_TIMEOUT = Duration.ofMinutes(5);
    private static final Duration SWEEP_INTERVAL = Duration.ofMinutes(1);
    private static final Duration KEEPALIVE_TIMEOUT = Duration.ofSeconds(3);

    private final Map<UUID, Entry> entries = new HashMap<>();
    private volatile boolean closed;

    private static final class Entry {
        ClientSession session;
        byte[] fingerprint;
        Instant lastUsed = Instant.now();
        boolean dropped;
    }

    public SshConnectionCache() {
        Thread sweeper = new Thread(() -> {
            while (true) {
                try {
                    Thread.sleep(SWEEP_INTERVAL.toMillis());
                } catch (InterruptedException e) {
                    return;
                }
                if (closed) {
                    return;
                }
                sweep();
            }
        }, "ssh-cache-sweeper");
        sweeper.setDaemon(true);
        sweeper.start();
    }

    /** Exception type used so handlers can answer 502 with a useful message. */
    public static final class CacheClosedException extends IOException {
        public CacheClosedException() {
            super("SSH connection cache is closed");
        }
    }

    /** A live session for the server; returned sessions are shared — callers must never close them. */
    public ClientSession get(ServerSecrets s) throws IOException {
        byte[] fingerprint = fingerprint(s);
        while (true) {
            synchronized (this) {
                if (closed) {
                    throw new CacheClosedException();
                }
            }
            Entry entry;
            synchronized (this) {
                entry = entries.get(s.id);
                if (entry == null || entry.dropped) {
                    entry = new Entry();
                    entries.put(s.id, entry);
                }
            }
            ClientSession session;
            try {
                session = ensureLive(entry, s, fingerprint);
            } catch (DroppedEntryRetry retry) {
                continue;
            }
            synchronized (this) {
                if (closed) {
                    throw new CacheClosedException();
                }
            }
            return session;
        }
    }

    private ClientSession ensureLive(Entry entry, ServerSecrets s, byte[] fingerprint) throws IOException {
        synchronized (entry) {
            if (entry.dropped) {
                // Lost a race with drop(): restart with a fresh entry via the caller's loop.
                throw new DroppedEntryRetry();
            }
            if (entry.session != null) {
                if (MessageDigest.isEqual(entry.fingerprint, fingerprint) && keepaliveOk(entry.session)) {
                    entry.lastUsed = Instant.now();
                    return entry.session;
                }
                closeQuietly(entry.session);
                entry.session = null;
            }
            ClientSession session = SshSupport.dial(s.host, s.port, s.sshUsername, s.sshPassword,
                s.sshKey, s.sshHostKey);
            entry.session = session;
            entry.fingerprint = fingerprint;
            entry.lastUsed = Instant.now();
            return session;
        }
    }

    private static final class DroppedEntryRetry extends IOException {
        DroppedEntryRetry() {
            super("entry dropped");
        }
    }

    /**
     * Lightweight liveness probe with a hard deadline: probes whether the
     * transport still produces data by opening and closing a session channel
     * inside the deadline. A half-dead peer blocks the open and fails the check.
     */
    private static boolean keepaliveOk(ClientSession session) {
        try {
            SshSupport.withTimeout(session, KEEPALIVE_TIMEOUT, () -> {
                session.auth().await(1); // no-op on an authenticated session
                org.apache.sshd.client.channel.ChannelExec probe = session.createExecChannel("true");
                try (probe) {
                    probe.open().verify(KEEPALIVE_TIMEOUT.toMillis() / 2);
                }
                return true;
            });
            return true;
        } catch (IOException | RuntimeException e) {
            return false;
        }
    }

    /** Closes and forgets the cached connection for a server (e.g. after deletion). */
    public void drop(UUID serverId) {
        Entry entry;
        synchronized (this) {
            entry = entries.remove(serverId);
        }
        if (entry == null) {
            return;
        }
        synchronized (entry) {
            entry.dropped = true;
            closeQuietly(entry.session);
            entry.session = null;
        }
    }

    private void sweep() {
        Map<UUID, Entry> snapshot;
        synchronized (this) {
            snapshot = new HashMap<>(entries);
        }
        Instant now = Instant.now();
        for (Entry entry : snapshot.values()) {
            synchronized (entry) {
                if (entry.session != null && entry.lastUsed.plus(IDLE_TIMEOUT).isBefore(now)) {
                    closeQuietly(entry.session);
                    entry.session = null;
                }
            }
        }
    }

    @Override
    public void close() {
        synchronized (this) {
            if (closed) {
                return;
            }
            closed = true;
        }
        Map<UUID, Entry> snapshot;
        synchronized (this) {
            snapshot = new HashMap<>(entries);
            entries.clear();
        }
        for (Entry entry : snapshot.values()) {
            synchronized (entry) {
                entry.dropped = true;
                closeQuietly(entry.session);
                entry.session = null;
            }
        }
    }

    private static void closeQuietly(ClientSession session) {
        if (session == null) {
            return;
        }
        try {
            session.close(true);
        } catch (Exception e) {
            log.debug("ssh cache close: {}", e.toString());
        }
    }

    /** Fingerprint of the connection settings: any edit forces a redial. Mirrors the Go SHA-256 layout. */
    public static byte[] fingerprint(ServerSecrets s) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            for (String field : new String[]{
                s.host, String.valueOf(s.port), s.sshUsername, s.sshPassword, s.sshKey, s.sshHostKey, s.serverType}) {
                digest.update(field.getBytes(StandardCharsets.UTF_8));
                digest.update((byte) 0);
            }
            return digest.digest();
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }
}
