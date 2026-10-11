package com.servermonitor.ssh;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyPair;
import java.security.PublicKey;
import java.time.Duration;
import java.util.List;
import java.util.concurrent.TimeUnit;

import org.apache.sshd.client.SshClient;
import org.apache.sshd.client.channel.ChannelExec;
import org.apache.sshd.client.channel.ClientChannelEvent;
import org.apache.sshd.client.session.ClientSession;
import org.apache.sshd.common.config.keys.AuthorizedKeyEntry;
import org.apache.sshd.common.config.keys.KeyUtils;
import org.apache.sshd.common.config.keys.PublicKeyEntry;
import org.apache.sshd.common.util.security.SecurityUtils;
import org.apache.sshd.sftp.client.SftpClient;

/**
 * SSH plumbing for the panel: one shared SshClient, password or key auth
 * (exactly one method per connection — with two methods a stale first attempt
 * still shows in fail2ban even when the fallback succeeds), and optional pinned
 * host keys in authorized_keys format.
 */
public final class SshSupport {

    private SshSupport() {
    }

    public static final Duration DIAL_TIMEOUT = Duration.ofSeconds(10);
    public static final Duration AUTH_TIMEOUT = Duration.ofSeconds(10);
    public static final Duration CHANNEL_OPEN_TIMEOUT = Duration.ofSeconds(10);

    private static final SshClient CLIENT;

    static {
        // A large nio2 worker pool: the default pool saturates when a fleet with
        // many unreachable hosts has dials in flight (a stuck SYN handshake can
        // outlive our 10s await), and a healthy host's connect then times out
        // without ever opening its socket — observed as a spurious "connect
        // failed". 64 workers keeps those stragglers from starving real work.
        CLIENT = SshClient.setUpDefaultClient();
        org.apache.sshd.common.io.nio2.Nio2ServiceFactoryFactory base =
            new org.apache.sshd.common.io.nio2.Nio2ServiceFactoryFactory();
        base.setExecutorServiceFactory(() ->
            org.apache.sshd.common.util.threads.ThreadUtils.newFixedThreadPool("sshd-nio2", 64));
        CLIENT.setIoServiceFactoryFactory(base);
        CLIENT.start();
        Runtime.getRuntime().addShutdownHook(new Thread(CLIENT::stop, "ssh-client-stop"));
    }

    /** Wraps dial/handshake failures so messages match the Go backend's "ssh dial:" / "ssh handshake:" prefixes. */
    public static final class SshDialException extends IOException {
        public SshDialException(String message) {
            super(message);
        }

        public SshDialException(String message, Throwable cause) {
            super(message, cause);
        }
    }

