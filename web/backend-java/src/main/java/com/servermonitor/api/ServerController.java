package com.servermonitor.api;

import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.dao.EmptyResultDataAccessException;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Credentials;
import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSummary;
import com.servermonitor.service.Collector;
import com.servermonitor.ssh.SshConnectionCache;

import jakarta.servlet.http.HttpServletRequest;

/** /api/servers CRUD, tags, and the manual poll retry. */
@RestController
@RequestMapping("/api/servers")
public class ServerController {

    private static final Logger log = LoggerFactory.getLogger(ServerController.class);

    private final Servers servers;
    private final Credentials credentials;
    private final SshConnectionCache sshCache;
    private final Collector collector;

    public ServerController(Servers servers, Credentials credentials,
        SshConnectionCache sshCache, Collector collector) {
        this.servers = servers;
        this.credentials = credentials;
        this.sshCache = sshCache;
        this.collector = collector;
    }

    /** Request body for create/update; null fields fall back to the Go handler's defaults. */
    public record ServerBody(String name, String host, Integer port, String ssh_username,
        String ssh_password, String ssh_key, String ssh_host_key, UUID credential_id,
        String server_type, String expires_at, String notes, Double billing_price,
        String billing_currency, String billing_cycle, Long traffic_limit_bytes,
        String public_location) {
    }

    @GetMapping
    public List<Map<String, Object>> list(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        List<ServerSummary> list = servers.getServersByUserId(userId);
        List<UUID> ids = new ArrayList<>();
        for (ServerSummary s : list) {
            ids.add(s.id);
        }
        try {
            var tagsByServer = servers.getTagsForServers(ids);
            for (ServerSummary s : list) {
                var tags = tagsByServer.get(s.id);
                if (tags != null && !tags.isEmpty()) {
                    s.tags = tags;
                }
            }
        } catch (Exception e) {
            log.warn("list server tags: {}", e.toString());
        }
        return list.stream().map(Jsons::serverSummary).toList();
    }

