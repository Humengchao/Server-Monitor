package com.servermonitor.files;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.regex.Pattern;

import org.apache.sshd.client.channel.ChannelExec;
import org.apache.sshd.client.channel.ClientChannelEvent;
import org.apache.sshd.client.session.ClientSession;

import com.servermonitor.files.RemoteFiles.Entry;
import com.servermonitor.files.RemoteFiles.Listing;
import com.servermonitor.files.RemoteFiles.Managed;
import com.servermonitor.ssh.SshSupport;

/**
 * File access inside a running Docker container over `docker exec` on the host,
 * mirroring services/container_*.go. Shells inside containers are minimal, so
 * the scripts rely only on sh + coreutils and map their own exit codes (44
 * exists, 45 not-regular, 46 not-found, 47 permission) onto the API's errors.
 */
public class ContainerFiles implements Managed {

    private static final Pattern CONTAINER_ID = Pattern.compile("^[a-fA-F0-9]{12,64}$");

    public static boolean validContainerId(String value) {
        return value != null && CONTAINER_ID.matcher(value).matches();
    }

    public static final class FileTooLargeSignal extends IOException {
        public FileTooLargeSignal() {
            super("file exceeds the size limit");
        }
    }

    /** stdout sink that fails the transfer once the retained bytes pass the limit. */
    private static final class FileOutputWriter extends OutputStream {
        private final OutputStream output;
        private long remaining;
        private boolean exceeded;
        private final Runnable close;

        FileOutputWriter(OutputStream output, long limit, Runnable close) {
            this.output = output;
            this.remaining = limit;
            this.close = close;
        }

        @Override
        public void write(int b) throws IOException {
            write(new byte[]{(byte) b}, 0, 1);
        }

        @Override
        public void write(byte[] data, int off, int len) throws IOException {
            if (len > remaining) {
                exceeded = true;
                close.run();
                throw new FileTooLargeSignal();
            }
            output.write(data, off, len);
            remaining -= len;
        }

        boolean exceeded() {
            return exceeded;
        }
    }

    private final ClientSession client;
    private final String command; // "docker" or "sudo -n docker"
    private final String containerId;
    private final Duration deadline;

    private ContainerFiles(ClientSession client, String command, String containerId, Duration deadline) {
        this.client = client;
        this.command = command;
        this.containerId = containerId;
        this.deadline = deadline;
    }

    public static ContainerFiles open(ClientSession client, String containerId, String serverType,
        Duration timeout) throws IOException {
        if (!validContainerId(containerId)) {
            throw FileErrors.PATH;
        }
        if ("windows".equals(serverType)) {
            throw FileErrors.UNSUPPORTED;
        }
        String command = "docker";
        String probe = " inspect --format '{{.State.Status}}' " + containerId;
        SshSupport.BoundedResult out = SshSupport.runCmdBounded(client, command + probe,
            Duration.ofSeconds(10), 1024);
        if (out.error() != null) {
            command = "sudo -n docker";
            out = SshSupport.runCmdBounded(client, command + probe, Duration.ofSeconds(10), 1024);
        }
        if (out.error() != null) {
            throw out.error();
        }
        if (!"running".equals(out.output().trim())) {
            throw FileErrors.UNSUPPORTED;
        }
        return new ContainerFiles(client, command, containerId, timeout);
    }

    @Override
    public void close() {
        // Nothing persistent: every call is its own docker exec.
    }

    /** Quotes one shell argument for `docker exec ... sh -c '…' sh 'arg'`. */
    static String quote(String value) {
        return "'" + value.replace("'", "'\"'\"'") + "'";
    }

