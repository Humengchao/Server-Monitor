package com.servermonitor.files;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HexFormat;
import java.util.List;

/** Abstractions and shared helpers for the remote file API, mirroring services/files.go's interface block. */
public final class RemoteFiles {

    private RemoteFiles() {
    }

    public static final long MAX_TEXT_FILE_SIZE = 2L << 20;
    public static final long MAX_UPLOAD_FILE_SIZE = 64L << 20;
    public static final long MAX_DOWNLOAD_FILE_SIZE = 256L << 20;
    public static final int MAX_DIRECTORY_ENTRIES = 5000;
    public static final java.time.Duration FILE_OPERATION_TIMEOUT = java.time.Duration.ofMinutes(2);

    public record Entry(String name, String path, long size, Instant modifiedAt, String mode,
        boolean isDir, boolean isFile, boolean isSymlink) {
    }

    public record Listing(String path, String parent, List<Entry> entries,
        boolean truncated, String nextCursor) {
    }

    /** The read/write surface both SFTP and container stores implement. */
    public interface Store extends AutoCloseable {
        Listing list(String directory) throws IOException;

        void read(String filename, OutputStream output, long limit) throws IOException;

        void write(String filename, InputStream input, long size, boolean overwrite) throws IOException;

        @Override
        void close() throws IOException;
    }

    /** The fuller surface used for metadata, mutations and paged listings. */
    public interface Managed extends Store {
        Listing page(String directory, int offset, int limit) throws IOException;

        Entry metadata(String filename) throws IOException;

        void mkdir(String directory, boolean privateDir) throws IOException;

        void rename(String source, String destination) throws IOException;

        void remove(String filename) throws IOException;
    }

    /** Normalizes a caller path; Windows drive forms convert to forward slashes. */
    public static String cleanRemotePath(String value, boolean allowHome) throws IOException {
        if ((value == null || value.isEmpty()) && allowHome) {
            return ".";
        }
        if (value == null || value.length() > 4096 || value.indexOf(0) >= 0) {
            throw FileErrors.PATH;
        }
        if (value.length() >= 3 && value.charAt(1) == ':' && value.charAt(2) == '\\') {
            value = value.replace('\\', '/');
        }
        boolean drivePath = value.length() >= 3
            && Character.isLetter(value.charAt(0))
            && value.charAt(1) == ':' && value.charAt(2) == '/';
        boolean absolute = value.startsWith("/") || drivePath;
        if (!absolute) {
            throw FileErrors.PATH;
        }
        String cleaned = normalize(value);
        if (drivePath && cleaned.length() == 2) {
            cleaned += "/";
        }
        if (cleaned.length() == 3 && cleaned.charAt(0) == '/' && cleaned.charAt(2) == ':') {
            cleaned += "/";
        }
        return cleaned;
    }

    /** Minimal path.Clean: resolves "." and ".." lexically, collapses slashes, keeps leading "/". */
    static String normalize(String value) {
        boolean root = value.startsWith("/");
        String[] parts = value.split("/+");
        List<String> out = new ArrayList<>();
        for (String part : parts) {
            if (part.isEmpty() || part.equals(".")) {
                continue;
            }
            if (part.equals("..")) {
                if (!out.isEmpty() && !out.get(out.size() - 1).equals("..")) {
                    out.remove(out.size() - 1);
                } else if (!root) {
                    out.add("..");
                }
                continue;
            }
            out.add(part);
        }
        String joined = String.join("/", out);
        if (root) {
            joined = "/" + joined;
        }
        if (joined.isEmpty()) {
            return root ? "/" : ".";
        }
        if (value.endsWith("/") && value.length() > 1 && !joined.equals("/")) {
            // path.Clean drops trailing slashes except for the root.
        }
        return joined;
    }

    public static String remoteFileParent(String directory) {
        if (directory.length() == 3 && directory.charAt(1) == ':' && directory.charAt(2) == '/') {
            return directory;
        }
        if (directory.length() == 4 && directory.charAt(0) == '/' && directory.charAt(2) == '/') {
            return directory;
        }
        String parent = dirOf(directory);
        if (parent.length() == 2 && parent.charAt(1) == ':') {
            parent += "/";
        }
        if (parent.length() == 3 && parent.charAt(0) == '/' && parent.charAt(2) == ':') {
            parent += "/";
        }
        return parent;
    }

