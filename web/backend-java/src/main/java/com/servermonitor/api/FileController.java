package com.servermonitor.api;

import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.Semaphore;

import org.apache.sshd.client.session.ClientSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.multipart.MultipartFile;
import org.springframework.web.servlet.mvc.method.annotation.StreamingResponseBody;

import com.servermonitor.files.ContainerFiles;
import com.servermonitor.files.FileErrors;
import com.servermonitor.files.RemoteFiles;
import com.servermonitor.files.RemoteFiles.Entry;
import com.servermonitor.files.RemoteFiles.Listing;
import com.servermonitor.files.RemoteFiles.Managed;
import com.servermonitor.files.RemoteFiles.Store;
import com.servermonitor.files.SftpFiles;
import com.servermonitor.repo.FileOperations;
import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.ssh.SshConnectionCache;

import jakarta.servlet.http.HttpServletRequest;

/**
 * /api/servers/{id}/files/* — browsing, UTF-8 text edit, upload/download, and
 * the mutation audit trail. Error mapping mirrors the Go fileError switch.
 */
@RestController
@RequestMapping("/api/servers/{id}/files")
public class FileController {

    private static final Logger log = LoggerFactory.getLogger(FileController.class);

    /** Bounded concurrency for remote file operations. */
    private final Semaphore slots = new Semaphore(8);
    /** Per-file-fingerprint write locks, like the Go [64]sync.Mutex array. */
    private final java.util.concurrent.locks.ReentrantLock[] writes = new java.util.concurrent.locks.ReentrantLock[64];

    private final Servers servers;
    private final SshConnectionCache sshCache;
    private final FileOperations audits;

    public FileController(Servers servers, SshConnectionCache sshCache, FileOperations audits) {
        this.servers = servers;
        this.sshCache = sshCache;
        this.audits = audits;
        for (int i = 0; i < writes.length; i++) {
            writes[i] = new java.util.concurrent.locks.ReentrantLock();
        }
    }

    // --- plumbing -----------------------------------------------------------

    private record Connection(Store store, java.util.concurrent.locks.ReentrantLock writeLock,
        UUID auditId, String auditPath, UUID serverId, String containerId)
        implements AutoCloseable {
        @Override
        public void close() throws IOException {
            store.close();
        }
    }

    /** Resolves the store or writes the error response. Every successful path acquires a slot. */
    private Connection connect(String id, String container, HttpServletRequest req,
        String auditAction, String auditPath) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        String containerId = container == null ? "" : container;
        if (!containerId.isEmpty() && !ContainerFiles.validContainerId(containerId)) {
            throwApi(FileErrors.PATH, null);
        }
        ServerSecrets server = servers.getServerByIdAndUser(serverId, userId)
            .orElseThrow(() -> ApiException.coded(404, "server_not_found", "server not found"));
        try {
            if (!slots.tryAcquire(RemoteFiles.FILE_OPERATION_TIMEOUT.toMillis(),
                java.util.concurrent.TimeUnit.MILLISECONDS)) {
                throwApi(FileErrors.TIMEOUT, null);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throwApi(FileErrors.TIMEOUT, null);
        }
        UUID auditId = null;
        if (auditAction != null) {
            try {
                auditId = UUID.randomUUID();
                audits.insert(auditId, userId, serverId, containerId, auditAction, auditPath);
            } catch (Exception e) {
                slots.release();
                throw ApiException.coded(503, "audit_unavailable",
                    "Audit storage unavailable; operation canceled");
            }
        }
        ClientSession client;
        try {
            client = sshCache.get(server);
        } catch (Exception e) {
            completeAudit(auditId, null, null, null, "failed", "remote_failed");
            slots.release();
            throwApi(new IOException("SSH connection failed", e), serverId.toString());
            throw new IllegalStateException(); // unreachable
        }
        try {
            Store store = containerId.isEmpty()
                ? SftpFiles.open(client, server.serverType, RemoteFiles.FILE_OPERATION_TIMEOUT)
                : ContainerFiles.open(client, containerId, server.serverType, RemoteFiles.FILE_OPERATION_TIMEOUT);
            return new Connection(store, null, auditId, auditPath, serverId, containerId);
        } catch (IOException e) {
            completeAudit(auditId, null, null, null, "failed", codeFor(e));
            slots.release();
            throwApi(e, serverId.toString());
            throw new IllegalStateException(); // unreachable
        }
    }