    /**
     * Runs one script inside the container with bounded output capture and the
     * store deadline, mapping the script's agreed exit codes onto API errors.
     */
    private void run(String script, List<String> arguments, InputStream input,
        OutputStream output, long limit) throws IOException {
        StringBuilder cmd = new StringBuilder(command).append(" exec -i ")
            .append(containerId).append(" sh -c ").append(quote(script)).append(" sh");
        for (String argument : arguments) {
            cmd.append(' ').append(quote(argument));
        }
        long startNanos = System.nanoTime();
        FileOutputWriter capture = new FileOutputWriter(output, limit, () -> { });
        try (ChannelExec channel = client.createExecChannel(cmd.toString())) {
            channel.setOut(capture == null ? OutputStream.nullOutputStream() : capture);
            channel.setErr(new SshSupport.LimitedOutput(4096));
            if (input != null) {
                channel.setIn(input);
            }
            channel.open().verify(SshSupport.CHANNEL_OPEN_TIMEOUT.toMillis());
            long waitMillis = Math.max(1, deadline.toMillis() - (System.nanoTime() - startNanos) / 1_000_000);
            java.util.Collection<ClientChannelEvent> events =
                channel.waitFor(List.of(ClientChannelEvent.CLOSED), waitMillis);
            if (events.contains(ClientChannelEvent.TIMEOUT)) {
                channel.close(true);
                throw FileErrors.TIMEOUT;
            }
            if (capture.exceeded()) {
                throw FileErrors.TOO_LARGE;
            }
            Integer status = channel.getExitStatus();
            if (status != null && status != 0) {
                switch (status) {
                    case 44 -> throw FileErrors.EXISTS;
                    case 45 -> throw FileErrors.NOT_REGULAR;
                    case 46 -> throw FileErrors.NOT_FOUND;
                    case 47 -> throw FileErrors.PERMISSION;
                    default -> throw new SshSupport.CommandFailedException("", status);
                }
            }
        }
    }

    private static String path(String directory, String name) {
        return directory.endsWith("/") ? directory + name : directory + "/" + name;
    }

    private static String dirOf(String p) {
        int idx = p.lastIndexOf('/');
        if (idx < 0) {
            return ".";
        }
        String head = p.substring(0, idx);
        return head.isEmpty() ? "/" : head;
    }

    // --- full listing (non-paged) ----------------------------------------

    private static final String LIST_SCRIPT = """
        command -v stat >/dev/null || exit 127
        cd -P "$1" || exit 1
        printf '%s\\000' "$PWD"
        count=0
        for entry in .[!.]* ..?* *; do
            [ -e "$entry" ] || [ -L "$entry" ] || continue
            count=$((count + 1))
            [ "$count" -le 5000 ] || { printf 'TRUNCATED\\000'; break; }
            details=$(stat -L -c '%f %s %Y %a' -- "$entry") || details='0 0 0 0'
            link=0
            [ ! -L "$entry" ] || link=1
            printf '%s %s\\000%s\\000' "$details" "$link" "$entry"
        done""";

    @Override
    public Listing list(String directory) throws IOException {
        if (".".equals(directory)) {
            directory = "/";
        }
        java.io.ByteArrayOutputStream output = new java.io.ByteArrayOutputStream();
        run(LIST_SCRIPT, List.of(directory), null, output, 4L << 20);
        return parseFileList(output.toByteArray());
    }

    static Listing parseFileList(byte[] output) throws IOException {
        List<byte[]> fields = splitNul(output);
        if (fields.size() < 2 || fields.get(fields.size() - 1).length != 0) {
            throw new IOException("invalid directory response");
        }
        String canonical = RemoteFiles.cleanRemotePath(new String(fields.get(0), StandardCharsets.UTF_8), false);
        List<Entry> entries = new ArrayList<>();
        boolean truncated = false;
        for (int index = 1; index < fields.size() - 1; index += 2) {
            String metadata = new String(fields.get(index), StandardCharsets.UTF_8);
            if (metadata.equals("TRUNCATED")) {
                truncated = true;
                break;
            }
            if (index + 1 >= fields.size() - 1) {
                throw new IOException("incomplete directory entry");
            }
            String name = new String(fields.get(index + 1), StandardCharsets.UTF_8);
            String[] parts = metadata.trim().split("\\s+");
            if (parts.length != 5 || name.isEmpty() || name.contains("/") || name.equals(".") || name.equals("..")) {
                throw new IOException("invalid file metadata");
            }
            int mode;
            long size, modified;
            try {
                mode = Integer.parseInt(parts[0], 16);
                size = Long.parseLong(parts[1]);
                modified = Long.parseLong(parts[2]);
            } catch (NumberFormatException e) {
                throw new IOException("invalid file metadata");
            }
            entries.add(new Entry(name, path(canonical, name), size, Instant.ofEpochSecond(modified),
                parts[3], (mode & 0170000) == 0040000, (mode & 0170000) == 0100000, "1".equals(parts[4])));
        }
        RemoteFiles.sortEntries(entries);
        return new Listing(canonical, RemoteFiles.remoteFileParent(canonical), entries, truncated, null);
    }

