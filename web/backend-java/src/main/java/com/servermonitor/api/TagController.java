package com.servermonitor.api;

import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Servers;

import jakarta.servlet.http.HttpServletRequest;

/** /api/tags CRUD. */
@RestController
@RequestMapping("/api/tags")
public class TagController {

    private final Servers servers;

    public TagController(Servers servers) {
        this.servers = servers;
    }

    @GetMapping
    public List<Map<String, Object>> list(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        return servers.getTagsByUserId(userId).stream().map(Jsons::tag).toList();
    }

    public record TagBody(String name, String color) {
    }

    @PostMapping
    public ResponseEntity<Map<String, Object>> create(@RequestBody TagBody body, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        if (body == null || body.name() == null || body.name().isBlank()) {
            throw ApiException.badRequest("name is required");
        }
        String color = body.color() == null || body.color().isEmpty() ? "#1890ff" : body.color();
        var tag = new Servers.Tag(UUID.randomUUID(), userId, body.name(), color);
        servers.createTag(tag);
        return ResponseEntity.status(HttpStatus.CREATED).body(Jsons.tag(tag));
    }

    @DeleteMapping("/{id}")
    public Map<String, Object> delete(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        servers.deleteTag(UuidParser.parse(id), userId);
        return Map.of("message", "deleted");
    }
}