    private void release(Connection connection) {
        try {
            connection.close();
        } catch (IOException ignored) { }
        slots.release();
    }

    private java.util.concurrent.locks.ReentrantLock writeLock(String id, String container, String filename) {
        long hash = 2166136261L;
        String material = id + ":" + (container == null ? "" : container) + ":" + filename;
        for (byte b : material.getBytes(StandardCharsets.UTF_8)) {
            hash ^= b & 0xff;
            hash = (hash * 16777619) & 0xffff_ffffL;
        }
        return writes[(int) (hash % writes.length)];
    }

    private void completeAudit(Connection connection, String action, String target, String backup,
        String outcome, String code) {
        completeAudit(connection == null ? null : connection.auditId(), action, target, backup,
            outcome, code);
    }

    private void completeAudit(UUID auditId, String action, String target, String backup,
        String outcome, String code) {
        if (auditId == null) {
            return;
        }
        try {
            audits.complete(auditId, action == null ? "" : action, target == null ? "" : target,
                backup == null ? "" : backup, outcome, code == null ? "" : code);
        } catch (Exception e) {
            log.warn("file audit completion failed id={}: {}", auditId, e.toString());
        }
    }

    // --- error mapping (mirrors the Go fileError switch) -------------------

    private void throwApi(IOException err, String serverId) {
        int status = 502;
        String code = "remote_failed";
        String message = "File operation failed; check SSH/SFTP access, permissions and container tools";
        if (err == FileErrors.PATH) {
            status = 400;
            code = "invalid_path";
            message = err.getMessage();
        } else if (err == FileErrors.TOO_LARGE || err instanceof ContainerFiles.FileTooLargeSignal) {
            status = 413;
            code = "file_too_large";
            message = "file exceeds the size limit";
        } else if (err == FileErrors.NOT_TEXT) {
            status = 415;
            code = "not_text";
            message = err.getMessage();
        } else if (err == FileErrors.EXISTS) {
            status = 409;
            code = "file_exists";
            message = err.getMessage();
        } else if (err == FileErrors.CHANGED) {
            status = 409;
            code = "file_changed";
            message = err.getMessage();
        } else if (err == FileErrors.NOT_REGULAR) {
            status = 400;
            code = "not_regular";
            message = err.getMessage();
        } else if (err == FileErrors.UNSUPPORTED) {
            status = 422;
            code = "unsupported";
            message = err.getMessage();
        } else if (FileErrors.isNotExist(err)) {
            status = 404;
            code = "not_found";
            message = "File or directory not found";
        } else if (FileErrors.isPermission(err)) {
            status = 403;
            code = "permission_denied";
            message = "Remote file permission denied";
        } else if (err == FileErrors.TIMEOUT || FileErrors.isTimeout(err)) {
            status = 504;
            code = "timeout";
            message = "File operation timed out or was canceled";
        }
        if (status == 502) {
            log.warn("file operation server={}: {}", serverId, err.toString());
        }
        throw ApiException.coded(status, code, message);
    }

    private static String codeFor(IOException err) {
        if (err == FileErrors.PATH) {
            return "invalid_path";
        }
        if (err == FileErrors.TOO_LARGE) {
            return "file_too_large";
        }
        if (err == FileErrors.NOT_TEXT) {
            return "not_text";
        }
        if (err == FileErrors.EXISTS) {
            return "file_exists";
        }
        if (err == FileErrors.CHANGED) {
            return "file_changed";
        }
        if (err == FileErrors.NOT_REGULAR) {
            return "not_regular";
        }
        if (err == FileErrors.UNSUPPORTED) {
            return "unsupported";
        }
        if (FileErrors.isNotExist(err)) {
            return "not_found";
        }
        if (FileErrors.isPermission(err)) {
            return "permission_denied";
        }
        if (err == FileErrors.TIMEOUT || FileErrors.isTimeout(err)) {
            return "timeout";
        }
        return "remote_failed";
    }