    private static List<byte[]> splitNul(byte[] data) {
        List<byte[]> out = new ArrayList<>();
        int start = 0;
        for (int i = 0; i < data.length; i++) {
            if (data[i] == 0) {
                out.add(java.util.Arrays.copyOfRange(data, start, i));
                start = i + 1;
            }
        }
        out.add(java.util.Arrays.copyOfRange(data, start, data.length));
        return out;
    }

    @Override
    public void read(String filename, OutputStream output, long limit) throws IOException {
        run("[ -f \"$1\" ] || exit 45\ncat -- \"$1\"", List.of(filename), null, output, limit);
    }

    // --- write -------------------------------------------------------------

    private static final String WRITE_SCRIPT = """
        if [ -e "$1" ] || [ -L "$1" ]; then
            [ "$3" = 1 ] || exit 44
            [ ! -L "$1" ] && [ -f "$1" ] || exit 45
        fi
        umask 077
        (set -C; : > "$2") || exit 1
        trap 'rm -f -- "$2"' EXIT HUP INT TERM
        if [ -f "$1" ]; then
            ownership=$(stat -c '%u:%g' -- "$1") || exit 1
            permissions=$(stat -c '%a' -- "$1") || exit 1
            chown "$ownership" "$2" || exit 1
            chmod "$permissions" "$2" || exit 1
        else
            chmod 644 "$2" || exit 1
        fi
        cat > "$2" || exit 1
        [ "$(wc -c < "$2")" -eq "$4" ] || exit 1
        if [ "$3" = 1 ]; then
            [ ! -L "$1" ] || exit 45
            [ ! -e "$1" ] || [ -f "$1" ] || exit 45
            mv -f -- "$2" "$1" || exit 1
        else
            ln -- "$2" "$1" || exit 44
        fi""";

    @Override
    public void write(String filename, InputStream input, long size, boolean overwrite) throws IOException {
        String temporary = dirOf(filename) + "/.server-monitor-" + UUID.randomUUID();
        run(WRITE_SCRIPT, List.of(filename, temporary, overwrite ? "1" : "0", String.valueOf(size)),
            input, OutputStream.nullOutputStream(), 4096);
    }

    // --- metadata / mutations -------------------------------------------

    private static Entry containerEntry(String filename, String line) throws IOException {
        String[] fields = line.trim().split("\\s+");
        if (fields.length != 4) {
            throw new IOException("invalid file metadata");
        }
        int mode;
        long size, modified;
        try {
            mode = Integer.parseInt(fields[0], 16);
            size = Long.parseLong(fields[1]);
            modified = Long.parseLong(fields[2]);
        } catch (NumberFormatException e) {
            throw new IOException(e.getMessage(), e);
        }
        String name = filename.substring(filename.lastIndexOf('/') + 1);
        return new Entry(name, filename, size, Instant.ofEpochSecond(modified), fields[3],
            (mode & 0170000) == 0040000, (mode & 0170000) == 0100000, (mode & 0170000) == 0120000);
    }

    @Override
    public Entry metadata(String filename) throws IOException {
        java.io.ByteArrayOutputStream output = new java.io.ByteArrayOutputStream();
        run("[ -e \"$1\" ] || [ -L \"$1\" ] || exit 46\nstat -c '%f %s %Y %a' -- \"$1\"",
            List.of(filename), null, output, 1024);
        return containerEntry(filename, output.toString(StandardCharsets.UTF_8));
    }

    @Override
    public void mkdir(String directory, boolean privateDir) throws IOException {
        String script;
        if (privateDir) {
            script = "if [ -e \"$1\" ] || [ -L \"$1\" ]; then\n"
                + " [ -d \"$1\" ] && [ ! -L \"$1\" ] || exit 45\nelse\n"
                + " (umask 077; mkdir -- \"$1\") || exit 47\nfi\nchmod 700 -- \"$1\"";
        } else {
            script = "[ ! -e \"$1\" ] && [ ! -L \"$1\" ] || exit 44\nmkdir -- \"$1\"";
        }
        run(script, List.of(directory), null, OutputStream.nullOutputStream(), 1024);
    }

    @Override
    public void rename(String source, String destination) throws IOException {
        run("[ -e \"$1\" ] || [ -L \"$1\" ] || exit 46\n"
            + "[ ! -e \"$2\" ] && [ ! -L \"$2\" ] || exit 44\n"
            + "mv -nT -- \"$1\" \"$2\" || exit 1\n"
            + "[ ! -e \"$1\" ] && [ ! -L \"$1\" ] || exit 44",
            List.of(source, destination), null, OutputStream.nullOutputStream(), 1024);
    }

