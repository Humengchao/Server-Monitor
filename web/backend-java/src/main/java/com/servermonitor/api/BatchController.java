package com.servermonitor.api;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Servers;
import com.servermonitor.service.BatchService;

import jakarta.servlet.http.HttpServletRequest;

/** /api/servers/bulk/* — multi-select tag, delete, and fan-out command execution. */
@RestController
@RequestMapping("/api/servers/bulk")
public class BatchController {

    private final Servers servers;
    private final BatchService batch;
    private final com.servermonitor.ssh.SshConnectionCache sshCache;

    public BatchController(Servers servers, BatchService batch,
        com.servermonitor.ssh.SshConnectionCache sshCache) {
        this.servers = servers;
        this.batch = batch;
        this.sshCache = sshCache;
    }

    public record BulkIdsBody(List<UUID> server_ids, List<UUID> tag_ids, String action, String command) {
    }

    @PostMapping("/tags")
    public Map<String, Object> bulkTags(@RequestBody BulkIdsBody body, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        if (body == null) {
            throw ApiException.badRequest("request body is required");
        }
        String action = body.action() == null ? "" : body.action().trim().toLowerCase();
        if (!action.equals("add") && !action.equals("remove")) {
            throw ApiException.badRequest("action must be add or remove");
        }
        List<UUID> serverIds = dedupe(body.server_ids(), 0);
        if (body.tag_ids() == null || body.tag_ids().isEmpty()) {
            throw ApiException.badRequest("no tags selected");
        }
        int owned = servers.countOwnedServers(serverIds, userId);
        if (owned == 0) {
            throw ApiException.notFound("no matching servers");
        }
        if ("add".equals(action)) {
            servers.addTagsToServers(serverIds, body.tag_ids(), userId);
        } else {
            servers.removeTagsFromServers(serverIds, body.tag_ids(), userId);
        }
        return Map.of("message", "tags updated", "servers", owned);
    }

    @PostMapping("/delete")
    public Map<String, Object> bulkDelete(@RequestBody BulkIdsBody body, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        if (body == null) {
            throw ApiException.badRequest("request body is required");
        }
        List<UUID> serverIds = dedupe(body.server_ids(), 0);
        long deleted = servers.deleteServers(serverIds, userId);
        for (UUID id : serverIds) {
            sshCache.drop(id);
        }
        return Map.of("message", "deleted", "deleted", deleted);
    }

    @PostMapping("/exec")
    public Map<String, Object> bulkExec(@RequestBody BulkIdsBody body, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        if (body == null) {
            throw ApiException.badRequest("request body is required");
        }
        String command = body.command() == null ? "" : body.command().trim();
        if (command.isEmpty()) {
            throw ApiException.badRequest("command is required");
        }
        if (command.length() > BatchService.BATCH_MAX_COMMAND_LENGTH) {
            throw ApiException.badRequest(
                "command must be at most " + BatchService.BATCH_MAX_COMMAND_LENGTH + " characters");
        }
        if (command.chars().anyMatch(c -> c != '\t' && Character.isISOControl(c))) {
            throw ApiException.badRequest("command must be a single line without control characters");
        }
        List<UUID> serverIds = dedupe(body.server_ids(), BatchService.BATCH_MAX_TARGETS);
        List<Servers.ServerSecrets> fleet = new ArrayList<>();
        for (UUID id : serverIds) {
            servers.getServerByIdAndUser(id, userId).ifPresent(fleet::add);
        }
        if (fleet.isEmpty()) {
            throw ApiException.notFound("no matching servers");
        }
        var results = batch.runBatchCommand(fleet, command);
        int succeeded = 0;
        List<Map<String, Object>> out = new ArrayList<>();
        for (BatchService.BatchResult r : results) {
            if (r.ok()) {
                succeeded++;
            }
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("server_id", r.serverId());
            row.put("server_name", r.serverName());
            row.put("ok", r.ok());
            row.put("output", r.output());
            row.put("error", r.error());
            row.put("truncated", r.truncated());
            row.put("duration_ms", r.durationMs());
            out.add(row);
        }
        return Map.of("results", out, "succeeded", succeeded, "failed", out.size() - succeeded);
    }

    /** Keeps caller order, drops duplicates, rejects an oversized selection. */
    private static List<UUID> dedupe(List<UUID> ids, int limit) {
        if (ids == null || ids.isEmpty()) {
            throw ApiException.badRequest("no servers selected");
        }
        List<UUID> unique = new ArrayList<>(new LinkedHashSet<>(ids));
        if (limit > 0 && unique.size() > limit) {
            throw ApiException.badRequest("select at most " + limit + " servers at a time");
        }
        return unique;
    }
}