    private Connection open(String id, String container, HttpServletRequest req,
        String auditAction, String auditPath, String filename, boolean wantManaged) {
        Connection connection = connect(id, container, req, auditAction, auditPath);
        if (wantManaged && !(connection.store() instanceof Managed)) {
            IOException err = FileErrors.UNSUPPORTED;
            release(connection);
            completeAudit(connection, null, null, null, "failed", "unsupported");
            throwApi(err, id);
        }
        return connection;
    }

    /** Runs op against the store, translating every IOException onto the API surface. */
    private interface StoreOp<T> {
        T run(Store store, Connection connection) throws IOException;
    }

    private <T> T invoke(String id, String filename, Connection connection, StoreOp<T> op) {
        return invokeWithAudit(id, filename, connection, null, null, op);
    }

    private <T> T invokeWithAudit(String id, String filename, Connection connection,
        Object auditExtra, String auditTarget, StoreOp<T> op) {
        String auditAction = auditExtra instanceof String s ? s : null;
        String backup = auditExtra instanceof java.util.concurrent.atomic.AtomicReference<?> r
            ? String.valueOf(((java.util.concurrent.atomic.AtomicReference<?>) r).get()) : "";
        try {
            T value = op.run(connection.store(), connection);
            backup = auditExtra instanceof java.util.concurrent.atomic.AtomicReference<?> r
                ? String.valueOf(((java.util.concurrent.atomic.AtomicReference<?>) r).get()) : "";
            completeAudit(connection, auditAction, auditTarget, "null".equals(backup) ? "" : backup, "success", null);
            return value;
        } catch (IOException e) {
            releaseQuietly(connection);
            completeAudit(connection, auditAction, auditTarget, null, "failed", codeFor(e));
            throwApi(e, id);
            throw new IllegalStateException();
        }
    }

    private void releaseQuietly(Connection connection) {
        try {
            connection.close();
        } catch (IOException ignored) { }
        slots.release();
    }

    // --- endpoints ----------------------------------------------------------

    @GetMapping
    public Map<String, Object> list(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container,
        @RequestParam(required = false, defaultValue = "0") String cursor,
        @RequestParam(required = false, defaultValue = "100") String limit,
        HttpServletRequest req) {
        String directory = parsePath(path, true, id);
        int offset;
        int pageLimit;
        try {
            offset = Integer.parseInt(cursor);
            pageLimit = Integer.parseInt(limit);
        } catch (NumberFormatException e) {
            throwApi(FileErrors.PATH, id);
            throw new IllegalStateException();
        }
        if (offset < 0 || offset > 1000000 || pageLimit < 1 || pageLimit > 200) {
            throwApi(FileErrors.PATH, id);
        }
        Connection connection = open(id, container, req, null, directory, directory, true);
        try {
            Listing listing = invoke(id, directory, connection,
                (store, conn) -> ((Managed) store).page(directory, offset, pageLimit));
            return listingView(listing);
        } finally {
            release(connection);
        }
    }

    @GetMapping("/text")
    public Map<String, Object> readText(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container, HttpServletRequest req) {
        String filename = parsePath(path, false, id);
        Connection connection = open(id, container, req, null, filename, filename, false);
        try {
            byte[] content = invoke(id, filename, connection,
                (store, conn) -> RemoteFiles.readRemoteText(store, filename));
            return Map.of("path", filename, "content", new String(content, StandardCharsets.UTF_8),
                "revision", RemoteFiles.fileRevision(content));
        } finally {
            release(connection);
        }
    }

    public record SaveTextBody(String content, String revision) {
    }