    @Override
    public void remove(String filename) throws IOException {
        run("[ -e \"$1\" ] || [ -L \"$1\" ] || exit 46\n"
            + "if [ -d \"$1\" ] && [ ! -L \"$1\" ]; then rmdir -- \"$1\"; else rm -- \"$1\"; fi",
            List.of(filename), null, OutputStream.nullOutputStream(), 1024);
    }

    // --- paged listing -----------------------------------------------------

    /** Collects NUL-delimited names incrementally so a paged listing needn't buffer the whole dir. */
    private static final class NamesCollector extends OutputStream {
        private final java.io.ByteArrayOutputStream pending = new java.io.ByteArrayOutputStream();
        String directory;
        final List<String> names = new ArrayList<>();
        final int offset;
        final int limit;
        int seen;
        boolean done;

        NamesCollector(int offset, int limit) {
            this.offset = offset;
            this.limit = limit;
        }

        @Override
        public void write(int b) throws IOException {
            write(new byte[]{(byte) b}, 0, 1);
        }

        @Override
        public void write(byte[] data, int off, int len) throws IOException {
            pending.write(data, off, len);
            while (true) {
                byte[] buf = pending.toByteArray();
                int index = -1;
                for (int i = 0; i < buf.length; i++) {
                    if (buf[i] == 0) {
                        index = i;
                        break;
                    }
                }
                if (index < 0) {
                    break;
                }
                String value = new String(buf, 0, index, StandardCharsets.UTF_8);
                pending.reset();
                pending.write(buf, index + 1, buf.length - index - 1);
                if (directory == null) {
                    directory = value;
                    continue;
                }
                if (seen >= offset) {
                    names.add(value.startsWith("./") ? value.substring(2) : value);
                }
                seen++;
                if (names.size() > limit) {
                    done = true;
                    return;
                }
            }
            if (pending.size() > 8192) {
                throw FileErrors.PATH;
            }
        }
    }

    @Override
    public Listing page(String directory, int offset, int limit) throws IOException {
        if (".".equals(directory)) {
            directory = "/";
        }
        NamesCollector collector = new NamesCollector(offset, limit);
        IOException firstErr = null;
        try {
            run("cd -P \"$1\" || exit 46\nprintf '%s\\000' \"$PWD\"\nfind . -mindepth 1 -maxdepth 1 -print0",
                List.of(directory), null, collector, (long) (offset + limit + 2) * 8192);
        } catch (IOException e) {
            if (!collector.done) {
                firstErr = e;
            }
        }
        if (firstErr != null) {
            throw firstErr;
        }
        String canonical = RemoteFiles.cleanRemotePath(collector.directory == null ? "" : collector.directory, false);
        List<String> names = collector.names;
        if (names.size() > limit) {
            names = new ArrayList<>(names.subList(0, limit));
        }
        List<Entry> entries = new ArrayList<>();
        if (!names.isEmpty()) {
            List<String> arguments = new ArrayList<>();
            arguments.add(canonical);
            arguments.addAll(names);
            java.io.ByteArrayOutputStream statOut = new java.io.ByteArrayOutputStream();
            run("cd -P \"$1\" || exit 46\nshift\nstat -c '%f %s %Y %a' -- \"$@\"",
                arguments, null, statOut, (long) limit * 256);
            String[] lines = statOut.toString(StandardCharsets.UTF_8).trim().split("\n");
            if (lines.length != names.size()) {
                throw new IOException("directory changed during listing; refresh");
            }
            for (int index = 0; index < names.size(); index++) {
                Entry entry = containerEntry(path(canonical, names.get(index)), lines[index]);
                if (entry.isSymlink()) {
                    java.io.ByteArrayOutputStream target = new java.io.ByteArrayOutputStream();
                    try {
                        run("stat -L -c '%f %s %Y %a' -- \"$1\"", List.of(entry.path()), null, target, 256);
                        Entry resolved = containerEntry(entry.path(), target.toString(StandardCharsets.UTF_8));
                        entry = new Entry(resolved.name(), resolved.path(), resolved.size(), resolved.modifiedAt(),
                            resolved.mode(), resolved.isDir(), resolved.isFile(), true);
                    } catch (IOException ignored) { }
                }
                entries.add(entry);
            }
        }
        RemoteFiles.sortEntries(entries);
        String nextCursor = collector.done ? String.valueOf(offset + limit) : null;
        return new Listing(canonical, RemoteFiles.remoteFileParent(canonical), entries, false, nextCursor);
    }
}
