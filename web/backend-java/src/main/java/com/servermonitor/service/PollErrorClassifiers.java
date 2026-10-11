package com.servermonitor.service;

import java.util.Locale;

/** Poll error taxonomy shared by the collector and the manual retry endpoint. */
public final class PollErrorClassifiers {

    private PollErrorClassifiers() {
    }

    public enum Kind {
        AUTH("auth"), HOST_KEY("host_key"), UNREACHABLE("unreachable"), TIMEOUT("timeout"),
        COMMAND("command"), STORAGE("storage"), OTHER("other"), NONE("");

        private final String wire;

        Kind(String wire) {
            this.wire = wire;
        }

        public String wire() {
            return wire;
        }
    }

    public static Kind classify(Throwable err) {
        if (err == null) {
            return Kind.NONE;
        }
        String message = messageChain(err).toLowerCase(Locale.ROOT);
        if (message.contains("host key") || message.contains("knownhosts")) {
            return Kind.HOST_KEY;
        }
        if (isSSHAuthenticationFailure(err)) {
            return Kind.AUTH;
        }
        if (message.contains("save metric") || message.contains("sql")) {
            return Kind.STORAGE;
        }
        if (message.contains("timeout") || message.contains("timed out") || message.contains("deadline exceeded")) {
            return Kind.TIMEOUT;
        }
        if (message.contains("connection refused") || message.contains("no route to host")
            || message.contains("no such host") || message.contains("network is unreachable")
            || message.contains("connection reset")) {
            return Kind.UNREACHABLE;
        }
        if (message.contains("ssh session") || message.contains("run probe") || message.contains("exited with status")) {
            return Kind.COMMAND;
        }
        return Kind.OTHER;
    }

    /** Authentication failures get the conservative circuit-breaker delay; match the stable phrases. */
    public static boolean isSSHAuthenticationFailure(Throwable err) {
        if (err == null) {
            return false;
        }
        String message = messageChain(err).toLowerCase(Locale.ROOT);
        return message.contains("unable to authenticate")
            || message.contains("no authentication methods")
            || message.contains("authentication failed")
            || message.contains("auth fail");
    }

    private static String messageChain(Throwable err) {
        StringBuilder out = new StringBuilder();
        for (Throwable t = err; t != null && out.length() < 2000; t = t.getCause()) {
            if (t.getMessage() != null) {
                out.append(t.getMessage()).append(": ");
            }
        }
        return out.toString();
    }

    private static final int DETAIL_MAX = 300;

    /** One-line, whitespace-collapsed, UTF-8-safe bounded detail for the UI. */
    public static String trimPollErrorDetail(Throwable err) {
        if (err == null) {
            return "";
        }
        String detail = messageChain(err).trim().replaceAll("\\s+", " ");
        if (detail.length() <= DETAIL_MAX) {
            return detail;
        }
        return detail.substring(0, DETAIL_MAX).trim() + "…";
    }
}