    @PutMapping("/text")
    public Map<String, Object> saveText(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container,
        @RequestBody(required = false) SaveTextBody body, HttpServletRequest req) {
        String filename = parsePath(path, false, id);
        if (body == null || body.content() == null || body.revision() == null) {
            throw ApiException.coded(400, "invalid_request", "invalid text request");
        }
        if (body.content().getBytes(StandardCharsets.UTF_8).length > RemoteFiles.MAX_TEXT_FILE_SIZE) {
            throwApi(FileErrors.TOO_LARGE, id);
        }
        if (body.revision().length() != 64) {
            throwApi(FileErrors.CHANGED, id);
        }
        Connection connection = open(id, container, req, "PUT text", filename, filename, false);
        var lock = writeLock(id, container, filename);
        java.util.concurrent.atomic.AtomicReference<String> lastBackup = new java.util.concurrent.atomic.AtomicReference<>("");
        lock.lock();
        try {
            return invokeWithAudit(id, filename, connection, lastBackup, null, (store, conn) -> {
                byte[] current = RemoteFiles.readRemoteText(store, filename);
                if (!RemoteFiles.fileRevision(current).equals(body.revision())) {
                    throw FileErrors.CHANGED;
                }
                String backup = RemoteFiles.backupRemoteText(store, filename, current);
                RemoteFiles.saveRemoteText(store, filename, body.content(), body.revision());
                lastBackup.set(backup);
                return Map.of(
                    "revision",
                    RemoteFiles.fileRevision(body.content().getBytes(StandardCharsets.UTF_8)),
                    "backup_path", backup);
            });
        } finally {
            lock.unlock();
            release(connection);
        }
    }

    @GetMapping("/download")
    public ResponseEntity<StreamingResponseBody> download(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container, HttpServletRequest req) {
        String filename = parsePath(path, false, id);
        Connection connection = open(id, container, req, null, filename, filename, false);
        String base = filename.substring(filename.lastIndexOf('/') + 1)
            .replaceAll("\\p{Cntrl}", "");
        StreamingResponseBody body = output -> {
            try {
                connection.store().read(filename, new LimitedDownload(output, connection, id),
                    RemoteFiles.MAX_DOWNLOAD_FILE_SIZE);
            } catch (IOException e) {
                log.warn("download {} on {}: {}", filename, id, e.toString());
                throw e;
            } finally {
                release(connection);
            }
        };
        return ResponseEntity.ok()
            .header(HttpHeaders.CONTENT_DISPOSITION, "attachment; filename*=UTF-8''"
                + URLEncoder.encode(base, StandardCharsets.UTF_8).replace("+", "%20"))
            .header("X-Content-Type-Options", "nosniff")
            .contentType(MediaType.APPLICATION_OCTET_STREAM)
            .body(body);
    }

    /** Fails the response as file_too_large instead of delivering a silently truncated file. */
    private class LimitedDownload extends OutputStream {
        private final OutputStream out;
        private final Connection connection;
        private final String serverId;

        LimitedDownload(OutputStream out, Connection connection, String serverId) {
            this.out = out;
            this.connection = connection;
            this.serverId = serverId;
        }

        @Override
        public void write(int b) throws IOException {
            out.write(b);
        }

        @Override
        public void write(byte[] b, int off, int len) throws IOException {
            out.write(b, off, len);
        }

        @Override
        public void close() throws IOException {
            try {
                out.close();
            } finally {
                completeAudit(connection != null ? connection.auditId() : null, null, null, null, "success", null);
            }
        }
    }