    public static String dirOf(String path) {
        int idx = path.lastIndexOf('/');
        if (idx < 0) {
            return ".";
        }
        String head = path.substring(0, idx);
        if (head.isEmpty()) {
            return "/";
        }
        return head;
    }

    public static String baseOf(String path) {
        int idx = path.lastIndexOf('/');
        return idx < 0 ? path : path.substring(idx + 1);
    }

    public static boolean validUploadName(String name) {
        return name != null && !name.isEmpty() && !name.equals(".") && !name.equals("..")
            && name.length() <= 255 && !name.matches(".*[/\\\\\\x00\\r\\n].*");
    }

    public static void sortEntries(List<Entry> entries) {
        entries.sort(Comparator.comparing((Entry e) -> !e.isDir())
            .thenComparing(e -> e.name().toLowerCase()));
    }

    /** sha256(content) hex — the optimistic-concurrency token for text edits. */
    public static String fileRevision(byte[] content) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(content));
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    public static String fileMetadataVersion(Entry entry) {
        // Match Go's UnixNano formatting exactly: seconds*1e9 + fractional nanos.
        long nanos = entry.modifiedAt().getEpochSecond() * 1_000_000_000L + entry.modifiedAt().getNano();
        String stamp = entry.path() + ":" + entry.size() + ":" + nanos + ":" + entry.mode() + ":" + entry.isSymlink();
        return fileRevision(stamp.getBytes(java.nio.charset.StandardCharsets.UTF_8));
    }

    /** Bounded, UTF-8-and-NUL validated text read for the editor. */
    public static byte[] readRemoteText(Store store, String filename) throws IOException {
        java.io.ByteArrayOutputStream content = new java.io.ByteArrayOutputStream();
        store.read(filename, content, MAX_TEXT_FILE_SIZE);
        byte[] bytes = content.toByteArray();
        if (!isValidUtf8(bytes) || indexOfByte(bytes, (byte) 0) >= 0) {
            throw FileErrors.NOT_TEXT;
        }
        return bytes;
    }

    public static void saveRemoteText(Store store, String filename, String content, String revision)
        throws IOException {
        byte[] bytes = content.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        if (bytes.length > MAX_TEXT_FILE_SIZE) {
            throw FileErrors.TOO_LARGE;
        }
        if (content.indexOf(0) >= 0) {
            throw FileErrors.NOT_TEXT;
        }
        byte[] current = readRemoteText(store, filename);
        if (revision.isEmpty() || !fileRevision(current).equals(revision)) {
            throw FileErrors.CHANGED;
        }
        store.write(filename, new java.io.ByteArrayInputStream(bytes), bytes.length, true);
    }

    /** Backs content up beside the original in a hidden .server-monitor-backups directory. */
    public static String backupRemoteText(Store store, String filename, byte[] content) throws IOException {
        if (!(store instanceof Managed managed)) {
            throw new IOException("file backups unsupported");
        }
        String directory = dirOf(filename) + "/.server-monitor-backups";
        managed.mkdir(directory, true);
        String name = baseOf(filename);
        if (name.length() > 80) {
            name = "file";
        }
        String stamp = java.time.format.DateTimeFormatter.ofPattern("yyyyMMdd'T'HHmmss")
            .withZone(java.time.ZoneOffset.UTC).format(java.time.Instant.now());
        String backup = directory + "/" + name + "." + stamp + "." + java.util.UUID.randomUUID();
        store.write(backup, new java.io.ByteArrayInputStream(content), content.length, false);
        return backup;
    }

    static boolean isValidUtf8(byte[] bytes) {
        java.nio.charset.CharsetDecoder decoder = java.nio.charset.StandardCharsets.UTF_8.newDecoder()
            .onMalformedInput(java.nio.charset.CodingErrorAction.REPORT)
            .onUnmappableCharacter(java.nio.charset.CodingErrorAction.REPORT);
        try {
            decoder.decode(java.nio.ByteBuffer.wrap(bytes));
            return true;
        } catch (java.nio.charset.CharacterCodingException e) {
            return false;
        }
    }

    static int indexOfByte(byte[] bytes, byte value) {
        for (int i = 0; i < bytes.length; i++) {
            if (bytes[i] == value) {
                return i;
            }
        }
        return -1;
    }
}
