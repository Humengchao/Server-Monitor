package com.servermonitor.files;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.PipedInputStream;
import java.io.PipedOutputStream;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

import org.apache.sshd.client.session.ClientSession;
import org.apache.sshd.sftp.client.SftpClient;

import com.servermonitor.files.RemoteFiles.Entry;
import com.servermonitor.files.RemoteFiles.Listing;

/**
 * Paged SFTP directory reads, mirroring services/file_directory.go. Speaks the
 * v3 wire protocol over its own subsystem session so one directory read can
 * stop at a page boundary instead of materializing the whole listing.
 */
final class RemoteDirectoryPages {

    private RemoteDirectoryPages() {
    }

    private static final byte SSH_FXP_INIT = 1;
    private static final byte SSH_FXP_VERSION = 2;
    private static final byte SSH_FXP_CLOSE = 4;
    private static final byte SSH_FXP_OPENDIR = 11;
    private static final byte SSH_FXP_READDIR = 12;
    private static final byte SSH_FXP_REALPATH = 16;
    private static final byte SSH_FXP_STATUS = 101;
    private static final byte SSH_FXP_HANDLE = 102;
    private static final byte SSH_FXP_NAME = 104;
    private static final byte SSH_FXP_ATTRS = 105;

    private static final class Protocol implements AutoCloseable {
        private final PipedOutputStream writer;
        private final PipedInputStream reader;
        private final AutoCloseable channel;
        private int sequence;

        Protocol(PipedOutputStream writer, PipedInputStream reader, AutoCloseable channel) {
            this.writer = writer;
            this.reader = reader;
            this.channel = channel;
        }

        /** Sends one packet and parses the response's kind + payload. */
        byte[] send(byte kind, byte[] payload) throws IOException {
            sequence++;
            int length = 1 + 4 + payload.length;
            ByteBuffer packet = ByteBuffer.allocate(4 + length);
            packet.putInt(length);
            packet.put(kind);
            if (kind == SSH_FXP_INIT) {
                packet.putInt(3); // protocol version 3
            } else {
                packet.putInt(sequence);
            }
            packet.put(payload);
            writer.write(packet.array());
            writer.flush();
            byte[] header = readFully(4);
            int size = ByteBuffer.wrap(header).getInt();
            if (size < 5 || size > (1 << 20)) {
                throw new IOException("invalid SFTP directory packet");
            }
            byte[] response = readFully(size);
            if (kind == SSH_FXP_INIT) {
                int version = ByteBuffer.wrap(response, 1, 4).getInt();
                if (version != 3) {
                    throw new IOException("SFTP v3 required");
                }
            } else {
                int requestId = ByteBuffer.wrap(response, 1, 4).getInt();
                if (requestId != sequence) {
                    throw new IOException("unexpected SFTP request id");
                }
            }
            byte[] body = new byte[response.length - 5];
            System.arraycopy(response, 5, body, 0, body.length);
            byte[] out = new byte[body.length + 1];
            out[0] = response[0];
            System.arraycopy(body, 0, out, 1, body.length);
            return out;
        }

        private byte[] readFully(int count) throws IOException {
            byte[] buf = new byte[count];
            int off = 0;
            while (off < count) {
                int read = reader.read(buf, off, count - off);
                if (read < 0) {
                    throw new IOException("unexpected EOF");
                }
                off += read;
            }
            return buf;
        }

        @Override
        public void close() throws IOException {
            try {
                writer.close();
            } catch (IOException ignored) { }
            try {
                reader.close();
            } catch (IOException ignored) { }
            try {
                channel.close();
            } catch (Exception ignored) { }
        }
    }

    private static byte[] protocolString(String value) {
        byte[] bytes = value.getBytes(StandardCharsets.UTF_8);
        ByteBuffer out = ByteBuffer.allocate(4 + bytes.length);
        out.putInt(bytes.length);
        out.put(bytes);
        return out.array();
    }

    private static final class AttributeReader {
        private final ByteBuffer buf;
        IOException err;

        AttributeReader(byte[] payload) {
            this.buf = ByteBuffer.wrap(payload).order(ByteOrder.BIG_ENDIAN);
        }

