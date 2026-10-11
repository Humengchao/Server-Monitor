package com.servermonitor.api;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.regex.Pattern;

import org.apache.sshd.client.session.ClientSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.service.Collector;
import com.servermonitor.service.DockerService;
import com.servermonitor.service.RequestCache;
import com.servermonitor.ssh.SshConnectionCache;

import jakarta.servlet.http.HttpServletRequest;

/** /api/servers/{id}/docker/* — cached container lists, actions, logs, and the install probe. */
@RestController
@RequestMapping("/api/servers")
public class DockerController {

    private static final Logger log = LoggerFactory.getLogger(DockerController.class);

    /** Docker container IDs are hex, 12-64 chars. */
    private static final Pattern VALID_DOCKER_ID = Pattern.compile("^[a-fA-F0-9]{1,64}$");

    private final Servers servers;
    private final SshConnectionCache sshCache;
    private final DockerService docker;
    private final RequestCache<List<DockerService.Container>> results = new RequestCache<>(4);
    private final RequestCache<List<DockerService.Container>> statistics = new RequestCache<>(4);

    public DockerController(Servers servers, SshConnectionCache sshCache, DockerService docker) {
        this.servers = servers;
        this.sshCache = sshCache;
        this.docker = docker;
    }

    private record Resolved(ServerSecrets server, ClientSession client) {
    }