    @PostMapping("/upload")
    public Map<String, Object> upload(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container,
        @RequestParam(required = false, defaultValue = "") String overwrite,
        @RequestParam(required = false, defaultValue = "") String version,
        @RequestParam("file") MultipartFile file, HttpServletRequest req) {
        String directory = parsePath(path, false, id);
        String name = file.getOriginalFilename() == null ? "" : file.getOriginalFilename();
        if (!RemoteFiles.validUploadName(name)) {
            throwApi(FileErrors.PATH, id);
        }
        if (file.getSize() > RemoteFiles.MAX_UPLOAD_FILE_SIZE) {
            throwApi(FileErrors.TOO_LARGE, id);
        }
        if (version.isEmpty()) {
            Connection rejected = null;
            try {
                rejected = connect(id, container, req, "POST upload", directory);
            } finally {
                if (rejected != null) {
                    release(rejected);
                    completeAudit(rejected, null, null, null, "failed", "file_changed");
                }
            }
            throwApi(FileErrors.CHANGED, id);
        }
        String filename = directory.endsWith("/") ? directory + name : directory + "/" + name;
        Connection connection = open(id, container, req, "POST upload", directory, filename, true);
        var lock = writeLock(id, container, filename);
        lock.lock();
        try (InputStream input = file.getInputStream()) {
            final ContainerFiles unused = null;
            Map<String, Object> result = invokeWithAudit(id, filename, connection, null, filename,
                (store, conn) -> {
                    expectedUpload((Managed) store, filename, version);
                    store.write(filename, input, file.getSize(), "1".equals(overwrite));
                    return Map.<String, Object>of("path", filename, "size", file.getSize());
                });
            return result;
        } catch (IOException e) {
            release(connection);
            completeAudit(connection, null, filename, null, "failed", "remote_failed");
            throwApi(e, id);
            throw new IllegalStateException();
        } finally {
            lock.unlock();
        }
    }

    @GetMapping("/metadata")
    public Map<String, Object> metadata(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container, HttpServletRequest req) {
        String filename = parsePath(path, false, id);
        Connection connection = open(id, container, req, null, filename, filename, true);
        try {
            Entry entry;
            try {
                entry = ((Managed) connection.store()).metadata(filename);
            } catch (IOException e) {
                if (FileErrors.isNotExist(e)) {
                    completeAudit(connection, null, null, null, "success", null);
                    return Map.of("exists", false, "version", "missing");
                }
                releaseQuietly(connection);
                completeAudit(connection, null, null, null, "failed", codeFor(e));
                throwApi(e, id);
                throw new IllegalStateException();
            }
            completeAudit(connection, null, null, null, "success", null);
            return Map.of("exists", true, "entry", entryView(entry), "version",
                RemoteFiles.fileMetadataVersion(entry));
        } finally {
            release(connection);
        }
    }

    public record ChangeBody(String action, String kind, String name, String version) {
    }

    @PostMapping("/change")
    public Map<String, Object> change(@PathVariable String id,
        @RequestParam(required = false, defaultValue = "") String path,
        @RequestParam(required = false) String container,
        @RequestBody(required = false) ChangeBody body, HttpServletRequest req) {
        String filename = parsePath(path, false, id);
        String parent = RemoteFiles.dirOf(filename);
        String base = filename.substring(filename.lastIndexOf('/') + 1);
        if (base.isEmpty() || ".".equals(base) || safeParent(filename).equals(filename)) {
            throwApi(FileErrors.PATH, id);
        }
        if (body == null || body.action() == null) {
            throwApi(FileErrors.PATH, id);
        }
        if (!body.action().equals("create") && !body.action().equals("rename") && !body.action().equals("delete")) {
            throwApi(FileErrors.PATH, id);
        }
        if (body.action().equals("create") && !"file".equals(body.kind()) && !"directory".equals(body.kind())) {
            throwApi(FileErrors.PATH, id);
        }
        Connection connection = open(id, container, req, "POST change", filename, filename, true);
        var lock = writeLock(id, container, filename);
        lock.lock();
        try {
            String finalTarget = "";
            Map<String, Object> result = invokeWithAudit(id, filename, connection, body.action(), null,
                (store, conn) -> {
                    Managed managed = (Managed) store;
                    String destination = "";
                    if (!"create".equals(body.action())) {
                        try {
                            Entry entry = managed.metadata(filename);
                            if (body.version() == null || body.version().isEmpty()
                                || !RemoteFiles.fileMetadataVersion(entry).equals(body.version())) {
                                throw FileErrors.CHANGED;
                            }
                        } catch (IOException e) {
                            if (FileErrors.isNotExist(e)) {
                                throw FileErrors.CHANGED;
                            }
                            throw e;
                        }
                    }
                    switch (body.action()) {
                        case "create" -> {
                            if ("directory".equals(body.kind())) {
                                managed.mkdir(filename, false);
                            } else {
                                managed.write(filename, InputStream.nullInputStream(), 0, false);
                            }
                        }
                        case "rename" -> {
                            if (!RemoteFiles.validUploadName(body.name())) {
                                throw FileErrors.PATH;
                            }
                            String dest = parent.equals("/") ? "/" + body.name() : parent + "/" + body.name();
                            managed.rename(filename, dest);
                            completeAudit(conn, "rename", dest, null, null, null);
                            return Map.of("path", filename, "target", dest);
                        }
                        case "delete" -> managed.remove(filename);
                    }
                    return Map.of("path", filename, "target", destination);
                });
            return result;
        } finally {
            lock.unlock();
            release(connection);
        }
    }