        long u32() {
            if (err != null) {
                return 0;
            }
            if (buf.remaining() < 4) {
                err = new IOException("unexpected EOF");
                return 0;
            }
            return Integer.toUnsignedLong(buf.getInt());
        }

        long u64() {
            if (err != null) {
                return 0;
            }
            if (buf.remaining() < 8) {
                err = new IOException("unexpected EOF");
                return 0;
            }
            return buf.getLong();
        }

        String text() {
            long size = u32();
            if (err != null) {
                return "";
            }
            if (size > buf.remaining()) {
                err = new IOException("unexpected EOF");
                return "";
            }
            byte[] bytes = new byte[(int) size];
            buf.get(bytes);
            return new String(bytes, StandardCharsets.UTF_8);
        }

        Entry entry(String directory) {
            String name = text();
            text(); // long name (human rendering) — unused
            long flags = u32();
            long size = 0;
            if ((flags & 1) != 0) {
                size = u64();
            }
            if ((flags & 2) != 0) {
                u32();
                u32();
            }
            int mode = 0;
            if ((flags & 4) != 0) {
                mode = (int) u32();
            }
            int modified = 0;
            if ((flags & 8) != 0) {
                u32(); // atime
                modified = (int) u32();
            }
            if ((flags & 0x80000000L) != 0) {
                long count = u32();
                if (count > 1024) {
                    err = new IOException("too many SFTP attributes");
                } else {
                    for (long i = 0; i < count; i++) {
                        text();
                        text();
                    }
                }
            }
            boolean dir = (mode & 0170000) == 0040000;
            boolean symlink = (mode & 0170000) == 0120000;
            boolean regular = (mode & 0170000) == 0100000;
            return new Entry(name, directory.endsWith("/") ? directory + name : directory + "/" + name,
                size, Instant.ofEpochSecond(modified & 0xffffffffL), modeString(mode, symlink), dir, regular, symlink);
        }
    }

    static String modeString(int mode, boolean symlink) {
        String kind = (mode & 0170000) == 0040000 ? "d" : symlink ? "L" : (mode & 0170000) == 0100000 ? "-" : "?";
        StringBuilder out = new StringBuilder(kind);
        for (int shift = 6; shift >= 0; shift -= 3) {
            int bits = (mode >> shift) & 7;
            out.append((bits & 4) != 0 ? 'r' : '-')
                .append((bits & 2) != 0 ? 'w' : '-')
                .append((bits & 1) != 0 ? 'x' : '-');
        }
        return out.toString();
    }

    private static IOException directoryStatus(byte[] payload) {
        if (payload.length < 4) {
            return new IOException("unexpected EOF");
        }
        int code = ByteBuffer.wrap(payload).getInt();
        return switch (code) {
            case 0 -> null;
            case 1 -> new IOException("EOF");
            case 2 -> FileErrors.NOT_FOUND;
            case 3 -> FileErrors.PERMISSION;
            default -> new IOException("SFTP directory request failed");
        };
    }

    static Listing pageOverSftp(SftpFiles store, ClientSession session, String directory,
        int offset, int limit) throws IOException {
        // A dedicated subsystem channel carries the raw SFTP byte stream: its
        // stdout feeds the reader side, the writer side feeds its stdin.
        PipedInputStream rawStdin = new PipedInputStream(1 << 16);
        PipedOutputStream channelIn = new PipedOutputStream(rawStdin);
        PipedInputStream rawStdout = new PipedInputStream(1 << 16);
        PipedOutputStream channelOut = new PipedOutputStream(rawStdout);

        org.apache.sshd.client.channel.ChannelSession channel = session.createSubsystemChannel("sftp");
        channel.setIn(rawStdin);
        channel.setOut(channelOut);
        channel.setErr(new ByteArrayOutputStream());
        channel.open().verify(java.time.Duration.ofSeconds(10).toMillis());
        Protocol protocol = new Protocol(channelIn, rawStdout, channel);
        try (protocol) {
            Listing result = readDirectoryPage(protocol, directory, offset, limit);
            // Resolve symlink targets through the long-lived SFTP client, like the Go version.
            List<Entry> resolved = new ArrayList<>(result.entries().size());
            for (Entry entry : result.entries()) {
                if (entry.isSymlink()) {
                    try {
                        Entry target = store.metadata(entry.path());
                        resolved.add(new Entry(target.name(), target.path(), target.size(), target.modifiedAt(),
                            target.mode(), target.isDir(), target.isFile(), true));
                        continue;
                    } catch (IOException ignored) { }
                }
                resolved.add(entry);
            }
            return new Listing(result.path(), result.parent(), resolved, result.truncated(), result.nextCursor());
        }
    }

