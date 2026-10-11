package com.servermonitor.files;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.EnumSet;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.Callable;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

import org.apache.sshd.client.session.ClientSession;
import org.apache.sshd.sftp.client.SftpClient;
import org.apache.sshd.sftp.client.SftpClientFactory;

import com.servermonitor.files.RemoteFiles.Entry;
import com.servermonitor.files.RemoteFiles.Listing;
import com.servermonitor.files.RemoteFiles.Managed;

/**
 * SFTP-backed remote files. Every blocking call runs behind the file-operation
 * deadline; on timeout the underlying SSH channel is closed so nothing stays
 * stuck against a wedged server.
 */
public class SftpFiles implements Managed {

    private final ClientSession session;
    private final SftpClient sftp;
    private final boolean windows;
    private final ExecutorService io;

    private SftpFiles(ClientSession session, SftpClient sftp, boolean windows) {
        this.session = session;
        this.sftp = sftp;
        this.windows = windows;
        this.io = Executors.newCachedThreadPool(r -> {
            Thread t = new Thread(r, "sftp-io");
            t.setDaemon(true);
            return t;
        });
    }

    public static SftpFiles open(ClientSession session, String serverType, Duration timeout)
        throws IOException {
        ExecutorService one = Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "sftp-open");
            t.setDaemon(true);
            return t;
        });
        SftpClient sftp = null;
        try {
            Future<SftpClient> future = one.submit(() -> SftpClientFactory.instance().createSftpClient(session));
            sftp = future.get(timeout.toMillis(), TimeUnit.MILLISECONDS);
            return new SftpFiles(session, sftp, "windows".equals(serverType));
        } catch (Exception e) {
            if (sftp != null) {
                try {
                    sftp.close();
                } catch (IOException ignored) { }
            }
            if (e instanceof IOException io) {
                throw io;
            }
            throw new IOException("open sftp: " + e.getMessage(), e);
        } finally {
            one.shutdownNow();
        }
    }

    /** Runs one blocking SFTP call behind the operation deadline. */
    private <T> T bounded(Duration timeout, Callable<T> call) throws IOException {
        Future<T> future = io.submit(call);
        try {
            return future.get(timeout.toMillis(), TimeUnit.MILLISECONDS);
        } catch (java.util.concurrent.TimeoutException e) {
            future.cancel(true);
            // A wedged SFTP channel never answers an interrupt; closing the
            // underlying session is what actually unblocks waiters.
            try {
                session.close(true);
            } catch (Exception ignored) { }
            throw FileErrors.TIMEOUT;
        } catch (java.util.concurrent.ExecutionException e) {
            Throwable cause = e.getCause() == null ? e : e.getCause();
            if (cause instanceof IOException io) {
                throw io;
            }
            throw new IOException(cause.toString(), cause);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw FileErrors.TIMEOUT;
        }
    }

    @Override
    public void close() {
        io.shutdownNow();
        try {
            sftp.close();
        } catch (IOException ignored) { }
    }

    private static boolean isSymlink(SftpClient.Attributes attributes) {
        return (attributes.getPermissions() & 0170000) == 0120000;
    }

    private static Instant modified(SftpClient.Attributes attributes) {
        return attributes.getModifyTime() == null ? Instant.EPOCH : attributes.getModifyTime().toInstant();
    }

    static String modeString(SftpClient.Attributes attributes) {
        return RemoteDirectoryPages.modeString(attributes.getPermissions(), isSymlink(attributes));
    }

    private static Entry entry(String directory, String name, SftpClient.Attributes attributes, boolean symlink) {
        return new Entry(name, join(directory, name),
            attributes.getSize(), modified(attributes), modeString(attributes),
            attributes.isDirectory(), attributes.isRegularFile(), symlink);
    }

    private static String join(String directory, String name) {
        return directory.endsWith("/") ? directory + name : directory + "/" + name;
    }

    @Override
    public Listing list(String directory) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        String canonical = bounded(timeout, () -> sftp.canonicalPath(directory));
        List<SftpClient.DirEntry> entries = bounded(timeout, () -> {
            List<SftpClient.DirEntry> all = new ArrayList<>();
            for (SftpClient.DirEntry dirEntry : sftp.readDir(canonical)) {
                all.add(dirEntry);
            }
            return all;
        });
        boolean truncated = entries.size() > RemoteFiles.MAX_DIRECTORY_ENTRIES;
        if (truncated) {
            entries = new ArrayList<>(entries.subList(0, RemoteFiles.MAX_DIRECTORY_ENTRIES));
        }
        List<Entry> out = new ArrayList<>();
        for (SftpClient.DirEntry dirEntry : entries) {
            SftpClient.Attributes attributes = dirEntry.getAttributes();
            String name = dirEntry.getFilename();
            boolean symlink = isSymlink(attributes);
            if (symlink) {
                try {
                    SftpClient.Attributes target = bounded(timeout, () -> sftp.stat(join(canonical, name)));
                    attributes = target;
                } catch (IOException ignored) { }
            }
            out.add(entry(canonical, name, attributes, symlink));
        }
        RemoteFiles.sortEntries(out);
        return new Listing(canonical, RemoteFiles.remoteFileParent(canonical), out, truncated, null);
    }

    @Override
    public void read(String filename, OutputStream output, long limit) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        SftpClient.Attributes info = bounded(timeout, () -> sftp.stat(filename));
        if (!info.isRegularFile()) {
            throw FileErrors.NOT_REGULAR;
        }
        if (info.getSize() > limit) {
            throw FileErrors.TOO_LARGE;
        }
        bounded(timeout, () -> {
            try (InputStream in = sftp.read(filename)) {
                byte[] buf = new byte[64 * 1024];
                long copied = 0;
                while (true) {
                    int read = in.read(buf);
                    if (read < 0) {
                        break;
                    }
                    if (read == 0) {
                        continue;
                    }
                    if (copied + read > limit) {
                        throw FileErrors.TOO_LARGE;
                    }
                    output.write(buf, 0, read);
                    copied += read;
                }
            }
            return null;
        });
    }

    @Override
    public void write(String filename, InputStream input, long size, boolean overwrite) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        SftpClient.Attributes existing = bounded(timeout, () -> {
            try {
                return sftp.lstat(filename);
            } catch (IOException e) {
                return null;
            }
        });
        if (existing != null && !overwrite) {
            throw FileErrors.EXISTS;
        }
        if (existing != null && (!existing.isRegularFile() || isSymlink(existing))) {
            throw FileErrors.NOT_REGULAR;
        }
        String temporary = RemoteFiles.dirOf(filename) + "/.server-monitor-" + UUID.randomUUID();
        try {
            bounded(timeout, () -> {
                try (OutputStream out = sftp.write(temporary,
                    EnumSet.of(SftpClient.OpenMode.Write, SftpClient.OpenMode.Create, SftpClient.OpenMode.Exclusive))) {
                    if (existing != null) {
                        if (!windows) {
                            // Preserve ownership where the server allows it.
                            try {
                                SftpClient.Attributes attrs = new SftpClient.Attributes();
                                attrs.owner(existing.getUserId(), existing.getGroupId());
                                sftp.setStat(temporary, attrs);
                            } catch (IOException ignored) { }
                        }
                        SftpClient.Attributes attrs = new SftpClient.Attributes();
                        attrs.setPermissions(existing.getPermissions() & 0777);
                        sftp.setStat(temporary, attrs);
                    } else {
                        SftpClient.Attributes attrs = new SftpClient.Attributes();
                        attrs.setPermissions(0644);
                        sftp.setStat(temporary, attrs);
                    }
                    byte[] buf = new byte[64 * 1024];
                    long copied = 0;
                    while (true) {
                        int read = input.read(buf);
                        if (read < 0) {
                            break;
                        }
                        if (read == 0) {
                            continue;
                        }
                        out.write(buf, 0, read);
                        copied += read;
                    }
                    if (copied != size) {
                        throw new IOException("unexpected EOF");
                    }
                }
                return null;
            });
            if (overwrite) {
                bounded(timeout, () -> {
                    sftp.rename(temporary, filename);
                    return null;
                });
            } else {
                try {
                    bounded(timeout, () -> {
                        sftp.rename(temporary, filename);
                        return null;
                    });
                } catch (IOException e) {
                    SftpClient.Attributes check = bounded(timeout, () -> {
                        try {
                            return sftp.lstat(filename);
                        } catch (IOException e2) {
                            return null;
                        }
                    });
                    if (check != null) {
                        throw FileErrors.EXISTS;
                    }
                    throw e;
                }
            }
        } finally {
            try {
                bounded(timeout, () -> {
                    try {
                        sftp.remove(temporary);
                    } catch (IOException ignored) { }
                    return null;
                });
            } catch (IOException ignored) { }
        }
    }

    @Override
    public Entry metadata(String filename) throws IOException {
        SftpClient.Attributes attributes = bounded(RemoteFiles.FILE_OPERATION_TIMEOUT,
            () -> sftp.lstat(filename));
        return entry(filename, RemoteFiles.baseOf(filename), attributes, isSymlink(attributes));
    }

    @Override
    public Listing page(String directory, int offset, int limit) throws IOException {
        return RemoteDirectoryPages.pageOverSftp(this, session, directory, offset, limit);
    }

    @Override
    public void mkdir(String directory, boolean privateDir) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        IOException mkdirError = null;
        try {
            bounded(timeout, () -> {
                sftp.mkdir(directory);
                return null;
            });
        } catch (IOException e) {
            mkdirError = e;
            SftpClient.Attributes existing = bounded(timeout, () -> {
                try {
                    return sftp.lstat(directory);
                } catch (IOException e2) {
                    return null;
                }
            });
            if (!privateDir && existing != null) {
                throw FileErrors.EXISTS;
            }
            if (!privateDir || existing == null || !existing.isDirectory() || isSymlink(existing)) {
                throw mkdirError;
            }
        }
        if (privateDir) {
            bounded(timeout, () -> {
                SftpClient.Attributes attrs = new SftpClient.Attributes();
                attrs.setPermissions(0700);
                sftp.setStat(directory, attrs);
                return null;
            });
        }
    }

    @Override
    public void rename(String source, String destination) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        SftpClient.Attributes existing = bounded(timeout, () -> {
            try {
                return sftp.lstat(destination);
            } catch (IOException e) {
                return null;
            }
        });
        if (existing != null) {
            throw FileErrors.EXISTS;
        }
        bounded(timeout, () -> {
            sftp.rename(source, destination);
            return null;
        });
    }

    @Override
    public void remove(String filename) throws IOException {
        Duration timeout = RemoteFiles.FILE_OPERATION_TIMEOUT;
        SftpClient.Attributes info = bounded(timeout, () -> sftp.lstat(filename));
        if (info.isDirectory()) {
            bounded(timeout, () -> {
                sftp.rmdir(filename);
                return null;
            });
        } else {
            bounded(timeout, () -> {
                sftp.remove(filename);
                return null;
            });
        }
    }
}