    @GetMapping("/{id}")
    public Map<String, Object> get(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        ServerSummary s = servers.getServerSummary(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
        var tags = servers.getServerTags(s.id);
        if (tags != null) {
            s.tags = tags;
        }
        return Jsons.serverSummary(s);
    }

    @PostMapping
    public ResponseEntity<Map<String, Object>> create(@RequestBody ServerBody body, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        Servers.ServerUpsert in = normalize(body, null, userId, true);
        ServerSummary created = servers.createServer(UUID.randomUUID(), in);
        return ResponseEntity.status(HttpStatus.CREATED).body(Jsons.serverSummary(created));
    }

    @PutMapping("/{id}")
    public Map<String, Object> update(@PathVariable String id, @RequestBody ServerBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        Servers.ServerUpsert in = normalize(body, serverId, userId, false);
        boolean updated = servers.updateServer(serverId, userId, in);
        if (!updated) {
            throw ApiException.notFound("server not found");
        }
        ServerSummary s = servers.getServerSummary(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
        return Jsons.serverSummary(s);
    }

    @DeleteMapping("/{id}")
    public Map<String, Object> delete(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        servers.deleteServer(serverId, userId);
        sshCache.drop(serverId);
        return Map.of("message", "deleted");
    }

    public record SetTagsBody(List<UUID> tag_ids) {
    }

    @PutMapping("/{id}/tags")
    public Map<String, Object> setTags(@PathVariable String id, @RequestBody SetTagsBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        if (body == null || body.tag_ids() == null) {
            throw ApiException.badRequest("tag_ids is required");
        }
        boolean ok = servers.setServerTags(serverId, userId, body.tag_ids());
        if (!ok) {
            throw ApiException.notFound("server not found");
        }
        return Map.of("message", "tags updated");
    }

    /** Retry a failed host immediately instead of waiting out the collector's backoff. */
    @PostMapping("/{id}/poll")
    public ResponseEntity<Map<String, Object>> pollRetry(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        if (!servers.serverOwnedByUser(serverId, userId)) {
            throw ApiException.notFound("server not found");
        }
        var server = servers.getServerByIdAndUser(serverId, userId)
            .orElseThrow(() -> ApiException.of(500, "failed to load server"));
        try {
            collector.pollNow(server);
            return ResponseEntity.ok(Map.of("ok", true));
        } catch (Collector.PollNowException e) {
            return switch (e.getResult()) {
                case IN_FLIGHT, BUSY -> ResponseEntity.status(HttpStatus.CONFLICT)
                    .body(Map.of("error", e.getMessage()));
                case STOPPED -> ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                    .body(Map.of("error", "collector unavailable"));
                default -> ResponseEntity.ok(Map.of("ok", false,
                    "kind", e.getKind().wire(), "error", e.getDetail()));
            };
        }
    }

    // --- validation (mirrors normalizeServerConnection / normalizeServerType) ---

    private Servers.ServerUpsert normalize(ServerBody body, UUID serverId, UUID userId, boolean creating) {
        if (body == null) {
            throw ApiException.badRequest("request body is required");
        }
        String name = body.name() == null ? "" : body.name().trim();
        String host = body.host() == null ? "" : body.host().trim();
        String username = body.ssh_username() == null ? "" : body.ssh_username().trim();
        if (name.isEmpty()) {
            throw ApiException.badRequest("server name is required");
        }
        if (host.isEmpty()) {
            throw ApiException.badRequest("host is required");
        }
        if (host.startsWith("[") || host.endsWith("]")) {
            if (host.length() <= 2 || !host.startsWith("[") || !host.endsWith("]")) {
                throw ApiException.badRequest("host has invalid brackets");
            }
            String candidate = host.substring(1, host.length() - 1);
            if (!isIp(candidate)) {
                throw ApiException.badRequest("brackets are only valid around an IPv6 address");
            }
            host = candidate;
        }
        if (host.contains("://")) {
            throw ApiException.badRequest("host must not include a URL scheme");
        }
        if (host.chars().anyMatch(c -> Character.isWhitespace(c) || Character.isISOControl(c))) {
            throw ApiException.badRequest("host contains whitespace or invisible characters");
        }
        if (!isIp(host) && host.contains(":")) {
            throw ApiException.badRequest("host must not include a port");
        }
        if (host.length() > 255) {
            throw ApiException.badRequest("host is too long");
        }
        if (username.isEmpty()) {
            username = "root";
        }
        if (username.chars().anyMatch(c -> Character.isWhitespace(c) || Character.isISOControl(c))) {
            throw ApiException.badRequest("SSH username contains whitespace or control characters");
        }
        int port = body.port() == null ? 0 : body.port();
        if (port == 0) {
            port = 22;
        }
        if (port < 1 || port > 65535) {
            throw ApiException.badRequest("SSH port must be between 1 and 65535");
        }
        String serverType = body.server_type() == null ? "" : body.server_type().trim().toLowerCase();
        if (serverType.isEmpty()) {
            serverType = "linux";
        }
        if (!serverType.equals("linux") && !serverType.equals("windows")) {
            throw ApiException.badRequest("server type must be linux or windows");
        }
        String sshKey = body.ssh_key() == null ? "" : body.ssh_key();
        String sshHostKey = body.ssh_host_key() == null ? "" : body.ssh_host_key();
        if ("windows".equals(serverType)) {
            sshKey = "";
            sshHostKey = "";
        }
        String sshPassword = body.ssh_password() == null ? "" : body.ssh_password();
        String billingCurrency = body.billing_currency() == null || body.billing_currency().isEmpty()
            ? "CNY" : body.billing_currency();
        String billingCycle = body.billing_cycle() == null || body.billing_cycle().isEmpty()
            ? "year" : body.billing_cycle();
        if (body.credential_id() != null) {
            var cred = credentials.getCredentialSummary(body.credential_id(), userId)
                .orElseThrow(() -> ApiException.badRequest("invalid credential"));
            if (!cred.credentialType().equals(serverType)) {
                throw ApiException.badRequest("invalid credential");
            }
        } else if (creating && "windows".equals(serverType) && sshPassword.trim().isEmpty()) {
            throw ApiException.badRequest("Windows password is required");
        } else if (creating && sshPassword.isEmpty() && sshKey.trim().isEmpty()) {
            throw ApiException.badRequest("SSH password or key is required");
        } else if (!creating && sshPassword.isEmpty() && sshKey.trim().isEmpty()) {
            // Updating with no inline secrets is only fine when the stored row
            // still has some of its own.
            boolean hasDirectAuth;
            try {
                hasDirectAuth = servers.serverHasDirectSSHAuth(serverId, userId);
            } catch (EmptyResultDataAccessException e) {
                throw ApiException.notFound("server not found");
            }
            if (!hasDirectAuth) {
                throw ApiException.badRequest("SSH password or key is required when removing a credential");
            }
        }
        Instant expiresAt = null;
        if (body.expires_at() != null && !body.expires_at().isBlank()) {
            try {
                expiresAt = Instant.parse(body.expires_at().trim());
            } catch (DateTimeParseException e) {
                throw ApiException.badRequest("expires_at must be RFC 3339");
            }
        }
        return new Servers.ServerUpsert(userId, name, host, port, username, sshPassword, sshKey,
            sshHostKey, body.credential_id(), serverType, expiresAt,
            body.notes() == null ? "" : body.notes(),
            body.billing_price() == null ? 0 : body.billing_price(),
            billingCurrency, billingCycle,
            body.traffic_limit_bytes() == null ? 0 : body.traffic_limit_bytes(),
            body.public_location() == null ? "" : body.public_location());
    }

    private static boolean isIp(String value) {
        return value.matches("[0-9]+(?:\\.[0-9]+){3}") || value.contains(":") && value.matches("[0-9a-fA-F:]+");
    }
}