    /** servicesRemoteParent's rule for recognizing an un-renameable root. */
    private static String safeParent(String filename) {
        String parent = RemoteFiles.dirOf(filename);
        if (filename.length() == 3 && filename.endsWith(":/")) {
            return filename;
        }
        if (filename.length() == 4 && filename.charAt(0) == '/' && filename.endsWith(":/")) {
            return filename;
        }
        return parent;
    }

    @GetMapping("/audit")
    public List<Map<String, Object>> history(@PathVariable String id,
        @RequestParam(required = false) String container, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        if (!servers.serverOwnedByUser(serverId, userId)) {
            throw ApiException.notFound("server not found");
        }
        var entries = audits.history(userId, serverId, container == null ? "" : container);
        List<Map<String, Object>> out = new ArrayList<>();
        for (var e : entries) {
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("id", e.id());
            row.put("action", e.action());
            row.put("path", e.path());
            row.put("target", e.target());
            row.put("backup_path", e.backupPath());
            row.put("outcome", e.outcome());
            row.put("error_code", e.errorCode());
            row.put("created_at", Jsons.timestamp(e.createdAt()));
            out.add(row);
        }
        return out;
    }

    // --- helpers -------------------------------------------------------------

    private String parsePath(String raw, boolean allowHome, String id) {
        try {
            return RemoteFiles.cleanRemotePath(raw, allowHome);
        } catch (IOException e) {
            throwApi(FileErrors.PATH, id);
            throw new IllegalStateException();
        }
    }

    private static Map<String, Object> listingView(Listing listing) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("path", listing.path());
        out.put("parent", listing.parent());
        List<Map<String, Object>> entries = new ArrayList<>();
        for (Entry e : listing.entries()) {
            entries.add(entryView(e));
        }
        out.put("entries", entries);
        out.put("truncated", listing.truncated());
        if (listing.nextCursor() != null) {
            out.put("next_cursor", listing.nextCursor());
        }
        return out;
    }

    private static Map<String, Object> entryView(Entry e) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("name", e.name());
        out.put("path", e.path());
        out.put("size", e.size());
        out.put("modified_at", Jsons.timestamp(e.modifiedAt()));
        out.put("mode", e.mode());
        out.put("is_dir", e.isDir());
        out.put("is_file", e.isFile());
        out.put("is_symlink", e.isSymlink());
        return out;
    }

    /** Mirrors handlers/file_operations.go's expectedUpload. */
    private static void expectedUpload(Managed store, String filename, String version) throws IOException {
        Entry entry;
        try {
            entry = store.metadata(filename);
        } catch (IOException e) {
            if (FileErrors.isNotExist(e)) {
                if ("missing".equals(version)) {
                    return;
                }
                throw FileErrors.CHANGED;
            }
            throw e;
        }
        if ("missing".equals(version)) {
            throw FileErrors.EXISTS;
        }
        if (!RemoteFiles.fileMetadataVersion(entry).equals(version)) {
            throw FileErrors.CHANGED;
        }
        if (!entry.isFile() || entry.isSymlink()) {
            throw FileErrors.NOT_REGULAR;
        }
    }
}