    private static Listing readDirectoryPage(Protocol protocol, String directory, int offset, int limit)
        throws IOException {
        byte[] init = protocol.send(SSH_FXP_INIT, new byte[0]);
        if (init[0] != SSH_FXP_VERSION) {
            throw new IOException("SFTP v3 required");
        }
        byte[] real = protocol.send(SSH_FXP_REALPATH, protocolString(directory));
        if (real[0] == SSH_FXP_STATUS) {
            IOException status = directoryStatus(java.util.Arrays.copyOfRange(real, 1, real.length));
            throw status == null ? new IOException("unexpected directory status") : status;
        }
        if (real[0] != SSH_FXP_NAME) {
            throw new IOException("invalid realpath response");
        }
        AttributeReader attrs = new AttributeReader(java.util.Arrays.copyOfRange(real, 1, real.length));
        if (attrs.u32() != 1 || attrs.err != null) {
            throw FileErrors.PATH;
        }
        String canonical = attrs.text();
        if (attrs.err != null) {
            throw attrs.err;
        }
        RemoteFiles.cleanRemotePath(canonical, false);

        byte[] opened = protocol.send(SSH_FXP_OPENDIR, protocolString(canonical));
        if (opened[0] == SSH_FXP_STATUS) {
            IOException status = directoryStatus(java.util.Arrays.copyOfRange(opened, 1, opened.length));
            throw status == null ? new IOException("unexpected directory status") : status;
        }
        if (opened[0] != SSH_FXP_HANDLE) {
            throw new IOException("invalid directory handle");
        }
        AttributeReader handleReader = new AttributeReader(java.util.Arrays.copyOfRange(opened, 1, opened.length));
        String handle = handleReader.text();
        if (handleReader.err != null) {
            throw handleReader.err;
        }
        try {
            List<Entry> entries = new ArrayList<>();
            int seen = 0;
            while (entries.size() <= limit) {
                byte[] batch = protocol.send(SSH_FXP_READDIR, protocolString(handle));
                if (batch[0] == SSH_FXP_STATUS) {
                    IOException status = directoryStatus(java.util.Arrays.copyOfRange(batch, 1, batch.length));
                    if (status != null && "EOF".equals(status.getMessage())) {
                        break;
                    }
                    throw status == null ? new IOException("unexpected directory status") : status;
                }
                if (batch[0] != SSH_FXP_NAME) {
                    throw new IOException("invalid directory entries");
                }
                AttributeReader batchReader = new AttributeReader(java.util.Arrays.copyOfRange(batch, 1, batch.length));
                long count = batchReader.u32();
                if (batchReader.err != null) {
                    throw batchReader.err;
                }
                if (count > 10000) {
                    throw new IOException("directory batch too large");
                }
                for (long i = 0; i < count; i++) {
                    Entry entry = batchReader.entry(canonical);
                    if (batchReader.err != null) {
                        throw batchReader.err;
                    }
                    if (entry.name().equals(".") || entry.name().equals("..")) {
                        continue;
                    }
                    if (entry.name().contains("/")) {
                        throw FileErrors.PATH;
                    }
                    if (seen >= offset) {
                        entries.add(entry);
                    }
                    seen++;
                    if (entries.size() > limit) {
                        break;
                    }
                }
            }
            String nextCursor = null;
            if (entries.size() > limit) {
                nextCursor = String.valueOf(offset + limit);
                entries = new ArrayList<>(entries.subList(0, limit));
            }
            RemoteFiles.sortEntries(entries);
            return new Listing(canonical, RemoteFiles.remoteFileParent(canonical), entries, false, nextCursor);
        } finally {
            try {
                protocol.send(SSH_FXP_CLOSE, protocolString(handle));
            } catch (IOException ignored) { }
        }
    }

}