    private Resolved resolve(String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        ServerSecrets server = servers.getServerByIdAndUser(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
        ClientSession client;
        try {
            client = sshCache.get(server);
        } catch (Exception e) {
            throw ApiException.of(502, "SSH connection failed");
        }
        return new Resolved(server, client);
    }

    @GetMapping("/{id}/docker/check")
    public Map<String, Object> checkDocker(@PathVariable String id,
        @RequestParam(required = false) String refresh, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        var info = servers.getServerDockerInfo(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
        boolean installed = info.hasDocker();
        String version = info.version();

        if ("1".equals(refresh)) {
            // Ask the host directly and update the stored flag — the recovery
            // path for a server whose flag was lost to a transient probe failure.
            ServerSecrets server = servers.getServerByIdAndUser(serverId, userId)
                .orElseThrow(() -> ApiException.notFound("server not found"));
            ClientSession client;
            try {
                client = sshCache.get(server);
            } catch (Exception e) {
                throw ApiException.of(502, "SSH connection failed");
            }
            Collector.DockerProbe probe = Collector.probeDocker(client, server.serverType);
            if (probe.known()) {
                String probeVersion = probe.version();
                if (probe.installed() && probeVersion.isEmpty() && installed) {
                    probeVersion = version;
                }
                if (probe.installed() != installed || !probeVersion.equals(version)) {
                    servers.updateDockerInfo(serverId, probe.installed(), probeVersion);
                }
                installed = probe.installed();
                version = probeVersion;
            }
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("installed", installed);
            out.put("version", version);
            out.put("refreshed", probe.known());
            return out;
        }
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("installed", installed);
        out.put("version", version);
        return out;
    }

    @GetMapping("/{id}/docker/containers")
    public List<Map<String, Object>> listContainers(@PathVariable String id, HttpServletRequest req) {
        return containers(id, false, req);
    }

    @GetMapping("/{id}/docker/stats")
    public List<Map<String, Object>> containerStats(@PathVariable String id, HttpServletRequest req) {
        return containers(id, true, req);
    }

    private List<Map<String, Object>> containers(String id, boolean detailed, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        ServerSecrets server = servers.getServerByIdAndUser(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
        Duration ttl = detailed ? Duration.ofSeconds(10) : Duration.ofSeconds(3);
        RequestCache<List<DockerService.Container>> cache = detailed ? statistics : results;
        List<DockerService.Container> containers;
        try {
            containers = cache.get(cacheKey(server, detailed), ttl, () -> {
                try {
                    ClientSession client = sshCache.get(server);
                    return docker.readContainers(client, detailed);
                } catch (Exception e) {
                    throw new DockerLoadException(e);
                }
            });
        } catch (Exception e) {
            log.debug("docker {} for {}: {}", detailed ? "stats" : "list", id, e.toString());
            throw ApiException.of(502, "failed to read Docker data");
        }
        return containers.stream().map(DockerController::containerView).toList();
    }

    private static final class DockerLoadException extends RuntimeException {
        DockerLoadException(Throwable cause) {
            super(cause);
        }
    }

    private static Map<String, Object> containerView(DockerService.Container c) {
        Map<String, Object> row = new LinkedHashMap<>();
        row.put("id", c.id());
        row.put("name", c.name());
        row.put("image", c.image());
        row.put("status", c.status());
        row.put("state", c.state());
        row.put("ports", c.ports());
        row.put("created", c.created());
        row.put("cpu_percent", c.cpuPercent());
        row.put("memory_usage", c.memoryUsage());
        row.put("memory_limit", c.memoryLimit());
        row.put("memory_percent", c.memoryPercent());
        row.put("disk_read_bytes", c.diskReadBytes());
        row.put("disk_write_bytes", c.diskWriteBytes());
        row.put("block_io_available", c.blockIOAvailable());
        row.put("disk_usage", c.diskUsage());
        row.put("disk_virtual_usage", c.diskVirtualUsage());
        row.put("disk_available", c.diskAvailable());
        row.put("stats_available", c.statsAvailable());
        return row;
    }

    /** Cache key over the connection settings fingerprint, like the Go handler's. */
    private static String cacheKey(ServerSecrets server, boolean detailed) {
        String material = server.host + ":" + server.port + ":" + server.sshUsername + ":"
            + server.sshPassword + ":" + server.sshKey + ":" + server.sshHostKey;
        try {
            byte[] hash = MessageDigest.getInstance("SHA-256")
                .digest(material.getBytes(StandardCharsets.UTF_8));
            return server.id + ":" + java.util.HexFormat.of().formatHex(hash) + ":" + detailed;
        } catch (java.security.NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    @PostMapping("/{id}/docker/containers/{containerId}/{action}")
    public Map<String, Object> containerAction(@PathVariable String id, @PathVariable String containerId,
        @PathVariable String action, HttpServletRequest req) {
        if (!VALID_DOCKER_ID.matcher(containerId).matches()) {
            throw ApiException.badRequest("invalid container id");
        }
        if (!action.equals("start") && !action.equals("stop") && !action.equals("restart")) {
            throw ApiException.badRequest("invalid action");
        }
        Resolved resolved = resolve(id, req);
        try {
            docker.runDockerCmd(resolved.client(), action + " " + containerId);
        } catch (Exception e) {
            throw ApiException.of(500, "action failed");
        }
        // A state change invalidates both caches for this host.
        results.forget(cacheKey(resolved.server(), false));
        statistics.forget(cacheKey(resolved.server(), true));
        return Map.of("message", "ok");
    }

    @GetMapping("/{id}/docker/containers/{containerId}/logs")
    public Map<String, Object> containerLogs(@PathVariable String id, @PathVariable String containerId,
        @RequestParam(required = false) String tail, HttpServletRequest req) {
        if (!VALID_DOCKER_ID.matcher(containerId).matches()) {
            throw ApiException.badRequest("invalid container id");
        }
        int tailNum = 200;
        if (tail != null) {
            try {
                int parsed = Integer.parseInt(tail);
                if (parsed >= 1 && parsed <= 10000) {
                    tailNum = parsed;
                }
            } catch (NumberFormatException ignored) { }
        }
        Resolved resolved = resolve(id, req);
        String output;
        try {
            output = docker.runDockerCmd(resolved.client(),
                "logs --tail " + tailNum + " " + containerId);
        } catch (Exception e) {
            throw ApiException.of(500, "failed to get logs");
        }
        return Map.of("logs", output);
    }
}