    /** Runs fn with a timeout; on expiry closes the session (which unblocks it) and rethrows. */
    public static <T> T withTimeout(ClientSession session, Duration timeout, java.util.concurrent.Callable<T> fn)
        throws IOException {
        java.util.concurrent.ExecutorService one = java.util.concurrent.Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "ssh-op");
            t.setDaemon(true);
            return t;
        });
        java.util.concurrent.Future<T> future = one.submit(fn);
        try {
            return future.get(timeout.toMillis(), TimeUnit.MILLISECONDS);
        } catch (java.util.concurrent.TimeoutException e) {
            session.close(true);
            future.cancel(true);
            throw new IOException("timed out after " + timeout.toSeconds() + "s", e);
        } catch (java.util.concurrent.ExecutionException e) {
            Throwable cause = e.getCause() == null ? e : e.getCause();
            if (cause instanceof IOException io) {
                throw io;
            }
            throw new IOException(cause.toString(), cause);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("interrupted", e);
        } finally {
            one.shutdownNow();
        }
    }

    public static ClientSession dial(String host, int port, String username, String password,
        String key, String hostKey) throws IOException {
        org.apache.sshd.client.future.ConnectFuture connectFuture;
        try {
            connectFuture = CLIENT.connect(username, host, port);
        } catch (RuntimeException e) {
            throw new SshDialException("ssh dial: " + e.getMessage(), e);
        }
        try {
            connectFuture.await(DIAL_TIMEOUT.toMillis());
        } catch (IOException e) {
            throw new SshDialException("ssh dial: " + e.getMessage(), e);
        }
        ClientSession session;
        try {
            session = connectFuture.getSession();
        } catch (Exception e) {
            Throwable t = connectFuture.getException();
            throw new SshDialException("ssh dial: " + (t != null ? t.toString() : e.getMessage()),
                t != null ? t : e);
        }
        if (session == null) {
            Throwable t = connectFuture.getException();
            String reason = t != null ? t.toString() : "";
            String lower = reason.toLowerCase(java.util.Locale.ROOT);
            int port2 = port;
            if (reason.isEmpty()) {
                reason = "connect failed";
            }
            // Normalize to the error phrasing the Go backend's poll classifier knows.
            if (lower.contains("refused")) {
                reason = "dial tcp " + host + ":" + port2 + ": connect: connection refused";
            } else if (lower.contains("time") || lower.contains("deadline")) {
                reason = "dial tcp " + host + ":" + port2 + ": i/o timeout";
            }
            throw new SshDialException("ssh dial: " + reason, t);
        }
        try {
            configureAuth(session, password, key);
            if (hostKey != null && !hostKey.isBlank()) {
                session.setServerKeyVerifier(new PinnedHostKeyVerifier(parseAuthorizedKey(hostKey)));
            } else {
                session.setServerKeyVerifier(
                    org.apache.sshd.client.keyverifier.AcceptAllServerKeyVerifier.INSTANCE);
            }
            if (!session.auth().await(AUTH_TIMEOUT.toMillis())) {
                throw new SshDialException(
                    "ssh handshake: ssh: unable to authenticate, attempted methods [none], no supported methods remain");
            }
        } catch (SshDialException e) {
            if (session != null) {
                session.close(true);
            }
            throw e;
        } catch (Exception e) {
            if (session != null) {
                session.close(true);
            }
            throw new SshDialException("ssh handshake: " + e.getMessage(), e);
        }
        return session;
    }

    private static void configureAuth(ClientSession session, String password, String key)
        throws IOException {
        if (password != null && !password.isEmpty()) {
            session.addPasswordIdentity(password);
            return;
        }
        if (key != null && !key.isBlank()) {
            session.addPublicKeyIdentity(loadPrivateKey(key));
            return;
        }
        throw new SshDialException("no auth method");
    }

    /** Parses OpenSSH/PEM private keys, mirroring x/crypto/ssh's accepted formats. */
    static KeyPair loadPrivateKey(String pem) throws IOException {
        java.io.InputStream stream = new java.io.ByteArrayInputStream(pem.getBytes(StandardCharsets.UTF_8));
        try {
            Iterable<KeyPair> pairs = SecurityUtils.loadKeyPairIdentities(null, null, stream,
                org.apache.sshd.common.config.keys.FilePasswordProvider.EMPTY);
            for (KeyPair pair : pairs) {
                if (pair != null && pair.getPrivate() != null) {
                    return pair;
                }
            }
        } catch (Exception e) {
            throw new IOException("parse key: " + e.getMessage(), e);
        }
        throw new IOException("parse key: invalid format");
    }

    /** Parses an authorized_keys line ("ssh-ed25519 AAAA... comment") into a PublicKey. */
    static PublicKey parseAuthorizedKey(String authorizedKey) throws IOException {
        String trimmed = authorizedKey.trim();
        try {
            List<AuthorizedKeyEntry> entries = AuthorizedKeyEntry.readAuthorizedKeys(
                new java.io.StringReader(trimmed), true);
            for (AuthorizedKeyEntry entry : entries) {
                PublicKey key = entry.resolvePublicKey(null, null);
                if (key != null) {
                    return key;
                }
            }
        } catch (GeneralSecurityException | IOException e) {
            throw new IOException("parse host key: " + e.getMessage(), e);
        }
        throw new IOException("parse host key: invalid format");
    }

    /** Verifies the server's host key against the stored key. */
    static final class PinnedHostKeyVerifier implements org.apache.sshd.client.keyverifier.ServerKeyVerifier {

        private final PublicKey expected;

        PinnedHostKeyVerifier(PublicKey expected) {
            this.expected = expected;
        }

        @Override
        public boolean verifyServerKey(ClientSession session, java.net.SocketAddress remoteAddress,
            PublicKey serverKey) {
            return expected != null
                && KeyUtils.compareKeys(expected, serverKey);
        }
    }

    public record CommandResult(String output, int exitStatus, boolean timedOut) {
    }

    /** Runs a command capturing stdout (stderr discarded), like services.RunCmd. */
    public static String runCmd(ClientSession session, String command) throws IOException {
        CommandResult result = runCmd(session, command, false);
        if (result.timedOut()) {
            throw new IOException("timed out after 300s", null);
        }
        if (result.exitStatus() != 0 && result.exitStatus() != -1) {
            throw new CommandFailedException(result.output(), result.exitStatus());
        }
        return result.output();
    }

    /** RunCmd but keeps stderr, for commands whose failure message is worth showing. */
    public static String runCmdCombined(ClientSession session, String command) throws IOException {
        CommandResult result = runCmd(session, command, true);
        if (result.timedOut()) {
            throw new IOException("timed out after 300s", null);
        }
        if (result.exitStatus() != 0 && result.exitStatus() != -1) {
            throw new CommandFailedException(result.output(), result.exitStatus());
        }
        return result.output();
    }

    /** Non-zero remote exit, carrying the trimmed diagnostic the Go backend surfaces. */
    public static class CommandFailedException extends IOException {
        private final int exitStatus;

        public CommandFailedException(String output, int exitStatus) {
            super(output == null || output.isBlank() ? "exited with status " + exitStatus : output);
            this.exitStatus = exitStatus;
        }

        public int getExitStatus() {
            return exitStatus;
        }
    }

    /**
     * Thread-safe output sink that caps how much it keeps while always reporting
     * a full write (a short count would abort the copy and read as a command
     * failure). Mirrors services.syncLimitedWriter.
     */
    public static final class LimitedOutput extends java.io.OutputStream {
        private final ByteArrayOutputStream buf = new ByteArrayOutputStream();
        private final int limit;
        private boolean truncated;

        public LimitedOutput(int limit) {
            this.limit = limit;
        }

        @Override
        public synchronized void write(int b) {
            if (buf.size() < limit) {
                buf.write(b);
            } else {
                truncated = true;
            }
        }

        @Override
        public synchronized void write(byte[] p, int off, int len) {
            int remaining = limit - buf.size();
            if (remaining > 0) {
                buf.write(p, off, Math.min(len, remaining));
                if (len > remaining) {
                    truncated = true;
                }
            } else if (len > 0) {
                truncated = true;
            }
        }

        public synchronized String text() {
            return buf.toString(StandardCharsets.UTF_8);
        }

        public synchronized boolean truncated() {
            return truncated;
        }
    }

    /**
     * Runs a command capturing combined output with a byte cap and a hard
     * timeout; on timeout the channel is closed and the partial output keeps.
     * Mirrors services.RunCmdBounded.
     */
    public record BoundedResult(String output, boolean truncated, IOException error) {
    }

    public static BoundedResult runCmdBounded(ClientSession session, String command,
        Duration timeout, int limit) {
        LimitedOutput writer = new LimitedOutput(limit);
        Integer exitStatus = null;
        try (ChannelExec channel = session.createExecChannel(command)) {
            channel.setOut(writer);
            channel.setErr(writer);
            channel.open().verify(CHANNEL_OPEN_TIMEOUT.toMillis());
            java.util.Collection<ClientChannelEvent> events =
                channel.waitFor(List.of(ClientChannelEvent.CLOSED), timeout.toMillis());
            if (events.contains(ClientChannelEvent.TIMEOUT)) {
                channel.close(true);
                return new BoundedResult(writer.text(), writer.truncated(),
                    new IOException("timed out after " + timeout.toSeconds() + "s"));
            }
            exitStatus = channel.getExitStatus();
        } catch (IOException e) {
            return new BoundedResult(writer.text(), writer.truncated(), e);
        }
        if (exitStatus != null && exitStatus != 0) {
            return new BoundedResult(writer.text(), writer.truncated(),
                new CommandFailedException(writer.text(), exitStatus));
        }
        return new BoundedResult(writer.text(), writer.truncated(), null);
    }

    /** Runs a command; when combinedOutput the returned text prefers stderr (trimmed), else stdout, like RunCmdCombined. */
    static CommandResult runCmd(ClientSession session, String command, boolean combinedOutput)
        throws IOException {
        ByteArrayOutputStream stdout = new ByteArrayOutputStream();
        ByteArrayOutputStream stderr = new ByteArrayOutputStream();
        int exit = -1;
        boolean timedOut = false;
        try (ChannelExec channel = session.createExecChannel(command)) {
            channel.setOut(stdout);
            channel.setErr(stderr);
            channel.open().verify(CHANNEL_OPEN_TIMEOUT.toMillis());
            java.util.Collection<ClientChannelEvent> events = channel.waitFor(
                List.of(ClientChannelEvent.CLOSED), TimeUnit.SECONDS.toMillis(300));
            if (events.contains(ClientChannelEvent.TIMEOUT)) {
                timedOut = true;
                channel.close(true);
            }
            Integer status = channel.getExitStatus();
            exit = status == null ? -1 : status;
        }
        String out = stdout.toString(StandardCharsets.UTF_8);
        String err = stderr.toString(StandardCharsets.UTF_8);
        if (combinedOutput) {
            String trimmed = err.trim();
            return new CommandResult(trimmed.isEmpty() ? out.trim() : trimmed, exit, timedOut);
        }
        return new CommandResult(out, exit, timedOut);
    }

    public static SftpClient openSftp(ClientSession session) throws IOException {
        return org.apache.sshd.sftp.client.SftpClientFactory.instance().createSftpClient(session);
    }
}
