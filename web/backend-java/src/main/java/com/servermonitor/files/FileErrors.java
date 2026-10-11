package com.servermonitor.files;

import java.io.IOException;

/** The stable error vocabulary of the file API; handlers map these onto status + code. */
public final class FileErrors {

    private FileErrors() {
    }

    public static final IOException TOO_LARGE = new IOException("file exceeds the size limit");
    public static final IOException NOT_TEXT = new IOException("only UTF-8 text files can be edited");
    public static final IOException NOT_REGULAR = new IOException("not a regular file or refusing to replace a symbolic link");
    public static final IOException EXISTS = new IOException("file already exists");
    public static final IOException CHANGED = new IOException("file changed since it was opened");
    public static final IOException PATH = new IOException("invalid remote path");
    public static final IOException UNSUPPORTED = new IOException("container files require a running Linux container with sh and core file utilities on a Linux host");
    public static final IOException NOT_FOUND = new IOException("no such file or directory");
    public static final IOException PERMISSION = new IOException("permission denied");
    public static final IOException TIMEOUT = new IOException("file operation timed out or was canceled");

    public static boolean isNotExist(Throwable err) {
        for (Throwable t = err; t != null; t = t.getCause()) {
            String msg = t.getMessage();
            if (msg != null) {
                String lower = msg.toLowerCase();
                if (lower.contains("no such file") || lower.contains("not exist")
                    || lower.contains("not found") || lower.contains("status=fxp_no_such_file")
                    || lower.contains("no_such_file")) {
                    return true;
                }
            }
        }
        return false;
    }

    public static boolean isPermission(Throwable err) {
        for (Throwable t = err; t != null; t = t.getCause()) {
            String msg = t.getMessage();
            if (msg != null && msg.toLowerCase().contains("permission")) {
                return true;
            }
        }
        return false;
    }

    public static boolean isTimeout(Throwable err) {
        for (Throwable t = err; t != null; t = t.getCause()) {
            if (t instanceof java.util.concurrent.TimeoutException) {
                return true;
            }
            String msg = t.getMessage();
            if (msg != null && (msg.toLowerCase().contains("timed out") || msg.toLowerCase().contains("timeout"))) {
                return true;
            }
        }
        return false;
    }
}
